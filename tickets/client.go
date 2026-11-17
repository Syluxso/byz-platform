// Package tickets is the client a business app uses to call byz-tickets.
//
// The app keeps its own session. For each call it passes the tenant, the
// acting user, and whether that user is an admin. Those three values are sent
// as headers. They are never taken from the widget body. The call uses the
// business app's service token, and byz-tickets pins every row to that
// token's organisation.
//
//	c := tickets.New(os.Getenv("BYZ_TICKETS_URL"), tickets.WithServiceToken(tokens))
//	row, err := c.Create(ctx, tickets.Actor{TenantID: tenant, UserID: user, Admin: isAdmin}, tickets.Create{
//		Title: "Save is broken", Route: r.URL.Path, QueryString: r.URL.RawQuery,
//	})
package tickets

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/byz-platform/downstream"
	"github.com/Syluxso/byz-platform/iam"
	"github.com/Syluxso/byz-platform/iamclient"
)

// Headers byz-tickets reads. Keep them identical to the service.
const (
	HeaderTenant = "X-Byz-Tenant-Id"
	HeaderActor  = "X-Byz-Actor-Id"
	HeaderAdmin  = "X-Byz-Admin"
)

// Sentinel errors mapped from the service status.
var (
	ErrNotFound     = errors.New("tickets: not found")
	ErrForbidden    = errors.New("tickets: forbidden")
	ErrUnauthorized = errors.New("tickets: unauthorized")
	ErrInvalid      = errors.New("tickets: invalid request")
	ErrConflict     = errors.New("tickets: conflict")
)

// Actor is who the business app says this call is for.
type Actor struct {
	TenantID string
	UserID   string
	Admin    bool
}

// Ticket is one page-scoped feedback item.
type Ticket struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organizationId"`
	TenantID       string         `json:"tenantId"`
	Route          string         `json:"route"`
	QueryString    string         `json:"queryString"`
	Metadata       map[string]any `json:"metadata"`
	StatusID       string         `json:"statusId"`
	StatusName     string         `json:"statusName"`
	StatusSlug     string         `json:"statusSlug"`
	AssignedTo     *string        `json:"assignedTo"`
	AuthorID       string         `json:"authorId"`
	Title          string         `json:"title"`
	Body           string         `json:"body"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// Status is a tenant's ticket column.
type Status struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Position int    `json:"position"`
	IsClosed bool   `json:"isClosed"`
}

// Comment is a note on a ticket.
type Comment struct {
	ID        string    `json:"id"`
	TicketID  string    `json:"ticketId"`
	AuthorID  string    `json:"authorId"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"createdAt"`
}

// History is one audited change.
type History struct {
	ID        string         `json:"id"`
	TicketID  string         `json:"ticketId"`
	ActorID   string         `json:"actorId"`
	ClientID  string         `json:"clientId"`
	Action    string         `json:"action"`
	Changes   map[string]any `json:"changes"`
	CreatedAt time.Time      `json:"createdAt"`
}

// Page is the byz-platform list envelope.
type Page[T any] struct {
	Items []T   `json:"items"`
	Total int64 `json:"total"`
	Page  int   `json:"page"`
	Size  int   `json:"size"`
}

// Create is a new ticket. The author is Actor.UserID.
type Create struct {
	Title       string         `json:"title"`
	Body        string         `json:"body,omitempty"`
	Route       string         `json:"route"`
	QueryString string         `json:"queryString,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
	StatusID    string         `json:"statusId,omitempty"`
	AssignedTo  string         `json:"assignedTo,omitempty"`
}

// Update patches a ticket. Set the matching *Set flag to send a field.
// AssignedTo nil with AssignedSet clears the assignee.
type Update struct {
	Title       *string
	Body        *string
	Route       *string
	QueryString *string
	StatusID    *string
	Metadata    map[string]any
	MetadataSet bool
	AssignedTo  *string
	AssignedSet bool
}

