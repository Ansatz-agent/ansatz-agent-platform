package inbox

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	bolt "go.etcd.io/bbolt"
)

var (
	metaBucket       = []byte("meta")
	batchesBucket    = []byte("batches")
	fifoBucket       = []byte("fifo")
	receiptsBucket   = []byte("receipts")
	quarantineBucket = []byte("quarantine")
)

const (
	defaultReceiptRetention     = 30 * 24 * time.Hour
	defaultMaxQuarantineEntries = 1000
)

type BoltStore struct {
	db                   *bolt.DB
	receiptRetention     time.Duration
	quarantineRetention  time.Duration
	maxQuarantineEntries int
	storageGuard         StorageGuard
	now                  func() time.Time
	syncFn               func() error
	writeMu              sync.Mutex
	closed               atomic.Bool
	pageSize             int
	allocSize            int
}

type storedBatch struct {
	Envelope
	Sequence   uint64    `json:"sequence"`
	AcceptedAt time.Time `json:"accepted_at"`
	Attempts   int       `json:"attempts"`
	LastError  string    `json:"last_error,omitempty"`
	NextRetry  time.Time `json:"next_retry,omitempty"`
}

type receiptState string

const (
	receiptPending     receiptState = "pending"
	receiptDelivered   receiptState = "delivered"
	receiptQuarantined receiptState = "quarantined"
)

type storedReceipt struct {
	AccountID     string         `json:"account_id"`
	BatchID       string         `json:"batch_id"`
	PayloadSHA256 string         `json:"payload_sha256"`
	Outcome       ReceiptOutcome `json:"outcome"`
	AcceptedAt    time.Time      `json:"accepted_at"`
	State         receiptState   `json:"state"`
	CompletedAt   time.Time      `json:"completed_at,omitempty"`
	ErrorClass    string         `json:"error_class,omitempty"`
}

func Open(path string, options Options) (*BoltStore, error) {
	if path == "" {
		return nil, errors.New("trace inbox path is required")
	}
	if options.ReceiptRetention < 0 || options.QuarantineRetention < 0 || options.MaxQuarantineEntries < 0 ||
		options.MaxDBBytes < 0 || options.MinFreeBytes < 0 || options.AllocSize < 0 || options.OpenTimeout < 0 {
		return nil, errors.New("trace inbox options must not be negative")
	}
	if options.ReceiptRetention == 0 {
		options.ReceiptRetention = defaultReceiptRetention
	}
	if options.QuarantineRetention == 0 {
		options.QuarantineRetention = options.ReceiptRetention
	}
	if options.MaxQuarantineEntries == 0 {
		options.MaxQuarantineEntries = defaultMaxQuarantineEntries
	}
	if options.OpenTimeout == 0 {
		options.OpenTimeout = time.Second
	}
	if options.Now == nil {
		options.Now = time.Now
	}

	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: options.OpenTimeout, NoSync: true})
	if err != nil {
		return nil, fmt.Errorf("open trace inbox: %w", err)
	}
	if options.AllocSize > 0 {
		db.AllocSize = options.AllocSize
	}
	store := &BoltStore{
		db:                   db,
		receiptRetention:     options.ReceiptRetention,
		quarantineRetention:  options.QuarantineRetention,
		maxQuarantineEntries: options.MaxQuarantineEntries,
		now:                  options.Now,
		pageSize:             db.Info().PageSize,
		allocSize:            db.AllocSize,
	}
	store.syncFn = db.Sync
	if options.StorageGuard != nil {
		store.storageGuard = options.StorageGuard
	} else {
		store.storageGuard = fileStorageGuard{path: path, maxDBBytes: options.MaxDBBytes, minFreeBytes: options.MinFreeBytes}
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{metaBucket, batchesBucket, fifoBucket, receiptsBucket, quarantineBucket} {
			if _, err := tx.CreateBucketIfNotExists(name); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize trace inbox: %w", err)
	}
	if err := db.Sync(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("sync trace inbox schema: %w", err)
	}
	return store, nil
}

