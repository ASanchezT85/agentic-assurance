package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentic-assurance/internal/evidence"
)

// The consumer end of the event backbone.
//
// The gateway commits evidence to PostgreSQL and hands it to JetStream through an
// outbox. This is what reads the other side: a durable consumer that projects the
// stream into ClickHouse so a question about a tenant's decision history over months
// does not become a scan of the enforcement plane's own table.
//
// It is a projection, never a source. PostgreSQL holds the evidence; if the two ever
// disagree, PostgreSQL is right and this is rebuilt. Nothing here is read to authorize
// anything, which is what keeps it on the intelligence side of INV-009.

// EvidenceBatchSink is the analytical store, as this file needs it.
//
// An interface rather than *Sink so a test can make an insert fail on demand. Delivery
// semantics — what is acknowledged, and when — cannot be tested against a store that
// always succeeds, and the alternative is mocking JetStream itself, which would test the
// mock.
type EvidenceBatchSink interface {
	InsertEvidence(ctx context.Context, rows ...string) error
}

// EvidenceProjection writes consumed events into the analytical store.
type EvidenceProjection struct {
	Sink EvidenceBatchSink
	Log  *slog.Logger
}

func (p *EvidenceProjection) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Handle is the consumer callback.
//
// At-least-once delivery means this runs twice for the same event sooner or later. The
// table deduplicates by event id, so a redelivery overwrites itself rather than
// counting twice — a consumer that could not tolerate a duplicate would be the defect,
// not the delivery (ADR-008).
func (p *EvidenceProjection) Handle(ctx context.Context, e evidence.Event) error {
	if p.Sink == nil {
		return nil
	}
	return p.Sink.InsertEvidence(ctx, projectionRow(e))
}

// HandleBatch inserts a whole fetch, and is what decides whether it may be acknowledged.
func (p *EvidenceProjection) HandleBatch(ctx context.Context, events []evidence.Event) error {
	if p.Sink == nil || len(events) == 0 {
		return nil
	}
	rows := make([]string, 0, len(events))
	for _, e := range events {
		rows = append(rows, projectionRow(e))
	}
	return p.Sink.InsertEvidence(ctx, rows...)
}

// projectionRow renders one event for the analytical store.
func projectionRow(e evidence.Event) string {
	return fmt.Sprintf(
		`{"tenant_id":%q,"event_id":%q,"event_name":%q,"aggregate_id":%q,`+
			`"correlation_id":%q,"causation_id":%q,"producer":%q,"sequence":%d,`+
			`"occurred_at":%q}`,
		e.TenantID, e.EventID, string(e.EventName), e.AggregateID,
		e.CorrelationID, e.CausationID, e.Producer, e.Sequence,
		e.OccurredAt.UTC().Format("2006-01-02 15:04:05.000"))
}

// RunEvidenceConsumer drains the stream until the context ends.
//
// Failure here never stops anything upstream: an unreachable bus or a full analytical
// store leaves the events in PostgreSQL and in the outbox, where they are still the
// record. The projection catches up.
func RunEvidenceConsumer(ctx context.Context, consumer *evidence.Consumer,
	projection *EvidenceProjection, log *slog.Logger) {

	if consumer == nil || projection == nil {
		return
	}
	if log == nil {
		log = slog.Default()
	}

	// Terminated messages are surfaced. Dropping one is a decision to lose an event, and
	// a decision to lose an event that nothing says out loud looks exactly like a bug in
	// the projection months later.
	consumer.Report = func(poison int) {
		log.Error("evidence messages discarded as unparseable",
			"count", poison,
			"consequence", "those events will never reach the analytical copy; "+
				"PostgreSQL still holds them and the projection can be rebuilt")
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// One insert per fetch, and the acknowledgement comes after it.
		//
		// It used to append rows to a slice inside a per-event handler that returned
		// nil, so Fetch acknowledged every message, and the insert ran afterwards. A
		// failed insert had nothing left to redeliver: the events stayed in PostgreSQL,
		// which is the record, and never reached the analytical plane, which is what an
		// incident review reads. An acknowledgement is a promise that the side effect it
		// stands for has happened.
		//
		// A failed batch is redelivered whole. The table deduplicates by event id, so a
		// retry overwrites itself rather than counting twice (ADR-008).
		count, err := consumer.FetchBatch(ctx, 500, 2*time.Second, projection.HandleBatch)
		if err != nil && ctx.Err() == nil {
			log.Warn("evidence projection paused", "err", err,
				"consequence", "the analytical copy is behind and the batch stays "+
					"unacknowledged; PostgreSQL still holds the evidence")
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		if count == 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
	}
}
