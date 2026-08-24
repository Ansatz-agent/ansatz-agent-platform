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
	"strings"
	"sync"
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

const defaultReceiptRetention = 30 * 24 * time.Hour

type BoltStore struct {
	db               *bolt.DB
	receiptRetention time.Duration
	storageGuard     StorageGuard
	now              func() time.Time
	syncFn           func() error
	writeMu          sync.Mutex
	closed           bool
	pageSize         int
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
	if options.ReceiptRetention < 0 || options.MaxDBBytes < 0 || options.MinFreeBytes < 0 || options.OpenTimeout < 0 {
		return nil, errors.New("trace inbox options must not be negative")
	}
	if options.ReceiptRetention == 0 {
		options.ReceiptRetention = defaultReceiptRetention
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
	store := &BoltStore{
		db:               db,
		receiptRetention: options.ReceiptRetention,
		now:              options.Now,
		pageSize:         db.Info().PageSize,
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
	if s.closed {
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
		if err := s.storageGuard.Check(projectedAcceptanceGrowth(s.pageSize, key, batchBytes, receiptBytes)); err != nil {
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
	if err := s.syncFn(); err != nil {
		return AcceptResult{}, fmt.Errorf("sync accepted trace batch: %w", err)
	}
	return result, nil
}

func (s *BoltStore) PeekEligible(ctx context.Context, now time.Time) (*Batch, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	var result *Batch
	err := s.db.View(func(tx *bolt.Tx) error {
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

func (s *BoltStore) CollectReceipts(ctx context.Context, now time.Time) (int, error) {
	collected := 0
	err := s.updateAndSync(ctx, func(tx *bolt.Tx) error {
		cutoff := now.Add(-s.receiptRetention)
		cursor := tx.Bucket(receiptsBucket).Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			receipt, err := decodeReceipt(value)
			if err != nil {
				return err
			}
			if receipt.State != receiptDelivered || receipt.CompletedAt.IsZero() || receipt.CompletedAt.After(cutoff) {
				continue
			}
			if err := cursor.Delete(); err != nil {
				return err
			}
			collected++
		}
		return nil
	})
	return collected, err
}

func (s *BoltStore) Sync() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return ErrClosed
	}
	return s.syncFn()
}

func (s *BoltStore) Close() error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	syncErr := s.syncFn()
	closeErr := s.db.Close()
	return errors.Join(syncErr, closeErr)
}

func (s *BoltStore) updateAndSync(ctx context.Context, update func(*bolt.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed {
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

const (
	bboltLeafElementBytes    = int64(16)
	bboltAcceptanceSafePages = int64(32)
)

// projectedAcceptanceGrowth accounts for the exact JSON-encoded batch and
// receipt (including base64 payload expansion), all three bucket key/value
// pairs, and bbolt leaf-element overhead. It rounds data to whole pages,
// doubles those pages for copy-on-write leaf splits, then adds thirty-two pages
// for branch splits and freelist growth. Admission may therefore reject early,
// but never treats raw payload length as the expected on-disk growth.
func projectedAcceptanceGrowth(pageSize int, key, batch, receipt []byte) int64 {
	if pageSize <= 0 {
		return math.MaxInt64
	}
	recordBytes := int64(len(batch)+len(receipt)+3*len(key)+8) + 3*bboltLeafElementBytes
	pageBytes := int64(pageSize)
	dataPages := (recordBytes + pageBytes - 1) / pageBytes
	projectedPages := 2*dataPages + bboltAcceptanceSafePages
	if projectedPages > math.MaxInt64/pageBytes {
		return math.MaxInt64
	}
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