// MarshalJSON sends only the fields the caller set.
func (u Update) MarshalJSON() ([]byte, error) {
	m := map[string]any{}
	if u.Title != nil {
		m["title"] = *u.Title
	}
	if u.Body != nil {
		m["body"] = *u.Body
	}
	if u.Route != nil {
		m["route"] = *u.Route
	}
	if u.QueryString != nil {
		m["queryString"] = *u.QueryString
	}
	if u.StatusID != nil {
		m["statusId"] = *u.StatusID
	}
	if u.MetadataSet {
		if u.Metadata == nil {
			m["metadata"] = map[string]any{}
		} else {
			m["metadata"] = u.Metadata
		}
	}
	if u.AssignedSet {
		if u.AssignedTo == nil || *u.AssignedTo == "" {
			m["assignedTo"] = nil
		} else {
			m["assignedTo"] = *u.AssignedTo
		}
	}
	return json.Marshal(m)
}

// ListFilter selects tickets. Route and Query are applied only when their
// Set flag is true, so an empty query string is a real page.
type ListFilter struct {
	Route    string
	RouteSet bool
	Query    string
	QuerySet bool
	StatusID string
	Page     int
	Size     int
}

// StatusInput creates a tenant status. An empty slug is derived from the name.
type StatusInput struct {
	Name     string `json:"name"`
	Slug     string `json:"slug,omitempty"`
	IsClosed bool   `json:"isClosed,omitempty"`
}

// Problem is the service's error body.
type Problem struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
}

func (p *Problem) Error() string {
	return "byz-tickets " + strconv.Itoa(p.Status) + " " + p.Code + ": " + p.Detail
}

// AsProblem unwraps a *Problem.
func AsProblem(err error) (*Problem, bool) {
	var p *Problem
	return p, errors.As(err, &p)
}

// Client calls one byz-tickets base URL.
type Client struct {
	down   *downstream.Client
	tokens *iamclient.TokenSource
	static string
}

// Option configures a Client.
type Option func(*Client)

// WithServiceToken uses the business app's client_credentials token for every call.
func WithServiceToken(ts *iamclient.TokenSource) Option {
	return func(c *Client) { c.tokens = ts }
}

// WithStaticToken uses a fixed bearer. Tests and the local demo use this.
// WithServiceToken wins when both are set.
func WithStaticToken(token string) Option {
	return func(c *Client) { c.static = token }
}

// WithHTTPClient replaces the default 15s client.
func WithHTTPClient(h *http.Client) Option {
	return func(c *Client) { c.down.HTTP = h }
}

// New builds a client. baseURL is the tickets service, or the gateway's /tickets prefix.
func New(baseURL string, opts ...Option) *Client {
	c := &Client{down: downstream.New(baseURL)}
	c.down.UserAgent = "byz-platform/tickets"
	for _, o := range opts {
		o(c)
	}
	return c
}

// Create stores a ticket and returns it.
func (c *Client) Create(ctx context.Context, a Actor, in Create) (Ticket, error) {
	var out Ticket
	err := c.do(ctx, a, http.MethodPost, "/api/v1/tickets", in, &out)
	return out, err
}

// List returns the actor's visible tickets.
func (c *Client) List(ctx context.Context, a Actor, f ListFilter) (Page[Ticket], error) {
	var out Page[Ticket]
	err := c.do(ctx, a, http.MethodGet, "/api/v1/tickets"+query(f), nil, &out)
	if out.Items == nil {
		out.Items = []Ticket{}
	}
	return out, err
}

// Get returns one visible ticket.
func (c *Client) Get(ctx context.Context, a Actor, id string) (Ticket, error) {
	var out Ticket
	err := c.do(ctx, a, http.MethodGet, "/api/v1/tickets/"+url.PathEscape(id), nil, &out)
	return out, err
}

// Update patches a ticket.
func (c *Client) Update(ctx context.Context, a Actor, id string, in Update) (Ticket, error) {
	var out Ticket
	err := c.do(ctx, a, http.MethodPatch, "/api/v1/tickets/"+url.PathEscape(id), in, &out)
	return out, err
}

// Delete soft-deletes a ticket. The author or an admin may delete it.
func (c *Client) Delete(ctx context.Context, a Actor, id string) error {
	return c.do(ctx, a, http.MethodDelete, "/api/v1/tickets/"+url.PathEscape(id), nil, nil)
}

// ListComments lists notes on a visible ticket.
func (c *Client) ListComments(ctx context.Context, a Actor, id string, page, size int) (Page[Comment], error) {
	var out Page[Comment]
	err := c.do(ctx, a, http.MethodGet, "/api/v1/tickets/"+url.PathEscape(id)+"/comments"+pageQuery(page, size), nil, &out)
	if out.Items == nil {
		out.Items = []Comment{}
	}
	return out, err
}

