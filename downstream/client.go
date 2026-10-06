// Package downstream calls other Byz services as the caller, forwarding the
// caller's bearer and request id so the callee sees the tenant actor and not
// the calling service (no confused deputy, kan CW-39).
//
// Frozen verbs: New, NewRequest, Do, JSON.
//
// Do refuses to send a request that has no Authorization header and no bearer
// on the context. A service that genuinely needs its own identity sets the
// header explicitly from iamclient; nothing here swaps credentials silently.
package downstream

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

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// ErrNoBearer is returned by Do when neither the request nor its context
// carries a credential.
var ErrNoBearer = errors.New("downstream: no bearer on request or context")

// Client targets one downstream base URL.
type Client struct {
	BaseURL string
	HTTP    *http.Client
	// UserAgent is sent on every request. Defaults to "byz-platform".
	UserAgent string
}

// New builds a Client with a 15 second timeout.
func New(baseURL string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// NewRequest builds a request against BaseURL + path with ctx attached.
func (c *Client) NewRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
}

// Do sends the request, copying the caller's bearer and X-Request-Id from
// req.Context() when the request does not already carry them.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	ctx := req.Context()
	if req.Header.Get("Authorization") == "" {
		tok := iam.BearerFrom(ctx)
		if tok == "" {
			return nil, ErrNoBearer
		}
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if req.Header.Get(httpx.RequestIDHeader) == "" {
		if id := httpx.RequestIDFrom(ctx); id != "" {
			req.Header.Set(httpx.RequestIDHeader, id)
		}
	}
	if req.Header.Get("User-Agent") == "" {
		ua := c.UserAgent
		if ua == "" {
			ua = "byz-platform"
		}
		req.Header.Set("User-Agent", ua)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	return hc.Do(req)
}

// Error is a non-2xx downstream reply, with the body captured (truncated) so
// callers can log it or map it to their own problem.
type Error struct {
	Status int
	Body   string
	Method string
	URL    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("downstream: %s %s returned %d: %s", e.Method, e.URL, e.Status, e.Body)
}

// Status returns the HTTP status carried by a downstream *Error, or 0 and
// false for any other error. Clients use it to map 404/403/401 to their own
// sentinels without importing net/http everywhere.
func Status(err error) (int, bool) {
	var de *Error
	if errors.As(err, &de) {
		return de.Status, true
	}
	return 0, false
}

// JSON sends in (nil for no body) as JSON and decodes a 2xx reply into out
// (nil to discard). Non-2xx replies return *Error with the status.
func (c *Client) JSON(ctx context.Context, method, path string, in, out any) (int, error) {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return 0, err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.NewRequest(ctx, method, path, body)
	if err != nil {
		return 0, err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, &Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw)), Method: method, URL: req.URL.String()}
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("downstream: decode %s %s: %w", method, req.URL, err)
		}
	}
	return resp.StatusCode, nil
}
