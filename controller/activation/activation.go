// Package activation talks to the Tab Router activation backend and stores
// the resulting installation token. Decodo passwords are not kept here:
// every start asks the backend for the proxy access that installation is
// allowed to use.
package activation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const httpTimeout = 15 * time.Second

var (
	ErrInvalidKey   = errors.New("activation key is invalid")
	ErrExpired      = errors.New("activation expired")
	ErrRevoked      = errors.New("activation revoked")
	ErrLimit        = errors.New("activation key is already in use")
	ErrUnavailable  = errors.New("activation server unavailable")
	ErrNotActivated = errors.New("not activated")
	ErrProxyMissing = errors.New("activation did not include proxy access")
)

// Proxy is the limited Decodo access the backend assigned to this
// installation. Username is the Decodo account, not a per-identity session.
type Proxy struct {
	Host           string `json:"host"`
	Port           int    `json:"port"`
	Username       string `json:"username"`
	Password       string `json:"password"`
	Country        string `json:"country,omitempty"`
	SessionMinutes int    `json:"session_minutes,omitempty"`
}

// Activation is one successful authorize or validate response.
type Activation struct {
	InstallationID string
	Token          string
	Status         string
	ExpiresAt      time.Time
	Server         string
	Proxy          Proxy
}

// Client is the activation HTTP API.
type Client struct {
	Server string
	HTTP   *http.Client
}

type apiError struct {
	Error string `json:"error"`
}

type apiActivation struct {
	InstallationID string    `json:"installation_id"`
	Token          string    `json:"token"`
	Status         string    `json:"status"`
	ExpiresAt      time.Time `json:"expires_at"`
	Proxy          Proxy     `json:"proxy"`
}

// Activate exchanges a key for an installation token and proxy access.
// installationID may be empty for a first activation.
func (c Client) Activate(ctx context.Context, key, installationID string) (*Activation, error) {
	return c.post(ctx, "/v1/activate", map[string]string{
		"key":             strings.TrimSpace(key),
		"installation_id": installationID,
	})
}

// Validate checks a stored installation and returns current proxy access.
func (c Client) Validate(ctx context.Context, installationID, token string) (*Activation, error) {
	return c.post(ctx, "/v1/validate", map[string]string{
		"installation_id": installationID,
		"token":           token,
	})
}

// Deactivate revokes this installation on the backend.
func (c Client) Deactivate(ctx context.Context, installationID, token string) error {
	_, err := c.post(ctx, "/v1/deactivate", map[string]string{
		"installation_id": installationID,
		"token":           token,
	})
	if err != nil && !errors.Is(err, ErrProxyMissing) {
		return err
	}
	return nil
}

func (c Client) post(ctx context.Context, path string, body any) (*Activation, error) {
	server := strings.TrimRight(strings.TrimSpace(c.Server), "/")
	if server == "" {
		return nil, ErrUnavailable
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server+path, bytes.NewReader(raw))
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w", ErrUnavailable)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, fmt.Errorf("%w", ErrUnavailable)
	}
	if resp.StatusCode != http.StatusOK {
		var api apiError
		_ = json.Unmarshal(payload, &api)
		return nil, mapAPIError(resp.StatusCode, api.Error)
	}
	var out apiActivation
	if err := json.Unmarshal(payload, &out); err != nil {
		return nil, fmt.Errorf("%w", ErrUnavailable)
	}
	act := &Activation{
		InstallationID: out.InstallationID,
		Token:          out.Token,
		Status:         out.Status,
		ExpiresAt:      out.ExpiresAt,
		Server:         server,
		Proxy:          out.Proxy,
	}
	if path != "/v1/deactivate" {
		if act.Proxy.Username == "" || act.Proxy.Password == "" || act.Proxy.Host == "" || act.Proxy.Port == 0 {
			return nil, ErrProxyMissing
		}
	}
	return act, nil
}

func mapAPIError(status int, code string) error {
	switch code {
	case "invalid_key", "invalid_token":
		return ErrInvalidKey
	case "expired":
		return ErrExpired
	case "revoked":
		return ErrRevoked
	case "installation_limit":
		return ErrLimit
	case "proxy_unconfigured":
		return ErrProxyMissing
	}
	if status == http.StatusUnauthorized {
		return ErrInvalidKey
	}
	if status == http.StatusForbidden {
		return ErrRevoked
	}
	return ErrUnavailable
}
