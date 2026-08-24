package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	bolt "go.etcd.io/bbolt"
)

var testNow = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func TestBoltStoreAcceptIsDurableIdempotentAndTokenIndependent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := openTestStore(t, path)
	envelope := validEnvelope("account-a", "batch-a", []byte("protobuf"))

	first, err := store.Accept(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if first.Outcome != ReceiptAccepted {
		t.Fatalf("outcome = %q", first.Outcome)
	}
	if first.BatchID != "batch-a" {
		t.Fatalf("batch ID = %q", first.BatchID)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openTestStore(t, path)
	duplicate, err := store.Accept(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Outcome != ReceiptDuplicate {
		t.Fatalf("outcome = %q", duplicate.Outcome)
	}
	conflict := envelope
	conflict.Payload = []byte("different")
	conflict.PayloadSHA256 = sha256Hex(conflict.Payload)
	_, err = store.Accept(context.Background(), conflict)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("error = %v", err)
	}
	if got := countCanonicalBatches(t, store); got != 1 {
		t.Fatalf("batches = %d", got)
	}

	batch, err := store.PeekEligible(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || string(batch.Payload) != "protobuf" {
		t.Fatalf("pending batch = %#v", batch)
	}
}

func TestBoltStoreConcurrentAcceptCreatesOneCanonicalBatch(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	envelope := validEnvelope("account-a", "batch-a", []byte("protobuf"))

	const callers = 24
	outcomes := make(chan ReceiptOutcome, callers)
	errorsSeen := make(chan error, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := store.Accept(context.Background(), envelope)
			if err != nil {
				errorsSeen <- err
				return
			}
			outcomes <- result.Outcome
		}()
	}
	wait.Wait()
	close(outcomes)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Errorf("Accept: %v", err)
	}
	accepted, duplicates := 0, 0
	for outcome := range outcomes {
		switch outcome {
		case ReceiptAccepted:
			accepted++
		case ReceiptDuplicate:
			duplicates++
		default:
			t.Errorf("unexpected outcome %q", outcome)
		}
	}
	if accepted != 1 || duplicates != callers-1 {
		t.Fatalf("accepted = %d, duplicates = %d", accepted, duplicates)
	}
	if got := countCanonicalBatches(t, store); got != 1 {
		t.Fatalf("batches = %d", got)
	}
	if got := countBucketEntries(t, store, fifoBucket); got != 1 {
		t.Fatalf("FIFO entries = %d", got)
	}
}

