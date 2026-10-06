// Package iam verifies Byzantine IAM bearer tokens locally against the IAM
// JWKS and puts the claims and raw bearer on the request context.
//
// Frozen verbs: NewVerifier, Validate, FromRequest, ClaimsFrom, BearerFrom.
//
// This package never issues or revokes tokens; byz-iam alone does that. It
// never calls IAM per request. FromRequest does not require a tenant; route
// policy lives in package tenant.
package iam

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Grant types byz-iam mints. Service tokens have no user and usually no tenant.
const (
	GrantPassword          = "password"
	GrantClientCredentials = "client_credentials"
	GrantSubject           = "subject"
	GrantUserAPIKey        = "user_api_key"
	GrantTenantAPIKey      = "tenant_api_key"
)

// Claims is the flattened view of a Byz token. Field names follow the JWT
// claim names byz-iam emits: organization_id, tenant_id, user_id, sub,
// client_id (or app_id), grant_type, email, roles.
type Claims struct {
	Subject        string
	UserID         string
	OrganizationID string
	TenantID       string
	ClientID       string
	GrantType      string
	Email          string
	Roles          []string
	ExpiresAt      time.Time
	// Method is the signing algorithm that verified the token: RS256 for IAM
	// tokens, HS256 for a service-local personal access token.
	Method string
}

// ActorID is the user behind the request: user_id when present (API-key
// tokens), otherwise sub.
func (c Claims) ActorID() string {
	if c.UserID != "" {
		return c.UserID
	}
	return c.Subject
}

// IsService reports a machine identity (client_credentials or subject grant).
func (c Claims) IsService() bool {
	return c.GrantType == GrantClientCredentials || c.GrantType == GrantSubject
}

// HasRole reports whether the token carries the exact role claim, e.g.
// "org:admin" or "tenant:admin:<tenant-uuid>".
func (c Claims) HasRole(role string) bool {
	for _, r := range c.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// HasTenantRole reports a tenant-scoped role for the token's own tenant,
// accepting either the unqualified form ("tenant:admin") or the qualified
// form ("tenant:admin:<tenantID>").
func (c Claims) HasTenantRole(role string) bool {
	if c.HasRole(role) {
		return true
	}
	return c.TenantID != "" && c.HasRole(role+":"+c.TenantID)
}

func claimsFromMap(m jwt.MapClaims, method string) Claims {
	c := Claims{
		Subject:        claimString(m, "sub"),
		UserID:         claimString(m, "user_id"),
		OrganizationID: claimString(m, "organization_id"),
		TenantID:       claimString(m, "tenant_id"),
		ClientID:       claimString(m, "client_id"),
		GrantType:      claimString(m, "grant_type"),
		Email:          strings.ToLower(claimString(m, "email")),
		Roles:          claimStrings(m, "roles"),
		Method:         method,
	}
	if c.ClientID == "" {
		c.ClientID = claimString(m, "app_id")
	}
	// byz-iam mints service tokens with sub = clientId and often no client_id
	// claim; surface the client so audit events can name the caller.
	if c.ClientID == "" && (c.GrantType == GrantClientCredentials || c.GrantType == GrantSubject) {
		c.ClientID = c.Subject
	}
	if exp, err := m.GetExpirationTime(); err == nil && exp != nil {
		c.ExpiresAt = exp.Time
	}
	return c
}

func claimString(m jwt.MapClaims, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if s == "null" {
			return ""
		}
		return s
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func claimStrings(m jwt.MapClaims, key string) []string {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return t
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{strings.TrimSpace(t)}
	}
	return nil
}

type ctxKey int

const (
	claimsKey ctxKey = 1
	bearerKey ctxKey = 2
)

// WithClaims stores claims on a context. Services use it in tests and in
// non-HTTP entry points; FromRequest calls it for real requests.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey, c)
}

// ClaimsFrom returns the verified claims placed by FromRequest.
func ClaimsFrom(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey).(Claims)
	return c, ok
}

// WithBearer stores the caller's raw token on a context.
func WithBearer(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, bearerKey, token)
}

// BearerFrom returns the caller's raw token so a downstream call can present
// the caller's credential rather than a service identity (no confused deputy).
func BearerFrom(ctx context.Context) string {
	v, _ := ctx.Value(bearerKey).(string)
	return v
}
