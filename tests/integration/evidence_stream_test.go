//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"agentic-assurance/internal/evidence"
)

// The Phase 6 exit criteria, against the real backbone:
//
//  1. at-least-once duplication does not corrupt state
//  2. the evidence timeline reconstructs order flow
//
// Both need real infrastructure. A test against an in-memory queue would prove that
// the code copes with a duplicate it was handed, not that it copes with the ones
// JetStream actually delivers.
//
// Run with:  make up && make migrate && make test-integration

const streamTenant = "tenant_stream"

func natsURL() string {
	if u := os.Getenv("NATS_URL"); u != "" {
		return u
	}
	return "nats://localhost:4222"
}

func evidencePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		dsn = "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("postgres: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("postgres ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func connectNATS(t *testing.T) *nats.Conn {
	t.Helper()
	nc, err := nats.Connect(natsURL(), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("nats (is `make up` running?): %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func runID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func orderFlowEvents(correlationID string) []evidence.Event {
	base := time.Now().UTC()
	names := []evidence.EventName{
		evidence.IntentReceived,
		evidence.IdentityVerified,
		evidence.AuthorityEvaluated,
		evidence.PolicyEvaluated,
		evidence.OrderSubmitted,
		evidence.OrderAccepted,
		evidence.OrderFilled,
	}

	events := make([]evidence.Event, 0, len(names))
	var previous string
	for i, name := range names {
		id := fmt.Sprintf("%s_ev_%d", correlationID, i)
		events = append(events, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       id,
			EventName:     name,
			TenantID:      streamTenant,
			AggregateID:   "env_" + correlationID,
			CorrelationID: correlationID,
			CausationID:   previous,
			OccurredAt:    base.Add(time.Duration(i) * 10 * time.Millisecond),
			ProducedAt:    base.Add(time.Duration(i) * 10 * time.Millisecond),
			Producer:      "assurance-gateway",
			Sequence:      int64(i),
			Payload:       map[string]any{"step": i},
		})
		previous = id
	}
	return events
}

// Exit criterion 2: the evidence timeline reconstructs order flow.
func TestTimelineReconstructsTheOrderFlow(t *testing.T) {
	store := evidence.NewStore(evidencePool(t))
	ctx := context.Background()
	correlationID := runID("corr_flow")

	for _, e := range orderFlowEvents(correlationID) {
		if _, err := store.Append(ctx, e); err != nil {
			t.Fatalf("append %s: %v", e.EventID, err)
		}
	}

	chain, err := store.Chain(ctx, streamTenant, correlationID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}

	want := []evidence.EventName{
		evidence.IntentReceived, evidence.IdentityVerified, evidence.AuthorityEvaluated,
		evidence.PolicyEvaluated, evidence.OrderSubmitted, evidence.OrderAccepted,
		evidence.OrderFilled,
	}
	if len(chain) != len(want) {
		t.Fatalf("chain has %d events, want %d", len(chain), len(want))
	}
	for i, name := range want {
		if chain[i].EventName != name {
			t.Errorf("position %d is %s, want %s", i, chain[i].EventName, name)
		}
	}

	// The chain is walkable by causation: every event but the first names its
	// predecessor, which is what makes "what led to this" answerable.
	for i := 1; i < len(chain); i++ {
		if chain[i].CausationID != chain[i-1].EventID {
			t.Errorf("event %d does not name its predecessor: causation=%q, previous=%q",
				i, chain[i].CausationID, chain[i-1].EventID)
		}
	}
}

// Exit criterion 1, at the store: redelivering every event changes nothing.
func TestRedeliveryDoesNotDuplicateEvidence(t *testing.T) {
	store := evidence.NewStore(evidencePool(t))
	ctx := context.Background()
	correlationID := runID("corr_redeliver")
	events := orderFlowEvents(correlationID)

	for _, e := range events {
		recorded, err := store.Append(ctx, e)
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if !recorded {
			t.Fatalf("%s was reported as already present on first delivery", e.EventID)
		}
	}

	// Three more deliveries of everything, as at-least-once produces.
	for round := 0; round < 3; round++ {
		for _, e := range events {
			recorded, err := store.Append(ctx, e)
			if err != nil {
				t.Fatalf("redelivery: %v", err)
			}
			if recorded {
				t.Errorf("round %d recorded %s a second time (ADR-008)", round, e.EventID)
			}
		}
	}

	chain, err := store.Chain(ctx, streamTenant, correlationID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != len(events) {
		t.Fatalf("the timeline holds %d events after four deliveries of %d; "+
			"at-least-once duplication corrupted it (ADR-008)", len(chain), len(events))
	}
}

// The same criterion end to end: publish through JetStream, consume twice, and
// confirm the timeline is unchanged.
func TestJetStreamRoundTripIsIdempotent(t *testing.T) {
	nc := connectNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	js, err := evidence.EnsureStream(ctx, nc)
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	store := evidence.NewStore(evidencePool(t))
	correlationID := runID("corr_js")
	events := orderFlowEvents(correlationID)

	pub := evidence.NewPublisher(js)
	for _, e := range events {
		if err := pub.Publish(ctx, e); err != nil {
			t.Fatalf("publish %s: %v", e.EventID, err)
		}
	}

	// A durable consumer per run, filtered to this tenant.
	durable := "recorder_" + fmt.Sprint(time.Now().UnixNano())
	cons, err := evidence.NewConsumer(ctx, js, durable, "evidence."+streamTenant+".>")
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}

	handler := evidence.StoreHandler(store)
	deadline := time.Now().Add(30 * time.Second)
	seen := 0
	for seen < len(events) && time.Now().Before(deadline) {
		n, err := cons.Fetch(ctx, len(events), 2*time.Second, handler)
		if err != nil {
			t.Fatalf("fetch: %v", err)
		}
		seen += n
	}
	if seen < len(events) {
		t.Fatalf("consumed %d of %d events before the deadline", seen, len(events))
	}

	// Replay the same events straight into the handler, as a redelivery would.
	for _, e := range events {
		if err := handler(ctx, e); err != nil {
			t.Fatalf("replay: %v", err)
		}
	}

	chain, err := store.Chain(ctx, streamTenant, correlationID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != len(events) {
		t.Fatalf("the timeline holds %d events after a round trip and a replay of %d "+
			"(ADR-008)", len(chain), len(events))
	}
}

// JetStream must be configured so a second consumer sees the same history. A
// work-queue stream would delete on acknowledgement and the second reader would find
// nothing, which for an audit backbone is silent data loss.
func TestASecondConsumerSeesTheSameHistory(t *testing.T) {
	nc := connectNATS(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	js, err := evidence.EnsureStream(ctx, nc)
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	correlationID := runID("corr_twoconsumers")
	events := orderFlowEvents(correlationID)

	pub := evidence.NewPublisher(js)
	for _, e := range events {
		if err := pub.Publish(ctx, e); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	count := func(durable string) int {
		cons, err := evidence.NewConsumer(ctx, js, durable, "evidence."+streamTenant+".>")
		if err != nil {
			t.Fatalf("consumer %s: %v", durable, err)
		}
		got := 0
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			n, err := cons.Fetch(ctx, 64, 2*time.Second, func(context.Context, evidence.Event) error {
				return nil
			})
			if err != nil {
				break
			}
			if n == 0 {
				break
			}
			got += n
		}
		return got
	}

	stamp := time.Now().UnixNano()
	first := count(fmt.Sprintf("reader_a_%d", stamp))
	second := count(fmt.Sprintf("reader_b_%d", stamp))

	if first < len(events) {
		t.Fatalf("the first consumer saw %d events, want at least %d", first, len(events))
	}
	if second < len(events) {
		t.Errorf("the second consumer saw %d events, want at least %d; the stream must "+
			"not delete on acknowledgement", second, len(events))
	}
}
