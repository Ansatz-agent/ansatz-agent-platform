package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var (
	ErrInactive    = errors.New("trace credential inactive")
	ErrUnavailable = errors.New("trace credential service unavailable")
)

type Principal struct {
	TokenID        string
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
	TokenID          string `json:"token_id"`
	PlatformUserID   string `json:"platform_user_id"`
	PlatformUsername string `json:"platform_username"`
	InstallationID   string `json:"installation_id"`
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
	decoder.DisallowUnknownFields()
	var body introspectionResponse
	if err := decoder.Decode(&body); err != nil {
		return Principal{}, ErrUnavailable
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Principal{}, ErrUnavailable
	}
	if !body.Active {
		if body != (introspectionResponse{Active: false}) {
			return Principal{}, ErrUnavailable
		}
		return Principal{}, ErrInactive
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil || body.TokenID == "" || body.PlatformUserID == "" || body.PlatformUsername == "" || body.InstallationID == "" {
		return Principal{}, ErrUnavailable
	}
	if body.Scope != "trace:write" || body.Audience != "ansatz-trace-gateway" || !expiresAt.After(time.Now()) {
		return Principal{}, ErrInactive
	}
	return Principal{
		TokenID:        body.TokenID,
		UserID:         body.PlatformUserID,
		Username:       body.PlatformUsername,
		InstallationID: body.InstallationID,
		ExpiresAt:      expiresAt,
		Scope:          body.Scope,
		Audience:       body.Audience,
	}, nil
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
