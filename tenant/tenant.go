// Package tenant reads org, tenant and actor from the verified claims and
// enforces per-route scope policy.
//
// Frozen verbs: From, RequireOrg, RequireTenant, RequireUser, AllowService.
//
// iam.FromRequest never rejects a missing tenant because byz-iam issues user
// tokens with a nullable tenant_id and service tokens with none. Routes state
// what they need:
//
//	RequireTenant  user-scoped product routes (kan boards, bb quotes)
//	RequireOrg     org-only routes; service tokens pass (alias AllowService)
//	RequireUser    a human actor is required; service tokens are refused
package tenant

import (
	"context"
	"net/http"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// Scope is the tenant-scoping view of a request. Every store call that
// touches tenant data should take OrgID and TenantID from here.
type Scope struct {
	OrgID     string
	TenantID  string
	ActorID   string
	IsService bool
}

// From returns the scope on the context. ok is false when iam.FromRequest
// did not run.
func From(ctx context.Context) (Scope, bool) {
	c, ok := iam.ClaimsFrom(ctx)
	if !ok {
		return Scope{}, false
	}
	return Scope{
		OrgID:     c.OrganizationID,
		TenantID:  c.TenantID,
		ActorID:   c.ActorID(),
		IsService: c.IsService(),
	}, true
}

// MustFrom is From for handlers that run behind a Require* wrapper. It
// returns a zero Scope when claims are absent rather than panicking.
func MustFrom(ctx context.Context) Scope {
	s, _ := From(ctx)
	return s
}

// RequireOrg rejects tokens without organization_id. Service tokens pass.
func RequireOrg(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := From(r.Context())
		if !ok {
			httpx.WriteProblem(w, http.StatusUnauthorized, "iam.unauthorized", "no verified claims on request")
			return
		}
		if s.OrgID == "" {
			httpx.WriteProblem(w, http.StatusForbidden, "tenant.missing_organization", "token missing organization_id")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AllowService is RequireOrg under the name the board uses: the route accepts
// service tokens and needs only an organisation.
var AllowService = RequireOrg

// RequireTenant rejects tokens without organization_id or tenant_id.
func RequireTenant(next http.Handler) http.Handler {
	return RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := MustFrom(r.Context())
		if s.TenantID == "" {
			httpx.WriteProblem(w, http.StatusForbidden, "tenant.missing_tenant", "token missing tenant_id")
			return
		}
		next.ServeHTTP(w, r)
	}))
}

// RequireUser rejects service tokens and tokens with no actor. It does not
// require a tenant; wrap with RequireTenant as well when both are needed.
func RequireUser(next http.Handler) http.Handler {
	return RequireOrg(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s := MustFrom(r.Context())
		if s.IsService || s.ActorID == "" {
			httpx.WriteProblem(w, http.StatusForbidden, "tenant.user_required", "route requires a user token")
			return
		}
		next.ServeHTTP(w, r)
	}))
}
