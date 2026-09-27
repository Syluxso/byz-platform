// Package obs is logging and metrics for Byz services: a slog logger that
// also feeds the in-memory ring the admin UI tails, Prometheus request
// metrics, and the auth-reject counter iam.FromRequest reports into.
//
// Frozen verbs: New, LogsHandler, Metrics, HTTPMetrics, AuthRejected,
// RequestLogger.
//
// One id: X-Request-Id on the wire is request_id in every log line and
// traceId on event envelopes.
package obs

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Syluxso/byz-platform/httpx"
)

// DefaultBufferSize matches the 500 lines every service kept before.
const DefaultBufferSize = 500

// Entry is one buffered log line. The JSON shape is what the admin log
// viewer already parses from byz-kan and the gateway.
type Entry struct {
	Ts        string  `json:"ts"`
	Level     string  `json:"level"`
	Logger    string  `json:"logger"`
	Message   string  `json:"message"`
	Exception *string `json:"exception"`
}

// Buffer is a fixed-size ring of recent log entries.
type Buffer struct {
	mu      sync.Mutex
	max     int
	entries []Entry
}

// NewBuffer creates a ring holding at most n entries (DefaultBufferSize if n <= 0).
func NewBuffer(n int) *Buffer {
	if n <= 0 {
		n = DefaultBufferSize
	}
	return &Buffer{max: n, entries: make([]Entry, 0, n)}
}

// Add appends an entry, evicting the oldest when full. Messages are
// truncated at 4000 characters.
func (b *Buffer) Add(e Entry) {
	if len(e.Message) > 4000 {
		e.Message = e.Message[:4000] + "…"
	}
	if e.Ts == "" {
		e.Ts = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) >= b.max {
		copy(b.entries, b.entries[1:])
		b.entries = b.entries[:len(b.entries)-1]
	}
	b.entries = append(b.entries, e)
}

// Tail returns the last `lines` entries, optionally filtered by level
// (case-insensitive). lines is clamped to [1, capacity].
func (b *Buffer) Tail(lines int, level string) []Entry {
	if lines < 1 {
		lines = 1
	}
	if lines > b.max {
		lines = b.max
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	src := b.entries
	if level != "" {
		src = make([]Entry, 0, len(b.entries))
		for _, e := range b.entries {
			if strings.EqualFold(e.Level, level) {
				src = append(src, e)
			}
		}
	}
	if len(src) > lines {
		src = src[len(src)-lines:]
	}
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// LogsHandler serves GET ?lines=N&level=ERROR as a JSON array of Entry. Mount
// it behind iam.FromRequest at /api/v1/admin/logs; it does no auth itself.
func LogsHandler(b *Buffer) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lines := 200
		if v, err := strconv.Atoi(r.URL.Query().Get("lines")); err == nil && v > 0 {
			lines = v
		}
		httpx.WriteJSON(w, http.StatusOK, b.Tail(lines, strings.TrimSpace(r.URL.Query().Get("level"))))
	})
}
