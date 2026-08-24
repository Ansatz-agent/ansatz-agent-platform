package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/delivery"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/server"
)

const (
	shutdownTimeout      = 15 * time.Second
	upstreamBodyMaxBytes = int64(1024 * 1024)
)

type runtimeConfig struct {
	listenAddress    string
	introspectionURL string
	internalSecret   string
	upstreamURL      string
	publicKey        string
	secretKey        string
	inboxPath        string
	receiptRetention time.Duration
	maxDBBytes       int64
	minFreeBytes     int64
}

type admissionServer interface {
	Shutdown(context.Context) error
}

type inboxCloser interface {
	Close() error
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		runHealthCheck()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	config, err := loadRuntimeConfig(os.Getenv)
	if err != nil {
		logger.Error("invalid gateway configuration", "error", err.Error())
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := runGateway(ctx, config, logger); err != nil {
		logger.Error("trace gateway stopped", "error", "runtime_failed")
		os.Exit(1)
	}
}

func loadRuntimeConfig(getenv func(string) string) (runtimeConfig, error) {
	config := runtimeConfig{listenAddress: environmentFrom(getenv, "TRACE_GATEWAY_LISTEN_ADDR", ":8080")}
	var err error
	if config.introspectionURL, err = requiredFrom(getenv, "AUTH_INTROSPECTION_URL"); err != nil {
		return runtimeConfig{}, err
	}
	if config.internalSecret, err = requiredFrom(getenv, "TRACE_GATEWAY_INTERNAL_SECRET"); err != nil {
		return runtimeConfig{}, err
	}
	if config.upstreamURL, err = requiredURLFrom(getenv, "LANGFUSE_OTLP_TRACES_URL"); err != nil {
		return runtimeConfig{}, err
	}
	if config.publicKey, err = requiredFrom(getenv, "LANGFUSE_PUBLIC_KEY"); err != nil {
		return runtimeConfig{}, err
	}
	if config.secretKey, err = requiredFrom(getenv, "LANGFUSE_SECRET_KEY"); err != nil {
		return runtimeConfig{}, err
	}
	if config.inboxPath, err = requiredFrom(getenv, "TRACE_GATEWAY_INBOX_PATH"); err != nil {
		return runtimeConfig{}, err
	}
	if config.receiptRetention, err = requiredDurationFrom(getenv, "TRACE_GATEWAY_RECEIPT_RETENTION"); err != nil {
		return runtimeConfig{}, err
	}
	if config.maxDBBytes, err = requiredPositiveInt64From(getenv, "TRACE_GATEWAY_MAX_DB_BYTES"); err != nil {
		return runtimeConfig{}, err
	}
	if config.minFreeBytes, err = requiredPositiveInt64From(getenv, "TRACE_GATEWAY_MIN_FREE_BYTES"); err != nil {
		return runtimeConfig{}, err
	}
	return config, nil
}

func runGateway(ctx context.Context, config runtimeConfig, logger *slog.Logger) error {
	store, err := inbox.Open(config.inboxPath, inbox.Options{
		ReceiptRetention: config.receiptRetention,
		MaxDBBytes:       config.maxDBBytes,
		MinFreeBytes:     config.minFreeBytes,
	})
	if err != nil {
		return fmt.Errorf("open durable trace inbox: %w", err)
	}
	upstream := langfuseUpstream{
		url:    config.upstreamURL,
		basic:  base64.StdEncoding.EncodeToString([]byte(config.publicKey + ":" + config.secretKey)),
		client: &http.Client{Timeout: 10 * time.Second},
	}
	worker := delivery.New(store, upstream, delivery.Options{})
	gateway, err := server.New(server.Config{
		Introspector: auth.NewIntrospector(config.introspectionURL, config.internalSecret, 2*time.Second),
		Store:        store,
		Trigger:      worker.Trigger,
		Logger:       logger,
	})
	if err != nil {
		return errors.Join(err, store.Close())
	}
	httpServer := &http.Server{
		Addr:              config.listenAddress,
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	workerCtx, cancelWorker := context.WithCancel(context.Background())
	workerDone := make(chan error, 1)
	go func() { workerDone <- worker.Run(workerCtx) }()
	listenDone := make(chan error, 1)
	go func() { listenDone <- httpServer.ListenAndServe() }()
	logger.Info("trace gateway listening", "address", config.listenAddress)
	select {
	case <-ctx.Done():
		return shutdownGateway(context.Background(), httpServer, cancelWorker, workerDone, store)
	case err := <-listenDone:
		if errors.Is(err, http.ErrServerClosed) {
			return shutdownGateway(context.Background(), httpServer, cancelWorker, workerDone, store)
		}
		cancelWorker()
		workerErr := <-workerDone
		return errors.Join(err, workerErr, store.Close())
	}
}

func shutdownGateway(ctx context.Context, admission admissionServer, cancelWorker func(), workerDone <-chan error, store inboxCloser) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, shutdownTimeout)
	defer cancel()
	admissionErr := admission.Shutdown(shutdownCtx)
	cancelWorker()
	workerErr := <-workerDone
	closeErr := store.Close()
	return errors.Join(admissionErr, workerErr, closeErr)
}

type langfuseUpstream struct {
	url    string
	basic  string
	client *http.Client
}

func (u langfuseUpstream) Send(ctx context.Context, payload []byte) (delivery.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, u.url, bytes.NewReader(payload))
	if err != nil {
		return delivery.Response{}, err
	}
	request.Header.Set("Authorization", "Basic "+u.basic)
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Accept", "application/x-protobuf")
	response, err := u.client.Do(request)
	if err != nil {
		return delivery.Response{}, err
	}
	defer response.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(response.Body, upstreamBodyMaxBytes)); err != nil {
		return delivery.Response{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}, err
	}
	return delivery.Response{Status: response.StatusCode, RetryAfter: response.Header.Get("Retry-After")}, nil
}

func runHealthCheck() {
	client := &http.Client{Timeout: 2 * time.Second}
	response, err := client.Get("http://127.0.0.1:8080/healthz")
	if err != nil {
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		os.Exit(1)
	}
}

func environmentFrom(getenv func(string) string, name, fallback string) string {
	if value := strings.TrimSpace(getenv(name)); value != "" {
		return value
	}
	return fallback
}

func requiredFrom(getenv func(string) string, name string) (string, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return "", fmt.Errorf("required environment variable is missing: %s", name)
	}
	return value, nil
}

func requiredURLFrom(getenv func(string) string, name string) (string, error) {
	value, err := requiredFrom(getenv, name)
	if err != nil {
		return "", err
	}
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("environment variable is not a valid URL: %s", name)
	}
	return parsed.String(), nil
}

func requiredDurationFrom(getenv func(string) string, name string) (time.Duration, error) {
	value, err := requiredFrom(getenv, name)
	if err != nil {
		return 0, err
	}
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("environment variable must be a positive duration: %s", name)
	}
	return duration, nil
}

func requiredPositiveInt64From(getenv func(string) string, name string) (int64, error) {
	value, err := requiredFrom(getenv, name)
	if err != nil {
		return 0, err
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("environment variable must be a positive integer: %s", name)
	}
	return parsed, nil
}
