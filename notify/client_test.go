package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Syluxso/byz-platform/iam"
)

func TestClient(t *testing.T) {
	var gotCreate InApp
	var gotRecipient, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			http.Error(w, "no bearer", 401)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "POST /api/v1/notifications/in-app":
			_ = json.NewDecoder(r.Body).Decode(&gotCreate)
			w.WriteHeader(201)
			_, _ = w.Write([]byte(`{"id":"n1","userId":"u1","channel":"in_app","status":"unread","title":"T","message":"M","createdAt":"2026-09-27T10:00:00Z","readAt":null}`))
		case "GET /api/v1/notifications":
			gotRecipient = r.Header.Get("X-Recipient-Id")
			gotQuery = r.URL.RawQuery
			_, _ = w.Write([]byte(`{"content":[{"id":"n1","status":"unread","title":"T","createdAt":"2026-09-27T10:00:00Z"}],"totalElements":1,"totalPages":1,"number":0,"size":20}`))
		case "GET /api/v1/notifications/unread-count":
			_, _ = w.Write([]byte(`{"count":7}`))
		case "PATCH /api/v1/notifications/n1/read":
			_, _ = w.Write([]byte(`{"id":"n1","status":"read","readAt":"2026-09-27T11:00:00Z","createdAt":"2026-09-27T10:00:00Z"}`))
		case "POST /api/v1/notifications/mark-all-read", "DELETE /api/v1/notifications/n1":
			w.WriteHeader(204)
		case "PATCH /api/v1/notifications/nope/read":
			http.Error(w, "{}", 404)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	c := New(srv.URL)
	ctx := iam.WithBearer(context.Background(), "tok")

	n, err := c.Create(ctx, InApp{UserID: "u1", Title: "T", Message: "M", Link: "/x", Priority: "high", Source: "kan", Data: map[string]any{"ticketId": "t1"}})
	if err != nil || n.ID != "n1" || n.ReadAt != nil {
		t.Fatalf("create: %v %+v", err, n)
	}
	if gotCreate.UserID != "u1" || gotCreate.Priority != "high" || gotCreate.Data["ticketId"] != "t1" || gotCreate.Source != "kan" {
		t.Fatalf("create body = %+v", gotCreate)
	}
	if _, err := c.Create(ctx, InApp{UserID: "u1"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("validation: %v", err)
	}

	p, err := c.List(ctx, ListOptions{Status: "unread", Size: 20, RecipientID: "u1"})
	if err != nil || p.TotalElements != 1 || len(p.Content) != 1 || p.Size != 20 {
		t.Fatalf("list: %v %+v", err, p)
	}
	if gotRecipient != "u1" || gotQuery != "size=20&status=unread" {
		t.Fatalf("list request: recipient=%q query=%q", gotRecipient, gotQuery)
	}
	if cnt, err := c.UnreadCount(ctx); err != nil || cnt != 7 {
		t.Fatalf("unread: %v %d", err, cnt)
	}
	if r, err := c.MarkRead(ctx, "n1"); err != nil || r.Status != "read" || r.ReadAt == nil {
		t.Fatalf("mark read: %v %+v", err, r)
	}
	if err := c.MarkAllRead(ctx); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.MarkRead(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("404: %v", err)
	}
	if _, err := c.UnreadCount(context.Background()); err == nil {
		t.Fatal("expected refusal without bearer")
	}
}
