package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	uploadBearer = "valid-upload-token-that-must-not-leak-1234567890"
	batchID      = "11111111-1111-4111-8111-111111111111"
)

type fakeIntrospector struct{ err error }

func (f fakeIntrospector) Introspect(_ context.Context, _ string) (auth.Principal, error) {
	return auth.Principal{
		TokenID:        "token-id",
		AccountID:      "22222222-2222-4222-8222-222222222222",
		SessionID:      "33333333-3333-4333-8333-333333333333",
		UserID:         "mutable-display-id",
		Username:       "yiyuxiao",
		InstallationID: "44444444-4444-4444-8444-444444444444",
		ExpiresAt:      time.Now().Add(time.Hour),
		Scope:          "trace:write",
		Audience:       "ansatz-trace-gateway",
	}, f.err
}

type denyLimiter struct{}

func (denyLimiter) Allow(string, string, time.Time) bool { return false }

type recordingStore struct {
	inbox.Store
	result  inbox.AcceptResult
	err     error
	syncErr error

	mu        sync.Mutex
	envelopes []inbox.Envelope
}

func (s *recordingStore) Sync() error { return s.syncErr }

func (s *recordingStore) Accept(_ context.Context, envelope inbox.Envelope) (inbox.AcceptResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envelopes = append(s.envelopes, envelope)
	return s.result, s.err
}

func (s *recordingStore) envelope(t *testing.T) inbox.Envelope {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.envelopes) != 1 {
		t.Fatalf("accepted envelopes = %d, want 1", len(s.envelopes))
	}
	return s.envelopes[0]
}

type blockingStore struct {
	inbox.Store
	accepted chan struct{}
}

type healthStore struct {
	inbox.Store
	syncErr error
}

func (s healthStore) Sync() error { return s.syncErr }

func (s *blockingStore) Accept(_ context.Context, envelope inbox.Envelope) (inbox.AcceptResult, error) {
	<-s.accepted
	return inbox.AcceptResult{BatchID: envelope.BatchID, Outcome: inbox.ReceiptAccepted}, nil
}

func TestHandlerAcknowledgesOnlySyncedInboxOwnership(t *testing.T) {
	store := &blockingStore{accepted: make(chan struct{})}
	handler := mustHandler(t, Config{Introspector: fakeIntrospector{}, Store: store})
	body := validBody(t)
	request := validRequest(body)
	response := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { handler.ServeHTTP(response, request); close(done) }()
	assertNotClosed(t, done)
	close(store.accepted)
	<-done
	assertSuccess(t, response, inbox.ReceiptAccepted)
}

func TestHandlerStoresCanonicalBatchUnderImmutableAccountIdentity(t *testing.T) {
	store := &recordingStore{result: inbox.AcceptResult{BatchID: batchID, Outcome: inbox.ReceiptAccepted}}
	handler := mustHandler(t, Config{Introspector: fakeIntrospector{}, Store: store})
	body := validBody(t)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validRequest(body))
	assertSuccess(t, response, inbox.ReceiptAccepted)

	envelope := store.envelope(t)
	if envelope.AccountID != "22222222-2222-4222-8222-222222222222" {
		t.Fatalf("account ID = %q", envelope.AccountID)
	}
	if envelope.SessionID != "33333333-3333-4333-8333-333333333333" || envelope.InstallationID != "44444444-4444-4444-8444-444444444444" {
		t.Fatalf("trusted envelope identity = %+v", envelope)
	}
	if envelope.BatchID != batchID || envelope.PayloadSHA256 != sha256Hex(body) {
		t.Fatalf("envelope receipt identity = %+v", envelope)
	}
	var exported collectortracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(envelope.Payload, &exported); err != nil {
		t.Fatal(err)
	}
	attributes := exported.ResourceSpans[0].Resource.Attributes
	assertAttribute(t, attributes, "platform.user.id", envelope.AccountID)
	assertAttribute(t, attributes, "user.id", envelope.AccountID)
	assertAttribute(t, attributes, "langfuse.user.id", envelope.AccountID)
	assertAttribute(t, attributes, "trace.gateway.batch.id", batchID)
	if got := exported.String(); strings.Contains(got, "mutable-display-id") || strings.Contains(got, "request-id") {
		t.Fatalf("mutable or request identity reached canonical payload: %s", got)
	}
}

