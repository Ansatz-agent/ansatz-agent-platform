package main

import (
	"errors"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/server"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		runHealthCheck()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	listenAddress := environment("TRACE_GATEWAY_LISTEN_ADDR", ":8080")
	introspectionURL := required("AUTH_INTROSPECTION_URL", logger)
	internalSecret := required("TRACE_GATEWAY_INTERNAL_SECRET", logger)
	upstreamURL := required("LANGFUSE_OTLP_TRACES_URL", logger)
	publicKey := required("LANGFUSE_PUBLIC_KEY", logger)
	secretKey := required("LANGFUSE_SECRET_KEY", logger)

	gateway, err := server.New(server.Config{
		Introspector:      auth.NewIntrospector(introspectionURL, internalSecret, 2*time.Second),
		UpstreamURL:       upstreamURL,
		LangfusePublicKey: publicKey,
		LangfuseSecretKey: secretKey,
		Logger:            logger,
	})
	if err != nil {
		logger.Error("invalid gateway configuration")
		os.Exit(1)
	}
	httpServer := &http.Server{
		Addr:              listenAddress,
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 * 1024,
	}
	logger.Info("trace gateway listening", "address", listenAddress)
	if err := httpServer.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		logger.Error("trace gateway stopped", "error", "listen_failed")
		os.Exit(1)
	}
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

func environment(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func required(name string, logger *slog.Logger) string {
	value := os.Getenv(name)
	if value == "" {
		logger.Error("required environment variable is missing", "name", name)
		os.Exit(1)
	}
	return value
}
