package fail

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Syluxso/byz-platform/events"
	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

func TestReportLogsWithContextWithoutPublisher(t *testing.T) {
	var out bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&out, nil))
	r := New(Options{Service: "svc", Logger: logger})

	ctx := httpx.WithRequestID(context.Background(), "rid")
	ctx = iam.WithClaims(ctx, iam.Claims{OrganizationID: "o", TenantID: "t"})
	r.Report(ctx, Fault{Code: "store.unavailable", Err: errors.New("dial refused"), Attrs: map[string]any{"boardId": "b1"}})

	s := out.String()
	for _, want := range []string{`"level":"ERROR"`, `"msg":"dial refused"`, `"code":"store.unavailable"`, `"request_id":"rid"`, `"org":"o"`, `"tenant":"t"`, `"boardId":"b1"`} {
		if !strings.Contains(s, want) {
			t.Errorf("log missing %s: %s", want, s)
		}
	}
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Package-level Report before SetDefault must not panic.
	Report(ctx, Fault{Code: "x"})
}

func TestReportShipsToKafkaAndFlushesOnClose(t *testing.T) {
	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, DefaultTopic))
	if err != nil {
		t.Skipf("kfake unavailable: %v", err)
	}
	defer cluster.Close()
	pub, err := events.New(events.Options{Brokers: cluster.ListenAddrs(), Enabled: true, Producer: "svc", ProduceTimeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	var drops int32
	r := New(Options{Service: "svc", Publisher: pub, Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)), OnDrop: func(string) { atomic.AddInt32(&drops, 1) }})

	ctx, cancel := context.WithCancel(iam.WithClaims(context.Background(), iam.Claims{OrganizationID: "org-1"}))
	r.Report(ctx, Fault{Code: "downstream.files.5xx", Msg: "files returned 502", Attrs: map[string]any{"status": 502}})
	cancel() // request ends; the fault must still ship
	if err := r.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := pub.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&drops) != 0 {
		t.Fatalf("unexpected drops: %d", drops)
	}

	consumer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...), kgo.ConsumeTopics(DefaultTopic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	cctx, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	fetches := consumer.PollFetches(cctx)
	if err := fetches.Err(); err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	fetches.EachRecord(func(rec *kgo.Record) {
		_ = json.Unmarshal(rec.Value, &got)
		if string(rec.Key) != "svc" {
			t.Errorf("key = %s, want service", rec.Key)
		}
	})
	if got["type"] != EventType || got["code"] != "downstream.files.5xx" || got["service"] != "svc" || got["organizationId"] != "org-1" || got["status"] != float64(502) || got["message"] != "files returned 502" {
		t.Fatalf("fault event = %v", got)
	}
}

func TestReportDropsWhenQueueFull(t *testing.T) {
	// A publisher pointed at a dead broker: sends hang until timeout, the
	// queue fills, and further reports drop immediately without blocking.
	pub, err := events.New(events.Options{Brokers: []string{"127.0.0.1:1"}, Enabled: true, Producer: "svc", ProduceTimeout: 200 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	var drops int32
	r := New(Options{Service: "svc", Publisher: pub, Queue: 2, EmitTimeout: 200 * time.Millisecond,
		Logger: slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
		OnDrop: func(string) { atomic.AddInt32(&drops, 1) }})

	start := time.Now()
	for i := 0; i < 20; i++ {
		r.Report(context.Background(), Fault{Code: "store.unavailable", Err: errors.New("x")})
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatalf("Report blocked the caller for %v", time.Since(start))
	}
	cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Close(cctx)
	_ = pub.Close(cctx)
	if atomic.LoadInt32(&drops) == 0 {
		t.Fatal("expected drops with a dead broker and a tiny queue")
	}
}
