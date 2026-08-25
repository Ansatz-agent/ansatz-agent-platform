package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
)

func TestLoadRuntimeConfigRequiresValidDurableInboxSettingsWithoutLeakingSecrets(t *testing.T) {
	const secret = "super-secret-value-that-must-not-appear"
	tests := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{
			name:   "missing inbox path",
			mutate: func(values map[string]string) { delete(values, "TRACE_GATEWAY_INBOX_PATH") },
			want:   "TRACE_GATEWAY_INBOX_PATH",
		},
		{
			name:   "invalid receipt retention",
			mutate: func(values map[string]string) { values["TRACE_GATEWAY_RECEIPT_RETENTION"] = "not-a-duration" },
			want:   "TRACE_GATEWAY_RECEIPT_RETENTION",
		},
		{
			name:   "zero database ceiling",
			mutate: func(values map[string]string) { values["TRACE_GATEWAY_MAX_DB_BYTES"] = "0" },
			want:   "TRACE_GATEWAY_MAX_DB_BYTES",
		},
		{
			name:   "negative free reserve",
			mutate: func(values map[string]string) { values["TRACE_GATEWAY_MIN_FREE_BYTES"] = "-1" },
			want:   "TRACE_GATEWAY_MIN_FREE_BYTES",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validGatewayEnvironment(secret)
			test.mutate(values)
			_, err := loadRuntimeConfig(func(name string) string { return values[name] })
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want validation for %s", err, test.want)
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("configuration error leaked a secret: %v", err)
			}
		})
	}
}

func TestLoadRuntimeConfigUsesExactBoundedInboxValues(t *testing.T) {
	values := validGatewayEnvironment("test-only-secret")
	config, err := loadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.inboxPath != "/data/inbox.db" {
		t.Fatalf("inbox path = %q", config.inboxPath)
	}
	if config.receiptRetention != 720*time.Hour {
		t.Fatalf("receipt retention = %s", config.receiptRetention)
	}
	if config.maxDBBytes != 68_719_476_736 {
		t.Fatalf("database ceiling = %d", config.maxDBBytes)
	}
	if config.minFreeBytes != 10_737_418_240 {
		t.Fatalf("free reserve = %d", config.minFreeBytes)
	}
}

func TestLoadRuntimeConfigAcceptsOnlySafeAbsoluteServiceEndpointURLs(t *testing.T) {
	const secret = "secret-url-value-that-must-not-appear"
	tests := []struct {
		name     string
		variable string
		value    string
		want     string
	}{
		{"accepts HTTPS introspection endpoint", "AUTH_INTROSPECTION_URL", "https://auth.example.test/internal/trace-token/introspect/", ""},
		{"accepts HTTP upstream endpoint", "LANGFUSE_OTLP_TRACES_URL", "http://langfuse.example.test/langfuse/api/public/otel/v1/traces", ""},
		{"rejects malformed introspection endpoint", "AUTH_INTROSPECTION_URL", "https://%", "invalid URL"},
		{"rejects relative introspection endpoint", "AUTH_INTROSPECTION_URL", "/internal/trace-token/introspect/", "invalid URL"},
		{"rejects introspection endpoint without host", "AUTH_INTROSPECTION_URL", "https:/internal/trace-token/introspect/", "invalid URL"},
		{"rejects introspection endpoint with port but no hostname", "AUTH_INTROSPECTION_URL", "https://:8080/internal/", "invalid URL"},
		{"rejects introspection endpoint with invalid port", "AUTH_INTROSPECTION_URL", "https://auth.example.test:not-a-port/internal/", "invalid URL"},
		{"rejects introspection endpoint with out of range port", "AUTH_INTROSPECTION_URL", "https://auth.example.test:65536/internal/", "invalid URL"},
		{"rejects introspection credentials", "AUTH_INTROSPECTION_URL", "https://user:password@auth.example.test/internal/", "invalid URL"},
		{"rejects introspection query", "AUTH_INTROSPECTION_URL", "https://auth.example.test/internal/?token=secret", "invalid URL"},
		{"rejects introspection fragment", "AUTH_INTROSPECTION_URL", "https://auth.example.test/internal/#secret", "invalid URL"},
		{"rejects introspection FTP", "AUTH_INTROSPECTION_URL", "ftp://auth.example.test/internal/", "invalid URL"},
		{"rejects upstream credentials", "LANGFUSE_OTLP_TRACES_URL", "https://user:password@langfuse.example.test/v1/traces", "invalid URL"},
		{"rejects upstream endpoint with port but no hostname", "LANGFUSE_OTLP_TRACES_URL", "https://:8080/v1/traces", "invalid URL"},
		{"rejects upstream query", "LANGFUSE_OTLP_TRACES_URL", "https://langfuse.example.test/v1/traces?key=secret", "invalid URL"},
		{"rejects upstream fragment", "LANGFUSE_OTLP_TRACES_URL", "https://langfuse.example.test/v1/traces#secret", "invalid URL"},
		{"rejects upstream non-HTTP scheme", "LANGFUSE_OTLP_TRACES_URL", "file:///private/trace", "invalid URL"},
		{"accepts IPv6 endpoint with explicit port", "AUTH_INTROSPECTION_URL", "https://[2001:db8::1]:8443/internal/", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			values := validGatewayEnvironment(secret)
			values[test.variable] = test.value
			config, err := loadRuntimeConfig(func(name string) string { return values[name] })
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				if config.introspectionURL != values["AUTH_INTROSPECTION_URL"] || config.upstreamURL != values["LANGFUSE_OTLP_TRACES_URL"] {
					t.Fatalf("endpoint URLs changed: introspection=%q upstream=%q", config.introspectionURL, config.upstreamURL)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.variable) || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %s %s", err, test.variable, test.want)
			}
			if strings.Contains(err.Error(), test.value) || strings.Contains(err.Error(), secret) {
				t.Fatalf("configuration error leaked endpoint material: %v", err)
			}
		})
	}
}

