package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/inbox"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/otlp"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	defaultMaxBodyBytes = int64(8 * 1024 * 1024)
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-4[0-9a-fA-F]{3}-[89aAbB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var sha256HexPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type PrincipalIntrospector interface {
	Introspect(context.Context, string) (auth.Principal, error)
}

type RateLimiter interface {
	Allow(tokenID, source string, now time.Time) bool
}

type Config struct {
	Introspector PrincipalIntrospector
	Store        inbox.Store
	Trigger      func()
	MaxBodyBytes int64
	Limiter      RateLimiter
	Logger       *slog.Logger
	Now          func() time.Time
	RequestID    func() string

	UpstreamURL       string
	LangfusePublicKey string
	LangfuseSecretKey string
	HTTPClient        *http.Client
}

type Server struct {
	introspector PrincipalIntrospector
	store        inbox.Store
	trigger      func()
	maxBodyBytes int64
	limiter      RateLimiter
	logger       *slog.Logger
	now          func() time.Time
	requestID    func() string
}

func New(config Config) (*Server, error) {
	if config.Introspector == nil {
		return nil, errors.New("introspector is required")
	}
	if config.Store == nil {
		return nil, errors.New("inbox store is required")
	}
	if config.MaxBodyBytes <= 0 {
		config.MaxBodyBytes = defaultMaxBodyBytes
	}
	if config.Limiter == nil {
		config.Limiter = newWindowLimiter(120, time.Minute, 10_000)
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.RequestID == nil {
		config.RequestID = randomRequestID
	}
	return &Server{
		introspector: config.Introspector,
		store:        config.Store,
		trigger:      config.Trigger,
		maxBodyBytes: config.MaxBodyBytes,
		limiter:      config.Limiter,
		logger:       config.Logger,
		now:          config.Now,
		requestID:    config.RequestID,
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.health)
	mux.HandleFunc("/v1/traces", s.traces)
	return mux
}

func (s *Server) health(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if err := s.store.Sync(); err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) traces(w http.ResponseWriter, request *http.Request) {
	started := s.now()
	requestID := s.requestID()
	status, size := s.serveTrace(w, request, started)
	s.logger.Info("trace request",
		"request_id", requestID,
		"status", status,
		"size", size,
		"duration_ms", s.now().Sub(started).Milliseconds(),
	)
}

func (s *Server) serveTrace(w http.ResponseWriter, request *http.Request, now time.Time) (int, int) {
	if request.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return http.StatusMethodNotAllowed, 0
	}
	contentType, contentTypeOK := singleRequiredHeader(request.Header, "Content-Type")
	contentEncoding, contentEncodingOK := singleOptionalHeader(request.Header, "Content-Encoding")
	if !contentTypeOK || !contentEncodingOK || !validMediaType(contentType) || !validEncoding(contentEncoding) {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type")
		return http.StatusUnsupportedMediaType, 0
	}
	headers, ok := validatedHeaders(request.Header)
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_headers")
		return http.StatusBadRequest, 0
	}
	authorization, authorizationOK := singleRequiredHeader(request.Header, "Authorization")
	bearer, ok := bearerToken(authorization)
	if !authorizationOK || !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return http.StatusUnauthorized, 0
	}
	principal, err := s.introspector.Introspect(request.Context(), bearer)
	if err != nil {
		if errors.Is(err, auth.ErrExplicitRevocation) {
			if writeExplicitRevocation(w, err) {
				return http.StatusForbidden, 0
			}
			writeError(w, http.StatusServiceUnavailable, "authentication_unavailable")
			return http.StatusServiceUnavailable, 0
		}
		if errors.Is(err, auth.ErrRefreshRequired) {
			writeError(w, http.StatusUnauthorized, inactiveErrorCode(err, "trace_token_refresh_required"))
			return http.StatusUnauthorized, 0
		}
		writeError(w, http.StatusServiceUnavailable, "authentication_unavailable")
		return http.StatusServiceUnavailable, 0
	}
	if !s.limiter.Allow(principal.TokenID, sourceAddress(request.RemoteAddr), now) {
		writeError(w, http.StatusTooManyRequests, "rate_limited")
		return http.StatusTooManyRequests, 0
	}

	request.Body = http.MaxBytesReader(w, request.Body, s.maxBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload_too_large")
			return http.StatusRequestEntityTooLarge, 0
		}
		writeError(w, http.StatusBadRequest, "invalid_body")
		return http.StatusBadRequest, 0
	}
	var export collectortracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &export); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_protobuf")
		return http.StatusBadRequest, len(body)
	}
	digest := sha256.Sum256(body)
	if hex.EncodeToString(digest[:]) != headers.PayloadSHA256 {
		writeError(w, http.StatusConflict, "payload_digest_mismatch")
		return http.StatusConflict, len(body)
	}
	if err := otlp.Canonicalize(&export, principal, headers.Trace, headers.BatchID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	canonicalBody, err := proto.Marshal(&export)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	result, err := s.store.Accept(request.Context(), inbox.Envelope{
		AccountID:      principal.AccountID,
		SessionID:      principal.SessionID,
		InstallationID: principal.InstallationID,
		BatchID:        headers.BatchID,
		PayloadSHA256:  headers.PayloadSHA256,
		Payload:        canonicalBody,
		Headers: inbox.TraceHeaders{
			SessionID: headers.Trace.SessionID, Entrypoint: headers.Trace.Entrypoint,
			RunID: headers.Trace.RunID, SchemaVersion: headers.Trace.SchemaVersion,
		},
	})
	if err != nil {
		if errors.Is(err, inbox.ErrIdempotencyConflict) {
			writeError(w, http.StatusConflict, "idempotency_conflict")
			return http.StatusConflict, len(body)
		}
		if errors.Is(err, inbox.ErrStorageUnavailable) {
			writeError(w, http.StatusInsufficientStorage, "storage_unavailable")
			return http.StatusInsufficientStorage, len(body)
		}
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return http.StatusServiceUnavailable, len(body)
	}
	if result.BatchID != headers.BatchID || (result.Outcome != inbox.ReceiptAccepted && result.Outcome != inbox.ReceiptDuplicate) {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return http.StatusServiceUnavailable, len(body)
	}
	if result.Outcome == inbox.ReceiptAccepted && s.trigger != nil {
		s.trigger()
	}
	writeSuccess(w, headers.BatchID, result.Outcome)
	return http.StatusOK, len(body)
}

