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

func TestBoltStoreDuplicateAcceptDoesNotFsync(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	syncs := 0
	baseSync := store.syncFn
	store.syncFn = func() error { syncs++; return baseSync() }
	envelope := validEnvelope("account-a", "batch-a", []byte("protobuf"))

	if _, err := store.Accept(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if syncs != 1 {
		t.Fatalf("syncs after first accept = %d", syncs)
	}
	duplicate, err := store.Accept(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Outcome != ReceiptDuplicate {
		t.Fatalf("outcome = %q", duplicate.Outcome)
	}
	if syncs != 1 {
		t.Fatalf("syncs after duplicate accept = %d", syncs)
	}
}

func TestBoltStoreEmptyCollectionDoesNotFsync(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	syncs := 0
	baseSync := store.syncFn
	store.syncFn = func() error { syncs++; return baseSync() }

	collected, err := store.CollectReceipts(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if collected != 0 || syncs != 0 {
		t.Fatalf("collected = %d, syncs = %d", collected, syncs)
	}
}

func TestBoltStoreViewsDoNotBlockOnWriterMutexDuringSync(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-a", []byte("protobuf"))); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	var block sync.Once
	baseSync := store.syncFn
	store.syncFn = func() error {
		block.Do(func() {
			close(entered)
			<-release
		})
		return baseSync()
	}
	accepted := make(chan error, 1)
	go func() {
		_, err := store.Accept(context.Background(), validEnvelope("account-a", "batch-b", []byte("protobuf-b")))
		accepted <- err
	}()
	<-entered

	viewed := make(chan error, 1)
	go func() {
		if _, err := store.PeekEligible(context.Background(), testNow); err != nil {
			viewed <- err
			return
		}
		_, err := store.NextRetryAt(context.Background())
		viewed <- err
	}()
	select {
	case err := <-viewed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("view operations blocked behind the writer mutex during fsync")
	}
	close(release)
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
}

func TestBoltStoreViewsReturnErrClosedAfterClose(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PeekEligible(context.Background(), testNow); !errors.Is(err, ErrClosed) {
		t.Fatalf("PeekEligible error = %v", err)
	}
	if _, err := store.NextRetryAt(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("NextRetryAt error = %v", err)
	}
}

func TestBoltStoreCollectsExpiredQuarantineAcrossRestartWithoutTouchingPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store, err := Open(path, Options{
		ReceiptRetention:    30 * 24 * time.Hour,
		QuarantineRetention: 24 * time.Hour,
		Now:                 func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, batchID := range []string{"bad", "pending"} {
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", batchID, []byte(batchID))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkQuarantined(context.Background(), "account-a", "bad", "upstream_400", testNow); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path, Options{
		ReceiptRetention:    30 * 24 * time.Hour,
		QuarantineRetention: 24 * time.Hour,
		Now:                 func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	collected, err := store.CollectReceipts(context.Background(), testNow.Add(24*time.Hour-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if collected != 0 {
		t.Fatalf("collected fresh quarantine = %d", collected)
	}
	collected, err = store.CollectReceipts(context.Background(), testNow.Add(24*time.Hour+time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if collected != 2 {
		t.Fatalf("collected expired quarantine payload and receipt = %d", collected)
	}
	if got := countBucketEntries(t, store, quarantineBucket); got != 0 {
		t.Fatalf("quarantine entries = %d", got)
	}
	if got := countCanonicalBatches(t, store); got != 1 {
		t.Fatalf("pending batches = %d", got)
	}
	head, err := store.PeekEligible(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.BatchID != "pending" {
		t.Fatalf("pending head = %#v", head)
	}
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "pending", []byte("pending"))); err != nil {
		t.Fatalf("pending receipt lost: %v", err)
	}
}

func TestBoltStoreCapsQuarantinePayloadsWithoutDroppingReceipts(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "inbox.db"), Options{
		ReceiptRetention:     30 * 24 * time.Hour,
		MaxQuarantineEntries: 2,
		Now:                  func() time.Time { return testNow },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, batchID := range []string{"q1", "q2", "q3"} {
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", batchID, []byte(batchID))); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkQuarantined(context.Background(), "account-a", batchID, "upstream_400", testNow); err != nil {
			t.Fatal(err)
		}
	}

	collected, err := store.CollectReceipts(context.Background(), testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if collected != 1 {
		t.Fatalf("evicted quarantine payloads = %d", collected)
	}
	if got := countBucketEntries(t, store, quarantineBucket); got != 2 {
		t.Fatalf("quarantine entries = %d", got)
	}
	if got := countBucketEntries(t, store, receiptsBucket); got != 3 {
		t.Fatalf("receipts = %d", got)
	}
	if err := store.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket(quarantineBucket).Get([]byte("account-a\x00q1")) != nil {
			return errors.New("oldest quarantine payload was not evicted")
		}
		for _, batchID := range []string{"q2", "q3"} {
			if tx.Bucket(quarantineBucket).Get([]byte("account-a\x00"+batchID)) == nil {
				return fmt.Errorf("newer quarantine payload %s was evicted", batchID)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "q1", []byte("q1"))); err != nil {
		t.Fatalf("evicted payload lost its idempotency receipt: %v", err)
	}
}

func TestBoltStoreReplayQuarantinedRestoresFIFOWithIdempotentReceipt(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	envelope := validEnvelope("account-a", "bad", []byte("protobuf"))
	if _, err := store.Accept(context.Background(), envelope); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQuarantined(context.Background(), "account-a", "bad", "upstream_401", testNow); err != nil {
		t.Fatal(err)
	}

	if err := store.ReplayQuarantined(context.Background(), "account-a", "bad"); err != nil {
		t.Fatal(err)
	}
	head, err := store.PeekEligible(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.BatchID != "bad" || head.Attempts != 0 || head.LastError != "" || string(head.Payload) != "protobuf" {
		t.Fatalf("replayed head = %#v", head)
	}
	if got := countBucketEntries(t, store, quarantineBucket); got != 0 {
		t.Fatalf("quarantine entries = %d", got)
	}
	duplicate, err := store.Accept(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Outcome != ReceiptDuplicate {
		t.Fatalf("replayed batch lost idempotency: outcome = %q", duplicate.Outcome)
	}
	if err := store.ReplayQuarantined(context.Background(), "account-a", "bad"); !errors.Is(err, ErrBatchNotFound) {
		t.Fatalf("second replay error = %v", err)
	}
}

func TestBoltStoreReplayAllQuarantinedPreservesOriginalOrder(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	for _, batchID := range []string{"q1", "q2"} {
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", batchID, []byte(batchID))); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkQuarantined(context.Background(), "account-a", batchID, "upstream_401", testNow); err != nil {
			t.Fatal(err)
		}
	}

	replayed, err := store.ReplayAllQuarantined(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if replayed != 2 {
		t.Fatalf("replayed = %d", replayed)
	}
	for _, want := range []string{"q1", "q2"} {
		head, err := store.PeekEligible(context.Background(), testNow)
		if err != nil {
			t.Fatal(err)
		}
		if head == nil || head.BatchID != want {
			t.Fatalf("head = %#v, want %s", head, want)
		}
		if err := store.MarkDelivered(context.Background(), "account-a", want, testNow); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBoltStoreSteadyStateDeliveredReceiptsAndQuarantineStayBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	now := testNow
	store, err := Open(path, Options{
		ReceiptRetention:     time.Hour,
		QuarantineRetention:  time.Hour,
		MaxQuarantineEntries: 4,
		Now:                  func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	const cycles = 40
	var halfway int64
	for i := range cycles {
		deliveredID := fmt.Sprintf("delivered-%03d", i)
		quarantinedID := fmt.Sprintf("quarantined-%03d", i)
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", deliveredID, []byte("payload"))); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkDelivered(context.Background(), "account-a", deliveredID, now); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", quarantinedID, []byte("payload"))); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkQuarantined(context.Background(), "account-a", quarantinedID, "upstream_400", now); err != nil {
			t.Fatal(err)
		}
		now = now.Add(2 * time.Hour)
		if _, err := store.CollectReceipts(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		if i == cycles/2 {
			halfway = fileSize(t, path)
		}
	}
	if final := fileSize(t, path); final > halfway {
		t.Fatalf("database kept growing at steady state: halfway = %d, final = %d", halfway, final)
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
