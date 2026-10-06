// Package events publishes Kafka events in the flat camelCase envelope the
// platform's live consumers already parse.
//
// Frozen verb: Emit.
//
// Envelope (unchanged from byz-iam, byz-file-service and the gateway):
//
//	eventId, type, occurredAt, organizationId, tenantId, userId, <domain fields>
//
// Two additive fields: producer (service name) and traceId (the request id).
// Nothing is nested, org and tenant stay at the top level because consumers
// key and partition on them, and type is always present. A breaking change
// to a shape means a new topic name, never a silent byte change.
//
// Producer settings match the live gateway: acks=all (idempotent producer),
// 2s produce timeout, 3 retries, 1MB batches.
package events

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/Syluxso/byz-platform/httpx"
	"github.com/Syluxso/byz-platform/iam"
)

// Event is one thing to publish. Payload is a struct or map with the domain
// fields; it is flattened into the envelope, so its JSON tags must already be
// camelCase. Envelope fields left blank are filled from ctx (org, tenant,
// user from claims; traceId from the request id) or generated.
type Event struct {
	Topic string
	// Key partitions the topic. Defaults to OrganizationID, then EventID.
	Key string

	Type           string
	EventID        string
	OccurredAt     time.Time
	OrganizationID string
	TenantID       string
	UserID         string

	Payload any
}

// Options configures a Publisher.
type Options struct {
	Brokers []string
	// Enabled false yields a nil-safe publisher that drops everything and
	// returns nil, so a service runs without Kafka in development.
	Enabled bool
	// Producer is stamped on every event and used as the Kafka client id.
	Producer string
	// ProduceTimeout bounds Emit. Default 2s.
	ProduceTimeout time.Duration
	Logger         *slog.Logger
	// OnError is called for async failures (EmitAsync). Wire to a metric.
	OnError func(topic string, err error)
	// AutoCreateTopics lets the producer create a missing topic. Off by
	// default: byz-events owns topic creation on the platform, and a typo in
	// a topic name should fail loudly rather than mint a new topic.
	AutoCreateTopics bool
}

// Publisher owns one Kafka client.
type Publisher struct {
	client  *kgo.Client
	opts    Options
	enabled bool
}

// ErrDisabled is returned by MustEmit when the publisher is disabled.
var ErrDisabled = errors.New("events: publisher disabled")

// New builds a Publisher. Creating the client does not contact brokers; the
// first Emit does, and fails within ProduceTimeout if they are down.
func New(opts Options) (*Publisher, error) {
	if opts.ProduceTimeout <= 0 {
		opts.ProduceTimeout = 2 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if strings.TrimSpace(opts.Producer) == "" {
		return nil, errors.New("events: Producer (service name) is required")
	}
	if !opts.Enabled || len(opts.Brokers) == 0 {
		return &Publisher{opts: opts}, nil
	}
	kopts := []kgo.Opt{
		kgo.SeedBrokers(opts.Brokers...),
		kgo.ClientID(opts.Producer),
		kgo.ProducerBatchMaxBytes(1_000_000),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.ProduceRequestTimeout(opts.ProduceTimeout),
		kgo.RecordRetries(3),
	}
	if opts.AutoCreateTopics {
		kopts = append(kopts, kgo.AllowAutoTopicCreation())
	}
	cl, err := kgo.NewClient(kopts...)
	if err != nil {
		return nil, fmt.Errorf("events: kafka client: %w", err)
	}
	return &Publisher{client: cl, opts: opts, enabled: true}, nil
}

// Enabled reports whether events actually leave the process.
func (p *Publisher) Enabled() bool { return p != nil && p.enabled && p.client != nil }

// Emit publishes synchronously and returns the broker's answer. A disabled
// publisher returns nil. Use it where the caller wants to know (after a
// commit, before answering the client). For fire-and-forget use EmitAsync.
func (p *Publisher) Emit(ctx context.Context, ev Event) error {
	if !p.Enabled() {
		return nil
	}
	rec, err := p.record(ctx, ev)
	if err != nil {
		return err
	}
	pctx, cancel := context.WithTimeout(ctx, p.opts.ProduceTimeout+time.Second)
	defer cancel()
	if err := p.client.ProduceSync(pctx, rec).FirstErr(); err != nil {
		return fmt.Errorf("events: produce %s: %w", ev.Topic, err)
	}
	return nil
}

// EmitAsync publishes without waiting. Failures go to Logger and OnError.
// The request path never blocks; Close flushes what is still queued.
func (p *Publisher) EmitAsync(ctx context.Context, ev Event) {
	if !p.Enabled() {
		return
	}
	rec, err := p.record(ctx, ev)
	if err != nil {
		p.fail(ev.Topic, err)
		return
	}
	p.client.Produce(context.Background(), rec, func(r *kgo.Record, err error) {
		if err != nil {
			p.fail(r.Topic, err)
		}
	})
}

func (p *Publisher) fail(topic string, err error) {
	p.opts.Logger.Error("events: produce failed", "topic", topic, "error", err.Error())
	if p.opts.OnError != nil {
		p.opts.OnError(topic, err)
	}
}

// Close flushes queued records within ctx and closes the client. It fits
// shutdown.Closer.
func (p *Publisher) Close(ctx context.Context) error {
	if !p.Enabled() {
		return nil
	}
	err := p.client.Flush(ctx)
	p.client.Close()
	return err
}

func (p *Publisher) record(ctx context.Context, ev Event) (*kgo.Record, error) {
	if strings.TrimSpace(ev.Topic) == "" {
		return nil, errors.New("events: Topic is required")
	}
	if strings.TrimSpace(ev.Type) == "" {
		return nil, errors.New("events: Type is required")
	}
	body, key, err := Encode(ctx, p.opts.Producer, ev)
	if err != nil {
		return nil, err
	}
	return &kgo.Record{Topic: ev.Topic, Key: []byte(key), Value: body}, nil
}

// Encode renders the envelope bytes and the partition key without a broker.
// Exposed so services can unit-test their event shapes and so tests can
// assert the exact JSON.
func Encode(ctx context.Context, producer string, ev Event) ([]byte, string, error) {
	m := map[string]any{}
	if ev.Payload != nil {
		raw, err := json.Marshal(ev.Payload)
		if err != nil {
			return nil, "", fmt.Errorf("events: marshal payload: %w", err)
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, "", fmt.Errorf("events: payload must be a JSON object: %w", err)
		}
	}

	if c, ok := iam.ClaimsFrom(ctx); ok {
		if ev.OrganizationID == "" {
			ev.OrganizationID = c.OrganizationID
		}
		if ev.TenantID == "" {
			ev.TenantID = c.TenantID
		}
		if ev.UserID == "" {
			ev.UserID = c.ActorID()
		}
	}
	if ev.EventID == "" {
		ev.EventID = newID()
	}
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = time.Now()
	}

	m["eventId"] = ev.EventID
	m["type"] = ev.Type
	m["occurredAt"] = ev.OccurredAt.UTC().Format(time.RFC3339Nano)
	m["organizationId"] = nullable(ev.OrganizationID)
	m["tenantId"] = nullable(ev.TenantID)
	m["userId"] = nullable(ev.UserID)
	m["producer"] = producer
	if id := httpx.RequestIDFrom(ctx); id != "" {
		m["traceId"] = id
	}

	body, err := json.Marshal(m)
	if err != nil {
		return nil, "", err
	}
	key := ev.Key
	if key == "" {
		key = ev.OrganizationID
	}
	if key == "" {
		key = ev.EventID
	}
	return body, key, nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// newID returns a random RFC 4122 v4 UUID string without importing a uuid
// package.
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
