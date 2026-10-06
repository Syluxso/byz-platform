package directory

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/byz-platform/iam"
)

func TestClient(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no bearer", 401)
			return
		}
		switch r.URL.Path {
		case "/api/v1/orgs/o1/profiles/u1":
			_, _ = w.Write([]byte(`{"id":"p1","userId":"u1","organizationId":"o1","email":"a@x.io","displayName":"Ada","orgRole":"admin","contact":{"email":"a@x.io"},"address":null,"metadata":{"k":1},"createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}`))
		case "/api/v1/orgs/o1/profiles/u2":
			http.Error(w, `{"message":"not found"}`, 404)
		case "/api/v1/orgs/o1/profiles":
			_, _ = w.Write([]byte(`[{"userId":"u1","displayName":"Ada","email":"a@x.io"},{"userId":"u3","displayName":"","email":"c@x.io"}]`))
		case "/api/v1/orgs/o1/tenants/t1":
			_, _ = w.Write([]byte(`{"id":"t1","organizationId":"o1","name":"Ops","slug":"ops","createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}`))
		case "/api/v1/orgs/o1/profile":
			_, _ = w.Write([]byte(`{"organizationId":"o1","displayName":"Nyberg Tech","createdAt":"2026-09-27T10:00:00Z","updatedAt":"2026-09-27T10:00:00Z"}`))
		case "/api/v1/tools/directory/me":
			_, _ = w.Write([]byte(`{"profileFound":true,"person":{"displayName":"Ada","email":"a@x.io","orgRole":"admin"},"organization":{"displayName":"Nyberg Tech"},"tenants":[{"name":"Ops","slug":"ops","role":"admin"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "tok")

	p, err := c.Profile(ctx, "o1", "u1")
	if err != nil || p.DisplayName != "Ada" || p.Contact == nil || p.Address != nil || p.Metadata["k"] != float64(1) {
		t.Fatalf("profile: %v %+v", err, p)
	}
	if _, err := c.Profile(ctx, "o1", "u2"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	n, err := c.Lookup(ctx, "o1", "u1")
	if err != nil || n.Label() != "Ada" {
		t.Fatalf("lookup: %v %+v", err, n)
	}
	names, err := c.Names(ctx, "o1", []string{"u1", "u3", "u9"})
	if err != nil || len(names) != 2 || names["u3"].Label() != "c@x.io" {
		t.Fatalf("names: %v %+v", err, names)
	}
	if (Name{ID: "u9"}).Label() != "u9" {
		t.Fatal("label fallback to id")
	}
	tn, err := c.Tenant(ctx, "o1", "t1")
	if err != nil || tn.Name != "Ops" {
		t.Fatalf("tenant: %v %+v", err, tn)
	}
	op, err := c.OrgProfile(ctx, "o1")
	if err != nil || op.DisplayName != "Nyberg Tech" {
		t.Fatalf("org: %v %+v", err, op)
	}
	me, err := c.WhoAmI(ctx)
	if err != nil || !me.ProfileFound || me.Person.DisplayName != "Ada" || len(me.Tenants) != 1 || me.Tenants[0].Role != "admin" {
		t.Fatalf("whoami: %v %+v", err, me)
	}
	if _, err := c.WhoAmI(context.Background()); err == nil {
		t.Fatal("expected refusal without bearer")
	}
}
