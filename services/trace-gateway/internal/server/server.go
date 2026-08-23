package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/auth"
	"github.com/Ansatz-agent/ansatz-agent-platform/services/trace-gateway/internal/otlp"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/protobuf/proto"
)

const (
	defaultMaxBodyBytes = int64(8 * 1024 * 1024)
	maxUpstreamBytes    = int64(1024 * 1024)
	cacheLifetime       = 15 * time.Minute
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type PrincipalIntrospector interface {
	Introspect(context.Context, string) (auth.Principal, error)
}

type RateLimiter interface {
	Allow(tokenID, source string, now time.Time) bool
}

type Config struct {
	Introspector      PrincipalIntrospector
	UpstreamURL       string
	LangfusePublicKey string
	LangfuseSecretKey string
	HTTPClient        *http.Client
	MaxBodyBytes      int64
	Limiter           RateLimiter
	Logger            *slog.Logger
	Now               func() time.Time
	RequestID         func() string
}

type Server struct {
	introspector  PrincipalIntrospector
	upstreamURL   string
	upstreamBasic string
	httpClient    *http.Client
	maxBodyBytes  int64
	limiter       RateLimiter
	logger        *slog.Logger
	now           func() time.Time
	requestID     func() string
	cache         *responseCache
}

func New(config Config) (*Server, error) {
	if config.Introspector == nil {
		return nil, errors.New("introspector is required")
	}
	parsed, err := url.Parse(config.UpstreamURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, errors.New("valid upstream URL is required")
	}
	if config.LangfusePublicKey == "" || config.LangfuseSecretKey == "" {
		return nil, errors.New("Langfuse server credentials are required")
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{Timeout: 10 * time.Second}
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
		introspector:  config.Introspector,
		upstreamURL:   parsed.String(),
		upstreamBasic: "Basic " + base64.StdEncoding.EncodeToString([]byte(config.LangfusePublicKey+":"+config.LangfuseSecretKey)),
		httpClient:    config.HTTPClient,
		maxBodyBytes:  config.MaxBodyBytes,
		limiter:       config.Limiter,
		logger:        config.Logger,
		now:           config.Now,
		requestID:     config.RequestID,
		cache:         newResponseCache(4096),
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (s *Server) traces(w http.ResponseWriter, request *http.Request) {
	started := s.now()
	requestID := s.requestID()
	status, size := s.serveTrace(w, request, requestID, started)
	s.logger.Info("trace request",
		"request_id", requestID,
		"status", status,
		"size", size,
		"duration_ms", s.now().Sub(started).Milliseconds(),
	)
}

func (s *Server) serveTrace(w http.ResponseWriter, request *http.Request, requestID string, now time.Time) (int, int) {
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
		if errors.Is(err, auth.ErrInactive) {
			writeError(w, http.StatusUnauthorized, "unauthorized")
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
	if err := otlp.Canonicalize(&export, principal, headers, "idempotency"); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	digestBody, err := proto.Marshal(&export)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	digest := requestDigest(principal.TokenID, digestBody)
	if cached, found := s.cache.Get(digest, now); found {
		writeUpstreamResponse(w, cached)
		return cached.status, len(body)
	}
	if err := otlp.Canonicalize(&export, principal, headers, requestID); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	canonicalBody, err := proto.Marshal(&export)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_trace")
		return http.StatusBadRequest, len(body)
	}
	upstream, err := s.forward(request.Context(), canonicalBody)
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_unavailable")
		return http.StatusBadGateway, len(body)
	}
	expiresAt := now.Add(cacheLifetime)
	if principal.ExpiresAt.Before(expiresAt) {
		expiresAt = principal.ExpiresAt
	}
	upstream.expiresAt = expiresAt
	s.cache.Put(digest, upstream, now)
	writeUpstreamResponse(w, upstream)
	return upstream.status, len(body)
}

func (s *Server) forward(ctx context.Context, body []byte) (cachedResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.upstreamURL, bytes.NewReader(body))
	if err != nil {
		return cachedResponse{}, err
	}
	request.Header.Set("Authorization", s.upstreamBasic)
	request.Header.Set("Content-Type", "application/x-protobuf")
	request.Header.Set("Accept", "application/x-protobuf")
	response, err := s.httpClient.Do(request)
	if err != nil {
		return cachedResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxUpstreamBytes+1))
	if err != nil || int64(len(responseBody)) > maxUpstreamBytes {
		return cachedResponse{}, errors.New("invalid upstream response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return cachedResponse{}, fmt.Errorf("upstream rejected request")
	}
	contentType := response.Header.Get("Content-Type")
	if !validMediaType(contentType) {
		contentType = "application/x-protobuf"
	}
	return cachedResponse{status: response.StatusCode, body: responseBody, contentType: contentType}, nil
}

func validatedHeaders(header http.Header) (otlp.Headers, bool) {
	sessionID, sessionOK := singleRequiredHeader(header, "X-Hermes-Session-ID")
	entrypoint, entrypointOK := singleRequiredHeader(header, "X-Trace-Entrypoint")
	runID, runOK := singleRequiredHeader(header, "X-Trace-Run-ID")
	schemaVersion, schemaOK := singleRequiredHeader(header, "X-Telemetry-Schema-Version")
	if !sessionOK || !entrypointOK || !runOK || !schemaOK {
		return otlp.Headers{}, false
	}
	values := otlp.Headers{
		SessionID:     sessionID,
		Entrypoint:    entrypoint,
		RunID:         runID,
		SchemaVersion: schemaVersion,
	}
	if !identifierPattern.MatchString(values.SessionID) || !identifierPattern.MatchString(values.RunID) || values.SchemaVersion != "1" {
		return otlp.Headers{}, false
	}
	switch values.Entrypoint {
	case "desktop", "voice", "cli", "dashboard":
		return values, true
	default:
		return otlp.Headers{}, false
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

func requestDigest(tokenID string, body []byte) string {
	digest := sha256.New()
	_, _ = io.WriteString(digest, tokenID)
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(body)
	return hex.EncodeToString(digest.Sum(nil))
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

type cachedResponse struct {
	status      int
	body        []byte
	contentType string
	expiresAt   time.Time
}

func writeUpstreamResponse(w http.ResponseWriter, response cachedResponse) {
	w.Header().Set("Content-Type", response.contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.status)
	_, _ = w.Write(response.body)
}

type responseCache struct {
	mu      sync.Mutex
	entries map[string]cachedResponse
	max     int
}

func newResponseCache(maximum int) *responseCache {
	return &responseCache{entries: make(map[string]cachedResponse), max: maximum}
}

func (cache *responseCache) Get(key string, now time.Time) (cachedResponse, bool) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	entry, found := cache.entries[key]
	if !found {
		return cachedResponse{}, false
	}
	if !entry.expiresAt.After(now) {
		delete(cache.entries, key)
		return cachedResponse{}, false
	}
	entry.body = append([]byte(nil), entry.body...)
	return entry, true
}

func (cache *responseCache) Put(key string, response cachedResponse, now time.Time) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	for existingKey, entry := range cache.entries {
		if !entry.expiresAt.After(now) {
			delete(cache.entries, existingKey)
		}
	}
	if len(cache.entries) >= cache.max {
		var oldestKey string
		var oldest time.Time
		for existingKey, entry := range cache.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = existingKey, entry.expiresAt
			}
		}
		delete(cache.entries, oldestKey)
	}
	response.body = append([]byte(nil), response.body...)
	cache.entries[key] = response
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
