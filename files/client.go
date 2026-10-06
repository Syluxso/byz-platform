// Package files is the caller-bearer client for byz-file-service.
//
// Every call forwards the caller's own token through downstream, so the file
// service enforces its organisation scope against the tenant actor, never
// against the calling service. There is deliberately no service-identity
// upload path for user files.
//
// Wire shapes match FileResponse in byz-file-service.
package files

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Syluxso/byz-platform/downstream"
)

// Sentinel errors mapped from the file service's status codes.
var (
	ErrNotFound     = errors.New("files: not found")
	ErrForbidden    = errors.New("files: forbidden")
	ErrUnauthorized = errors.New("files: unauthorized")
	ErrTooLarge     = errors.New("files: payload too large")
)

// File is the metadata record the service returns.
type File struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organizationId"`
	TenantID       string    `json:"tenantId"`
	UploadedBy     string    `json:"uploadedBy"`
	Name           string    `json:"name"`
	ContentType    string    `json:"contentType"`
	SizeBytes      int64     `json:"sizeBytes"`
	ChecksumSHA256 string    `json:"checksumSha256"`
	Status         string    `json:"status"`
	Visibility     string    `json:"visibility"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

// Object is a streamed file body. Callers must Close it.
type Object struct {
	Body        io.ReadCloser
	ContentType string
	// Size is Content-Length, or -1 when the service did not send one.
	Size int64
}

// Close releases the body.
func (o *Object) Close() error { return o.Body.Close() }

// Client talks to one file service base URL, e.g.
// https://api.byzantineapp.dev/files.
type Client struct {
	ds *downstream.Client
}

// New builds a Client. Pass an *http.Client to override the 15s default,
// for example one with a longer timeout for large uploads.
func New(baseURL string, httpClient ...*http.Client) *Client {
	ds := downstream.New(baseURL)
	if len(httpClient) > 0 && httpClient[0] != nil {
		ds.HTTP = httpClient[0]
	}
	ds.UserAgent = "byz-platform/files"
	return &Client{ds: ds}
}

// List returns the caller's organisation's active files.
func (c *Client) List(ctx context.Context) ([]File, error) {
	var out []File
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/files", nil, &out)
	if err != nil {
		return nil, mapErr(err)
	}
	if out == nil {
		out = []File{}
	}
	return out, nil
}

// Meta returns one file's metadata.
func (c *Client) Meta(ctx context.Context, id string) (File, error) {
	var out File
	_, err := c.ds.JSON(ctx, http.MethodGet, "/api/v1/files/"+id, nil, &out)
	return out, mapErr(err)
}

// Upload streams body as multipart part "file" and returns the stored
// metadata. contentType may be blank, in which case the service records
// application/octet-stream.
func (c *Client) Upload(ctx context.Context, name, contentType string, body io.Reader) (File, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		defer pw.Close()
		hdr := make(map[string][]string)
		hdr["Content-Disposition"] = []string{fmt.Sprintf(`form-data; name="file"; filename=%q`, sanitize(name))}
		if contentType != "" {
			hdr["Content-Type"] = []string{contentType}
		}
		part, err := mw.CreatePart(hdr)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(part, body); err != nil {
			pw.CloseWithError(err)
			return
		}
		pw.CloseWithError(mw.Close())
	}()

	req, err := c.ds.NewRequest(ctx, http.MethodPost, "/api/v1/files", pr)
	if err != nil {
		return File{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")
	resp, err := c.ds.Do(req)
	if err != nil {
		return File{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return File{}, mapErr(&downstream.Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw)), Method: http.MethodPost, URL: req.URL.String()})
	}
	var out File
	if err := decodeJSON(resp.Body, &out); err != nil {
		return File{}, fmt.Errorf("files: decode upload response: %w", err)
	}
	return out, nil
}

// Content opens the file body for streaming. The caller must Close it.
func (c *Client) Content(ctx context.Context, id string) (*Object, error) {
	req, err := c.ds.NewRequest(ctx, http.MethodGet, "/api/v1/files/"+id+"/content", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.ds.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		return nil, mapErr(&downstream.Error{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw)), Method: http.MethodGet, URL: req.URL.String()})
	}
	size := int64(-1)
	if v := resp.Header.Get("Content-Length"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			size = n
		}
	}
	return &Object{Body: resp.Body, ContentType: resp.Header.Get("Content-Type"), Size: size}, nil
}

// Delete soft-deletes a file.
func (c *Client) Delete(ctx context.Context, id string) error {
	_, err := c.ds.JSON(ctx, http.MethodDelete, "/api/v1/files/"+id, nil, nil)
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
	case http.StatusRequestEntityTooLarge:
		return fmt.Errorf("%w: %v", ErrTooLarge, err)
	}
	return err
}

func sanitize(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	if name == "" {
		name = "file"
	}
	return name
}
