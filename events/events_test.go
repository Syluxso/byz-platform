package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

type accessEvent struct {
	RequestID  string  `json:"requestId"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Status     int     `json:"status"`
	DurationMs int64   `json:"durationMs"`
	RouteID    *string `json:"routeId"`
}

func TestEncodeFlatEnvelope(t *testing.T) {
	ctx := httpx.WithRequestID(context.Background(), "rid-9")
	ctx = iam.WithClaims(ctx, iam.Claims{Subject: "u1", OrganizationID: "o1", TenantID: "t1"})
	at := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	body, key, err := Encode(ctx, "byz-api-gateway", Event{
		Topic:      "byz.gateway.access",
		Type:       "gateway.request.completed",
		EventID:    "e1",
		OccurredAt: at,
		Payload:    accessEvent{RequestID: "rid-9", Method: "GET", Path: "/kan/x", Status: 200, DurationMs: 12},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"eventId": "e1", "type": "gateway.request.completed", "occurredAt": "2026-09-27T12:00:00Z",
		"organizationId": "o1", "tenantId": "t1", "userId": "u1",
		"producer": "byz-api-gateway", "traceId": "rid-9",
		"requestId": "rid-9", "method": "GET", "path": "/kan/x", "status": float64(200), "durationMs": float64(12), "routeId": nil,
	}
	for k, v := range want {
		got, ok := m[k]
		if !ok {
			t.Errorf("missing %s", k)
			continue
		}
		if got != v {
			t.Errorf("%s = %v (%T), want %v", k, got, got, v)
		}
	}
	if len(m) != len(want) {
		t.Errorf("unexpected extra fields: %v", m)
	}
	if _, nested := m["payload"]; nested {
		t.Fatal("payload must be flattened, not nested")
	}
	if key != "o1" {
		t.Fatalf("key = %q, want organizationId", key)
	}
}

func TestEncodeDefaultsAndNulls(t *testing.T) {
	body, key, err := Encode(context.Background(), "svc", Event{Topic: "t", Type: "x.y", Payload: map[string]any{"fileId": "f1"}})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	if m["organizationId"] != nil || m["tenantId"] != nil || m["userId"] != nil {
		t.Fatalf("blank ids should be null: %v", m)
	}
	if _, ok := m["traceId"]; ok {
		t.Fatal("traceId should be omitted when no request id")
	}
	id, _ := m["eventId"].(string)
	if len(id) != 36 || key != id {
		t.Fatalf("eventId %q should be a uuid and the fallback key %q", id, key)
	}
	if _, err := time.Parse(time.RFC3339Nano, m["occurredAt"].(string)); err != nil {
		t.Fatalf("occurredAt not RFC3339: %v", m["occurredAt"])
	}
	if _, _, err := Encode(context.Background(), "svc", Event{Topic: "t", Type: "x", Payload: []int{1}}); err == nil {
		t.Fatal("non-object payload must be rejected")
	}
}

func TestDisabledPublisherIsNoop(t *testing.T) {
	p, err := New(Options{Producer: "svc", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	if p.Enabled() {
		t.Fatal("should be disabled")
	}
	if err := p.Emit(context.Background(), Event{Topic: "t", Type: "x"}); err != nil {
		t.Fatal(err)
	}
	p.EmitAsync(context.Background(), Event{Topic: "t", Type: "x"})
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Enabled: true}); err == nil {
		t.Fatal("Producer required")
	}
}

func TestEmitAgainstFakeCluster(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, "byz.test.events"))
	if err != nil {
		t.Skipf("kfake unavailable: %v", err)
	}
	defer cluster.Close()

	p, err := New(Options{Brokers: cluster.ListenAddrs(), Enabled: true, Producer: "byz-test", ProduceTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx := iam.WithClaims(context.Background(), iam.Claims{Subject: "u", OrganizationID: "org-a", TenantID: "ten-a"})
	if err := p.Emit(ctx, Event{Topic: "byz.test.events", Type: "thing.created", Payload: map[string]any{"thingId": "th1"}}); err != nil {
		t.Fatal(err)
	}
	p.EmitAsync(ctx, Event{Topic: "byz.test.events", Type: "thing.updated", Payload: map[string]any{"thingId": "th1"}})
	if err := p.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics("byz.test.events"), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	cctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var types []string
	for len(types) < 2 {
		fetches := consumer.PollFetches(cctx)
		if err := fetches.Err(); err != nil {
			t.Fatalf("poll: %v (got %v)", err, types)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			var m map[string]any
			_ = json.Unmarshal(r.Value, &m)
			types = append(types, m["type"].(string))
			if string(r.Key) != "org-a" || m["producer"] != "byz-test" || m["thingId"] != "th1" {
				t.Errorf("record key=%s body=%s", r.Key, r.Value)
			}
		})
	}
}