type requestHeaders struct {
	Trace         otlp.Headers
	BatchID       string
	PayloadSHA256 string
}

func validatedHeaders(header http.Header) (requestHeaders, bool) {
	sessionID, sessionOK := singleRequiredHeader(header, "X-Hermes-Session-ID")
	entrypoint, entrypointOK := singleRequiredHeader(header, "X-Trace-Entrypoint")
	runID, runOK := singleRequiredHeader(header, "X-Trace-Run-ID")
	schemaVersion, schemaOK := singleRequiredHeader(header, "X-Telemetry-Schema-Version")
	batchID, batchOK := singleRequiredHeader(header, "Idempotency-Key")
	payloadSHA256, digestOK := singleRequiredHeader(header, "X-Trace-Payload-SHA256")
	if !sessionOK || !entrypointOK || !runOK || !schemaOK || !batchOK || !digestOK {
		return requestHeaders{}, false
	}
	trace := otlp.Headers{
		SessionID:     sessionID,
		Entrypoint:    entrypoint,
		RunID:         runID,
		SchemaVersion: schemaVersion,
	}
	if !identifierPattern.MatchString(trace.SessionID) || !identifierPattern.MatchString(trace.RunID) || trace.SchemaVersion != "1" ||
		!uuidV4Pattern.MatchString(batchID) || !sha256HexPattern.MatchString(payloadSHA256) {
		return requestHeaders{}, false
	}
	switch trace.Entrypoint {
	case "desktop", "voice", "cli", "dashboard":
		return requestHeaders{Trace: trace, BatchID: strings.ToLower(batchID), PayloadSHA256: payloadSHA256}, true
	default:
		return requestHeaders{}, false
	}
}

func singleRequiredHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return "", false
	}
	return values[0], true
}

func singleOptionalHeader(header http.Header, name string) (string, bool) {
	values := header.Values(name)
	if len(values) == 0 {
		return "", true
	}
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func bearerToken(value string) (string, bool) {
	if !strings.HasPrefix(value, "Bearer ") || strings.Count(value, " ") != 1 {
		return "", false
	}
	token := strings.TrimPrefix(value, "Bearer ")
	return token, len(token) >= 32 && len(token) <= 128
}

func validMediaType(value string) bool {
	base := strings.TrimSpace(strings.SplitN(value, ";", 2)[0])
	return strings.EqualFold(base, "application/x-protobuf")
}

func validEncoding(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "identity")
}

func sourceAddress(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return "unknown"
	}
	return host
}

func randomRequestID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("fallback-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func writeError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": code})
}

func writeSuccess(w http.ResponseWriter, batchID string, outcome inbox.ReceiptOutcome) {
	body, err := proto.Marshal(&collectortracepb.ExportTraceServiceResponse{})
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "storage_unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/x-protobuf")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Trace-Batch-ID", batchID)
	w.Header().Set("X-Trace-Receipt", string(outcome))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func writeExplicitRevocation(w http.ResponseWriter, err error) bool {
	var inactive *auth.InactiveError
	if !errors.As(err, &inactive) || !inactive.Explicit ||
		!explicitRevocationCode(inactive.Code) ||
		!uuidV4Pattern.MatchString(inactive.AccountID) ||
		!uuidV4Pattern.MatchString(inactive.SessionID) ||
		!uuidV4Pattern.MatchString(inactive.InstallationID) || inactive.RevokedAt.IsZero() {
		return false
	}
	body := struct {
		State     string `json:"state"`
		Code      string `json:"code"`
		AccountID string `json:"account_id"`
		SessionID string `json:"session_id"`
		RevokedAt string `json:"revoked_at"`
		Retryable bool   `json:"retryable"`
	}{
		State:     "revoked",
		Code:      inactive.Code,
		AccountID: inactive.AccountID,
		SessionID: inactive.SessionID,
		RevokedAt: inactive.RevokedAt.UTC().Format(time.RFC3339Nano),
		Retryable: false,
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(body)
	return true
}

func explicitRevocationCode(code string) bool {
	switch code {
	case "account_disabled", "account_revoked", "session_revoked":
		return true
	default:
		return false
	}
}

func inactiveErrorCode(err error, fallback string) string {
	var inactive *auth.InactiveError
	if errors.As(err, &inactive) && inactive.Code != "" {
		return inactive.Code
	}
	return fallback
}

type windowLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	maximum int
	entries map[string]windowEntry
}

type windowEntry struct {
	started time.Time
	count   int
}

func newWindowLimiter(limit int, window time.Duration, maximum int) *windowLimiter {
	return &windowLimiter{limit: limit, window: window, maximum: maximum, entries: make(map[string]windowEntry)}
}

func (limiter *windowLimiter) Allow(tokenID, source string, now time.Time) bool {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	key := tokenID + "\x00" + source
	entry, found := limiter.entries[key]
	if !found || now.Sub(entry.started) >= limiter.window {
		if len(limiter.entries) >= limiter.maximum {
			for existingKey, existing := range limiter.entries {
				if now.Sub(existing.started) >= limiter.window {
					delete(limiter.entries, existingKey)
				}
			}
			if len(limiter.entries) >= limiter.maximum {
				return false
			}
		}
		limiter.entries[key] = windowEntry{started: now, count: 1}
		return true
	}
	if entry.count >= limiter.limit {
		return false
	}
	entry.count++
	limiter.entries[key] = entry
	return true
}