func TestHandlerReturnsDuplicateDurableReceipt(t *testing.T) {
	store := &recordingStore{result: inbox.AcceptResult{BatchID: batchID, Outcome: inbox.ReceiptDuplicate}}
	handler := mustHandler(t, Config{Introspector: fakeIntrospector{}, Store: store})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validRequest(validBody(t)))
	assertSuccess(t, response, inbox.ReceiptDuplicate)
}

func TestHandlerTriggersWorkerOnlyAfterAcceptedInboxBatch(t *testing.T) {
	store := &recordingStore{result: inbox.AcceptResult{BatchID: batchID, Outcome: inbox.ReceiptAccepted}}
	triggered := make(chan struct{}, 1)
	handler := mustHandler(t, Config{
		Introspector: fakeIntrospector{},
		Store:        store,
		Trigger:      func() { triggered <- struct{}{} },
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, validRequest(validBody(t)))
	assertSuccess(t, response, inbox.ReceiptAccepted)
	select {
	case <-triggered:
	case <-time.After(time.Second):
		t.Fatal("accepted batch did not trigger the delivery worker")
	}
}

func TestHandlerRejectsInvalidContractsWithFixedNoStoreErrors(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		request func(*testing.T) *http.Request
		status  int
		code    string
	}{
		{"missing idempotency key", Config{Introspector: fakeIntrospector{}, Store: acceptedStore()}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Del("Idempotency-Key")
			return r
		}, 400, "invalid_headers"},
		{"duplicate idempotency key", Config{Introspector: fakeIntrospector{}, Store: acceptedStore()}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Add("Idempotency-Key", batchID)
			return r
		}, 400, "invalid_headers"},
		{"invalid idempotency key", Config{Introspector: fakeIntrospector{}, Store: acceptedStore()}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Set("Idempotency-Key", "not-a-v4-uuid")
			return r
		}, 400, "invalid_headers"},
		{"uppercase payload digest", Config{Introspector: fakeIntrospector{}, Store: acceptedStore()}, func(t *testing.T) *http.Request {
			body := validBody(t)
			r := validRequest(body)
			r.Header.Set("X-Trace-Payload-SHA256", strings.ToUpper(sha256Hex(body)))
			return r
		}, 400, "invalid_headers"},
		{"digest mismatch", Config{Introspector: fakeIntrospector{}, Store: acceptedStore()}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Set("X-Trace-Payload-SHA256", strings.Repeat("0", 64))
			return r
		}, 409, "payload_digest_mismatch"},
		{"idempotency conflict", Config{Introspector: fakeIntrospector{}, Store: &recordingStore{err: inbox.ErrIdempotencyConflict}}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 409, "idempotency_conflict"},
		{"storage unavailable", Config{Introspector: fakeIntrospector{}, Store: &recordingStore{err: inbox.ErrStorageUnavailable}}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 507, "storage_unavailable"},
		{"refresh required", Config{Introspector: fakeIntrospector{err: &auth.InactiveError{Code: "trace_token_refresh_required"}}, Store: acceptedStore()}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 401, "trace_token_refresh_required"},
		{"auth unavailable", Config{Introspector: fakeIntrospector{err: auth.ErrUnavailable}, Store: acceptedStore()}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 503, "authentication_unavailable"},
		{"too large", Config{Introspector: fakeIntrospector{}, Store: acceptedStore(), MaxBodyBytes: 32}, func(t *testing.T) *http.Request { return validRequest(bytes.Repeat([]byte("x"), 33)) }, 413, "payload_too_large"},
		{"rate limited", Config{Introspector: fakeIntrospector{}, Store: acceptedStore(), Limiter: denyLimiter{}}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 429, "rate_limited"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			mustHandler(t, test.config).ServeHTTP(response, test.request(t))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			var body map[string]string
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["error"] != test.code {
				t.Fatalf("error = %q, want %q", body["error"], test.code)
			}
			if bytes.Contains(response.Body.Bytes(), []byte(uploadBearer)) {
				t.Fatal("bearer leaked in error")
			}
		})
	}
}

