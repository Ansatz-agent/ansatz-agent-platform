package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/delivery"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const continuityBearerOne = "first-upload-token-that-is-never-a-receipt-123456"
const continuityBearerTwo = "second-upload-token-that-is-never-a-receipt-12345"

func TestLostClientResponseAndGatewayRestartKeepOneLogicalBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	upstream := &continuityUpstream{}
	first := newContinuityGateway(t, path, upstream, "first-token-id")

	firstResponse := postContinuityTrace(t, first.http.URL, continuityBearerOne, validBody(t))
	discardResponse(t, firstResponse)
	if !eventuallyContinuity(t, time.Second, func() bool { return upstream.Attempts() >= 1 }) {
		t.Fatal("first gateway worker did not attempt durable delivery")
	}
	first.Close(t)

	upstream.SetHealthy(true)
	second := newContinuityGateway(t, path, upstream, "replacement-token-id")
	defer second.Close(t)

	retry := postContinuityTrace(t, second.http.URL, continuityBearerTwo, validBody(t))
	defer retry.Body.Close()
	if retry.StatusCode != http.StatusOK {
		t.Fatalf("retry status = %d", retry.StatusCode)
	}
	if got := retry.Header.Get("X-Trace-Receipt"); got != string(inbox.ReceiptDuplicate) {
		t.Fatalf("retry receipt = %q", got)
	}
	if got := retry.Header.Get("X-Trace-Batch-ID"); got != batchID {
		t.Fatalf("retry batch ID = %q", got)
	}

	conflictBody := append(append([]byte(nil), validBody(t)...), 0x78, 0x01)
	conflict := postContinuityTrace(t, second.http.URL, continuityBearerTwo, conflictBody)
	defer conflict.Body.Close()
	if conflict.StatusCode != http.StatusConflict {
		t.Fatalf("conflict status = %d", conflict.StatusCode)
	}

	if !eventuallyContinuity(t, time.Second, func() bool { return upstream.Successes() == 1 }) {
		t.Fatalf("successful upstream deliveries = %d", upstream.Successes())
	}
	if got := second.ReceiptCount(t); got != 1 {
		t.Fatalf("stored receipts = %d", got)
	}
	if got := upstream.LogicalIdentities(); len(got) != 1 {
		t.Fatalf("logical upstream identities = %v", got)
	}
}

type continuityGateway struct {
	store      *inbox.BoltStore
	http       *httptest.Server
	cancel     context.CancelFunc
	workerDone <-chan error
}

func newContinuityGateway(t *testing.T, path string, upstream *continuityUpstream, tokenID string) *continuityGateway {
	t.Helper()
	store, err := inbox.Open(path, inbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	worker := delivery.New(store, upstream, delivery.Options{
		BaseRetry: 5 * time.Millisecond,
		MaxRetry:  5 * time.Millisecond,
		Random:    func() float64 { return 0 },
	})
	gateway, err := New(Config{
		Introspector: continuityIntrospector{tokenID: tokenID},
		Store:        store,
		Trigger:      worker.Trigger,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		_ = store.Close()
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	return &continuityGateway{
		store: store, http: httptest.NewServer(gateway.Handler()), cancel: cancel, workerDone: workerDone,
	}
}

func (g *continuityGateway) Close(t *testing.T) {
	t.Helper()
	g.http.Close()
	g.cancel()
	if err := <-g.workerDone; err != nil {
		t.Errorf("worker shutdown: %v", err)
	}
	if err := g.store.Close(); err != nil {
		t.Errorf("store close: %v", err)
	}
}

func (g *continuityGateway) ReceiptCount(t *testing.T) int {
	t.Helper()
	// Receipt cleanup returns how many persisted receipts it removed. A future
	// cutoff therefore proves how many receipts were durable at this boundary.
	count, err := g.store.CollectReceipts(context.Background(), time.Now().Add(31*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return count
}

type continuityIntrospector struct{ tokenID string }

func (i continuityIntrospector) Introspect(_ context.Context, bearer string) (auth.Principal, error) {
	if bearer != continuityBearerOne && bearer != continuityBearerTwo {
		return auth.Principal{}, auth.ErrUnavailable
	}
	return auth.Principal{
		TokenID:        i.tokenID,
		AccountID:      "22222222-2222-4222-8222-222222222222",
		SessionID:      "33333333-3333-4333-8333-333333333333",
		Username:       "continuity-user",
		InstallationID: "44444444-4444-4444-8444-444444444444",
		ExpiresAt:      time.Now().Add(time.Hour),
		Scope:          "trace:write",
		Audience:       "ansatz-trace-gateway",
	}, nil
}

type continuityUpstream struct {
	mu         sync.Mutex
	healthy    bool
	attempts   int
	successes  int
	identities map[string]struct{}
}

func (u *continuityUpstream) Send(_ context.Context, payload []byte) (delivery.Response, error) {
	identity, err := traceSpanIdentity(payload)
	if err != nil {
		return delivery.Response{Status: http.StatusBadRequest}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.attempts++
	if u.identities == nil {
		u.identities = make(map[string]struct{})
	}
	if len(u.identities) > 0 {
		if _, seen := u.identities[identity]; !seen {
			return delivery.Response{Status: http.StatusConflict}, errors.New("second logical upstream identity")
		}
	}
	u.identities[identity] = struct{}{}
	if !u.healthy {
		return delivery.Response{Status: http.StatusServiceUnavailable}, nil
	}
	u.successes++
	return delivery.Response{Status: http.StatusOK}, nil
}

func (u *continuityUpstream) SetHealthy(healthy bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.healthy = healthy
}

func (u *continuityUpstream) Attempts() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.attempts
}

func (u *continuityUpstream) Successes() int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.successes
}

func (u *continuityUpstream) LogicalIdentities() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	identities := make([]string, 0, len(u.identities))
	for identity := range u.identities {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	return identities
}

func postContinuityTrace(t *testing.T, baseURL, bearer string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, baseURL+"/v1/traces", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	request.Header.Set("Authorization", "Bearer "+bearer)
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Content-Encoding", "identity")
	request.Header.Set("X-Hermes-Session-ID", "continuity-session")
	request.Header.Set("X-Trace-Entrypoint", "desktop")
	request.Header.Set("X-Trace-Run-ID", "continuity-run")
	request.Header.Set("X-Telemetry-Schema-Version", "1")
	request.Header.Set("Idempotency-Key", batchID)
	request.Header.Set("X-Trace-Payload-SHA256", hex.EncodeToString(digest[:]))
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func discardResponse(t *testing.T, response *http.Response) {
	t.Helper()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("first upload status = %d; body=%s", response.StatusCode, body)
	}
}

func eventuallyContinuity(t *testing.T, timeout time.Duration, condition func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return true
		}
		time.Sleep(time.Millisecond)
	}
	return condition()
}

func traceSpanIdentity(payload []byte) (string, error) {
	var request collectortracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(payload, &request); err != nil {
		return "", err
	}
	identities := make([]string, 0)
	for _, resource := range request.ResourceSpans {
		for _, scope := range resource.ScopeSpans {
			for _, span := range scope.Spans {
				identities = append(identities, hex.EncodeToString(span.TraceId)+":"+hex.EncodeToString(span.SpanId))
			}
		}
	}
	if len(identities) == 0 {
		return "", io.ErrUnexpectedEOF
	}
	sort.Strings(identities)
	return strings.Join(identities, ","), nil
}
