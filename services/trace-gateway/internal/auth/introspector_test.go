package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const (
	testBearer = "upload-token-that-must-never-appear-in-errors-123456"
	testSecret = "internal-service-secret-that-must-not-leak"
)

func TestIntrospectorReturnsExactActivePrincipal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Ansatz-Internal-Token"); got != testSecret {
			t.Fatalf("internal header = %q", got)
		}
		if got := r.Header.Get("X-Forwarded-Proto"); got != "https" {
			t.Fatalf("forwarded proto = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		var request map[string]string
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request["token"] != testBearer || len(request) != 1 {
			t.Fatalf("unexpected request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway","extra":"ok"}`)
	}))
	defer server.Close()

	client := NewIntrospector(server.URL, testSecret, 250*time.Millisecond)
	principal, err := client.Introspect(context.Background(), testBearer)
	if err != nil {
		t.Fatal(err)
	}
	if principal.TokenID != "token-id" ||
		principal.AccountID != "11111111-1111-4111-8111-111111111111" ||
		principal.SessionID != "22222222-2222-4222-8222-222222222222" ||
		principal.InstallationID != "33333333-3333-4333-8333-333333333333" ||
		principal.UserID != "42" || principal.Username != "yiyuxiao" || principal.Scope != "trace:write" {
		t.Fatalf("unexpected principal: %#v", principal)
	}
	if principal.ExpiresAt.Format(time.RFC3339) != "2100-01-02T03:04:05Z" {
		t.Fatalf("unexpected expiry: %s", principal.ExpiresAt)
	}
}

func TestIntrospectorClassifiesAdditiveInactiveReasons(t *testing.T) {
	tests := []struct {
		name string
		body string
		want error
		code string
	}{
		{"expired", `{"active":false,"reason":"token_expired","extra":"ok"}`, ErrRefreshRequired, "trace_token_refresh_required"},
		{"rotated", `{"active":false,"reason":"token_rotated"}`, ErrRefreshRequired, "trace_token_refresh_required"},
		{"token revoked", `{"active":false,"reason":"token_revoked"}`, ErrRefreshRequired, "trace_token_refresh_required"},
		{"invalid", `{"active":false,"reason":"invalid_token"}`, ErrRefreshRequired, "trace_token_refresh_required"},
		{"session revoked", `{"active":false,"reason":"session_revoked","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrExplicitRevocation, "session_revoked"},
		{"account disabled", `{"active":false,"reason":"account_disabled","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrExplicitRevocation, "account_disabled"},
		{"account revoked", `{"active":false,"reason":"account_revoked","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrExplicitRevocation, "account_revoked"},
		{"explicit reason without trusted identity", `{"active":false,"reason":"session_revoked","account_id":"not-a-uuid","session_id":"22222222-2222-4222-8222-222222222222"}`, ErrUnavailable, ""},
		{"unknown", `{"active":false,"reason":"unknown_new_reason"}`, ErrUnavailable, ""},
		{"missing", `{"active":false}`, ErrUnavailable, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertIntrospectionError(t, test.body, test.want, test.code)
		})
	}
}

func TestIntrospectorRejectsMalformedRequiredActiveFields(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"missing account", `{"active":true,"token_id":"token-id","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`},
		{"invalid session", `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"not-a-uuid","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`},
		{"invalid installation", `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"not-a-uuid","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`},
		{"expired", `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2000-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`},
		{"wrong scope", `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"other","audience":"ansatz-trace-gateway"}`},
		{"wrong audience", `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","platform_username":"yiyuxiao","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"other"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertIntrospectionError(t, test.body, ErrUnavailable, "")
		})
	}
}

func TestIntrospectorRejectsActiveResponseWithoutUsername(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"active":true,"token_id":"token-id","account_id":"11111111-1111-4111-8111-111111111111","session_id":"22222222-2222-4222-8222-222222222222","platform_user_id":"42","installation_id":"33333333-3333-4333-8333-333333333333","expires_at":"2100-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`)
	}))
	defer server.Close()

	_, err := NewIntrospector(server.URL, testSecret, time.Second).Introspect(
		context.Background(), testBearer,
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v", err)
	}
}

func TestIntrospectorRejectsUnknownShapeAndTimesOut(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"malformed body": func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"active":`)
		},
		"non-200": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream unavailable", http.StatusServiceUnavailable)
		},
		"timeout": func(w http.ResponseWriter, _ *http.Request) {
			time.Sleep(100 * time.Millisecond)
			io.WriteString(w, `{"active":false}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(handler)
			defer server.Close()
			_, err := NewIntrospector(server.URL, testSecret, 10*time.Millisecond).Introspect(
				context.Background(), testBearer,
			)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v", err)
			}
			if strings.Contains(err.Error(), testBearer) || strings.Contains(err.Error(), testSecret) {
				t.Fatalf("secret leaked in error: %v", err)
			}
		})
	}
}

func assertIntrospectionError(t *testing.T, body string, want error, code string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, body)
	}))
	defer server.Close()

	_, err := NewIntrospector(server.URL, testSecret, time.Second).Introspect(
		context.Background(), testBearer,
	)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	var inactive *InactiveError
	if code == "" {
		if errors.As(err, &inactive) {
			t.Fatalf("unexpected inactive error: %#v", inactive)
		}
	} else if !errors.As(err, &inactive) {
		t.Fatalf("error type = %T, want *InactiveError", err)
	} else if inactive.Code != code {
		t.Fatalf("code = %q, want %q", inactive.Code, code)
	}
	if strings.Contains(err.Error(), testBearer) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("secret leaked in error: %v", err)
	}
}
