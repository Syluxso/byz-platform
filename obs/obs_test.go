package obs

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

func TestBufferRingAndTail(t *testing.T) {
	b := NewBuffer(3)
	for _, m := range []string{"a", "b", "c", "d"} {
		lvl := "INFO"
		if m == "c" {
			lvl = "ERROR"
		}
		b.Add(Entry{Level: lvl, Message: m})
	}
	got := b.Tail(10, "")
	if len(got) != 3 || got[0].Message != "b" || got[2].Message != "d" {
		t.Fatalf("tail = %+v", got)
	}
	if e := b.Tail(10, "error"); len(e) != 1 || e[0].Message != "c" {
		t.Fatalf("level filter = %+v", e)
	}
	if e := b.Tail(1, ""); len(e) != 1 || e[0].Message != "d" {
		t.Fatalf("tail 1 = %+v", e)
	}
	long := strings.Repeat("x", 5000)
	b.Add(Entry{Message: long})
	if got := b.Tail(1, "")[0].Message; len(got) > 4010 {
		t.Fatalf("message not truncated: %d", len(got))
	}
}

func TestNewWritesJSONAndBuffer(t *testing.T) {
	var out bytes.Buffer
	f := false
	logger, buf := New("byz-test", Options{Writer: &out, SetDefault: &f})
	logger.Warn("disk almost full", "pct", 91, "error", "ENOSPC")

	var line map[string]any
	if err := json.Unmarshal(out.Bytes(), &line); err != nil {
		t.Fatalf("not json: %s", out.String())
	}
	if line["service"] != "byz-test" || line["level"] != "WARN" || line["msg"] != "disk almost full" {
		t.Fatalf("json line = %v", line)
	}
	e := buf.Tail(1, "")
	if len(e) != 1 || e[0].Level != "WARN" || e[0].Logger != "byz-test" || !strings.Contains(e[0].Message, "pct=91") || e[0].Exception == nil || *e[0].Exception != "ENOSPC" {
		t.Fatalf("buffer entry = %+v", e)
	}
}

func TestStdLogFlowsThroughDefault(t *testing.T) {
	var out bytes.Buffer
	_, buf := New("svc", Options{Writer: &out})
	t.Cleanup(func() { slog.SetDefault(slog.New(slog.NewTextHandler(bytes.NewBuffer(nil), nil))) })
	log.Printf("legacy line %d", 7)
	if e := buf.Tail(1, ""); len(e) != 1 || !strings.Contains(e[0].Message, "legacy line 7") {
		t.Fatalf("std log not captured: %+v", e)
	}
}

func TestWithEnrichesFromContext(t *testing.T) {
	var out bytes.Buffer
	f := false
	logger, _ := New("svc", Options{Writer: &out, SetDefault: &f})
	ctx := httpx.WithRequestID(context.Background(), "r1")
	ctx = iam.WithClaims(ctx, iam.Claims{Subject: "u1", OrganizationID: "o1", TenantID: "t1"})
	With(ctx, logger).Info("hello")
	s := out.String()
	for _, want := range []string{`"request_id":"r1"`, `"org":"o1"`, `"tenant":"t1"`, `"actor":"u1"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s in %s", want, s)
		}
	}
}

func TestLogsHandler(t *testing.T) {
	b := NewBuffer(5)
	b.Add(Entry{Level: "INFO", Message: "one"})
	b.Add(Entry{Level: "ERROR", Message: "two"})
	rr := httptest.NewRecorder()
	LogsHandler(b).ServeHTTP(rr, httptest.NewRequest("GET", "/api/v1/admin/logs?lines=5&level=ERROR", nil))
	var got []Entry
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got) != 1 || got[0].Message != "two" {
		t.Fatalf("logs = %s (%v)", rr.Body.String(), err)
	}
}

func TestMetricsAndMiddleware(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /boards/{id}", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(201) })
	mux.Handle("GET /metrics", Metrics())
	var out bytes.Buffer
	f := false
	logger, buf := New("svc", Options{Writer: &out, SetDefault: &f})
	h := httpx.Chain(mux, httpx.RequestID, HTTPMetrics("svc"), RequestLogger(logger))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/boards/abc", nil))
	if rr.Code != 201 {
		t.Fatalf("status %d", rr.Code)
	}
	AuthRejected("svc")(nil, "expired")

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("GET", "/metrics", nil))
	body := rr.Body.String()
	if !strings.Contains(body, `byz_http_request_duration_seconds_count{method="GET",route="GET /boards/{id}",service="svc",status="201"} 1`) {
		t.Fatalf("histogram missing route label:\n%s", body)
	}
	if !strings.Contains(body, `byz_auth_rejects_total{reason="expired",service="svc"} 1`) {
		t.Fatalf("auth reject counter missing:\n%s", body)
	}
	e := buf.Tail(1, "")
	if len(e) != 1 || !strings.Contains(e[0].Message, "route=GET /boards/{id}") || !strings.Contains(e[0].Message, "status=201") {
		t.Fatalf("request log = %+v", e)
	}
	if !strings.Contains(out.String(), `"request_id":`) {
		t.Fatalf("request_id missing from JSON log: %s", out.String())
	}
}

func TestStatusWriterFlushKeepsStatus(t *testing.T) {
	rr := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: rr}
	sw.Flush()
	if sw.status() != 200 || !rr.Flushed {
		t.Fatalf("flush: status=%d flushed=%v", sw.status(), rr.Flushed)
	}
}
