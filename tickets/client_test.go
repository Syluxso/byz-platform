package tickets

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestClientSendsActorAndNotBodyIdentity(t *testing.T) {
	var gotTenant, gotActor, gotAdmin, gotAuth string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get(HeaderTenant)
		gotActor = r.Header.Get(HeaderActor)
		gotAdmin = r.Header.Get(HeaderAdmin)
		gotAuth = r.Header.Get("Authorization")
		gotBody = nil
		if r.Body != nil {
			raw, _ := io.ReadAll(r.Body)
			if len(raw) > 0 {
				_ = json.Unmarshal(raw, &gotBody)
			}
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/tickets":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"t1","title":"Save","authorId":"ada","statusSlug":"open","route":"/desk","queryString":"tab=1","metadata":{},"createdAt":"2026-11-16T00:00:00Z","updatedAt":"2026-11-16T00:00:00Z"}`))
		case "GET /api/v1/tickets":
			if r.URL.Query().Get("route") != "/desk" || r.URL.Query().Get("query") != "tab=1" {
				t.Errorf("query %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"items":[{"id":"t1","title":"Save","createdAt":"2026-11-16T00:00:00Z","updatedAt":"2026-11-16T00:00:00Z"}],"total":1,"page":1,"size":50}`))
		case "POST /api/v1/tickets/t1/comments":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"id":"c1","ticketId":"t1","authorId":"ada","body":"more","createdAt":"2026-11-16T00:00:00Z"}`))
		case "DELETE /api/v1/tickets/missing":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"status":404,"code":"ticket.not_found","title":"Not Found","detail":"ticket not found"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL, WithStaticToken("svc-token"))
	ada := Actor{TenantID: "tenant-1", UserID: "ada", Admin: false}
	row, err := c.Create(context.Background(), ada, Create{Title: "Save", Route: "/desk", QueryString: "tab=1"})
	if err != nil || row.ID != "t1" {
		t.Fatalf("create: %v %+v", err, row)
	}
	if gotAuth != "Bearer svc-token" || gotTenant != "tenant-1" || gotActor != "ada" || gotAdmin != "false" {
		t.Fatalf("headers auth=%q tenant=%q actor=%q admin=%q", gotAuth, gotTenant, gotActor, gotAdmin)
	}
	if _, ok := gotBody["isAdmin"]; ok {
		t.Fatalf("body carried isAdmin: %+v", gotBody)
	}
	if _, ok := gotBody["authorId"]; ok {
		t.Fatalf("body carried authorId: %+v", gotBody)
	}

	admin := Actor{TenantID: "tenant-1", UserID: "sam", Admin: true}
	page, err := c.List(context.Background(), admin, ListFilter{Route: "/desk", RouteSet: true, Query: "tab=1", QuerySet: true, Size: 50})
	if err != nil || page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("list: %v %+v", err, page)
	}
	if gotActor != "sam" || gotAdmin != "true" {
		t.Fatalf("admin headers actor=%q admin=%q", gotActor, gotAdmin)
	}

	if _, err := c.AddComment(context.Background(), ada, "t1", "more"); err != nil {
		t.Fatal(err)
	}
	err = c.Delete(context.Background(), ada, "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete: %v", err)
	}
	if _, err := c.Create(context.Background(), Actor{}, Create{Title: "x", Route: "/"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing actor: %v", err)
	}
}
