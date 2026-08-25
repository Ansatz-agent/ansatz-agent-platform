package auth

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	ErrRefreshRequired    = errors.New("trace credential refresh required")
	ErrExplicitRevocation = errors.New("trace credential explicitly revoked")
	ErrUnavailable        = errors.New("trace credential service unavailable")

	// ErrInactive preserves the legacy refresh-required classification until
	// all Gateway consumers switch to ErrRefreshRequired.
	ErrInactive = ErrRefreshRequired
)

type InactiveError struct {
	Code           string
	Explicit       bool
	AccountID      string
	SessionID      string
	InstallationID string
	RevokedAt      time.Time
}

func (e *InactiveError) Error() string {
	return "trace credential inactive"
}

func (e *InactiveError) Is(target error) bool {
	if e.Explicit {
		return target == ErrExplicitRevocation
	}
	return target == ErrRefreshRequired
}

type Principal struct {
	TokenID        string
	AccountID      string
	SessionID      string
	UserID         string
	Username       string
	InstallationID string
	ExpiresAt      time.Time
	Scope          string
	Audience       string
}

type Introspector struct {
	endpoint       string
	internalSecret string
	client         *http.Client
}

type introspectionResponse struct {
	Active           bool   `json:"active"`
	Reason           string `json:"reason"`
	TokenID          string `json:"token_id"`
	AccountID        string `json:"account_id"`
	SessionID        string `json:"session_id"`
	PlatformUserID   string `json:"platform_user_id"`
	PlatformUsername string `json:"platform_username"`
	InstallationID   string `json:"installation_id"`
	RevokedAt        string `json:"revoked_at"`
	ExpiresAt        string `json:"expires_at"`
	Scope            string `json:"scope"`
	Audience         string `json:"audience"`
}

func NewIntrospector(endpoint, internalSecret string, timeout time.Duration) *Introspector {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Introspector{
		endpoint:       endpoint,
		internalSecret: internalSecret,
		client:         &http.Client{Timeout: timeout},
	}
}

func (i *Introspector) Introspect(ctx context.Context, bearer string) (Principal, error) {
	payload, err := json.Marshal(map[string]string{"token": bearer})
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint, bytes.NewReader(payload))
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	request.Header.Set("X-Ansatz-Internal-Token", i.internalSecret)
	request.Header.Set("X-Forwarded-Proto", "https")

	response, err := i.client.Do(request)
	if err != nil {
		return Principal{}, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Principal{}, ErrUnavailable
	}

	decoder := json.NewDecoder(io.LimitReader(response.Body, 16*1024))
	var body introspectionResponse
	if err := decoder.Decode(&body); err != nil {
		return Principal{}, ErrUnavailable
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Principal{}, ErrUnavailable
	}
	if !body.Active {
		switch body.Reason {
		case "token_expired", "token_rotated", "token_revoked", "invalid_token":
			return Principal{}, &InactiveError{Code: "trace_token_refresh_required"}
		case "session_revoked", "account_disabled", "account_revoked":
			revokedAt, err := time.Parse(time.RFC3339, body.RevokedAt)
			if err != nil || !validUUIDv4(body.AccountID) || !validUUIDv4(body.SessionID) || !validUUIDv4(body.InstallationID) {
				return Principal{}, ErrUnavailable
			}
			return Principal{}, &InactiveError{
				Code:           body.Reason,
				Explicit:       true,
				AccountID:      body.AccountID,
				SessionID:      body.SessionID,
				InstallationID: body.InstallationID,
				RevokedAt:      revokedAt,
			}
		default:
			return Principal{}, ErrUnavailable
		}
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil || body.TokenID == "" ||
		!validUUIDv4(body.AccountID) || !validUUIDv4(body.SessionID) || !validUUIDv4(body.InstallationID) ||
		body.PlatformUserID == "" || body.PlatformUsername == "" ||
		body.Scope != "trace:write" || body.Audience != "ansatz-trace-gateway" || !expiresAt.After(time.Now()) {
		return Principal{}, ErrUnavailable
	}
	return Principal{
		TokenID:        body.TokenID,
		AccountID:      body.AccountID,
		SessionID:      body.SessionID,
		UserID:         body.PlatformUserID,
		Username:       body.PlatformUsername,
		InstallationID: body.InstallationID,
		ExpiresAt:      expiresAt,
		Scope:          body.Scope,
		Audience:       body.Audience,
	}, nil
}

func validUUIDv4(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded := make([]byte, 16)
	n, err := hex.Decode(decoded, []byte(strings.ReplaceAll(value, "-", "")))
	return err == nil && n == len(decoded) && decoded[6]>>4 == 4 && decoded[8]&0xc0 == 0x80
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected extra JSON value")
		}
		return err
	}
	return nil
}
