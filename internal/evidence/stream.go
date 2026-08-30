package evidence

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// StreamName is the JetStream stream every evidence event lands in.
const StreamName = "EVIDENCE"

// SubjectPattern captures every tenant and every event name.
const SubjectPattern = "evidence.>"

// Publisher writes events to JetStream.
//
// Publishing is asynchronous by design and never on the hot path: INV-005 requires
// enforcement to survive NATS being gone, so a failure to publish must never fail a
// decision. The caller records evidence in PostgreSQL first and publishes second.
type Publisher struct {
	js jetstream.JetStream
}

// EnsureStream creates the stream if it does not exist.
//
// The retention policy is limits-based with a long max age rather than work-queue:
// evidence is read many times by many consumers, and a queue that deletes on
// acknowledgement would make the second reader see nothing.
func EnsureStream(ctx context.Context, nc *nats.Conn) (jetstream.JetStream, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{SubjectPattern},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    90 * 24 * time.Hour,
		Discard:   jetstream.DiscardOld,
	})
	if err != nil {
		return nil, fmt.Errorf("create stream: %w", err)
	}
	return js, nil
}

func NewPublisher(js jetstream.JetStream) *Publisher { return &Publisher{js: js} }

// Publish sends one event.
//
// The event id is used as the JetStream message id so the server deduplicates
// redeliveries within its window. That is a convenience, not the guarantee:
// ADR-008 makes consumers responsible for idempotency, and the Store's Append is
// idempotent by event id regardless of what the broker does.
func (p *Publisher) Publish(ctx context.Context, e Event) error {
	if err := e.Validate(); err != nil {
		return err
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}
	_, err = p.js.Publish(ctx, e.Subject(), raw, jetstream.WithMsgID(e.EventID))
	if err != nil {
		return fmt.Errorf("publish %s: %w", e.EventID, err)
	}
	return nil
}

// Handler processes one event. Returning an error causes redelivery, which is why
// a handler must be idempotent (ADR-008).
type Handler func(ctx context.Context, e Event) error

// Consumer reads events from JetStream.
type Consumer struct {
	cons jetstream.Consumer

	// Report is called with the number of messages terminated as unparseable in a
	// batch. Terminating one is a decision to lose it, and a decision to lose an event
	// that nothing surfaces is indistinguishable from a bug.
	Report func(poison int)
}

// NewConsumer creates or reuses a durable consumer.
//
// Durable and explicit-ack: an ephemeral consumer would silently skip everything
// produced while it was down, which for an audit trail is data loss that nothing
// reports.
func NewConsumer(ctx context.Context, js jetstream.JetStream, durable string, filter string) (*Consumer, error) {
	if filter == "" {
		filter = SubjectPattern
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       durable,
		AckPolicy:     jetstream.AckExplicitPolicy,
		DeliverPolicy: jetstream.DeliverAllPolicy,
		FilterSubject: filter,
		MaxDeliver:    -1,
	})
	if err != nil {
		return nil, fmt.Errorf("create consumer %s: %w", durable, err)
	}
	return &Consumer{cons: cons}, nil
}

// Fetch pulls up to n events and hands each to the handler.
//
// A handler error leaves the message unacknowledged, so JetStream redelivers it.
// That redelivery is the normal case ADR-008 describes, not an exception, which is
// why every handler in this codebase is written to tolerate seeing an event twice.
func (c *Consumer) Fetch(ctx context.Context, n int, wait time.Duration, h Handler) (int, error) {
	batch, err := c.cons.Fetch(n, jetstream.FetchMaxWait(wait))
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	processed := 0
	for msg := range batch.Messages() {
		var e Event
		if err := json.Unmarshal(msg.Data(), &e); err != nil {
			// An unparseable message will never become parseable. Redelivering it
			// forever would block the consumer, so it is terminated and the error
			// surfaces to operations rather than to the audit trail.
			_ = msg.Term()
			continue
		}
		if err := h(ctx, e); err != nil {
			_ = msg.Nak()
			continue
		}
		if err := msg.Ack(); err != nil {
			return processed, fmt.Errorf("ack %s: %w", e.EventID, err)
		}
		processed++
	}
	return processed, batch.Error()
}

// BatchHandler receives a whole fetch at once.
type BatchHandler func(ctx context.Context, events []Event) error

// FetchBatch pulls up to n events, hands the whole batch to the handler, and acknowledges
// only if the handler succeeded.
//
// Fetch acknowledges each message as its per-event handler returns. That is right for a
// handler that does the durable work itself, and wrong for the projection, whose handler
// appended to a slice and returned nil while the ClickHouse insert happened after the
// fetch had already acknowledged everything. A failed insert then had nothing left to
// redeliver: the events stayed in PostgreSQL, which is the record, and never reached the
// analytical plane, which is what an incident review reads.
//
// An acknowledgement is a promise that the side effect it stands for has happened. This
// makes the order match the promise.
func (c *Consumer) FetchBatch(ctx context.Context, n int, wait time.Duration,
	h BatchHandler) (int, error) {

	batch, err := c.cons.Fetch(n, jetstream.FetchMaxWait(wait))
	if err != nil {
		return 0, fmt.Errorf("fetch: %w", err)
	}

	var (
		events   []Event
		messages []jetstream.Msg
		poison   int
	)
	for msg := range batch.Messages() {
		var e Event
		if err := json.Unmarshal(msg.Data(), &e); err != nil {
			// An unparseable message will never become parseable, so redelivering it
			// forever would block every event behind it. Terminated — and counted, so
			// the caller can say so out loud rather than discovering a silent hole in
			// the analytical copy months later.
			_ = msg.Term()
			poison++
			continue
		}
		events = append(events, e)
		messages = append(messages, msg)
	}
	if err := batch.Error(); err != nil && len(events) == 0 {
		return 0, err
	}
	if poison > 0 && c.Report != nil {
		c.Report(poison)
	}
	if len(events) == 0 {
		return 0, batch.Error()
	}

	if err := h(ctx, events); err != nil {
		// Nothing is acknowledged. JetStream redelivers the whole batch, and the
		// projection deduplicates by event id, so a retry overwrites itself rather than
		// counting twice (ADR-008).
		for _, msg := range messages {
			_ = msg.Nak()
		}
		return 0, err
	}

	acked := 0
	for _, msg := range messages {
		if err := msg.Ack(); err != nil {
			return acked, fmt.Errorf("ack: %w", err)
		}
		acked++
	}
	return acked, batch.Error()
}

// StoreHandler returns a Handler that records events into the append-only store.
//
// It is the reference idempotent consumer: Append deduplicates by event id, so a
// redelivered event is recorded once and acknowledged again.
func StoreHandler(store *Store) Handler {
	return func(ctx context.Context, e Event) error {
		_, err := store.Append(ctx, e)
		return err
	}
}