func TestLoadRuntimeConfigNormalizesOnlyAnEmptyEndpointPathToRootSlash(t *testing.T) {
	values := validGatewayEnvironment("test-only-secret")
	values["AUTH_INTROSPECTION_URL"] = "https://auth.example.test"
	values["LANGFUSE_OTLP_TRACES_URL"] = "http://langfuse.example.test"
	config, err := loadRuntimeConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if config.introspectionURL != "https://auth.example.test/" || config.upstreamURL != "http://langfuse.example.test/" {
		t.Fatalf("normalized URLs = introspection=%q upstream=%q", config.introspectionURL, config.upstreamURL)
	}
}

func TestShutdownGatewayStopsAdmissionBeforeWorkerAndInbox(t *testing.T) {
	var events []string
	var mu sync.Mutex
	record := func(event string) {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
	}
	cancelled := make(chan struct{})
	workerDone := make(chan error, 1)
	go func() {
		<-cancelled
		record("worker-exit")
		workerDone <- nil
	}()

	err := shutdownGateway(
		context.Background(),
		shutdownFunc(func(context.Context) error { record("admission"); return nil }),
		func() { record("cancel-worker"); close(cancelled) },
		workerDone,
		closeFunc(func() error { record("close-inbox"); return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"admission", "cancel-worker", "worker-exit", "close-inbox"}; !slices.Equal(events, want) {
		t.Fatalf("shutdown order = %v, want %v", events, want)
	}
}

func TestShutdownGatewayClosesInboxAfterAdmissionFailure(t *testing.T) {
	admissionErr := errors.New("admission shutdown failed")
	workerDone := make(chan error, 1)
	workerDone <- nil
	closed := false
	err := shutdownGateway(
		context.Background(),
		shutdownFunc(func(context.Context) error { return admissionErr }),
		func() {},
		workerDone,
		closeFunc(func() error { closed = true; return nil }),
	)
	if !errors.Is(err, admissionErr) {
		t.Fatalf("error = %v", err)
	}
	if !closed {
		t.Fatal("inbox was not closed after admission shutdown failure")
	}
}

func TestSuperviseGatewayFailsFastWhenDeliveryWorkerDies(t *testing.T) {
	tests := []struct {
		name      string
		workerErr error
	}{
		{"worker error", errors.New("delivery worker crashed")},
		{"unexpected nil exit", nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workerDone := make(chan error, 1)
			workerDone <- test.workerErr
			admissionStopped := false
			storeClosed := false
			err := superviseGateway(
				context.Background(),
				shutdownFunc(func(context.Context) error { admissionStopped = true; return nil }),
				func() {},
				workerDone,
				make(chan error, 1),
				closeFunc(func() error { storeClosed = true; return nil }),
				discardLogger(),
			)
			if err == nil {
				t.Fatal("gateway kept running with a dead delivery worker")
			}
			if test.workerErr != nil && !errors.Is(err, test.workerErr) {
				t.Fatalf("error = %v, want wrapped %v", err, test.workerErr)
			}
			if !admissionStopped {
				t.Fatal("admission server was not shut down")
			}
			if !storeClosed {
				t.Fatal("inbox was not closed")
			}
		})
	}
}

func TestSuperviseGatewayStillShutsDownCleanlyOnSignalAndServerClose(t *testing.T) {
	tests := []struct {
		name  string
		drive func(cancel context.CancelFunc, listenDone chan error)
	}{
		{"signal", func(cancel context.CancelFunc, _ chan error) { cancel() }},
		{"server closed", func(_ context.CancelFunc, listenDone chan error) { listenDone <- http.ErrServerClosed }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			listenDone := make(chan error, 1)
			workerDone := make(chan error, 1)
			cancelWorker := func() { workerDone <- nil }
			test.drive(cancel, listenDone)
			err := superviseGateway(
				ctx,
				shutdownFunc(func(context.Context) error { return nil }),
				cancelWorker,
				workerDone,
				listenDone,
				closeFunc(func() error { return nil }),
				discardLogger(),
			)
			if err != nil {
				t.Fatalf("clean shutdown returned error: %v", err)
			}
		})
	}
}

