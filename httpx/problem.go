// Package httpx is the shared HTTP shape for Byz services: problem JSON,
// the list page envelope, request ids, CORS and health endpoints.
//
// Frozen verbs: WriteJSON, WriteProblem, WriteUnauthorized, Page, RequestID,
// CORS, Health. net/http only; ginx wraps these for Gin.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Problem is the error body every Byz Go service returns. It follows RFC 9457
// field names. Code is a stable machine-readable string such as
// "store.unique_violation" or "iam.token_invalid" and maps to fail.Report
// codes when the error is actionable.
type Problem struct {
	Type     string `json:"type,omitempty"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Code     string `json:"code,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// Error lets a Problem travel as an error value through service code.
func (p Problem) Error() string {
	if p.Detail != "" {
		return fmt.Sprintf("%d %s: %s", p.Status, p.Title, p.Detail)
	}
	return fmt.Sprintf("%d %s", p.Status, p.Title)
}

// NewProblem builds a Problem with Title derived from the status code.
func NewProblem(status int, code, detail string) Problem {
	return Problem{Title: http.StatusText(status), Status: status, Detail: detail, Code: code}
}

// WriteJSON writes v as JSON with the given status. A nil v writes only the
// status line and headers.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

// WriteProblem writes problem JSON with Title derived from status.
func WriteProblem(w http.ResponseWriter, status int, code, detail string) {
	WriteProblemValue(w, NewProblem(status, code, detail))
}

// WriteProblemValue writes a prebuilt Problem, filling Title from Status when
// blank. Content-Type is application/problem+json.
func WriteProblemValue(w http.ResponseWriter, p Problem) {
	if p.Title == "" {
		p.Title = http.StatusText(p.Status)
	}
	if p.Status == 0 {
		p.Status = http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	_ = json.NewEncoder(w).Encode(p)
}

// WriteError maps an error to problem JSON. A Problem error is written as-is.
// Anything else becomes a 500 with the fallback detail so internal messages do
// not leak to clients.
func WriteError(w http.ResponseWriter, err error, fallback string) {
	var p Problem
	if errors.As(err, &p) {
		WriteProblemValue(w, p)
		return
	}
	WriteProblem(w, http.StatusInternalServerError, "internal", fallback)
}

// WriteUnauthorized writes a 401 problem and the WWW-Authenticate header MCP
// clients and OAuth-aware SPAs use to discover the protected-resource
// metadata document (RFC 9728). publicURL is the externally reachable base of
// this service, for example https://api.byzantineapp.dev/kan.
func WriteUnauthorized(w http.ResponseWriter, publicURL, realm, detail string) {
	if realm == "" {
		realm = "byz"
	}
	if publicURL != "" {
		meta := strings.TrimRight(publicURL, "/") + "/.well-known/oauth-protected-resource"
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q, resource_metadata=%q`, realm, meta))
	} else {
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, realm))
	}
	WriteProblem(w, http.StatusUnauthorized, "iam.unauthorized", detail)
}

// DefaultMaxBody is the JSON body cap used by Decode when maxBytes is 0.
const DefaultMaxBody = 1 << 20

// Decode reads a JSON request body into v, rejecting unknown fields and
// trailing data. It returns a 400 Problem on failure so handlers can pass it
// straight to WriteError.
func Decode(r *http.Request, v any, maxBytes int64) error {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBody
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return NewProblem(http.StatusBadRequest, "request.invalid_json", err.Error())
	}
	if dec.More() {
		return NewProblem(http.StatusBadRequest, "request.invalid_json", "body must contain a single JSON value")
	}
	return nil
}
