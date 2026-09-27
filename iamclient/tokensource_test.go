package iamclient

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenCachesAndRefreshes(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/oauth/token" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		var in map[string]string
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in["grantType"] != "client_credentials" || in["clientId"] != "svc" || in["clientSecret"] != "s3cret" {
			t.Errorf("bad body %v", in)
		}
		n := atomic.AddInt32(&calls, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "tok" + string(rune('0'+n)), "expiresIn": 3600})
	}))
	defer srv.Close()

	now := time.Now()
	ts := NewTokenSource(srv.URL+"/", "svc", "s3cret", nil)
	ts.now = func() time.Time { return now }

	a, err := ts.Token(context.Background())
	if err != nil || a != "tok1" {
		t.Fatalf("first: %q %v", a, err)
	}
	b, _ := ts.Token(context.Background())
	if b != "tok1" || atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("second should be cached: %q calls=%d", b, calls)
	}
	// 90% of TTL passes: refresh expected.
	now = now.Add(3300 * time.Second)
	c, _ := ts.Token(context.Background())
	if c != "tok2" || atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("refresh: %q calls=%d", c, calls)
	}
	ts.Invalidate()
	d, _ := ts.Token(context.Background())
	if d != "tok3" {
		t.Fatalf("invalidate: %q", d)
	}
}

func TestTokenNotConfigured(t *testing.T) {
	ts := NewTokenSource("", "", "", nil)
	if _, err := ts.Token(context.Background()); err != ErrNotConfigured {
		t.Fatalf("err = %v", err)
	}
}

func TestTokenErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"detail":"bad client"}`, 401)
	}))
	defer srv.Close()
	ts := NewTokenSource(srv.URL, "svc", "wrong", nil)
	if _, err := ts.Token(context.Background()); err == nil {
		t.Fatal("expected error on 401")
	}
}
