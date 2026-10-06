package mcpx

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
	"github.com/Syluxso/byz-platform/tenant"
)

func verifier(t *testing.T) (*iam.Verifier, func(claims jwt.MapClaims) string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	v, err := iam.NewVerifier(context.Background(), iam.Options{
		Keyfunc:   func(*jwt.Token) (any, error) { return &key.PublicKey, nil },
		PublicURL: "https://api.byzantineapp.dev/kan",
		Realm:     "byz-kan",
		ResourceURL: func(r *http.Request) string {
			if r.Host == "kan.brand.example" {
				return "https://kan.brand.example"
			}
			return ""
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sign := func(c jwt.MapClaims) string {
		c["exp"] = time.Now().Add(time.Hour).Unix()
		s, _ := jwt.NewWithClaims(jwt.SigningMethodRS256, c).SignedString(key)
		return s
	}
	return v, sign
}

func TestMountAuthenticatesAndServesMetadata(t *testing.T) {
	v, sign := verifier(t)
	var seen iam.Claims
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = iam.ClaimsFrom(r.Context())
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0"}`))
	})
	mux := http.NewServeMux()
	Mount(mux, Options{
		Handler:   inner,
		Verifier:  v,
		PublicURL: "https://api.byzantineapp.dev/kan",
		ResourceURL: func(r *http.Request) string {
			if r.Host == "kan.brand.example" {
				return "https://kan.brand.example"
			}
			return ""
		},
		ScopesSupported:   []string{"kan"},
		DocumentationPath: "/api/v1/kan/ping",
		Policies:          []httpx.Middleware{tenant.RequireTenant},
	})

	// No bearer: 401 with WWW-Authenticate pointing at the metadata.
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest("POST", "/mcp", strings.NewReader("{}")))
	if rr.Code != 401 {
		t.Fatalf("no bearer: %d", rr.Code)
	}
	if got := rr.Header().Get("WWW-Authenticate"); !strings.Contains(got, `resource_metadata="https://api.byzantineapp.dev/kan/.well-known/oauth-protected-resource"`) {
		t.Fatalf("WWW-Authenticate = %q", got)
	}

	// Valid bearer with tenant reaches the handler with claims on ctx.
	req := httptest.NewRequest("POST", "/mcp", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sign(jwt.MapClaims{"sub": "u", "organization_id": "o", "tenant_id": "t"}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 200 || seen.TenantID != "t" {
		t.Fatalf("valid: %d claims=%+v", rr.Code, seen)
	}

	// Policy applies: no tenant → 403 from RequireTenant.
	req = httptest.NewRequest("POST", "/mcp/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+sign(jwt.MapClaims{"sub": "u", "organization_id": "o"}))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != 403 || !strings.Contains(rr.Body.String(), "tenant.missing_tenant") {
		t.Fatalf("policy: %d %s", rr.Code, rr.Body.String())
	}

	// Metadata on all three paths, unauthenticated, kan's shape.
	for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp", "/mcp/.well-known/oauth-protected-resource"} {
		rr = httptest.NewRecorder()
		mux.ServeHTTP(rr, httptest.NewRequest("GET", p, nil))
		if rr.Code != 200 {
			t.Fatalf("%s: %d", p, rr.Code)
		}
		var m Metadata
		if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		if m.Resource != "https://api.byzantineapp.dev/kan/mcp" || len(m.AuthorizationServers) != 1 || m.AuthorizationServers[0] != "https://api.byzantineapp.dev/kan" || m.BearerMethodsSupported[0] != "header" || m.ScopesSupported[0] != "kan" {
			t.Fatalf("%s metadata = %+v", p, m)
		}
	}

	// Branded host resolves through ResourceURL.
	req = httptest.NewRequest("GET", "/.well-known/oauth-protected-resource", nil)
	req.Host = "kan.brand.example"
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	var m Metadata
	_ = json.Unmarshal(rr.Body.Bytes(), &m)
	if m.Resource != "https://kan.brand.example/mcp" || m.AuthorizationServers[0] != "https://kan.brand.example" || m.ResourceDocumentation != "https://kan.brand.example/api/v1/kan/ping" {
		t.Fatalf("branded metadata = %+v", m)
	}
}

func TestMountRequiresHandlerAndVerifier(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic without Handler")
		}
	}()
	Mount(http.NewServeMux(), Options{})
}
