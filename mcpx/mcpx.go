// Package mcpx mounts a service-supplied MCP handler behind the same IAM
// bearer check as the REST routes and serves the RFC 9728 protected-resource
// metadata that lets connectors (Claude, Grok, ChatGPT) discover where to
// log in.
//
// Frozen verb: Mount.
//
// The MCP SDK stays in the service: tools, NewServer and the streamable-HTTP
// options are built there and passed in as an http.Handler. This package
// never imports the SDK, so the core module stays framework-free.
package mcpx

import (
	"net/http"
	"strings"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// Options configures Mount.
type Options struct {
	// Handler is the service's MCP streamable-HTTP handler. Required.
	Handler http.Handler
	// Verifier authenticates every MCP request. Required unless Auth is set.
	Verifier *iam.Verifier
	// Auth replaces Verifier.FromRequest when set. Services use it to run the
	// same test-claims middleware their REST routes use in integration tests.
	Auth httpx.Middleware
	// PublicURL is the service's external base, e.g.
	// https://api.byzantineapp.dev/kan. Required unless ResourceURL is set.
	PublicURL string
	// ResourceURL overrides PublicURL per request (branded hosts).
	ResourceURL func(r *http.Request) string
	// Path is where the MCP handler listens. Default "/mcp". Both Path and
	// Path + "/" are registered.
	Path string
	// AuthorizationServers lists issuer base URLs. Default: the resource base,
	// which is what kan advertises today (kan proxies the OAuth endpoints).
	AuthorizationServers []string
	// ScopesSupported for the metadata document. Default: none listed.
	ScopesSupported []string
	// ResourceDocumentation is an optional absolute URL for humans.
	ResourceDocumentation string
	// DocumentationPath, when set, is appended to the resolved base URL to
	// form resource_documentation, so branded hosts get their own link.
	// Takes precedence over ResourceDocumentation.
	DocumentationPath string
	// Policies run between authentication and the handler, e.g.
	// tenant.RequireTenant. Applied in order, first is outermost.
	Policies []httpx.Middleware
}

// WellKnownPath is the RFC 9728 discovery path.
const WellKnownPath = "/.well-known/oauth-protected-resource"

// Mount registers on mux:
//
//	<Path>, <Path>/                  Verifier.FromRequest → Policies → Handler
//	GET /.well-known/oauth-protected-resource
//	GET /.well-known/oauth-protected-resource<Path>
//	GET <Path>/.well-known/oauth-protected-resource
//
// The three metadata routes cover the places different connectors look
// (path-suffixed per RFC 9728 §3, and inside the MCP path for older ones).
func Mount(mux *http.ServeMux, opts Options) {
	if opts.Handler == nil {
		panic("mcpx: Handler is required")
	}
	if opts.Verifier == nil && opts.Auth == nil {
		panic("mcpx: Verifier or Auth is required")
	}
	path := strings.TrimRight(opts.Path, "/")
	if path == "" {
		path = "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	opts.Path = path

	protected := httpx.Chain(opts.Handler, opts.Policies...)
	if opts.Auth != nil {
		protected = opts.Auth(protected)
	} else {
		protected = opts.Verifier.FromRequest(protected)
	}
	mux.Handle(path, protected)
	mux.Handle(path+"/", protected)

	meta := metadataHandler(opts)
	mux.Handle("GET "+WellKnownPath, meta)
	mux.Handle("GET "+WellKnownPath+path, meta)
	mux.Handle("GET "+path+WellKnownPath, meta)
}

// Metadata is the RFC 9728 document.
type Metadata struct {
	Resource               string   `json:"resource"`
	AuthorizationServers   []string `json:"authorization_servers"`
	BearerMethodsSupported []string `json:"bearer_methods_supported"`
	ScopesSupported        []string `json:"scopes_supported,omitempty"`
	ResourceDocumentation  string   `json:"resource_documentation,omitempty"`
}

// MetadataFor builds the document for one request, resolving the base URL
// through ResourceURL when set.
func MetadataFor(r *http.Request, opts Options) Metadata {
	base := strings.TrimRight(opts.PublicURL, "/")
	if opts.ResourceURL != nil {
		if u := strings.TrimRight(opts.ResourceURL(r), "/"); u != "" {
			base = u
		}
	}
	path := opts.Path
	if path == "" {
		path = "/mcp"
	}
	as := opts.AuthorizationServers
	if len(as) == 0 {
		as = []string{base}
	}
	docs := opts.ResourceDocumentation
	if opts.DocumentationPath != "" {
		docs = base + "/" + strings.TrimLeft(opts.DocumentationPath, "/")
	}
	return Metadata{
		Resource:               base + path,
		AuthorizationServers:   as,
		BearerMethodsSupported: []string{"header"},
		ScopesSupported:        opts.ScopesSupported,
		ResourceDocumentation:  docs,
	}
}

func metadataHandler(opts Options) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		httpx.WriteJSON(w, http.StatusOK, MetadataFor(r, opts))
	})
}
