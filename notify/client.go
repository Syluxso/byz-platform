// Package notify is the thin client for byz-notifications so services stop
// inventing their own notification payloads.
//
// Wire shapes match InAppNotificationRequest, NotificationResponse and the
// Spring Page envelope the service returns today. Calls forward the caller's
// bearer; a service token must also set ListOptions.RecipientID because the
// service resolves the recipient from the token subject for user tokens and
// from X-Recipient-Id for service tokens.
package notify

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/Syluxso/byz-platform/downstream"
)

// Sentinel errors mapped from the service's status codes.
var (
	ErrNotFound     = errors.New("notify: not found")
	ErrForbidden    = errors.New("notify: forbidden")
	ErrUnauthorized = errors.New("notify: unauthorized")
	ErrInvalid      = errors.New("notify: invalid request")
)

// InApp is the create request. UserID, Title and Message are required by the
// service. OrganizationID may be left blank for user tokens; service tokens
// should set it.
type InApp struct {
	OrganizationID string         `json:"organizationId,omitempty"`
	UserID         string         `json:"userId"`
	Title          string         `json:"title"`
	Message        string         `json:"message"`
	Link           string         `json:"link,omitempty"`
	Data           map[string]any `json:"data,omitempty"`
	// Priority: low | normal | high. Blank means normal.
	Priority string `json:"priority,omitempty"`
	// Source names the producing service or feature, e.g. "kan".
	Source string `json:"source,omitempty"`
}

// Notification is the service's response record. Data is the raw JSON text
// the service stores.
type Notification struct {
	ID        string     `json:"id"`
	TenantID  string     `json:"tenantId"`
	UserID    string     `json:"userId"`
	Channel   string     `json:"channel"`
	Status    string     `json:"status"`
	Title     string     `json:"title"`
	Message   string     `json:"message"`
	Link      string     `json:"link"`
	Data      string     `json:"data"`
	Source    string     `json:"source"`
	Priority  string     `json:"priority"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// Page is the Spring Data page envelope the list endpoint returns.
type Page struct {
	Content       []Notification `json:"content"`
	TotalElements int64          `json:"totalElements"`
	TotalPages    int            `json:"totalPages"`
	Number        int            `json:"number"`
	Size          int            `json:"size"`
}

// ListOptions filters List. Page is 0-based to match the service.
type ListOptions struct {
	Status      string
	Page        int
	Size        int
	RecipientID string
}

// Client talks to one notifications base URL, e.g.
// https://api.byzantineapp.dev/notifications.
type Client struct {
	ds *downstream.Client
}

// New builds a Client.
func New(baseURL string, httpClient ...*http.Client) *Client {
	ds := downstream.New(baseURL)
	if len(httpClient) > 0 && httpClient[0] != nil {
		ds.HTTP = httpClient[0]
	}
	ds.UserAgent = "byz-platform/notify"
	return &Client{ds: ds}
}

// Create posts an in-app notification and returns the stored record.
func (c *Client) Create(ctx context.Context, n InApp) (Notification, error) {
	if n.UserID == "" || n.Title == "" || n.Message == "" {
		return Notification{}, fmt.Errorf("%w: userId, title and message are required", ErrInvalid)
	}
	var out Notification
	_, err := c.ds.JSON(ctx, http.MethodPost, "/api/v1/notifications/in-app", n, &out)
	return out, mapErr(err)
}

// List returns the recipient's inbox page.
func (c *Client) List(ctx context.Context, opts ListOptions) (Page, error) {
	q := url.Values{}
	if opts.Status != "" {
		q.Set("status", opts.Status)
	}
	if opts.Page > 0 {
		q.Set("page", strconv.Itoa(opts.Page))
	}
	if opts.Size > 0 {
		q.Set("size", strconv.Itoa(opts.Size))
	}
	path := "/api/v1/notifications"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	req, err := c.ds.NewRequest(ctx, http.MethodGet, path, nil)
	if err != nil {
		return Page{}, err
	}
	if opts.RecipientID != "" {
		req.Header.Set("X-Recipient-Id", opts.RecipientID)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.ds.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	var out Page
	if err := downstreamDecode(resp, http.MethodGet, req.URL.String(), &out); err != nil {
		return Page{}, mapErr(err)
	}
	if out.Content == nil {
		out.Content = []Notification{}
	}
	return out, nil
}

// UnreadCount returns the recipient's unread total.
func (c *Client) UnreadCount(ctx context.Context) (int64, error) {
	var out struct {
		Count int64 `json:"count"`
	}
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/notifications/unread-count", nil, &out)
	return out.Count, mapErr(err)
}

// MarkRead marks one notification read.
func (c *Client) MarkRead(ctx context.Context, id string) (Notification, error) {
	var out Notification
	_, err := c.ds.JSON(ctx, http.MethodPatch, "/api/v1/notifications/"+id+"/read", nil, &out)
	return out, mapErr(err)
}

// MarkAllRead marks the recipient's inbox read.
func (c *Client) MarkAllRead(ctx context.Context) error {
	_, err := c.ds.JSON(ctx, http.MethodPost, "/api/v1/notifications/mark-all-read", nil, nil)
	return mapErr(err)
}

// Delete soft-deletes one notification.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.ds.JSON(ctx, http.MethodDelete, "/api/v1/notifications/"+id, nil, nil)
	return mapErr(err)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	switch st, _ := downstream.Status(err); st {
	case http.StatusNotFound:
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	case http.StatusForbidden:
		return fmt.Errorf("%w: %v", ErrForbidden, err)
	case http.StatusUnauthorized:
		return fmt.Errorf("%w: %v", ErrUnauthorized, err)
	case http.StatusBadRequest:
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return err
}