func (s *BoltStore) Accept(ctx context.Context, env Envelope) (AcceptResult, error) {
	if err := validateEnvelope(env); err != nil {
		return AcceptResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return AcceptResult{}, err
	}

	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return AcceptResult{}, ErrClosed
	}
	var result AcceptResult
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		key := receiptKey(env.AccountID, env.BatchID)
		if existing := tx.Bucket(receiptsBucket).Get(key); existing != nil {
			receipt, err := decodeReceipt(existing)
			if err != nil {
				return err
			}
			if receipt.PayloadSHA256 != env.PayloadSHA256 {
				return ErrIdempotencyConflict
			}
			result = AcceptResult{BatchID: env.BatchID, Outcome: ReceiptDuplicate, AcceptedAt: receipt.AcceptedAt}
			return nil
		}
		sequence, err := tx.Bucket(metaBucket).NextSequence()
		if err != nil {
			return err
		}
		acceptedAt := s.now().UTC()
		batch := storedBatch{Envelope: cloneEnvelope(env), Sequence: sequence, AcceptedAt: acceptedAt}
		batchBytes, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		receiptBytes, err := json.Marshal(storedReceipt{
			AccountID: env.AccountID, BatchID: env.BatchID, PayloadSHA256: env.PayloadSHA256,
			Outcome: ReceiptAccepted, AcceptedAt: acceptedAt, State: receiptPending,
		})
		if err != nil {
			return err
		}
		if err := s.storageGuard.Check(projectedAcceptanceGrowth(s.pageSize, s.allocSize, key, batchBytes, receiptBytes)); err != nil {
			return err
		}
		fifoKey := sequenceKey(sequence)
		batches := tx.Bucket(batchesBucket)
		fifo := tx.Bucket(fifoBucket)
		receipts := tx.Bucket(receiptsBucket)
		if err := batches.Put(key, batchBytes); err != nil {
			return err
		}
		if err := fifo.Put(fifoKey, key); err != nil {
			return err
		}
		if err := receipts.Put(key, receiptBytes); err != nil {
			return err
		}
		result = AcceptResult{BatchID: env.BatchID, Outcome: ReceiptAccepted, AcceptedAt: acceptedAt}
		return nil
	})
	if err != nil {
		return AcceptResult{}, err
	}
	// A duplicate writes no state, so skipping the fsync cannot weaken the
	// pre-ACK durability guarantee for newly accepted batches.
	if result.Outcome == ReceiptDuplicate {
		return result, nil
	}
	if err := s.syncFn(); err != nil {
		return AcceptResult{}, fmt.Errorf("sync accepted trace batch: %w", err)
	}
	return result, nil
}

func (s *BoltStore) PeekEligible(ctx context.Context, now time.Time) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var result *Batch
	err := s.view(func(tx *bolt.Tx) error {
		fifoKey, batchKey := tx.Bucket(fifoBucket).Cursor().First()
		if fifoKey == nil {
			return nil
		}
		encoded := tx.Bucket(batchesBucket).Get(batchKey)
		if encoded == nil {
			return errors.New("trace inbox FIFO references a missing batch")
		}
		stored, err := decodeBatch(encoded)
		if err != nil {
			return err
		}
		if !stored.NextRetry.IsZero() && stored.NextRetry.After(now) {
			return nil
		}
		result = &Batch{
			Envelope: cloneEnvelope(stored.Envelope), Sequence: stored.Sequence, AcceptedAt: stored.AcceptedAt,
			Attempts: stored.Attempts, LastError: stored.LastError, NextRetry: stored.NextRetry,
		}
		return nil
	})
	return result, err
}