// AddComment adds a note.
func (c *Client) AddComment(ctx context.Context, a Actor, id, body string) (Comment, error) {
	var out Comment
	err := c.do(ctx, a, http.MethodPost, "/api/v1/tickets/"+url.PathEscape(id)+"/comments", map[string]string{"body": body}, &out)
	return out, err
}

// ListHistory lists the audit rows of a visible ticket.
func (c *Client) ListHistory(ctx context.Context, a Actor, id string, page, size int) (Page[History], error) {
	var out Page[History]
	err := c.do(ctx, a, http.MethodGet, "/api/v1/tickets/"+url.PathEscape(id)+"/history"+pageQuery(page, size), nil, &out)
	if out.Items == nil {
		out.Items = []History{}
	}
	return out, err
}

// ListStatuses returns the tenant's columns. A new tenant's list is empty
// until its first ticket, which creates open, in_progress and closed.
func (c *Client) ListStatuses(ctx context.Context, a Actor) ([]Status, error) {
	var out Page[Status]
	err := c.do(ctx, a, http.MethodGet, "/api/v1/statuses", nil, &out)
	if out.Items == nil {
		out.Items = []Status{}
	}
	return out.Items, err
}

// CreateStatus adds a column. byz-tickets allows this for admins only.
func (c *Client) CreateStatus(ctx context.Context, a Actor, in StatusInput) (Status, error) {
	var out Status
	err := c.do(ctx, a, http.MethodPost, "/api/v1/statuses", in, &out)
	return out, err
}

func (c *Client) do(ctx context.Context, a Actor, method, path string, in, out any) error {
	if strings.TrimSpace(a.TenantID) == "" || strings.TrimSpace(a.UserID) == "" {
		return fmt.Errorf("%w: tenant and actor are required", ErrInvalid)
	}
	if strings.ContainsAny(a.TenantID, "\r\n") || strings.ContainsAny(a.UserID, "\r\n") {
		return fmt.Errorf("%w: tenant or actor is not a valid id", ErrInvalid)
	}
	if c.tokens != nil {
		tok, err := c.tokens.Token(ctx)
		if err != nil {
			return err
		}
		ctx = iam.WithBearer(ctx, tok)
	} else if c.static != "" {
		ctx = iam.WithBearer(ctx, c.static)
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := c.down.NewRequest(ctx, method, path, body)
	if err != nil {
		return err
	}
	req.Header.Set(HeaderTenant, a.TenantID)
	req.Header.Set(HeaderActor, a.UserID)
	req.Header.Set(HeaderAdmin, strconv.FormatBool(a.Admin))
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.down.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		p := &Problem{Status: resp.StatusCode}
		if json.Unmarshal(raw, p) != nil || p.Code == "" {
			p.Code, p.Detail = "http."+strconv.Itoa(resp.StatusCode), strings.TrimSpace(string(raw))
		}
		p.Status = resp.StatusCode
		if resp.StatusCode == http.StatusUnauthorized && c.tokens != nil {
			c.tokens.Invalidate()
		}
		return mapErr(p)
	}
	if out != nil && resp.StatusCode != http.StatusNoContent {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
			return fmt.Errorf("tickets: decode %s %s: %w", method, path, err)
		}
	}
	return nil
}

func mapErr(p *Problem) error {
	switch p.Status {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %w", ErrNotFound, p)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %w", ErrForbidden, p)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %w", ErrUnauthorized, p)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %w", ErrInvalid, p)
	case http.StatusConflict:
		return fmt.Errorf("%w: %w", ErrConflict, p)
	default:
		return p
	}
}

func query(f ListFilter) string {
	q := url.Values{}
	if f.RouteSet {
		q.Set("route", f.Route)
	}
	if f.QuerySet {
		q.Set("query", f.Query)
	}
	if f.StatusID != "" {
		q.Set("statusId", f.StatusID)
	}
	if f.Page > 0 {
		q.Set("page", strconv.Itoa(f.Page))
	}
	if f.Size > 0 {
		q.Set("size", strconv.Itoa(f.Size))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

func pageQuery(page, size int) string {
	q := url.Values{}
	if page > 0 {
		q.Set("page", strconv.Itoa(page))
	}
	if size > 0 {
		q.Set("size", strconv.Itoa(size))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}
