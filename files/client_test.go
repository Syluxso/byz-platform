package files

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/byz-platform/iam"
)

func server(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/files":
			_, _ = w.Write([]byte(`[{"id":"f1","name":"a.txt","sizeBytes":3,"createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}]`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/f1":
			_, _ = w.Write([]byte(`{"id":"f1","name":"a.txt","contentType":"text/plain","sizeBytes":3,"checksumSha256":"abc","createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/f1/content":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Length", "3")
			_, _ = w.Write([]byte("hey"))
		case r.Method == "GET" && r.URL.Path == "/api/v1/files/missing":
			http.Error(w, `{"message":"no"}`, 404)
		case r.Method == "POST" && r.URL.Path == "/api/v1/files":
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			f, hdr, err := r.FormFile("file")
			if err != nil {
				http.Error(w, "no part named file", 400)
				return
			}
			b, _ := io.ReadAll(f)
			if hdr.Filename != "notes.md" || hdr.Header.Get("Content-Type") != "text/markdown" || string(b) != "# hi" {
				http.Error(w, "bad part: "+hdr.Filename+" "+hdr.Header.Get("Content-Type")+" "+string(b), 400)
				return
			}
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"f2","name":"notes.md","contentType":"text/markdown","sizeBytes":4,"createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}`))
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/files/f1":
			w.WriteHeader(204)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func TestClientForwardsBearerAndMapsShapes(t *testing.T) {
	srv, seen := server(t)
	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "tok")

	list, err := c.List(ctx)
	if err != nil || len(list) != 1 || list[0].ID != "f1" {
		t.Fatalf("list: %v %+v", err, list)
	}
	m, err := c.Meta(ctx, "f1")
	if err != nil || m.ChecksumSHA256 != "abc" || m.SizeBytes != 3 || m.CreatedAt.IsZero() {
		t.Fatalf("meta: %v %+v", err, m)
	}
	obj, err := c.Content(ctx, "f1")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(obj.Body)
	obj.Close()
	if string(b) != "hey" || obj.ContentType != "text/plain" || obj.Size != 3 {
		t.Fatalf("content: %q %+v", b, obj)
	}
	up, err := c.Upload(ctx, "notes.md", "text/markdown", strings.NewReader("# hi"))
	if err != nil || up.ID != "f2" {
		t.Fatalf("upload: %v %+v", err, up)
	}
	if err := c.Delete(ctx, "f1"); err != nil {
		t.Fatal(err)
	}
	for _, s := range *seen {
		if !strings.HasSuffix(s, "Bearer tok") {
			t.Fatalf("request without caller bearer: %s", s)
		}
	}
}

func TestClientErrors(t *testing.T) {
	srv, _ := server(t)
	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "tok")
	if _, err := c.Meta(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404 not mapped: %v", err)
	}
	// No bearer on context and none set: refused before any network call.
	if _, err := c.List(context.Background()); err == nil {
		t.Fatal("expected refusal without bearer")
	}
}