func TestRunReceiptGCCollectsPeriodicallyUntilCancelled(t *testing.T) {
	collector := &countingCollector{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReceiptGC(ctx, collector, time.Millisecond, discardLogger())
	}()

	deadline := time.After(time.Second)
	for collector.calls() < 2 {
		select {
		case <-deadline:
			t.Fatalf("receipt GC ran %d times, want at least 2", collector.calls())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("receipt GC did not stop on cancellation")
	}
}

func TestRunReceiptGCKeepsRunningAfterCollectionErrors(t *testing.T) {
	collector := &countingCollector{err: errors.New("transient collection failure")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReceiptGC(ctx, collector, time.Millisecond, discardLogger())
	}()

	deadline := time.After(time.Second)
	for collector.calls() < 2 {
		select {
		case <-deadline:
			t.Fatalf("receipt GC stopped after an error: %d calls", collector.calls())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
}

func TestLoadRuntimeConfigBoundsQuarantineAndGCSettings(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		values := validGatewayEnvironment("test-only-secret")
		config, err := loadRuntimeConfig(func(name string) string { return values[name] })
		if err != nil {
			t.Fatal(err)
		}
		if config.quarantineRetention != config.receiptRetention {
			t.Fatalf("quarantine retention = %s, want receipt retention %s", config.quarantineRetention, config.receiptRetention)
		}
		if config.maxQuarantineEntries != defaultMaxQuarantineEntries {
			t.Fatalf("max quarantine entries = %d", config.maxQuarantineEntries)
		}
		if config.gcInterval != defaultGCInterval {
			t.Fatalf("gc interval = %s", config.gcInterval)
		}
	})
	t.Run("overrides", func(t *testing.T) {
		values := validGatewayEnvironment("test-only-secret")
		values["TRACE_GATEWAY_QUARANTINE_RETENTION"] = "48h"
		values["TRACE_GATEWAY_MAX_QUARANTINE_ENTRIES"] = "250"
		values["TRACE_GATEWAY_GC_INTERVAL"] = "10m"
		config, err := loadRuntimeConfig(func(name string) string { return values[name] })
		if err != nil {
			t.Fatal(err)
		}
		if config.quarantineRetention != 48*time.Hour || config.maxQuarantineEntries != 250 || config.gcInterval != 10*time.Minute {
			t.Fatalf("config = %+v", config)
		}
	})
	for name, value := range map[string]string{
		"TRACE_GATEWAY_QUARANTINE_RETENTION":   "-1h",
		"TRACE_GATEWAY_MAX_QUARANTINE_ENTRIES": "0",
		"TRACE_GATEWAY_GC_INTERVAL":            "never",
	} {
		t.Run("rejects invalid "+name, func(t *testing.T) {
			values := validGatewayEnvironment("test-only-secret")
			values[name] = value
			if _, err := loadRuntimeConfig(func(n string) string { return values[n] }); err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("error = %v, want validation for %s", err, name)
			}
		})
	}
}

