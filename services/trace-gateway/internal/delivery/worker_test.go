package delivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
)

var testNow = time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)

func TestWorkerRetainsHeadAndResumesFIFOAfterRestart(t *testing.T) {
	path, store := durableFixture(t, "one", "two")
	now := testNow
	upstream := &fakeUpstream{responses: []response{
		{status: 503, retryAfter: "600"},
		{status: 200},
		{status: 200},
	}}
	worker := New(store, upstream, deterministicOptions(&now))

	runOne(t, worker)
	if !slices.Equal(upstream.seenPayloads(), []string{"one"}) {
		t.Fatalf("seen = %v", upstream.seenPayloads())
	}
	if got := pending(t, store, now.Add(600*time.Second)).NextRetry.Sub(testNow); got != 600*time.Second {
		t.Fatalf("retry = %s", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = reopenStore(t, path, &now)
	now = now.Add(600 * time.Second)
	runUntilIdle(t, New(store, upstream, deterministicOptions(&now)))
	if !slices.Equal(upstream.seenPayloads(), []string{"one", "one", "two"}) {
		t.Fatalf("seen = %v", upstream.seenPayloads())
	}
}

func TestWorkerUsesFullJitterForRetry(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	upstream := &fakeUpstream{responses: []response{{status: 503}}}
	options := deterministicOptions(&now)
	options.BaseRetry = 4 * time.Second
	options.Random = func() float64 { return 0.25 }

	runOne(t, New(store, upstream, options))
	if got := pending(t, store, now.Add(time.Second)).NextRetry; !got.Equal(testNow.Add(time.Second)) {
		t.Fatalf("next retry = %s", got)
	}
}

func TestWorkerSchedulesRetryFromFailedSendCompletion(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	upstream := &fakeUpstream{
		responses: []response{{status: 503}},
		onSend:    func() { now = now.Add(3 * time.Minute) },
	}
	options := deterministicOptions(&now)
	options.BaseRetry = 4 * time.Second

	runOne(t, New(store, upstream, options))
	nextRetry, err := store.NextRetryAt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(3*time.Minute + 4*time.Second); !nextRetry.Equal(want) {
		t.Fatalf("next retry = %s, want %s", nextRetry, want)
	}
}

func TestWorkerZeroJitterStillPersistsNonzeroRetryDelay(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	options := deterministicOptions(&now)
	options.Random = func() float64 { return 0 }

	runOne(t, New(store, &fakeUpstream{responses: []response{{status: 503}}}, options))
	nextRetry, err := store.NextRetryAt(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := testNow.Add(time.Millisecond); !nextRetry.Equal(want) {
		t.Fatalf("next retry = %s, want %s", nextRetry, want)
	}
}

func TestWorkerClampsUpstreamRetryAfterToConfiguredCeiling(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		cap        time.Duration
		want       time.Duration
	}{
		{"honors delta seconds under the ceiling", "600", time.Hour, 600 * time.Second},
		{"clamps absurd delta seconds", "31536000", time.Hour, time.Hour},
		{"clamps unparseable huge delta seconds", "999999999999999999999", time.Hour, time.Second},
		{"clamps far future HTTP date", testNow.Add(365 * 24 * time.Hour).Format(http.TimeFormat), time.Hour, time.Hour},
		{"honors near future HTTP date", testNow.Add(90 * time.Second).Format(http.TimeFormat), time.Hour, 90 * time.Second},
		{"applies a custom ceiling", "600", 2 * time.Minute, 2 * time.Minute},
		{"defaults the ceiling when unset", "999999999", 0, defaultRetryAfterCap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, store := durableFixture(t, "one")
			now := testNow
			options := deterministicOptions(&now)
			options.RetryAfterCap = test.cap
			upstream := &fakeUpstream{responses: []response{{status: 503, retryAfter: test.retryAfter}}}

			runOne(t, New(store, upstream, options))
			nextRetry, err := store.NextRetryAt(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if want := testNow.Add(test.want); !nextRetry.Equal(want) {
				t.Fatalf("next retry = %s, want %s", nextRetry, want)
			}
		})
	}
}

func TestWorkerRetriesRequestTimeoutInsteadOfQuarantining(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	upstream := &fakeUpstream{responses: []response{{status: 408}, {status: 200}}}
	worker := New(store, upstream, deterministicOptions(&now))

	runOne(t, worker)
	batch := pending(t, store, now.Add(time.Second))
	if batch.LastError != "upstream_408" {
		t.Fatalf("last error = %q", batch.LastError)
	}
	now = now.Add(time.Second)
	runUntilIdle(t, worker)
	if !slices.Equal(upstream.seenPayloads(), []string{"one", "one"}) {
		t.Fatalf("seen = %v", upstream.seenPayloads())
	}
}

func TestWorkerGivesOperatorFixableRejectionsBoundedRetriesBeforeQuarantine(t *testing.T) {
	for _, status := range []int{401, 403, 404} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			_, store := durableFixture(t, "one")
			now := testNow
			options := deterministicOptions(&now)
			options.OperatorRetryLimit = 3
			upstream := &fakeUpstream{responses: []response{
				{status: status}, {status: status}, {status: status},
			}}
			recording := &recordingStore{Store: store}
			worker := New(recording, upstream, options)

			for range 2 {
				runOne(t, worker)
				now = now.Add(time.Minute)
			}
			if recording.quarantined != (quarantineCall{}) {
				t.Fatalf("quarantined before the retry budget: %#v", recording.quarantined)
			}
			runOne(t, worker)
			want := quarantineCall{accountID: "account-a", batchID: "one", errorClass: "upstream_" + strconv.Itoa(status), at: now}
			if recording.quarantined != want {
				t.Fatalf("quarantine = %#v, want %#v", recording.quarantined, want)
			}
			if !slices.Equal(upstream.seenPayloads(), []string{"one", "one", "one"}) {
				t.Fatalf("seen = %v", upstream.seenPayloads())
			}
		})
	}
}