// NextRetryAt returns the persisted retry time of the FIFO head. A zero value
// means that the inbox is empty or that its head does not have a retry delay.
// It deliberately examines only the head so delivery scheduling cannot bypass
// strict FIFO ordering.
func (s *BoltStore) NextRetryAt(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	var nextRetry time.Time
	err := s.view(func(tx *bolt.Tx) error {
		_, batchKey := tx.Bucket(fifoBucket).Cursor().First()
		if batchKey == nil {
			return nil
		}
		encoded := tx.Bucket(batchesBucket).Get(batchKey)
		if encoded == nil {
			return errors.New("trace inbox FIFO references a missing batch")
		}
		batch, err := decodeBatch(encoded)
		if err != nil {
			return err
		}
		nextRetry = batch.NextRetry
		return nil
	})
	return nextRetry, err
}

func (s *BoltStore) MarkRetry(ctx context.Context, accountID, batchID string, retry Retry) error {
	if retry.NextRetry.IsZero() {
		return errors.New("next retry time is required")
	}
	return s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		key, batch, err := lookupBatch(tx, accountID, batchID)
		if err != nil {
			return err
		}
		batch.Attempts++
		batch.LastError = retry.LastError
		batch.NextRetry = retry.NextRetry.UTC()
		encoded, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		return tx.Bucket(batchesBucket).Put(key, encoded)
	})
}

func (s *BoltStore) MarkDelivered(ctx context.Context, accountID, batchID string, deliveredAt time.Time) error {
	if deliveredAt.IsZero() {
		deliveredAt = s.now()
	}
	deliveredAt = deliveredAt.UTC()
	return s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		key, batch, err := lookupBatch(tx, accountID, batchID)
		if err != nil {
			return err
		}
		if err := tx.Bucket(batchesBucket).Delete(key); err != nil {
			return err
		}
		if err := tx.Bucket(fifoBucket).Delete(sequenceKey(batch.Sequence)); err != nil {
			return err
		}
		return updateReceipt(tx, key, func(receipt *storedReceipt) {
			receipt.State = receiptDelivered
			receipt.CompletedAt = deliveredAt
			receipt.ErrorClass = ""
		})
	})
}

func (s *BoltStore) MarkQuarantined(ctx context.Context, accountID, batchID, errorClass string, quarantinedAt time.Time) error {
	if errorClass == "" {
		return errors.New("quarantine error class is required")
	}
	if quarantinedAt.IsZero() {
		quarantinedAt = s.now()
	}
	quarantinedAt = quarantinedAt.UTC()
	return s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		key, batch, err := lookupBatch(tx, accountID, batchID)
		if err != nil {
			return err
		}
		encoded, err := json.Marshal(batch)
		if err != nil {
			return err
		}
		if err := tx.Bucket(quarantineBucket).Put(key, encoded); err != nil {
			return err
		}
		if err := tx.Bucket(batchesBucket).Delete(key); err != nil {
			return err
		}
		if err := tx.Bucket(fifoBucket).Delete(sequenceKey(batch.Sequence)); err != nil {
			return err
		}
		return updateReceipt(tx, key, func(receipt *storedReceipt) {
			receipt.State = receiptQuarantined
			receipt.CompletedAt = quarantinedAt
			receipt.ErrorClass = errorClass
		})
	})
}

// CollectReceipts garbage-collects terminal state only: delivered receipts
// past the receipt retention, quarantined payloads and receipts past the
// quarantine retention, and the oldest quarantined payloads above the size
// cap (their receipts stay for idempotency until retention expires). Pending
// receipts and accepted-undelivered batches are never touched, and nothing is
// fsynced when no state changed.
func (s *BoltStore) CollectReceipts(ctx context.Context, now time.Time) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return 0, ErrClosed
	}
	collected := 0
	err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		receiptCutoff := now.Add(-s.receiptRetention)
		quarantineCutoff := now.Add(-s.quarantineRetention)
		quarantine := tx.Bucket(quarantineBucket)
		cursor := tx.Bucket(receiptsBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			receipt, err := decodeReceipt(value)
			if err != nil {
				return err
			}
			if receipt.CompletedAt.IsZero() {
				continue
			}
			switch receipt.State {
			case receiptDelivered:
				if receipt.CompletedAt.After(receiptCutoff) {
					continue
				}
			case receiptQuarantined:
				if receipt.CompletedAt.After(quarantineCutoff) {
					continue
				}
				if quarantine.Get(key) != nil {
					if err := quarantine.Delete(key); err != nil {
						return err
					}
					collected++
				}
			default:
				continue
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			collected++
		}
		evicted, err := evictQuarantineOverflow(quarantine, s.maxQuarantineEntries)
		collected += evicted
		return err
	})
	if err != nil {
		return 0, err
	}
	if collected == 0 {
		return 0, nil
	}
	if err := s.syncFn(); err != nil {
		return 0, err
	}
	return collected, nil
}

