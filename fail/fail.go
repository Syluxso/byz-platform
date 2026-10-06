// Package fail reports actionable faults: always to slog, and asynchronously
// to a Kafka fault topic through a bounded queue that never blocks the
// request path. When brokers are down the fault is logged, the Kafka copy is
// dropped, and OnDrop is called so a metric can count it.
//
// Frozen verb: Report.
//
// A fault is something an operator can act on: store down, a downstream 5xx,
// a unique violation after retry, a corrupt record. Expired or missing tokens
// are not faults; iam.FromRequest counts those through obs.AuthRejected.
package fail

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Syluxso/byz-platform/events"
	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// DefaultTopic is the platform fault topic. byz-events bootstraps it.
const DefaultTopic = "byz.faults"

// EventType is the envelope type stamped on every fault event.
const EventType = "fault.reported"

// Fault is one actionable failure.
type Fault struct {
	// Code is a stable dotted identifier, e.g. "store.unavailable",
	// "downstream.files.5xx", "kafka.consume.decode". Required.
	Code string
	// Err is the underlying error. Its text is logged and shipped.
	Err error
	// Msg is a human sentence when Err alone is not enough.
	Msg string
	// Attrs are extra structured fields (ids, counts). Values should be
	// JSON-friendly scalars.
	Attrs map[string]any
}

// Options configures a Reporter.
type Options struct {
	// Service name, stamped on every fault.
	Service string
	Logger  *slog.Logger
	// Publisher ships faults to Kafka. nil or disabled means log only.
	Publisher *events.Publisher
	// Topic overrides DefaultTopic.
	Topic string
	// Queue bounds the async buffer. Default 256.
	Queue int
	// OnDrop is called when the queue is full or the broker send fails.
	// Wire to a metric.
	OnDrop func(code string)
	// EmitTimeout bounds each background send. Default 3s.
	EmitTimeout time.Duration
}

type item struct {
	ctx context.Context
	ev  events.Event
}

// Reporter is safe for concurrent use. Create one per process.
type Reporter struct {
	opts  Options
	queue chan item
	wg    sync.WaitGroup
	once  sync.Once
	stop  chan struct{}
}

// New starts the background sender when a Publisher is present.
func New(opts Options) *Reporter {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Topic == "" {
		opts.Topic = DefaultTopic
	}
	if opts.Queue <= 0 {
		opts.Queue = 256
	}
	if opts.EmitTimeout <= 0 {
		opts.EmitTimeout = 3 * time.Second
	}
	r := &Reporter{opts: opts, stop: make(chan struct{})}
	if opts.Publisher.Enabled() {
		r.queue = make(chan item, opts.Queue)
		r.wg.Add(1)
		go r.drain()
	}
	return r
}

// Report logs the fault with request context and queues the Kafka copy.
// It never blocks and never returns an error; a fault about reporting a
// fault would not help anyone.
func (r *Reporter) Report(ctx context.Context, f Fault) {
	if r == nil {
		return
	}
	code := strings.TrimSpace(f.Code)
	if code == "" {
		code = "unknown"
	}
	msg := f.Msg
	if msg == "" && f.Err != nil {
		msg = f.Err.Error()
	}
	if msg == "" {
		msg = code
	}

	attrs := make([]any, 0, 8+2*len(f.Attrs))
	attrs = append(attrs, "code", code)
	if f.Err != nil {
		attrs = append(attrs, "error", f.Err.Error())
	}
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
	}
	for k, v := range f.Attrs {
		attrs = append(attrs, k, v)
	}
	r.opts.Logger.Error(msg, attrs...)

	if r.queue == nil {
		return
	}
	payload := map[string]any{
		"code":    code,
		"message": msg,
		"service": r.opts.Service,
	}
	if f.Err != nil {
		payload["error"] = f.Err.Error()
	}
	for k, v := range f.Attrs {
		if _, reserved := payload[k]; !reserved {
			payload[k] = v
		}
	}
	// Detach from the request's cancellation but keep its values.
	it := item{ctx: context.WithoutCancel(ctx), ev: events.Event{
		Topic:   r.opts.Topic,
		Type:    EventType,
		Key:     r.opts.Service,
		Payload: payload,
	}}
	select {
	case r.queue <- it:
	default:
		r.drop(code, errors.New("fault queue full"))
	}
}

func (r *Reporter) drain() {
	defer r.wg.Done()
	for {
		select {
		case it := <-r.queue:
			r.send(it)
		case <-r.stop:
			for {
				select {
				case it := <-r.queue:
					r.send(it)
				default:
					return
				}
			}
		}
	}
}

func (r *Reporter) send(it item) {
	ctx, cancel := context.WithTimeout(it.ctx, r.opts.EmitTimeout)
	defer cancel()
	if err := r.opts.Publisher.Emit(ctx, it.ev); err != nil {
		code, _ := it.ev.Payload.(map[string]any)["code"].(string)
		r.drop(code, err)
	}
}

func (r *Reporter) drop(code string, err error) {
	r.opts.Logger.Warn("fail: kafka copy dropped", "code", code, "error", err.Error())
	if r.opts.OnDrop != nil {
		r.opts.OnDrop(code)
	}
}

// Close stops accepting new Kafka copies, flushes what is queued within ctx,
// and returns. Faults reported after Close are still logged. Fits
// shutdown.Closer.
func (r *Reporter) Close(ctx context.Context) error {
	if r == nil || r.queue == nil {
		return nil
	}
	r.once.Do(func() { close(r.stop) })
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

var (
	defaultMu sync.RWMutex
	defaultR  *Reporter
)

// SetDefault installs the process-wide Reporter used by the package-level
// Report so handlers do not need to thread it through.
func SetDefault(r *Reporter) {
	defaultMu.Lock()
	defaultR = r
	defaultMu.Unlock()
}

// Report uses the default Reporter. Before SetDefault it logs only.
func Report(ctx context.Context, f Fault) {
	defaultMu.RLock()
	r := defaultR
	defaultMu.RUnlock()
	if r == nil {
		r = &Reporter{opts: Options{Logger: slog.Default()}}
	}
	r.Report(ctx, f)
}
