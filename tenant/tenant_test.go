package tenant

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/byz-platform/iam"
)

func run(t *testing.T, mw func(http.Handler) http.Handler, c *iam.Claims) *httptest.ResponseRecorder {
	t.Helper()
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }))
	req := httptest.NewRequest("GET", "/", nil)
	if c != nil {
		req = req.WithContext(iam.WithClaims(req.Context(), *c))
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func TestPolicies(t *testing.T) {
	user := &iam.Claims{Subject: "u", OrganizationID: "o", TenantID: "t", GrantType: iam.GrantPassword}
	userNoTenant := &iam.Claims{Subject: "u", OrganizationID: "o", GrantType: iam.GrantPassword}
	svc := &iam.Claims{Subject: "svc", OrganizationID: "o", GrantType: iam.GrantClientCredentials}
	noOrg := &iam.Claims{Subject: "u"}

	cases := []struct {
		name string
		mw   func(http.Handler) http.Handler
		c    *iam.Claims
		want int
		code string
	}{
		{"org/user", RequireOrg, user, 200, ""},
		{"org/svc", RequireOrg, svc, 200, ""},
		{"org/none", RequireOrg, noOrg, 403, "tenant.missing_organization"},
		{"org/noclaims", RequireOrg, nil, 401, "iam.unauthorized"},
		{"allowservice/svc", AllowService, svc, 200, ""},
		{"tenant/user", RequireTenant, user, 200, ""},
		{"tenant/userNoTenant", RequireTenant, userNoTenant, 403, "tenant.missing_tenant"},
		{"tenant/svc", RequireTenant, svc, 403, "tenant.missing_tenant"},
		{"user/user", RequireUser, userNoTenant, 200, ""},
		{"user/svc", RequireUser, svc, 403, "tenant.user_required"},
	}
	for _, tc := range cases {
		rr := run(t, tc.mw, tc.c)
		if rr.Code != tc.want {
			t.Errorf("%s: status %d want %d", tc.name, rr.Code, tc.want)
		}
		if tc.code != "" && !strings.Contains(rr.Body.String(), tc.code) {
			t.Errorf("%s: body %s missing %s", tc.name, rr.Body.String(), tc.code)
		}
	}
}

func TestFrom(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	if _, ok := From(req.Context()); ok {
		t.Fatal("no claims should be !ok")
	}
	ctx := iam.WithClaims(req.Context(), iam.Claims{UserID: "uid", Subject: "sub", OrganizationID: "o", TenantID: "t", GrantType: iam.GrantUserAPIKey})
	s, ok := From(ctx)
	if !ok || s.ActorID != "uid" || s.OrgID != "o" || s.TenantID != "t" || s.IsService {
		t.Fatalf("scope = %+v", s)
	}
}