func TestWorkerDefaultsOperatorRetryLimitToBoundedBudget(t *testing.T) {
	worker := New(nil, nil, Options{})
	if worker.operatorRetryLimit <= 0 {
		t.Fatalf("operator retry limit = %d, want positive default", worker.operatorRetryLimit)
	}
}

func TestWorkerQuarantinesPermanentUpstreamRejection(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	recording := &recordingStore{Store: store}

	runOne(t, New(recording, &fakeUpstream{responses: []response{{status: 400}}}, deterministicOptions(&now)))
	if got := recording.quarantined; got != (quarantineCall{accountID: "account-a", batchID: "one", errorClass: "upstream_400", at: testNow}) {
		t.Fatalf("quarantine = %#v", got)
	}
	batch, err := store.PeekEligible(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if batch != nil {
		t.Fatalf("quarantined batch remains eligible: %#v", batch)
	}
}

func TestWorkerRetriesResponseLossWithoutRecordingUpstreamError(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	upstream := &fakeUpstream{responses: []response{
		{status: 200, err: errors.New("lost response body")},
		{status: 200},
	}}
	worker := New(store, upstream, deterministicOptions(&now))

	runOne(t, worker)
	batch := pending(t, store, now.Add(time.Second))
	if batch.LastError != "upstream_network" {
		t.Fatalf("last error = %q", batch.LastError)
	}
	now = now.Add(time.Second)
	runUntilIdle(t, worker)
	if !slices.Equal(upstream.seenPayloads(), []string{"one", "one"}) {
		t.Fatalf("seen = %v", upstream.seenPayloads())
	}
}

func TestWorkerTriggerCoalescesWakeups(t *testing.T) {
	_, store := durableFixture(t)
	now := testNow
	worker := New(store, &fakeUpstream{}, deterministicOptions(&now))

	for range 10 {
		worker.Trigger()
	}
	if got := len(worker.trigger); got != 1 {
		t.Fatalf("queued wakeups = %d", got)
	}
}

func TestWorkerRunSchedulesRetryWithoutAnotherTrigger(t *testing.T) {
	_, store := durableFixture(t, "one")
	now := testNow
	upstream := &fakeUpstream{responses: []response{{status: 503}, {status: 200}}}
	options := deterministicOptions(&now)
	options.Now = time.Now
	options.BaseRetry = 10 * time.Millisecond
	options.MaxRetry = 10 * time.Millisecond
	worker := New(store, upstream, options)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	deadline := time.After(time.Second)
	for len(upstream.seenPayloads()) < 2 {
		select {
		case <-deadline:
			t.Fatalf("worker did not resume scheduled retry: seen = %v", upstream.seenPayloads())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v", err)
	}
}

type response struct {
	status     int
	retryAfter string
	err        error
}

type fakeUpstream struct {
	mu        sync.Mutex
	responses []response
	seen      []string
	onSend    func()
}

func (u *fakeUpstream) Send(_ context.Context, payload []byte) (Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.seen = append(u.seen, string(payload))
	if u.onSend != nil {
		u.onSend()
	}
	if len(u.responses) == 0 {
		return Response{Status: 200}, nil
	}
	result := u.responses[0]
	u.responses = u.responses[1:]
	return Response{Status: result.status, RetryAfter: result.retryAfter}, result.err
}

func (u *fakeUpstream) seenPayloads() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.seen...)
}

