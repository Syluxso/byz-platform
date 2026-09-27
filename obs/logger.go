package obs

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// Options configures New.
type Options struct {
	// Level is the minimum level. Default Info.
	Level slog.Leveler
	// Writer receives JSON lines. Default os.Stderr.
	Writer io.Writer
	// Buffer receives every record for LogsHandler. Default NewBuffer(0).
	Buffer *Buffer
	// SetDefault, when true, installs the logger as slog.Default so the
	// standard log package also flows into the buffer. Default true; set
	// false in tests.
	SetDefault *bool
}

// New builds the service logger. Every line carries service=<name>. Records
// are written as JSON to Writer and appended to Buffer.
func New(service string, opts Options) (*slog.Logger, *Buffer) {
	if opts.Writer == nil {
		opts.Writer = os.Stderr
	}
	if opts.Buffer == nil {
		opts.Buffer = NewBuffer(0)
	}
	if opts.Level == nil {
		opts.Level = slog.LevelInfo
	}
	jsonH := slog.NewJSONHandler(opts.Writer, &slog.HandlerOptions{Level: opts.Level})
	bufH := &bufferHandler{buf: opts.Buffer, logger: service, level: opts.Level}
	logger := slog.New(&fanout{handlers: []slog.Handler{jsonH, bufH}}).With("service", service)
	if opts.SetDefault == nil || *opts.SetDefault {
		slog.SetDefault(logger)
	}
	return logger, opts.Buffer
}

// With returns logger enriched with request_id, org and tenant from ctx so
// handler code can log without repeating them.
func With(ctx context.Context, logger *slog.Logger) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	attrs := make([]any, 0, 8)
	if id := httpx.RequestIDFrom(ctx); id != "" {
		attrs = append(attrs, "request_id", id)
	}
	if c, ok := iam.ClaimsFrom(ctx); ok {
		if c.OrganizationID != "" {
			attrs = append(attrs, "org", c.OrganizationID)
		}
		if c.TenantID != "" {
			attrs = append(attrs, "tenant", c.TenantID)
		}
		if a := c.ActorID(); a != "" {
			attrs = append(attrs, "actor", a)
		}
	}
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}

// fanout sends each record to every handler.
type fanout struct{ handlers []slog.Handler }

func (f *fanout) Enabled(ctx context.Context, l slog.Level) bool {
	for _, h := range f.handlers {
		if h.Enabled(ctx, l) {
			return true
		}
	}
	return false
}

func (f *fanout) Handle(ctx context.Context, r slog.Record) error {
	var first error
	for _, h := range f.handlers {
		if !h.Enabled(ctx, r.Level) {
			continue
		}
		if err := h.Handle(ctx, r.Clone()); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (f *fanout) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithAttrs(attrs)
	}
	return &fanout{handlers: out}
}

func (f *fanout) WithGroup(name string) slog.Handler {
	out := make([]slog.Handler, len(f.handlers))
	for i, h := range f.handlers {
		out[i] = h.WithGroup(name)
	}
	return &fanout{handlers: out}
}

// bufferHandler renders a record into the ring as "msg key=value ...".
type bufferHandler struct {
	buf    *Buffer
	logger string
	level  slog.Leveler
	attrs  []slog.Attr
	group  string
}

func (h *bufferHandler) Enabled(_ context.Context, l slog.Level) bool {
	return l >= h.level.Level()
}

func (h *bufferHandler) Handle(_ context.Context, r slog.Record) error {
	var sb strings.Builder
	sb.WriteString(r.Message)
	var exc *string
	write := func(a slog.Attr) {
		if a.Key == "service" {
			return
		}
		if a.Key == "error" || a.Key == "err" {
			s := a.Value.String()
			exc = &s
		}
		key := a.Key
		if h.group != "" {
			key = h.group + "." + key
		}
		sb.WriteByte(' ')
		sb.WriteString(key)
		sb.WriteByte('=')
		sb.WriteString(a.Value.String())
	}
	for _, a := range h.attrs {
		write(a)
	}
	r.Attrs(func(a slog.Attr) bool { write(a); return true })
	h.buf.Add(Entry{
		Ts:        r.Time.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		Level:     levelName(r.Level),
		Logger:    h.logger,
		Message:   sb.String(),
		Exception: exc,
	})
	return nil
}

func (h *bufferHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	n := *h
	n.attrs = append(append([]slog.Attr{}, h.attrs...), attrs...)
	return &n
}

func (h *bufferHandler) WithGroup(name string) slog.Handler {
	n := *h
	if n.group != "" {
		n.group += "." + name
	} else {
		n.group = name
	}
	return &n
}

func levelName(l slog.Level) string {
	switch {
	case l >= slog.LevelError:
		return "ERROR"
	case l >= slog.LevelWarn:
		return "WARN"
	case l >= slog.LevelInfo:
		return "INFO"
	default:
		return "DEBUG"
	}
}