// evictQuarantineOverflow deletes the oldest quarantined payloads (by original
// acceptance sequence) above the configured cap. Receipts are intentionally
// preserved so account_id+batch_id idempotency outlives the payload.
func evictQuarantineOverflow(quarantine *bolt.Bucket, maximum int) (int, error) {
	type entry struct {
		sequence uint64
		key      []byte
	}
	var entries []entry
	err := quarantine.ForEach(func(key, value []byte) error {
		batch, err := decodeBatch(value)
		if err != nil {
			return err
		}
		entries = append(entries, entry{sequence: batch.Sequence, key: append([]byte(nil), key...)})
		return nil
	})
	if err != nil || len(entries) <= maximum {
		return 0, err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].sequence < entries[j].sequence })
	evicted := 0
	for _, overflow := range entries[:len(entries)-maximum] {
		if err := quarantine.Delete(overflow.key); err != nil {
			return evicted, err
		}
		evicted++
	}
	return evicted, nil
}

// ReplayQuarantined moves one quarantined batch back to the FIFO tail with a
// fresh attempt budget and returns its receipt to the pending state, keeping
// the same account_id+batch_id key so duplicate uploads still coalesce.
func (s *BoltStore) ReplayQuarantined(ctx context.Context, accountID, batchID string) error {
	return s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		return s.replayQuarantined(tx, receiptKey(accountID, batchID))
	})
}

// ReplayAllQuarantined replays every quarantined batch in original acceptance
// order and reports how many were requeued.
func (s *BoltStore) ReplayAllQuarantined(ctx context.Context) (int, error) {
	replayed := 0
	err := s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		type entry struct {
			sequence uint64
			key      []byte
		}
		var entries []entry
		if err := tx.Bucket(quarantineBucket).ForEach(func(key, value []byte) error {
			batch, err := decodeBatch(value)
			if err != nil {
				return err
			}
			entries = append(entries, entry{sequence: batch.Sequence, key: append([]byte(nil), key...)})
			return nil
		}); err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].sequence < entries[j].sequence })
		for _, quarantined := range entries {
			if err := s.replayQuarantined(tx, quarantined.key); err != nil {
				return err
			}
			replayed++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return replayed, nil
}

func (s *BoltStore) replayQuarantined(tx *bolt.Tx, key []byte) error {
	encoded := tx.Bucket(quarantineBucket).Get(key)
	if encoded == nil {
		return ErrBatchNotFound
	}
	batch, err := decodeBatch(encoded)
	if err != nil {
		return err
	}
	sequence, err := tx.Bucket(metaBucket).NextSequence()
	if err != nil {
		return err
	}
	batch.Sequence = sequence
	batch.Attempts = 0
	batch.LastError = ""
	batch.NextRetry = time.Time{}
	requeued, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	if err := s.storageGuard.Check(projectedAcceptanceGrowth(s.pageSize, s.allocSize, key, requeued, nil)); err != nil {
		return err
	}
	if err := tx.Bucket(batchesBucket).Put(key, requeued); err != nil {
		return err
	}
	if err := tx.Bucket(fifoBucket).Put(sequenceKey(sequence), key); err != nil {
		return err
	}
	if err := tx.Bucket(quarantineBucket).Delete(key); err != nil {
		return err
	}
	if tx.Bucket(receiptsBucket).Get(key) == nil {
		receiptBytes, err := json.Marshal(storedReceipt{
			AccountID: batch.AccountID, BatchID: batch.BatchID, PayloadSHA256: batch.PayloadSHA256,
			Outcome: ReceiptAccepted, AcceptedAt: batch.AcceptedAt, State: receiptPending,
		})
		if err != nil {
			return err
		}
		return tx.Bucket(receiptsBucket).Put(key, receiptBytes)
	}
	return updateReceipt(tx, key, func(receipt *storedReceipt) {
		receipt.State = receiptPending
		receipt.CompletedAt = time.Time{}
		receipt.ErrorClass = ""
	})
}

