// Package ginx adapts byz-platform's net/http middleware and helpers to Gin
// for the product APIs (bb-api, hc-api, per-api, up-api).
//
// It is a separate module so the core module never imports Gin. Everything
// here is a thin wrapper: authentication, tenant policy, request ids, CORS,
// metrics and logging all run the exact same code the net/http services run.
//
// Typical wiring:
//
//	r := gin.New()
//	r.Use(ginx.Recovery(logger), ginx.RequestID(), ginx.CORS(httpx.DefaultCORS),
//	      ginx.Metrics("bb-api"), ginx.Logger(logger))
//	ginx.Health(r, store.Health(db))
//	api := r.Group("/api/v1", ginx.Auth(verifier), ginx.RequireTenant())
//	api.GET("/quotes", func(c *gin.Context) {
//	    sc := ginx.MustScope(c)
//	    ...
//	})
package ginx

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
	"github.com/Syluxso/byz-platform/obs"
	"github.com/Syluxso/byz-platform/store"
	"github.com/Syluxso/byz-platform/tenant"
)

// Wrap turns a net/http middleware into a Gin handler. If the middleware
// calls its next handler the Gin chain continues with the (possibly
// context-enriched) request; if it writes a response and stops, the Gin
// chain is aborted.
func Wrap(mw httpx.Middleware) gin.HandlerFunc {
	return func(c *gin.Context) {
		called := false
		next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			called = true
			c.Request = r
			c.Next()
		})
		mw(next).ServeHTTP(c.Writer, c.Request)
		if !called {
			c.Abort()
		}
	}
}

// Auth runs iam.FromRequest: a 401 problem with WWW-Authenticate on failure,
// claims and bearer on the request context on success.
func Auth(v *iam.Verifier) gin.HandlerFunc { return Wrap(v.FromRequest) }

// RequireOrg rejects tokens without organization_id. Service tokens pass.
func RequireOrg() gin.HandlerFunc { return Wrap(tenant.RequireOrg) }

// AllowService is RequireOrg under the board's name.
func AllowService() gin.HandlerFunc { return Wrap(tenant.AllowService) }

// RequireTenant rejects tokens without organization_id or tenant_id.
func RequireTenant() gin.HandlerFunc { return Wrap(tenant.RequireTenant) }

// RequireUser rejects service tokens.
func RequireUser() gin.HandlerFunc { return Wrap(tenant.RequireUser) }

// RequestID reads or generates X-Request-Id and echoes it.
func RequestID() gin.HandlerFunc { return Wrap(httpx.RequestID) }

// CORS applies the shared CORS policy and answers preflight.
func CORS(opts httpx.CORSOptions) gin.HandlerFunc { return Wrap(httpx.CORS(opts)) }

// Metrics records the shared request histogram, labelled by Gin's route
// pattern (c.FullPath) so ids in paths do not explode cardinality.
func Metrics(service string) gin.HandlerFunc {
	inner := obs.HTTPMetrics(service)
	return func(c *gin.Context) {
		c.Request.Pattern = c.FullPath()
		Wrap(inner)(c)
	}
}

// Logger writes the shared one-line request log.
func Logger(l *slog.Logger) gin.HandlerFunc {
	inner := obs.RequestLogger(l)
	return func(c *gin.Context) {
		c.Request.Pattern = c.FullPath()
		Wrap(inner)(c)
	}
}

// Recovery converts a panic into a 500 problem and logs it, instead of Gin's
// default HTML page.
func Recovery(l *slog.Logger) gin.HandlerFunc {
	if l == nil {
		l = slog.Default()
	}
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				obs.With(c.Request.Context(), l).Error("panic", "error", rec)
				if !c.Writer.Written() {
					httpx.WriteProblem(c.Writer, http.StatusInternalServerError, "internal.panic", "internal error")
				}
				c.Abort()
			}
		}()
		c.Next()
	}
}

// Health registers /healthz, /readyz and /actuator/health on the router.
func Health(r gin.IRouter, ready httpx.ReadyFunc) {
	mux := http.NewServeMux()
	httpx.Health(mux, ready)
	h := gin.WrapH(mux)
	r.GET("/healthz", h)
	r.GET("/readyz", h)
	r.GET("/actuator/health", h)
}

// Claims returns the verified claims placed by Auth.
func Claims(c *gin.Context) (iam.Claims, bool) { return iam.ClaimsFrom(c.Request.Context()) }

// Scope returns the tenant scope placed by Auth.
func Scope(c *gin.Context) (tenant.Scope, bool) { return tenant.From(c.Request.Context()) }

// MustScope is Scope for handlers behind a Require* policy.
func MustScope(c *gin.Context) tenant.Scope { return tenant.MustFrom(c.Request.Context()) }

// Bearer returns the caller's raw token for downstream calls.
func Bearer(c *gin.Context) string { return iam.BearerFrom(c.Request.Context()) }

// Problem writes problem JSON and aborts the chain.
func Problem(c *gin.Context, status int, code, detail string) {
	httpx.WriteProblem(c.Writer, status, code, detail)
	c.Abort()
}

// Error writes an error as problem JSON (a Problem error as-is, anything
// else as a 500 with fallback) and aborts.
func Error(c *gin.Context, err error, fallback string) {
	httpx.WriteError(c.Writer, err, fallback)
	c.Abort()
}

// StoreError maps a store error through store.Problem and aborts.
func StoreError(c *gin.Context, err error, fallback string) {
	store.WriteError(c.Writer, err, fallback)
	c.Abort()
}

// Decode reads a strict JSON body or writes a 400 problem and aborts.
// Returns false when the request should not proceed.
func Decode(c *gin.Context, v any) bool {
	if err := httpx.Decode(c.Request, v, 0); err != nil {
		Error(c, err, "invalid request body")
		return false
	}
	return true
}

// Page reads ?page= and ?size= with the shared defaults and clamps.
func Page(c *gin.Context, defSize, maxSize int) (page, size int) {
	return httpx.PageParams(c.Request, defSize, maxSize)
}