func TestHandlerReturnsExactStructuredExplicitRevocationEvidence(t *testing.T) {
	for _, code := range []string{"session_revoked", "account_disabled", "account_revoked"} {
		t.Run(code, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler := mustHandler(t, Config{
				Introspector: fakeIntrospector{err: &auth.InactiveError{
					Code:           code,
					Explicit:       true,
					AccountID:      "22222222-2222-4222-8222-222222222222",
					SessionID:      "33333333-3333-4333-8333-333333333333",
					InstallationID: "44444444-4444-4444-8444-444444444444",
					RevokedAt:      time.Date(2099, 8, 23, 14, 0, 0, 0, time.UTC),
				}},
				Store: acceptedStore(),
			})
			handler.ServeHTTP(response, validRequest(validBody(t)))

			if response.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", response.Code, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("headers = %v", response.Header())
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{
				"account_id": "22222222-2222-4222-8222-222222222222",
				"code":       code,
				"retryable":  false,
				"revoked_at": "2099-08-23T14:00:00Z",
				"session_id": "33333333-3333-4333-8333-333333333333",
				"state":      "revoked",
			}
			if !reflect.DeepEqual(body, want) {
				t.Fatalf("body = %#v, want %#v", body, want)
			}
			for _, secret := range []string{uploadBearer, "payload", "44444444-4444-4444-8444-444444444444"} {
				if strings.Contains(response.Body.String(), secret) {
					t.Fatalf("unsafe value leaked in response: %q", secret)
				}
			}
		})
	}
}

func TestHandlerExposesOnlyHealthAndTraceRoutes(t *testing.T) {
	handler := mustHandler(t, Config{Introspector: fakeIntrospector{}, Store: acceptedStore()})
	for path, status := range map[string]int{"/healthz": 200, "/": 404, "/metrics": 404, "/v1/logs": 404} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != status {
			t.Fatalf("%s status = %d, want %d", path, response.Code, status)
		}
	}
}

func TestHealthRejectsAdmissionWhenInboxIsNotWritable(t *testing.T) {
	handler := mustHandler(t, Config{
		Introspector: fakeIntrospector{},
		Store:        healthStore{syncErr: inbox.ErrClosed},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
}

func acceptedStore() *recordingStore {
	return &recordingStore{result: inbox.AcceptResult{BatchID: batchID, Outcome: inbox.ReceiptAccepted}}
}

func mustHandler(t *testing.T, config Config) http.Handler {
	t.Helper()
	gateway, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return gateway.Handler()
}

func validRequest(body []byte) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/v1/traces", bytes.NewReader(body))
	r.RemoteAddr = "192.0.2.1:1234"
	r.Header.Set("Authorization", "Bearer "+uploadBearer)
	r.Header.Set("Content-Type", "application/x-protobuf")
	r.Header.Set("Content-Encoding", "identity")
	r.Header.Set("X-Hermes-Session-ID", "session-1")
	r.Header.Set("X-Trace-Entrypoint", "desktop")
	r.Header.Set("X-Trace-Run-ID", "run-1")
	r.Header.Set("X-Telemetry-Schema-Version", "1")
	r.Header.Set("Idempotency-Key", batchID)
	r.Header.Set("X-Trace-Payload-SHA256", sha256Hex(body))
	return r
}

func validBody(t *testing.T) []byte {
	t.Helper()
	request := &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{},
		ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{{
			TraceId: bytes.Repeat([]byte{1}, 16), SpanId: bytes.Repeat([]byte{2}, 8), Name: "agent turn",
			Attributes: []*commonpb.KeyValue{{Key: "platform.user.id", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "forged-user"}}}},
		}}}},
	}}}
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func assertSuccess(t *testing.T, response *httptest.ResponseRecorder, receipt inbox.ReceiptOutcome) {
	t.Helper()
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("X-Trace-Batch-ID") != batchID {
		t.Fatalf("batch ID = %q", response.Header().Get("X-Trace-Batch-ID"))
	}
	if response.Header().Get("X-Trace-Receipt") != string(receipt) {
		t.Fatalf("receipt = %q", response.Header().Get("X-Trace-Receipt"))
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/x-protobuf" {
		t.Fatalf("success headers = %v", response.Header())
	}
	var body collectortracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("OTLP success body: %v", err)
	}
}

func assertNotClosed(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
		t.Fatal("handler replied before the inbox accepted the batch")
	case <-time.After(20 * time.Millisecond):
	}
}

func assertAttribute(t *testing.T, attributes []*commonpb.KeyValue, key, want string) {
	t.Helper()
	for _, attribute := range attributes {
		if attribute.Key == key {
			if got := attribute.Value.GetStringValue(); got != want {
				t.Fatalf("%s = %q, want %q", key, got, want)
			}
			return
		}
	}
	t.Fatalf("%s missing", key)
}

func sha256Hex(body []byte) string {
	digest := sha256.Sum256(body)
	return hex.EncodeToString(digest[:])
}
