package iam

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/MicahParks/keyfunc/v3"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Syluxso/byz-platform/httpx"
)

// Sentinel errors returned by Validate. Wrapped errors keep the jwt detail.
var (
	ErrMissingBearer = errors.New("iam: missing bearer token")
	ErrInvalidToken  = errors.New("iam: invalid token")
	ErrExpired       = errors.New("iam: token expired")
	ErrIssuer        = errors.New("iam: issuer not accepted")
	ErrAudience      = errors.New("iam: audience not accepted")
)

// Options configures a Verifier.
type Options struct {
	// JWKSURL is the IAM JWKS document. Required unless Keyfunc is set.
	JWKSURL string
	// Issuers, when non-empty, is the set of accepted iss values.
	Issuers []string
	// Audience, when non-empty, must appear in the token's aud.
	Audience string
	// PATSecret enables a second HS256 pass for service-local personal access
	// tokens (byz-kan). Issuer and audience checks do not apply to PATs.
	PATSecret []byte

	// PublicURL is this service's external base, used in WWW-Authenticate so
	// clients can find /.well-known/oauth-protected-resource.
	PublicURL string
	// ResourceURL overrides PublicURL per request, for branded hosts.
	ResourceURL func(r *http.Request) string
	// Realm for WWW-Authenticate. Defaults to "byz".
	Realm string

	// OnReject is called with a short reason on every 401 written by
	// FromRequest: missing_bearer, expired, issuer, audience, invalid.
	// Wire it to obs.AuthRejected. Rejects are metrics, never faults.
	OnReject func(r *http.Request, reason string)

	// Keyfunc bypasses JWKS fetching. Tests use it with a local key.
	Keyfunc jwt.Keyfunc
}

// Verifier validates tokens and serves as net/http middleware.
type Verifier struct {
	opts Options
	kf   jwt.Keyfunc
}

// NewVerifier fetches the JWKS (unless Options.Keyfunc is set) and returns a
// Verifier. keyfunc refreshes keys in the background for the life of ctx.
func NewVerifier(ctx context.Context, opts Options) (*Verifier, error) {
	if opts.Realm == "" {
		opts.Realm = "byz"
	}
	kf := opts.Keyfunc
	if kf == nil {
		if strings.TrimSpace(opts.JWKSURL) == "" {
			return nil, errors.New("iam: JWKSURL is required")
		}
		k, err := keyfunc.NewDefaultCtx(ctx, []string{opts.JWKSURL})
		if err != nil {
			return nil, fmt.Errorf("iam: jwks %s: %w", opts.JWKSURL, err)
		}
		kf = k.Keyfunc
	}
	return &Verifier{opts: opts, kf: kf}, nil
}

// Validate verifies a raw token string and returns its claims.
func (v *Verifier) Validate(tokenStr string) (Claims, error) {
	tokenStr = strings.TrimSpace(tokenStr)
	if tokenStr == "" {
		return Claims{}, ErrMissingBearer
	}

	parseOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256"}),
		jwt.WithExpirationRequired(),
	}
	if v.opts.Audience != "" {
		parseOpts = append(parseOpts, jwt.WithAudience(v.opts.Audience))
	}
	tok, err := jwt.Parse(tokenStr, v.kf, parseOpts...)
	if err == nil && tok.Valid {
		mc, _ := tok.Claims.(jwt.MapClaims)
		if len(v.opts.Issuers) > 0 {
			iss := claimString(mc, "iss")
			if !containsFold(v.opts.Issuers, iss) {
				return Claims{}, fmt.Errorf("%w: %q", ErrIssuer, iss)
			}
		}
		return claimsFromMap(mc, "RS256"), nil
	}
	rsErr := err

	if len(v.opts.PATSecret) > 0 {
		tok, perr := jwt.Parse(tokenStr,
			func(*jwt.Token) (any, error) { return v.opts.PATSecret, nil },
			jwt.WithValidMethods([]string{"HS256"}),
			jwt.WithExpirationRequired(),
		)
		if perr == nil && tok.Valid {
			mc, _ := tok.Claims.(jwt.MapClaims)
			return claimsFromMap(mc, "HS256"), nil
		}
	}

	switch {
	case errors.Is(rsErr, jwt.ErrTokenExpired):
		return Claims{}, fmt.Errorf("%w: %v", ErrExpired, rsErr)
	case errors.Is(rsErr, jwt.ErrTokenInvalidAudience):
		return Claims{}, fmt.Errorf("%w: %v", ErrAudience, rsErr)
	default:
		return Claims{}, fmt.Errorf("%w: %v", ErrInvalidToken, rsErr)
	}
}

// ParseBearer extracts the token from an Authorization: Bearer header.
func ParseBearer(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
		return "", false
	}
	tok := strings.TrimSpace(h[7:])
	return tok, tok != ""
}

// FromRequest is the auth middleware. On success the claims and raw bearer
// are on the context. On failure it writes a 401 problem with
// WWW-Authenticate and calls OnReject. It does not check org or tenant.
func (v *Verifier) FromRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenStr, ok := ParseBearer(r)
		if !ok {
			v.reject(w, r, "missing_bearer", "missing bearer token")
			return
		}
		c, err := v.Validate(tokenStr)
		if err != nil {
			v.reject(w, r, reasonFor(err), "invalid token")
			return
		}
		ctx := WithClaims(r.Context(), c)
		ctx = WithBearer(ctx, tokenStr)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Middleware returns FromRequest in httpx.Middleware form for Chain.
func (v *Verifier) Middleware() httpx.Middleware {
	return v.FromRequest
}

func (v *Verifier) reject(w http.ResponseWriter, r *http.Request, reason, detail string) {
	if v.opts.OnReject != nil {
		v.opts.OnReject(r, reason)
	}
	pub := v.opts.PublicURL
	if v.opts.ResourceURL != nil {
		if u := v.opts.ResourceURL(r); u != "" {
			pub = u
		}
	}
	httpx.WriteUnauthorized(w, pub, v.opts.Realm, detail)
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrMissingBearer):
		return "missing_bearer"
	case errors.Is(err, ErrExpired):
		return "expired"
	case errors.Is(err, ErrIssuer):
		return "issuer"
	case errors.Is(err, ErrAudience):
		return "audience"
	default:
		return "invalid"
	}
}

func containsFold(list []string, s string) bool {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	for _, l := range list {
		if strings.EqualFold(strings.TrimRight(strings.TrimSpace(l), "/"), s) {
			return true
		}
	}
	return false
}
