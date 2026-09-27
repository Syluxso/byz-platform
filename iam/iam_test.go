package iam

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type fixture struct {
	key *rsa.PrivateKey
	kf  jwt.Keyfunc
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	kf := func(tok *jwt.Token) (any, error) {
		if tok.Header["kid"] != "byz-iam-1" {
			return nil, jwt.ErrTokenUnverifiable
		}
		return &key.PublicKey, nil
	}
	return fixture{key: key, kf: kf}
}

func (f fixture) sign(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	if _, ok := claims["exp"]; !ok {
		claims["exp"] = time.Now().Add(time.Hour).Unix()
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "byz-iam-1"
	s, err := tok.SignedString(f.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func userClaims() jwt.MapClaims {
	return jwt.MapClaims{
		"iss":             "https://iam.byzantineapp.dev",
		"aud":             []string{"byz-api"},
		"sub":             "11111111-1111-4111-8111-111111111111",
		"organization_id": "22222222-2222-4222-8222-222222222222",
		"tenant_id":       "33333333-3333-4333-8333-333333333333",
		"client_id":       "byz-admin",
		"grant_type":      "password",
		"email":           "Darryn@Example.com",
		"roles":           []any{"org:admin", "tenant:admin:33333333-3333-4333-8333-333333333333"},
	}
}

func TestValidateMapsClaims(t *testing.T) {
	f := newFixture(t)
	v, err := NewVerifier(context.Background(), Options{Keyfunc: f.kf})
	if err != nil {
		t.Fatal(err)
	}
	c, err := v.Validate(f.sign(t, userClaims()))
	if err != nil {
		t.Fatal(err)
	}
	if c.OrganizationID == "" || c.TenantID == "" || c.ActorID() != c.Subject || c.Email != "darryn@example.com" || c.Method != "RS256" {
		t.Fatalf("claims = %+v", c)
	}
	if !c.HasRole("org:admin") || !c.HasTenantRole("tenant:admin") || c.HasRole("org:member") {
		t.Fatalf("roles = %v", c.Roles)
	}
	if c.IsService() {
		t.Fatal("password grant is not a service token")
	}
	if c.ExpiresAt.IsZero() {
		t.Fatal("ExpiresAt missing")
	}
}

func TestValidateServiceTokenWithoutTenant(t *testing.T) {
	f := newFixture(t)
	v, _ := NewVerifier(context.Background(), Options{Keyfunc: f.kf})
	c, err := v.Validate(f.sign(t, jwt.MapClaims{
		"sub": "byz-agent", "organization_id": "org", "grant_type": "client_credentials", "app_id": "app_1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.IsService() || c.TenantID != "" || c.ClientID != "app_1" {
		t.Fatalf("service claims = %+v", c)
	}
}

func TestValidateIssuerAndAudience(t *testing.T) {
	f := newFixture(t)
	v, _ := NewVerifier(context.Background(), Options{
		Keyfunc:  f.kf,
		Issuers:  []string{"https://iam.byzantineapp.dev/"},
		Audience: "byz-api",
	})
	if _, err := v.Validate(f.sign(t, userClaims())); err != nil {
		t.Fatalf("good iss/aud rejected: %v", err)
	}
	bad := userClaims()
	bad["iss"] = "https://evil.example"
	if _, err := v.Validate(f.sign(t, bad)); err == nil || reasonFor(err) != "issuer" {
		t.Fatalf("bad issuer: %v", err)
	}
	bad = userClaims()
	bad["aud"] = []string{"other"}
	if _, err := v.Validate(f.sign(t, bad)); err == nil || reasonFor(err) != "audience" {
		t.Fatalf("bad audience: %v", err)
	}
}

func TestValidateExpiredAndGarbage(t *testing.T) {
	f := newFixture(t)
	v, _ := NewVerifier(context.Background(), Options{Keyfunc: f.kf})
	c := userClaims()
	c["exp"] = time.Now().Add(-time.Minute).Unix()
	if _, err := v.Validate(f.sign(t, c)); err == nil || reasonFor(err) != "expired" {
		t.Fatalf("expired: %v", err)
	}
	if _, err := v.Validate("not.a.jwt"); err == nil || reasonFor(err) != "invalid" {
		t.Fatalf("garbage: %v", err)
	}
	if _, err := v.Validate(""); err != ErrMissingBearer {
		t.Fatalf("empty: %v", err)
	}
	// No exp at all must fail (expiration required).
	noExp := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"sub": "x"})
	noExp.Header["kid"] = "byz-iam-1"
	s, _ := noExp.SignedString(f.key)
	if _, err := v.Validate(s); err == nil {
		t.Fatal("token without exp accepted")
	}
}

func TestValidatePATFallback(t *testing.T) {
	f := newFixture(t)
	secret := []byte("0123456789abcdef0123456789abcdef")
	v, _ := NewVerifier(context.Background(), Options{Keyfunc: f.kf, PATSecret: secret, Issuers: []string{"https://iam.byzantineapp.dev"}})
	pat := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub": "u1", "organization_id": "o1", "tenant_id": "t1", "grant_type": "pat",
		"exp": time.Now().Add(time.Hour).Unix(),
	})
	s, _ := pat.SignedString(secret)
	c, err := v.Validate(s)
	if err != nil || c.Method != "HS256" || c.TenantID != "t1" {
		t.Fatalf("pat: %v %+v", err, c)
	}
	// HS256 must not be accepted when PATSecret is unset (alg confusion guard).
	v2, _ := NewVerifier(context.Background(), Options{Keyfunc: f.kf})
	if _, err := v2.Validate(s); err == nil {
		t.Fatal("HS256 accepted without PATSecret")
	}
}

func TestFromRequestMiddleware(t *testing.T) {
	f := newFixture(t)
	var rejected []string
	v, _ := NewVerifier(context.Background(), Options{
		Keyfunc:   f.kf,
		PublicURL: "https://api.byzantineapp.dev/kan",
		Realm:     "byz-kan",
		OnReject:  func(_ *http.Request, reason string) { rejected = append(rejected, reason) },
	})
	var got Claims
	var bearer string
	h := v.FromRequest(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = ClaimsFrom(r.Context())
		bearer = BearerFrom(r.Context())
		w.WriteHeader(200)
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/", nil))
	if rr.Code != 401 || !strings.Contains(rr.Header().Get("WWW-Authenticate"), "oauth-protected-resource") {
		t.Fatalf("missing bearer: %d %v", rr.Code, rr.Header())
	}
	if !strings.Contains(rr.Body.String(), `"code":"iam.unauthorized"`) {
		t.Fatalf("problem body: %s", rr.Body.String())
	}

	tok := f.sign(t, userClaims())
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "bearer "+tok)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 200 || got.OrganizationID == "" || bearer != tok {
		t.Fatalf("valid: %d claims=%+v bearer=%q", rr.Code, got, bearer)
	}

	req.Header.Set("Authorization", "Bearer "+tok+"x")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("tampered accepted: %d", rr.Code)
	}
	if len(rejected) != 2 || rejected[0] != "missing_bearer" || rejected[1] != "invalid" {
		t.Fatalf("OnReject reasons = %v", rejected)
	}
}

func TestNewVerifierRequiresJWKS(t *testing.T) {
	if _, err := NewVerifier(context.Background(), Options{}); err == nil {
		t.Fatal("expected error without JWKSURL or Keyfunc")
	}
}
