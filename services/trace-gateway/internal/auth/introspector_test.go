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
		body, _ := io.ReadAll(r.Body)
		var request map[string]string
		if err := json.Unmarshal(body, &request); err != nil {
			t.Fatal(err)
		}
		if request["token"] != testBearer || len(request) != 1 {
			t.Fatalf("unexpected request: %#v", request)
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"active":true,"token_id":"token-id","platform_user_id":"42","installation_id":"11111111-1111-4111-8111-111111111111","expires_at":"2030-01-02T03:04:05Z","scope":"trace:write","audience":"ansatz-trace-gateway"}`)
	}))
	defer server.Close()

	client := NewIntrospector(server.URL, testSecret, 250*time.Millisecond)
	principal, err := client.Introspect(context.Background(), testBearer)
	if err != nil {
		t.Fatal(err)
	}
	if principal.TokenID != "token-id" || principal.UserID != "42" || principal.Scope != "trace:write" {
		t.Fatalf("unexpected principal: %#v", principal)
	}
	if principal.ExpiresAt.Format(time.RFC3339) != "2030-01-02T03:04:05Z" {
		t.Fatalf("unexpected expiry: %s", principal.ExpiresAt)
	}
}

func TestIntrospectorMapsInactiveWithoutLeakingBearer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		io.WriteString(w, `{"active":false}`)
	}))
	defer server.Close()

	_, err := NewIntrospector(server.URL, testSecret, time.Second).Introspect(
		context.Background(), testBearer,
	)
	if !errors.Is(err, ErrInactive) {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), testBearer) || strings.Contains(err.Error(), testSecret) {
		t.Fatalf("secret leaked in error: %v", err)
	}
}

func TestIntrospectorRejectsUnknownShapeAndTimesOut(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"unknown field": func(w http.ResponseWriter, _ *http.Request) {
			io.WriteString(w, `{"active":false,"reason":"expired"}`)
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
