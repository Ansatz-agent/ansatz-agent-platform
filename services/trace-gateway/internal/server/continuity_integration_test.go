package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	upstream := newContinuityUpstream()
	first := newGatedContinuityGateway(t, path, upstream, "first-token-id")

	sendAndAbandonAfterDurableAccept(t, first, continuityBearerOne, validBody(t))
	first.WaitForResponseWriteFailure(t)
	if !eventuallyContinuity(t, time.Second, upstream.UnhealthyAttemptStarted) {
		t.Fatal("first gateway worker did not begin its controlled unavailable attempt")
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
	second.StopWorker(t)
	if got := second.ReceiptCount(t); got != 1 {
		t.Fatalf("stored receipts = %d", got)
	}
	if got := upstream.LogicalIdentities(); len(got) != 1 {
		t.Fatalf("logical upstream identities = %v", got)
	}
	if calls, successes, postSuccessCalls, violation := upstream.Snapshot(); calls != 2 || successes != 1 || postSuccessCalls != 0 || violation != nil {
		t.Fatalf("upstream calls=%d successes=%d post_success_calls=%d violation=%v", calls, successes, postSuccessCalls, violation)
	}
}

type continuityGateway struct {
	store         *inbox.BoltStore
	http          *httptest.Server
	cancel        context.CancelFunc
	workerDone    <-chan error
	workerStopped bool
	gate          *acceptGateStore
	writeErrors   <-chan error
}

func newContinuityGateway(t *testing.T, path string, upstream *continuityUpstream, tokenID string) *continuityGateway {
	return newContinuityGatewayWithAdmissionGate(t, path, upstream, tokenID, false)
}