func TestReplayQuarantinedBatchesRequeuesFromClosedInbox(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	store, err := inbox.Open(path, inbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, batchID := range []string{"batch-a", "batch-b"} {
		payload := []byte(batchID)
		digest := sha256.Sum256(payload)
		if _, err := store.Accept(context.Background(), inbox.Envelope{
			AccountID: "account-a", BatchID: batchID,
			Payload: payload, PayloadSHA256: hex.EncodeToString(digest[:]),
		}); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkQuarantined(context.Background(), "account-a", batchID, "upstream_401", time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	if _, err := replayQuarantinedBatches(path, []string{"only-one-argument"}); err == nil {
		t.Fatal("invalid arguments were accepted")
	}
	replayed, err := replayQuarantinedBatches(path, []string{"account-a", "batch-a"})
	if err != nil {
		t.Fatal(err)
	}
	if replayed != 1 {
		t.Fatalf("replayed = %d", replayed)
	}
	replayed, err = replayQuarantinedBatches(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if replayed != 1 {
		t.Fatalf("replayed remaining = %d", replayed)
	}

	store, err = inbox.Open(path, inbox.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	head, err := store.PeekEligible(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if head == nil || head.BatchID != "batch-a" {
		t.Fatalf("head = %#v", head)
	}
}

type countingCollector struct {
	mu    sync.Mutex
	count int
	err   error
}

func (c *countingCollector) CollectReceipts(context.Context, time.Time) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.count++
	return 1, c.err
}

func (c *countingCollector) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLangfuseUpstreamSendsProtobufAndReturnsOnlyDeliveryMetadata(t *testing.T) {
	const credential = "pk-test:sk-test"
	payload := []byte("canonical-otlp")
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost {
			t.Fatalf("method = %s", request.Method)
		}
		if request.Header.Get("Content-Type") != "application/x-protobuf" || request.Header.Get("Accept") != "application/x-protobuf" {
			t.Fatalf("unexpected content headers: %v", request.Header)
		}
		if want := "Basic " + base64.StdEncoding.EncodeToString([]byte(credential)); request.Header.Get("Authorization") != want {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		got, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, payload) {
			t.Fatalf("payload = %q", got)
		}
		response.Header().Set("Retry-After", "60")
		response.WriteHeader(http.StatusTooManyRequests)
	}))
	defer upstreamServer.Close()

	result, err := (langfuseUpstream{
		url:    upstreamServer.URL,
		basic:  base64.StdEncoding.EncodeToString([]byte(credential)),
		client: upstreamServer.Client(),
	}).Send(context.Background(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != http.StatusTooManyRequests || result.RetryAfter != "60" {
		t.Fatalf("result = %+v", result)
	}
}

type shutdownFunc func(context.Context) error

func (f shutdownFunc) Shutdown(ctx context.Context) error { return f(ctx) }

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

func validGatewayEnvironment(secret string) map[string]string {
	return map[string]string{
		"TRACE_GATEWAY_LISTEN_ADDR":       "127.0.0.1:8080",
		"AUTH_INTROSPECTION_URL":          "http://auth-service:8000/internal/trace-token/introspect/",
		"TRACE_GATEWAY_INTERNAL_SECRET":   secret,
		"LANGFUSE_OTLP_TRACES_URL":        "http://langfuse-web:3000/langfuse/api/public/otel/v1/traces",
		"LANGFUSE_PUBLIC_KEY":             "pk-test",
		"LANGFUSE_SECRET_KEY":             secret,
		"TRACE_GATEWAY_INBOX_PATH":        "/data/inbox.db",
		"TRACE_GATEWAY_RECEIPT_RETENTION": "720h",
		"TRACE_GATEWAY_MAX_DB_BYTES":      "68719476736",
		"TRACE_GATEWAY_MIN_FREE_BYTES":    "10737418240",
	}
}
