package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
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

func TestBoltStoreReopenPreservesFIFOAndDelayedHeadBlocksLaterBatches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := openTestStore(t, path)
	for _, batchID := range []string{"one", "two"} {
		if _, err := store.Accept(context.Background(), validEnvelope("account-a", batchID, []byte(batchID))); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.MarkRetry(context.Background(), "one", Retry{
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
	if err := store.MarkDelivered(context.Background(), "one", testNow.Add(time.Minute)); err != nil {
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

func TestBoltStoreQuarantineRemovesFIFOButRetainsCanonicalRecord(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "inbox.db"))
	if _, err := store.Accept(context.Background(), validEnvelope("account-a", "bad", []byte("protobuf"))); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkQuarantined(context.Background(), "bad", "upstream_400", testNow); err != nil {
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