type quarantineCall struct {
	accountID  string
	batchID    string
	errorClass string
	at         time.Time
}

type recordingStore struct {
	inbox.Store
	quarantined quarantineCall
}

func (s *recordingStore) MarkQuarantined(ctx context.Context, accountID, batchID, errorClass string, at time.Time) error {
	s.quarantined = quarantineCall{accountID: accountID, batchID: batchID, errorClass: errorClass, at: at}
	return s.Store.MarkQuarantined(ctx, accountID, batchID, errorClass, at)
}

func durableFixture(t *testing.T, batchIDs ...string) (string, *inbox.BoltStore) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inbox.db")
	store := reopenStore(t, path, testClock())
	for _, batchID := range batchIDs {
		payload := []byte(batchID)
		if _, err := store.Accept(context.Background(), inbox.Envelope{
			AccountID: "account-a", BatchID: batchID, Payload: payload, PayloadSHA256: payloadSHA256(payload),
		}); err != nil {
			t.Fatal(err)
		}
	}
	return path, store
}

func reopenStore(t *testing.T, path string, now *time.Time) *inbox.BoltStore {
	t.Helper()
	store, err := inbox.Open(path, inbox.Options{Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testClock() *time.Time {
	now := testNow
	return &now
}

func deterministicOptions(now *time.Time) Options {
	return Options{
		Now:       func() time.Time { return *now },
		Random:    func() float64 { return 1 },
		BaseRetry: time.Second,
		MaxRetry:  5 * time.Minute,
	}
}

func runOne(t *testing.T, worker *Worker) {
	t.Helper()
	delivered, err := worker.deliverHead(context.Background(), worker.now())
	if err != nil {
		t.Fatal(err)
	}
	if !delivered {
		t.Fatal("worker found no eligible batch")
	}
}

func runUntilIdle(t *testing.T, worker *Worker) {
	t.Helper()
	for {
		delivered, err := worker.deliverHead(context.Background(), worker.now())
		if err != nil {
			t.Fatal(err)
		}
		if !delivered {
			return
		}
	}
}

func pending(t *testing.T, store inbox.Store, now time.Time) *inbox.Batch {
	t.Helper()
	batch, err := store.PeekEligible(context.Background(), now)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil {
		t.Fatal("no eligible pending batch")
	}
	return batch
}

func payloadSHA256(payload []byte) string {
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}
