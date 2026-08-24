package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/proto"
)

const uploadBearer = "valid-upload-token-that-must-not-leak-1234567890"

type fakeIntrospector struct{ err error }

func (f fakeIntrospector) Introspect(_ context.Context, _ string) (auth.Principal, error) {
	return auth.Principal{
		TokenID:        "token-id",
		UserID:         "42",
		Username:       "yiyuxiao",
		InstallationID: "11111111-1111-4111-8111-111111111111",
		ExpiresAt:      time.Now().Add(time.Hour),
		Scope:          "trace:write",
		Audience:       "ansatz-trace-gateway",
	}, f.err
}

type denyLimiter struct{}

func (denyLimiter) Allow(string, string, time.Time) bool { return false }

func TestHandlerForwardsCanonicalProtobufOnceForIdenticalRetry(t *testing.T) {
	var calls atomic.Int32
	var upstreamAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		upstreamAuthorization = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "forged-user") {
			t.Fatal("forged identity reached upstream")
		}
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte{0x0a, 0x00})
	}))
	defer upstream.Close()

	var logs bytes.Buffer
	handler := mustHandler(t, Config{
		Introspector:      fakeIntrospector{},
		UpstreamURL:       upstream.URL,
		LangfusePublicKey: "public-key",
		LangfuseSecretKey: "server-secret",
		Logger:            slog.New(slog.NewTextHandler(&logs, nil)),
		RequestID:         func() string { return "request-id" },
	})
	body := validBody(t)
	for range 2 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, validRequest(body))
		if response.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
		}
		if response.Header().Get("Content-Type") != "application/x-protobuf" {
			t.Fatalf("content type = %q", response.Header().Get("Content-Type"))
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("public-key:server-secret"))
	if upstreamAuthorization != wantBasic {
		t.Fatalf("upstream authorization = %q", upstreamAuthorization)
	}
	for _, secret := range []string{uploadBearer, "server-secret", "public-key", "forged-user"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("secret leaked in logs: %s", logs.String())
		}
	}
}

func TestHandlerRejectsInvalidContractsWithFixedNoStoreErrors(t *testing.T) {
	unavailableUpstream := "http://127.0.0.1:1"
	tests := []struct {
		name    string
		config  Config
		request func(*testing.T) *http.Request
		status  int
	}{
		{"bad headers", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Del("X-Hermes-Session-ID")
			return r
		}, 400},
		{"inactive", Config{Introspector: fakeIntrospector{err: auth.ErrInactive}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 401},
		{"duplicate authorization", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Add("Authorization", "Bearer "+uploadBearer)
			return r
		}, 401},
		{"duplicate identity header", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Add("X-Hermes-Session-ID", "session-2")
			return r
		}, 400},
		{"wrong type", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Set("Content-Type", "application/json")
			return r
		}, 415},
		{"compressed", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request {
			r := validRequest(validBody(t))
			r.Header.Set("Content-Encoding", "gzip")
			return r
		}, 415},
		{"too large", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream, MaxBodyBytes: 32}, func(t *testing.T) *http.Request {
			return validRequest(bytes.Repeat([]byte("x"), 33))
		}, 413},
		{"rate limited", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream, Limiter: denyLimiter{}}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 429},
		{"auth unavailable", Config{Introspector: fakeIntrospector{err: auth.ErrUnavailable}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 503},
		{"upstream unavailable", Config{Introspector: fakeIntrospector{}, UpstreamURL: unavailableUpstream}, func(t *testing.T) *http.Request { return validRequest(validBody(t)) }, 502},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			handler := mustHandler(t, test.config)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, test.request(t))
			if response.Code != test.status {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.status, response.Body.String())
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
			}
			if strings.Contains(response.Body.String(), uploadBearer) {
				t.Fatal("bearer leaked in error")
			}
		})
	}
}

func TestHandlerExposesOnlyHealthAndTraceRoutes(t *testing.T) {
	handler := mustHandler(t, Config{Introspector: fakeIntrospector{}, UpstreamURL: "http://127.0.0.1:1"})
	for path, status := range map[string]int{"/healthz": 200, "/": 404, "/metrics": 404, "/v1/logs": 404} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != status {
			t.Fatalf("%s status = %d, want %d", path, response.Code, status)
		}
	}
}

func mustHandler(t *testing.T, config Config) http.Handler {
	t.Helper()
	if config.LangfusePublicKey == "" {
		config.LangfusePublicKey = "public"
	}
	if config.LangfuseSecretKey == "" {
		config.LangfuseSecretKey = "secret"
	}
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
	return r
}

func validBody(t *testing.T) []byte {
	t.Helper()
	request := &collectortracepb.ExportTraceServiceRequest{
		ResourceSpans: []*tracepb.ResourceSpans{
			{
				Resource: &resourcepb.Resource{},
				ScopeSpans: []*tracepb.ScopeSpans{
					{
						Spans: []*tracepb.Span{
							{
								TraceId: bytes.Repeat([]byte{1}, 16),
								SpanId:  bytes.Repeat([]byte{2}, 8),
								Name:    "agent turn",
								Attributes: []*commonpb.KeyValue{
									{
										Key:   "platform.user.id",
										Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "forged-user"}},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
