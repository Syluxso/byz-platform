package httpx

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
)

// Middleware is the ordinary net/http decorator shape.
type Middleware func(http.Handler) http.Handler

// Chain applies middlewares so the first listed runs outermost.
func Chain(h http.Handler, mws ...Middleware) http.Handler {
	for i := len(mws) - 1; i >= 0; i-- {
		h = mws[i](h)
	}
	return h
}

type ctxKey int

const requestIDKey ctxKey = 1

// RequestIDHeader is the wire name. The same value is request_id in logs and
// traceId on event envelopes.
const RequestIDHeader = "X-Request-Id"

// RequestID reads X-Request-Id from the request or generates one, stores it
// on the context, and echoes it on the response.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get(RequestIDHeader))
		if id == "" || len(id) > 128 {
			id = newRequestID()
			r.Header.Set(RequestIDHeader, id)
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(WithRequestID(r.Context(), id)))
	})
}

// WithRequestID stores a request id on the context. Exposed for tests and
// for non-HTTP entry points such as Kafka consumers.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

// RequestIDFrom returns the request id on the context, or "".
func RequestIDFrom(ctx context.Context) string {
	v, _ := ctx.Value(requestIDKey).(string)
	return v
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(b[:])
}

// CORSOptions configures CORS. Empty AllowOrigins reflects any Origin, which
// is the current fleet behaviour; set it once the front-end hosts are pinned.
type CORSOptions struct {
	AllowOrigins  []string
	AllowMethods  []string
	AllowHeaders  []string
	ExposeHeaders []string
	MaxAge        int
	// AllowCredentials sets Access-Control-Allow-Credentials. Only honoured
	// when the origin is reflected, never with "*".
	AllowCredentials bool
}

// DefaultCORS is what byz-kan and the gateway send today, including the MCP
// session headers.
var DefaultCORS = CORSOptions{
	AllowMethods:  []string{"GET", "POST", "PATCH", "PUT", "DELETE", "OPTIONS"},
	AllowHeaders:  []string{"Authorization", "Content-Type", RequestIDHeader, "Mcp-Session-Id", "Last-Event-Id", "MCP-Protocol-Version"},
	ExposeHeaders: []string{RequestIDHeader, "Mcp-Session-Id", "Last-Event-Id", "MCP-Protocol-Version"},
	MaxAge:        3600,
}

// CORS returns a middleware that answers preflight with 204 and stamps the
// CORS headers on every response.
func CORS(opts CORSOptions) Middleware {
	if len(opts.AllowMethods) == 0 {
		opts.AllowMethods = DefaultCORS.AllowMethods
	}
	if len(opts.AllowHeaders) == 0 {
		opts.AllowHeaders = DefaultCORS.AllowHeaders
	}
	if len(opts.ExposeHeaders) == 0 {
		opts.ExposeHeaders = DefaultCORS.ExposeHeaders
	}
	if opts.MaxAge == 0 {
		opts.MaxAge = DefaultCORS.MaxAge
	}
	allowed := map[string]bool{}
	for _, o := range opts.AllowOrigins {
		allowed[strings.ToLower(strings.TrimRight(o, "/"))] = true
	}
	methods := strings.Join(opts.AllowMethods, ", ")
	headers := strings.Join(opts.AllowHeaders, ", ")
	expose := strings.Join(opts.ExposeHeaders, ", ")
	maxAge := strconv.Itoa(opts.MaxAge)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			h := w.Header()
			switch {
			case origin == "":
				// Not a browser cross-origin request; nothing to stamp.
			case len(allowed) == 0:
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				if opts.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
			case allowed[strings.ToLower(strings.TrimRight(origin, "/"))]:
				h.Set("Access-Control-Allow-Origin", origin)
				h.Add("Vary", "Origin")
				if opts.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
			}
			if origin != "" {
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Expose-Headers", expose)
				h.Set("Access-Control-Max-Age", maxAge)
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