func TestBoltStoreDuplicateAndConflictBypassNewAdmissionGuard(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	envelope := validEnvelope("account-a", "batch-a", []byte("protobuf"))
	if _, err := store.Accept(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	rejecting := &rejectingStorageGuard{}
	store.storageGuard = rejecting

	duplicate, err := store.Accept(context.Background(), envelope)
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if duplicate.Outcome != ReceiptDuplicate {
		t.Fatalf("duplicate outcome = %q", duplicate.Outcome)
	}
	conflict := envelope
	conflict.PayloadSHA256 = sha256Hex([]byte("different"))
	_, err = store.Accept(context.Background(), conflict)
	if !errors.Is(err, ErrIdempotencyConflict) {
		t.Fatalf("conflict error = %v", err)
	}
	if rejecting.calls != 0 {
		t.Fatalf("guard calls = %d", rejecting.calls)
	}
}

func TestBoltStoreReopenPreservesFIFOAndDelayedHeadBlocksLaterBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := openTestStore(t, path)
	for _, batchID := range []string{"one", "two"} {
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", batchID, []byte(batchID))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkRetry(context.Background(), "account-a", "one", Retry{
		NextRetry: testNow.Add(time.Minute),
		LastError: "upstream_503",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = openTestStore(t, path)
	blocked, err := store.PeekEligible(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if blocked != nil {
		t.Fatalf("delayed head allowed delivery: %#v", blocked)
	}
	head, err := store.PeekEligible(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.BatchID != "one" || head.Attempts != 1 || head.LastError != "upstream_503" {
		t.Fatalf("head = %#v", head)
	}
	if err := store.MarkDelivered(context.Background(), "account-a", "one", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, err := store.PeekEligible(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.BatchID != "two" {
		t.Fatalf("next = %#v", next)
	}

	receipts, err := store.CollectReceipts(context.Background(), testNow.Add(30*24*time.Hour-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Fatalf("collected fresh receipts = %d", receipts)
	}
	receipts, err = store.CollectReceipts(context.Background(), testNow.Add(30*24*time.Hour+time.Minute+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if receipts != 1 {
		t.Fatalf("collected expired receipts = %d", receipts)
	}
}

func TestBoltStoreTransitionsAddressSameBatchIDInDifferentAccounts(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	for _, accountID := range []string{"account-a", "account-b"} {
		if _, err := store.Accept(context.Background(), validEnvelope(accountID, "shared", []byte(accountID))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkRetry(context.Background(), "account-a", "shared", Retry{
		NextRetry: testNow.Add(time.Minute), LastError: "upstream_503",
	}); err != nil {
		t.Fatal(err)
	}
	head, err := store.PeekEligible(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.AccountID != "account-a" || head.Attempts != 1 {
		t.Fatalf("first account head = %#v", head)
	}
	if err := store.MarkDelivered(context.Background(), "account-a", "shared", testNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, err := store.PeekEligible(context.Background(), testNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if next == nil || next.AccountID != "account-b" || next.Attempts != 0 {
		t.Fatalf("second account head = %#v", next)
	}
	if err := store.MarkQuarantined(context.Background(), "account-b", "shared", "upstream_400", testNow); err != nil {
		t.Fatal(err)
	}
	if got := countBucketEntries(t, store, quarantineBucket); got != 1 {
		t.Fatalf("quarantine entries = %d", got)
	}
}

func TestBoltStoreQuarantineRemovesFIFOButRetainsCanonicalRecord(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "bad", []byte("protobuf"))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQuarantined(context.Background(), "account-a", "bad", "upstream_400", testNow); err != nil {
		t.Fatal(err)
	}
	batch, err := store.PeekEligible(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if batch != nil {
		t.Fatalf("quarantined batch remained eligible: %#v", batch)
	}
	if got := countCanonicalBatches(t, store); got != 0 {
		t.Fatalf("pending batches = %d", got)
	}
	if got := countBucketEntries(t, store, quarantineBucket); got != 1 {
		t.Fatalf("quarantine entries = %d", got)
	}
}

func TestBoltStoreProjectsEncodedPageGrowthAgainstMaxDBBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := openTestStore(t, path)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, Options{
		ReceiptRetention: 30 * 24 * time.Hour,
		MaxDBBytes:       info.Size() + 1,
		Now:              func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	_, err = store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x")))
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if got := countCanonicalBatches(t, store); got != 0 {
		t.Fatalf("committed batches = %d", got)
	}
}

func TestBoltStoreProjectsEncodedPageGrowthAgainstMinFreeBytes(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	store.storageGuard = fileStorageGuard{
		minFreeBytes:   999,
		sizeBytes:      func() (int64, error) { return 0, nil },
		availableBytes: func() (int64, error) { return 1000, nil },
	}

	_, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x")))
	if !errors.Is(err, ErrStorageUnavailable) {
		t.Fatalf("error = %v", err)
	}
	if got := countCanonicalBatches(t, store); got != 0 {
		t.Fatalf("committed batches = %d", got)
	}
}

func TestBoltStoreBudgetsAllocSizeAtActualFileGrowthBoundaries(t *testing.T) {
	pageSize := int64(os.Getpagesize())
	projectedGrowth := 3 * pageSize // one encoded page, one COW page, one AllocSize page

	t.Run("MaxDBBytes", func(t *testing.T) {
		path, baseline := initializedStoreFile(t, int(pageSize))
		maximum := baseline + projectedGrowth
		store, err := Open(path, Options{
			ReceiptRetention: 30 * 24 * time.Hour,
			MaxDBBytes:       maximum,
			AllocSize:        int(pageSize),
			Now:              func() time.Time { return testNow },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x"))); err != nil {
			t.Fatal(err)
		}
		if got := fileSize(t, path); got > maximum {
			t.Fatalf("post-sync size = %d, maximum = %d", got, maximum)
		}

		tightPath, tightBaseline := initializedStoreFile(t, int(pageSize))
		tight, err := Open(tightPath, Options{
			ReceiptRetention: 30 * 24 * time.Hour,
			MaxDBBytes:       tightBaseline + projectedGrowth - 1,
			AllocSize:        int(pageSize),
			Now:              func() time.Time { return testNow },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tight.Close() })
		if _, err := tight.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x"))); !errors.Is(err, ErrStorageUnavailable) {
			t.Fatalf("tight error = %v", err)
		}
		if got := countCanonicalBatches(t, tight); got != 0 {
			t.Fatalf("tight committed batches = %d", got)
		}
	})

	t.Run("MinFreeBytes", func(t *testing.T) {
		path, baseline := initializedStoreFile(t, int(pageSize))
		available := 2 * projectedGrowth
		minimum := projectedGrowth
		store, err := Open(path, Options{
			ReceiptRetention: 30 * 24 * time.Hour,
			AllocSize:        int(pageSize),
			Now:              func() time.Time { return testNow },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		store.storageGuard = fileStorageGuard{
			minFreeBytes:   minimum,
			sizeBytes:      func() (int64, error) { return fileSize(t, path), nil },
			availableBytes: func() (int64, error) { return available, nil },
		}
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x"))); err != nil {
			t.Fatal(err)
		}
		actualGrowth := fileSize(t, path) - baseline
		if remaining := available - actualGrowth; remaining < minimum {
			t.Fatalf("post-sync free = %d, minimum = %d", remaining, minimum)
		}

		tightPath, _ := initializedStoreFile(t, int(pageSize))
		tight, err := Open(tightPath, Options{
			ReceiptRetention: 30 * 24 * time.Hour,
			AllocSize:        int(pageSize),
			Now:              func() time.Time { return testNow },
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tight.Close() })
		tight.storageGuard = fileStorageGuard{
			minFreeBytes:   minimum + 1,
			sizeBytes:      func() (int64, error) { return fileSize(t, tightPath), nil },
			availableBytes: func() (int64, error) { return available, nil },
		}
		if _, err := tight.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x"))); !errors.Is(err, ErrStorageUnavailable) {
			t.Fatalf("tight error = %v", err)
		}
		if got := countCanonicalBatches(t, tight); got != 0 {
			t.Fatalf("tight committed batches = %d", got)
		}
	})
}

func TestBoltStoreZeroDeliveryTimeUsesStoreClockForReceiptCollection(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkDelivered(context.Background(), "account-a", "batch-a", time.Time{}); err != nil {
		t.Fatal(err)
	}
	collected, err := store.CollectReceipts(context.Background(), testNow.Add(30*24*time.Hour+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if collected != 1 {
		t.Fatalf("collected receipts = %d", collected)
	}
}

func TestBoltStoreSyncFailurePreventsAcceptedOutcome(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	want := errors.New("injected sync failure")
	store.syncFn = func() error { return want }

	result, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("protobuf")))
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
	if result != (AcceptResult{}) {
		t.Fatalf("result = %#v", result)
	}
}

func TestBoltStoreUsesAccountNULBatchAndBigEndianFIFOKeys(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("protobuf"))); err != nil {
		t.Fatal(err)
	}

	err := store.db.View(func(tx *bolt.Tx) error {
		wantReceiptKey := []byte("account-a\x00batch-a")
		if got := tx.Bucket(receiptsBucket).Get(wantReceiptKey); got == nil {
			return errors.New("receipt does not use account + NUL + batch key")
		}
		key, value := tx.Bucket(fifoBucket).Cursor().First()
		if len(key) != 8 || binary.BigEndian.Uint64(key) != 1 {
			return fmt.Errorf("FIFO key = %x", key)
		}
		if string(value) != string(wantReceiptKey) {
			return fmt.Errorf("FIFO value = %q", value)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func openTestStore(t *testing.T, path string) *BoltStore {
	t.Helper()
	store, err := Open(path, Options{
		ReceiptRetention: 30 * 24 * time.Hour,
		Now:              func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func initializedStoreFile(t *testing.T, allocSize int) (string, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inbox.db")
	store, err := Open(path, Options{
		ReceiptRetention: 30 * 24 * time.Hour,
		AllocSize:        allocSize,
		Now:              func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	return path, fileSize(t, path)
}

func fileSize(t *testing.T, path string) int64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Size()
}

func validEnvelope(accountID, batchID string, payload []byte) Envelope {
	return Envelope{
		AccountID:      accountID,
		SessionID:      "session-a",
		InstallationID: "installation-a",
		BatchID:        batchID,
		PayloadSHA256:  sha256Hex(payload),
		Payload:        append([]byte(nil), payload...),
		Headers: TraceHeaders{
			SessionID:     "hermes-session-a",
			Entrypoint:    "desktop",
			RunID:         "run-a",
			SchemaVersion: "1",
		},
	}
}

func sha256Hex(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func countCanonicalBatches(t *testing.T, store *BoltStore) int {
	t.Helper()
	return countBucketEntries(t, store, batchesBucket)
}

func countBucketEntries(t *testing.T, store *BoltStore, bucket []byte) int {
	t.Helper()
	count := 0
	if err := store.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(bucket).ForEach(func(_, _ []byte) error {
			count++
			return nil
		})
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

type rejectingStorageGuard struct {
	calls int
}

func (guard *rejectingStorageGuard) Check(int64) error {
	guard.calls++
	return ErrStorageUnavailable
}
