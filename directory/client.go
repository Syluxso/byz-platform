// Package directory is the read-only lookup client for byz-directory:
// display names for users, tenants and organisations so UI labels agree
// across services. It does not create or change anything.
//
// Wire shapes match ProfileResponse, TenantResponse, OrgProfileResponse and
// ToolsWhoamiResponse in byz-directory. Calls forward the caller's bearer.
package directory

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/Syluxso/byz-platform/downstream"
)

// Sentinel errors mapped from the service's status codes.
var (
	ErrNotFound     = errors.New("directory: not found")
	ErrForbidden    = errors.New("directory: forbidden")
	ErrUnauthorized = errors.New("directory: unauthorized")
)

// Contact is a reusable contact card.
type Contact struct {
	ID             string `json:"id,omitempty"`
	OrganizationID string `json:"organizationId,omitempty"`
	Email          string `json:"email"`
	Phone          string `json:"phone"`
	Website        string `json:"website"`
	Label          string `json:"label"`
}

// Address is a reusable address card.
type Address struct {
	ID             string   `json:"id,omitempty"`
	OrganizationID string   `json:"organizationId,omitempty"`
	Line1          string   `json:"line1"`
	Line2          string   `json:"line2"`
	City           string   `json:"city"`
	Region         string   `json:"region"`
	PostalCode     string   `json:"postalCode"`
	Country        string   `json:"country"`
	Latitude       *float64 `json:"latitude"`
	Longitude      *float64 `json:"longitude"`
	Label          string   `json:"label"`
}

// Profile is a user's profile within one organisation.
type Profile struct {
	ID             string         `json:"id"`
	UserID         string         `json:"userId"`
	OrganizationID string         `json:"organizationId"`
	Email          string         `json:"email"`
	DisplayName    string         `json:"displayName"`
	OrgRole        string         `json:"orgRole"`
	Contact        *Contact       `json:"contact"`
	Address        *Address       `json:"address"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// Tenant is a directory tenant (workspace or group) inside an organisation.
type Tenant struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	Name           string    `json:"name"`
	Slug           string    `json:"slug"`
	Description    string    `json:"description"`
	CreatedBy      string    `json:"createdBy"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// OrgProfile is the organisation's own profile card.
type OrgProfile struct {
	OrganizationID string         `json:"organizationId"`
	DisplayName    string         `json:"displayName"`
	Contact        *Contact       `json:"contact"`
	Address        *Address       `json:"address"`
	Metadata       map[string]any `json:"metadata"`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// WhoAmI is the agent-tools view of the caller: names only, no ids.
type WhoAmI struct {
	ProfileFound bool `json:"profileFound"`
	Person       *struct {
		DisplayName string   `json:"displayName"`
		Email       string   `json:"email"`
		OrgRole     string   `json:"orgRole"`
		Contact     *Contact `json:"contact"`
		Address     *Address `json:"address"`
	} `json:"person"`
	Organization *struct {
		DisplayName string   `json:"displayName"`
		Contact     *Contact `json:"contact"`
		Address     *Address `json:"address"`
	} `json:"organization"`
	Tenants []struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Role        string `json:"role"`
	} `json:"tenants"`
}

// Name is the minimal label a UI needs.
type Name struct {
	ID          string
	DisplayName string
	Email       string
}

// Client talks to one directory base URL, e.g.
// https://api.byzantineapp.dev/directory.
type Client struct {
	ds *downstream.Client
}

// New builds a Client.
func New(baseURL string, httpClient ...*http.Client) *Client {
	ds := downstream.New(baseURL)
	if len(httpClient) > 0 && httpClient[0] != nil {
		ds.HTTP = httpClient[0]
	}
	ds.UserAgent = "byz-platform/directory"
	return &Client{ds: ds}
}

// Profile returns one user's profile in an organisation.
func (c *Client) Profile(ctx context.Context, orgID, userID string) (Profile, error) {
	var out Profile
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/orgs/"+esc(orgID)+"/profiles/"+esc(userID), nil, &out)
	return out, mapErr(err)
}

// MyProfile returns the caller's own profile.
func (c *Client) MyProfile(ctx context.Context, orgID string) (Profile, error) {
	var out Profile
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/orgs/"+esc(orgID)+"/profiles/me", nil, &out)
	return out, mapErr(err)
}

// Profiles returns every profile in the organisation. Use it to label a
// list of user ids in one call rather than N Profile calls.
func (c *Client) Profiles(ctx context.Context, orgID string) ([]Profile, error) {
	var out []Profile
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/orgs/"+esc(orgID)+"/profiles", nil, &out)
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []Profile{}
	}
	return out, nil
}

// Tenant returns one tenant.
func (c *Client) Tenant(ctx context.Context, orgID, tenantID string) (Tenant, error) {
	var out Tenant
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/orgs/"+esc(orgID)+"/tenants/"+esc(tenantID), nil, &out)
	return out, mapErr(err)
}

// OrgProfile returns the organisation's profile card.
func (c *Client) OrgProfile(ctx context.Context, orgID string) (OrgProfile, error) {
	var out OrgProfile
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/orgs/"+esc(orgID)+"/profile", nil, &out)
	return out, mapErr(err)
}

// WhoAmI returns the agent-tools view of the caller.
func (c *Client) WhoAmI(ctx context.Context) (WhoAmI, error) {
	var out WhoAmI
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/tools/directory/me", nil, &out)
	return out, mapErr(err)
}

// Lookup returns a user's display name and email. A missing profile yields
// ErrNotFound; callers usually fall back to the id.
func (c *Client) Lookup(ctx context.Context, orgID, userID string) (Name, error) {
	p, err := c.Profile(ctx, orgID, userID)
	if err != nil {
		return Name{ID: userID}, err
	}
	return Name{ID: p.UserID, DisplayName: p.DisplayName, Email: p.Email}, nil
}

// Names labels many user ids in one round trip. Ids with no profile are
// absent from the result.
func (c *Client) Names(ctx context.Context, orgID string, userIDs []string) (map[string]Name, error) {
	want := make(map[string]bool, len(userIDs))
	for _, id := range userIDs {
		want[id] = true
	}
	all, err := c.Profiles(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]Name, len(userIDs))
	for _, p := range all {
		if want[p.UserID] {
			out[p.UserID] = Name{ID: p.UserID, DisplayName: p.DisplayName, Email: p.Email}
		}
	}
	return out, nil
}

// Label is the display string for a Name: display name, then email, then id.
func (n Name) Label() string {
	switch {
	case n.DisplayName != "":
		return n.DisplayName
	case n.Email != "":
		return n.Email
	default:
		return n.ID
	}
}

func esc(s string) string { return url.PathEscape(s) }

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
	}
	return err
}