func (s *BoltStore) Sync() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return ErrClosed
	}
	return s.syncFn()
}

func (s *BoltStore) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return nil
	}
	s.closed.Store(true)
	syncErr := s.syncFn()
	closeErr := s.db.Close()
	return errors.Join(syncErr, closeErr)
}

// view runs a read-only transaction without taking the writer mutex, so reads
// are never serialized behind an in-flight update or fsync. This is safe
// because bbolt's DB.Close documents that it blocks until every open
// transaction (read-only included) finishes, so a View racing Close never
// observes a released database; a View that starts after Close instead fails
// with bolt.ErrDatabaseNotOpen, which is mapped to ErrClosed here.
func (s *BoltStore) view(fn func(*bolt.Tx) error) error {
	if s.closed.Load() {
		return ErrClosed
	}
	err := s.db.View(fn)
	if errors.Is(err, bolt.ErrDatabaseNotOpen) {
		return ErrClosed
	}
	return err
}

func (s *BoltStore) updateAndSync(ctx context.Context, update func(*bolt.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed.Load() {
		return ErrClosed
	}
	if err := s.db.Update(func(tx *bolt.Tx) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return update(tx)
	}); err != nil {
		return err
	}
	return s.syncFn()
}

func lookupBatch(tx *bolt.Tx, accountID, batchID string) ([]byte, storedBatch, error) {
	key := receiptKey(accountID, batchID)
	encoded := tx.Bucket(batchesBucket).Get(key)
	if encoded == nil {
		return nil, storedBatch{}, ErrBatchNotFound
	}
	batch, err := decodeBatch(encoded)
	if err != nil {
		return nil, storedBatch{}, err
	}
	return key, batch, nil
}

func updateReceipt(tx *bolt.Tx, key []byte, update func(*storedReceipt)) error {
	bucket := tx.Bucket(receiptsBucket)
	encoded := bucket.Get(key)
	if encoded == nil {
		return errors.New("trace batch receipt is missing")
	}
	receipt, err := decodeReceipt(encoded)
	if err != nil {
		return err
	}
	update(&receipt)
	encoded, err = json.Marshal(receipt)
	if err != nil {
		return err
	}
	return bucket.Put(key, encoded)
}

func validateEnvelope(env Envelope) error {
	if env.AccountID == "" || env.BatchID == "" || strings.ContainsRune(env.AccountID, '\x00') || strings.ContainsRune(env.BatchID, '\x00') {
		return ErrInvalidEnvelope
	}
	if len(env.PayloadSHA256) != sha256HexLength || strings.ToLower(env.PayloadSHA256) != env.PayloadSHA256 {
		return ErrInvalidEnvelope
	}
	decoded, err := hex.DecodeString(env.PayloadSHA256)
	if err != nil || len(decoded) != sha256ByteLength {
		return ErrInvalidEnvelope
	}
	return nil
}

const (
	sha256ByteLength = 32
	sha256HexLength  = sha256ByteLength * 2
)

func receiptKey(accountID, batchID string) []byte {
	key := make([]byte, 0, len(accountID)+1+len(batchID))
	key = append(key, accountID...)
	key = append(key, 0)
	return append(key, batchID...)
}

