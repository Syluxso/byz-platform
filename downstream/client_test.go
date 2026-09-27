package downstream

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

func TestDoForwardsCallerIdentity(t *testing.T) {
	var gotAuth, gotRID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotRID = r.Header.Get(httpx.RequestIDHeader)
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"id":"f1"}`))
	}))
	defer srv.Close()

	c := New(srv.URL + "/")
	ctx := iam.WithBearer(context.Background(), "caller-token")
	ctx = httpx.WithRequestID(ctx, "rid-1")

	var out struct {
		ID string `json:"id"`
	}
	status, err := c.JSON(ctx, http.MethodGet, "api/v1/files/f1", nil, &out)
	if err != nil || status != 200 || out.ID != "f1" {
		t.Fatalf("JSON: %d %v %+v", status, err, out)
	}
	if gotAuth != "Bearer caller-token" || gotRID != "rid-1" {
		t.Fatalf("headers not forwarded: auth=%q rid=%q", gotAuth, gotRID)
	}
}

func TestDoRefusesWithoutBearer(t *testing.T) {
	c := New("http://127.0.0.1:1")
	req, _ := c.NewRequest(context.Background(), http.MethodGet, "/x", nil)
	if _, err := c.Do(req); !errors.Is(err, ErrNoBearer) {
		t.Fatalf("err = %v", err)
	}
}

func TestExplicitHeaderWins(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(204)
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "caller")
	req, _ := c.NewRequest(ctx, http.MethodDelete, "/x", nil)
	req.Header.Set("Authorization", "Bearer service-token")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer service-token" {
		t.Fatalf("explicit header overridden: %q", gotAuth)
	}
}

func TestJSONNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteProblem(w, 404, "files.not_found", "no such file")
	}))
	defer srv.Close()
	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "t")
	status, err := c.JSON(ctx, http.MethodGet, "/f", nil, nil)
	var de *Error
	if status != 404 || !errors.As(err, &de) || de.Status != 404 || de.Body == "" {
		t.Fatalf("status=%d err=%v", status, err)
	}
}
