// Package iamclient fetches and caches client_credentials access tokens from
// byz-iam for a service's own machine identity.
//
// Frozen verbs: NewTokenSource, Token.
//
// Use this only for service-to-service calls that are genuinely the
// service's own. For anything done on behalf of a user, forward the caller's
// bearer with package downstream instead.
package iamclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ErrNotConfigured is returned when base URL, client id or secret is blank.
var ErrNotConfigured = errors.New("iamclient: IAM client credentials not configured")

// TokenSource mints and caches a client_credentials token, refreshing it
// shortly before expiry so a mid-request expiry is rare.
type TokenSource struct {
	baseURL      string
	clientID     string
	clientSecret string
	http         *http.Client
	now          func() time.Time

	mu        sync.Mutex
	token     string
	expiresAt time.Time
}

// NewTokenSource builds a source against IAM at baseURL (no trailing slash
// needed). A nil httpClient uses a 15 second timeout.
func NewTokenSource(baseURL, clientID, clientSecret string, httpClient *http.Client) *TokenSource {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &TokenSource{
		baseURL:      strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		clientID:     strings.TrimSpace(clientID),
		clientSecret: strings.TrimSpace(clientSecret),
		http:         httpClient,
		now:          time.Now,
	}
}

// Configured reports whether all three settings are present.
func (s *TokenSource) Configured() bool {
	return s != nil && s.baseURL != "" && s.clientID != "" && s.clientSecret != ""
}

// Token returns a valid access token, refreshing when needed.
func (s *TokenSource) Token(ctx context.Context) (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token != "" && s.now().Before(s.expiresAt) {
		return s.token, nil
	}
	return s.refreshLocked(ctx)
}

// Invalidate drops the cached token so the next Token call refreshes. Call
// it after a 401 from a downstream that should have accepted the token.
func (s *TokenSource) Invalidate() {
	s.mu.Lock()
	s.token = ""
	s.expiresAt = time.Time{}
	s.mu.Unlock()
}

func (s *TokenSource) refreshLocked(ctx context.Context) (string, error) {
	body, _ := json.Marshal(map[string]string{
		"grantType":    "client_credentials",
		"clientId":     s.clientID,
		"clientSecret": s.clientSecret,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/api/v1/oauth/token", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("iamclient: token request: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("iamclient: token status %d: %s", resp.StatusCode, truncate(string(raw), 300))
	}

	var out struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("iamclient: token decode: %w", err)
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", errors.New("iamclient: token response missing accessToken")
	}

	ttl := out.ExpiresIn
	if ttl <= 0 {
		ttl = 3600
	}
	skew := ttl / 10
	if skew < 30 {
		skew = 30
	}
	if skew >= ttl {
		skew = ttl / 2
	}
	s.token = out.AccessToken
	s.expiresAt = s.now().Add(time.Duration(ttl-skew) * time.Second)
	return s.token, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
