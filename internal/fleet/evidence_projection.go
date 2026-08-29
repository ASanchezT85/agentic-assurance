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

// EvidenceProjection writes consumed events into the analytical store.
type EvidenceProjection struct {
	Sink *Sink
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

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Batched into one insert per fetch rather than one per event.
		//
		// It was one HTTP round trip per event, so the projection's throughput was
		// bounded by the round-trip time to ClickHouse — a few hundred a second on this
		// host — while the outbox now delivers well over a thousand. The queue in front
		// of it drained and the consumer behind it became the bottleneck, which is the
		// same defect one hop further along: the analytical plane lagged a busy period
		// by the time an incident review would want to read it.
		//
		// A failed batch is retried by redelivery. The table deduplicates by event id,
		// so a redelivered batch overwrites itself rather than counting twice (ADR-008).
		batch := make([]string, 0, 500)
		count, err := consumer.Fetch(ctx, 500, 2*time.Second,
			func(_ context.Context, e evidence.Event) error {
				batch = append(batch, projectionRow(e))
				return nil
			})
		if len(batch) > 0 && projection.Sink != nil {
			if insertErr := projection.Sink.InsertEvidence(ctx, batch...); insertErr != nil {
				log.Warn("evidence projection paused", "err", insertErr, "batch", len(batch),
					"consequence", "the analytical copy is behind; PostgreSQL still holds the evidence")
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				continue
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Warn("evidence projection paused", "err", err,
				"consequence", "the analytical copy is behind; PostgreSQL still holds the evidence")
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