func sequenceKey(sequence uint64) []byte {
	key := make([]byte, 8)
	binary.BigEndian.PutUint64(key, sequence)
	return key
}

const bboltLeafElementBytes = int64(16)

// projectedAcceptanceGrowth accounts for the exact JSON-encoded batch and
// receipt (including base64 payload expansion), all three bucket key/value
// pairs, and bbolt leaf-element overhead. It rounds data to whole pages,
// doubles those pages for copy-on-write growth, then adds the opened database's
// AllocSize rounded up to a page. bbolt grow() can extend the file by that full
// allocation chunk beyond requested pages. Admission may therefore reject
// early, but never treats raw payload length as expected on-disk growth.
func projectedAcceptanceGrowth(pageSize, allocSize int, key, batch, receipt []byte) int64 {
	if pageSize <= 0 || allocSize < 0 {
		return math.MaxInt64
	}
	recordBytes := int64(len(batch)) + int64(len(receipt)) + 3*int64(len(key)) + 8 + 3*bboltLeafElementBytes
	pageBytes := int64(pageSize)
	if recordBytes > math.MaxInt64-pageBytes+1 || int64(allocSize) > math.MaxInt64-pageBytes+1 {
		return math.MaxInt64
	}
	dataPages := (recordBytes + pageBytes - 1) / pageBytes
	allocPages := (int64(allocSize) + pageBytes - 1) / pageBytes
	maxPages := math.MaxInt64 / pageBytes
	if allocPages > maxPages || dataPages > (maxPages-allocPages)/2 {
		return math.MaxInt64
	}
	projectedPages := 2*dataPages + allocPages
	return projectedPages * pageBytes
}

func decodeBatch(encoded []byte) (storedBatch, error) {
	var batch storedBatch
	if err := json.Unmarshal(encoded, &batch); err != nil {
		return storedBatch{}, fmt.Errorf("decode trace inbox batch: %w", err)
	}
	return batch, nil
}

func decodeReceipt(encoded []byte) (storedReceipt, error) {
	var receipt storedReceipt
	if err := json.Unmarshal(encoded, &receipt); err != nil {
		return storedReceipt{}, fmt.Errorf("decode trace inbox receipt: %w", err)
	}
	return receipt, nil
}

func cloneEnvelope(env Envelope) Envelope {
	env.Payload = append([]byte(nil), env.Payload...)
	return env
}

type fileStorageGuard struct {
	path           string
	maxDBBytes     int64
	minFreeBytes   int64
	sizeBytes      func() (int64, error)
	availableBytes func() (int64, error)
}

func (guard fileStorageGuard) Check(projectedGrowthBytes int64) error {
	if projectedGrowthBytes < 0 {
		return ErrStorageUnavailable
	}
	if guard.maxDBBytes > 0 {
		sizeBytes := guard.sizeBytes
		if sizeBytes == nil {
			sizeBytes = func() (int64, error) {
				info, err := os.Stat(guard.path)
				if err != nil {
					return 0, err
				}
				return info.Size(), nil
			}
		}
		size, err := sizeBytes()
		if err != nil {
			return fmt.Errorf("%w: inspect inbox size", ErrStorageUnavailable)
		}
		if size > guard.maxDBBytes-projectedGrowthBytes {
			return ErrStorageUnavailable
		}
	}
	if guard.minFreeBytes > 0 {
		availableBytes := guard.availableBytes
		if availableBytes == nil {
			availableBytes = func() (int64, error) {
				var stat syscall.Statfs_t
				if err := syscall.Statfs(guard.path, &stat); err != nil {
					return 0, err
				}
				return int64(stat.Bavail) * int64(stat.Bsize), nil
			}
		}
		available, err := availableBytes()
		if err != nil {
			return fmt.Errorf("%w: inspect inbox filesystem", ErrStorageUnavailable)
		}
		if available < guard.minFreeBytes || projectedGrowthBytes > available-guard.minFreeBytes {
			return ErrStorageUnavailable
		}
	}
	return nil
}
