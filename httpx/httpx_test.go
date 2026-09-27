package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteProblemShape(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteProblem(rr, http.StatusConflict, "store.unique_violation", "key exists")
	if rr.Code != 409 {
		t.Fatalf("status %d", rr.Code)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("content-type %q", ct)
	}
	var p Problem
	if err := json.Unmarshal(rr.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "Conflict" || p.Status != 409 || p.Code != "store.unique_violation" || p.Detail != "key exists" {
		t.Fatalf("problem = %+v", p)
	}
}

func TestWriteErrorHidesInternal(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteError(rr, errors.New("pq: secret table detail"), "could not save")
	if rr.Code != 500 || strings.Contains(rr.Body.String(), "secret") {
		t.Fatalf("leaked: %d %s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	WriteError(rr, NewProblem(404, "kan.board_not_found", "no board"), "x")
	if rr.Code != 404 || !strings.Contains(rr.Body.String(), "kan.board_not_found") {
		t.Fatalf("problem passthrough: %d %s", rr.Code, rr.Body.String())
	}
}

func TestWriteUnauthorizedHeader(t *testing.T) {
	rr := httptest.NewRecorder()
	WriteUnauthorized(rr, "https://api.byzantineapp.dev/kan/", "byz-kan", "missing bearer token")
	want := `Bearer realm="byz-kan", resource_metadata="https://api.byzantineapp.dev/kan/.well-known/oauth-protected-resource"`
	if got := rr.Header().Get("WWW-Authenticate"); got != want {
		t.Fatalf("WWW-Authenticate = %q", got)
	}
	if rr.Code != 401 {
		t.Fatalf("status %d", rr.Code)
	}
}

func TestDecodeStrict(t *testing.T) {
	type in struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a","extra":1}`))
	var v in
	err := Decode(r, &v, 0)
	var p Problem
	if !errors.As(err, &p) || p.Status != 400 {
		t.Fatalf("unknown field not rejected: %v", err)
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"a"}`))
	if err := Decode(r, &v, 0); err != nil || v.Name != "a" {
		t.Fatalf("decode: %v %+v", err, v)
	}
}

func TestPage(t *testing.T) {
	var nilItems []string
	p := NewPage(nilItems, 0, 1, 50)
	b, _ := json.Marshal(p)
	if string(b) != `{"items":[],"total":0,"page":1,"size":50}` {
		t.Fatalf("json = %s", b)
	}
	r := httptest.NewRequest("GET", "/?page=3&size=9999", nil)
	page, size := PageParams(r, 50, 200)
	if page != 3 || size != 200 {
		t.Fatalf("params = %d %d", page, size)
	}
	if Offset(page, size) != 400 {
		t.Fatalf("offset = %d", Offset(page, size))
	}
	r = httptest.NewRequest("GET", "/?page=-1&size=abc", nil)
	page, size = PageParams(r, 25, 200)
	if page != 1 || size != 25 {
		t.Fatalf("defaults = %d %d", page, size)
	}
}

func TestRequestID(t *testing.T) {
	var seen string
	h := RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = RequestIDFrom(r.Context())
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if seen == "" || rr.Header().Get(RequestIDHeader) != seen {
		t.Fatalf("generated id not echoed: %q vs %q", seen, rr.Header().Get(RequestIDHeader))
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set(RequestIDHeader, "abc-123")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if seen != "abc-123" || rr.Header().Get(RequestIDHeader) != "abc-123" {
		t.Fatalf("incoming id not preserved: %q", seen)
	}
	if RequestIDFrom(context.Background()) != "" {
		t.Fatal("empty ctx should yield empty id")
	}
}

func TestCORS(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })

	// Reflect-any mode (fleet default today).
	h := CORS(CORSOptions{})(ok)
	req := httptest.NewRequest("OPTIONS", "/x", nil)
	req.Header.Set("Origin", "https://sys.byzantineapp.dev")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 204 || rr.Header().Get("Access-Control-Allow-Origin") != "https://sys.byzantineapp.dev" {
		t.Fatalf("preflight: %d %v", rr.Code, rr.Header())
	}
	if !strings.Contains(rr.Header().Get("Access-Control-Allow-Headers"), "Mcp-Session-Id") {
		t.Fatalf("mcp headers missing: %v", rr.Header())
	}

	// Pinned origins.
	h = CORS(CORSOptions{AllowOrigins: []string{"https://app.byzantineapp.com"}, AllowCredentials: true})(ok)
	req = httptest.NewRequest("GET", "/x", nil)
	req.Header.Set("Origin", "https://evil.example")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unlisted origin must not be allowed")
	}
	req.Header.Set("Origin", "https://app.byzantineapp.com")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Header().Get("Access-Control-Allow-Origin") != "https://app.byzantineapp.com" || rr.Header().Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("listed origin: %v", rr.Header())
	}
}

func TestHealth(t *testing.T) {
	mux := http.NewServeMux()
	Health(mux, func(context.Context) error { return errors.New("db down") })
	for _, path := range []string{"/healthz", "/readyz", "/actuator/health"} {
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", path, nil))
		wantCode := 503
		if path == "/healthz" {
			wantCode = 200
		}
		if rr.Code != wantCode {
			t.Fatalf("%s = %d", path, rr.Code)
		}
	}
}