func newContinuityGatewayWithAdmissionGate(t *testing.T, path string, upstream *continuityUpstream, tokenID string, gated bool) *continuityGateway {
	t.Helper()
	store, err := inbox.Open(path, inbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var admission inbox.Store = store
	var gate *acceptGateStore
	if gated {
		gate = newAcceptGateStore(store)
		admission = gate
	}
	worker := delivery.New(store, upstream, delivery.Options{
		BaseRetry: 5 * time.Millisecond,
		MaxRetry:  5 * time.Millisecond,
		Random:    func() float64 { return 0 },
	})
	gateway, err := New(Config{
		Introspector: continuityIntrospector{tokenID: tokenID},
		Store:        admission,
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
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		cancel()
		<-workerDone
		_ = store.Close()
		t.Fatal(err)
	}
	writeErrors := make(chan error, 1)
	httpServer := httptest.NewUnstartedServer(gateway.Handler())
	httpServer.Listener = &responseObservingListener{Listener: listener, writeErrors: writeErrors}
	httpServer.Start()
	return &continuityGateway{
		store: store, http: httpServer, cancel: cancel, workerDone: workerDone, gate: gate, writeErrors: writeErrors,
	}
}

func newGatedContinuityGateway(t *testing.T, path string, upstream *continuityUpstream, tokenID string) *continuityGateway {
	return newContinuityGatewayWithAdmissionGate(t, path, upstream, tokenID, true)
}

func (g *continuityGateway) Close(t *testing.T) {
	t.Helper()
	g.http.Close()
	g.StopWorker(t)
	if err := g.store.Close(); err != nil {
		t.Errorf("store close: %v", err)
	}
}

func (g *continuityGateway) StopWorker(t *testing.T) {
	t.Helper()
	if g.workerStopped {
		return
	}
	g.workerStopped = true
	g.cancel()
	if err := <-g.workerDone; err != nil {
		t.Errorf("worker shutdown: %v", err)
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
	mu                     sync.Mutex
	healthy                bool
	calls                  int
	successes              int
	postSuccessCalls       int
	healthySuccessObserved bool
	violation              error
	identities             map[string]struct{}
	unhealthyAttempt       chan struct{}
	unhealthyOnce          sync.Once
}

func newContinuityUpstream() *continuityUpstream {
	return &continuityUpstream{unhealthyAttempt: make(chan struct{})}
}

func (u *continuityUpstream) Send(ctx context.Context, payload []byte) (delivery.Response, error) {
	identity, err := traceSpanIdentity(payload)
	if err != nil {
		return delivery.Response{Status: http.StatusBadRequest}, err
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls++
	if u.healthySuccessObserved {
		u.postSuccessCalls++
		u.violation = errors.New("upstream invoked after first healthy success")
		return delivery.Response{Status: http.StatusConflict}, u.violation
	}
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
		u.unhealthyOnce.Do(func() { close(u.unhealthyAttempt) })
		u.mu.Unlock()
		<-ctx.Done()
		u.mu.Lock()
		return delivery.Response{}, ctx.Err()
	}
	u.healthySuccessObserved = true
	u.successes++
	return delivery.Response{Status: http.StatusOK}, nil
}

func (u *continuityUpstream) SetHealthy(healthy bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.healthy = healthy
}

func (u *continuityUpstream) UnhealthyAttemptStarted() bool {
	select {
	case <-u.unhealthyAttempt:
		return true
	default:
		return false
	}
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

func (u *continuityUpstream) Snapshot() (calls, successes, postSuccessCalls int, violation error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.calls, u.successes, u.postSuccessCalls, u.violation
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

func sendAndAbandonAfterDurableAccept(t *testing.T, gateway *continuityGateway, bearer string, body []byte) {
	t.Helper()
	if gateway.gate == nil {
		t.Fatal("lost-response upload requires an admission gate")
	}
	parsed, err := url.Parse(gateway.http.URL)
	if err != nil {
		t.Fatal(err)
	}
	address, err := net.ResolveTCPAddr("tcp", parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := net.DialTCP("tcp", nil, address)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(body)
	headers := fmt.Sprintf(
		"POST /v1/traces HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\nContent-Type: application/x-protobuf\r\nContent-Encoding: identity\r\nX-Hermes-Session-ID: continuity-session\r\nX-Trace-Entrypoint: desktop\r\nX-Trace-Run-ID: continuity-run\r\nX-Telemetry-Schema-Version: 1\r\nIdempotency-Key: %s\r\nX-Trace-Payload-SHA256: %s\r\nContent-Length: %d\r\nConnection: close\r\n\r\n",
		parsed.Host, bearer, batchID, hex.EncodeToString(digest[:]), len(body),
	)
	if _, err := io.WriteString(connection, headers); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if _, err := connection.Write(body); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	select {
	case <-gateway.gate.accepted:
	case <-time.After(time.Second):
		connection.Close()
		t.Fatal("durable acceptance did not complete")
	}
	if err := connection.SetLinger(0); err != nil {
		connection.Close()
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	gateway.gate.Release()
}

func (g *continuityGateway) WaitForResponseWriteFailure(t *testing.T) {
	t.Helper()
	select {
	case err := <-g.writeErrors:
		if err == nil {
			t.Fatal("response write unexpectedly succeeded after client reset")
		}
	case <-time.After(time.Second):
		t.Fatal("gateway did not observe the abandoned client connection")
	}
}

type acceptGateStore struct {
	inbox.Store
	accepted     chan struct{}
	release      chan struct{}
	acceptedOnce sync.Once
	releaseOnce  sync.Once
}

func newAcceptGateStore(store inbox.Store) *acceptGateStore {
	return &acceptGateStore{Store: store, accepted: make(chan struct{}), release: make(chan struct{})}
}

func (s *acceptGateStore) Accept(ctx context.Context, envelope inbox.Envelope) (inbox.AcceptResult, error) {
	result, err := s.Store.Accept(ctx, envelope)
	if err != nil {
		return result, err
	}
	s.acceptedOnce.Do(func() { close(s.accepted) })
	<-s.release
	return result, nil
}

func (s *acceptGateStore) Release() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type responseObservingListener struct {
	net.Listener
	writeErrors chan<- error
}

func (l *responseObservingListener) Accept() (net.Conn, error) {
	connection, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &responseObservingConn{Conn: connection, writeErrors: l.writeErrors}, nil
}

type responseObservingConn struct {
	net.Conn
	writeErrors chan<- error
}

func (c *responseObservingConn) Write(body []byte) (int, error) {
	written, err := c.Conn.Write(body)
	if err != nil {
		select {
		case c.writeErrors <- err:
		default:
		}
	}
	return written, err
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
