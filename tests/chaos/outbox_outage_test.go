//go:build chaos

package chaos

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"agentic-assurance/internal/evidence"
)

// F4-OUTBOX-06: the bus goes away and comes back, and nothing is lost.
//
// The outbox exists for exactly this: evidence commits to PostgreSQL whether or not NATS
// is reachable, and the queue in front of the bus is what the platform owes it. What has
// to hold is that an outage costs latency and only latency — every event committed while
// the bus was down reaches it afterwards, and the backlog clears in a bounded time rather
// than staying behind for the rest of the day.
func TestEvidenceSurvivesANATSOutage(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_outage_%d", now.UnixNano())

	app := chaosPool(t, "POSTGRES_APP_DSN",
		"postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable")
	outbox := chaosPool(t, "POSTGRES_OUTBOX_DSN",
		"postgres://assurance_outbox:assurance_outbox_dev_only@localhost:5432/assurance?sslmode=disable")
	store := evidence.NewStore(app)
	drainer := evidence.NewStore(outbox)

	// Evidence committed while the bus is down.
	stop(t, "nats")

	const events = 500
	batch := make([]evidence.Event, 0, events)
	for i := range events {
		at := now.Add(time.Duration(i) * time.Millisecond)
		batch = append(batch, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("outage_%d_%d", now.UnixNano(), i),
			EventName:     evidence.AuthorityEvaluated,
			TenantID:      tenant,
			AggregateID:   "env_outage",
			CorrelationID: "corr_outage",
			OccurredAt:    at,
			ProducedAt:    at,
			Producer:      "assurance-gateway",
			Sequence:      int64(i + 1),
		})
	}
	if err := store.AppendBatch(ctx, batch); err != nil {
		t.Fatalf("evidence could not be committed while the bus was down: %v.\n\n"+
			"The enforcement plane must not depend on the bus (INV-005); the outbox is "+
			"what keeps that true.", err)
	}

	depth, _, err := drainer.Depth(ctx, tenant)
	if err != nil {
		t.Fatalf("depth: %v", err)
	}
	if depth != events {
		t.Fatalf("%d of %d events are queued; what the platform owes the bus has to be "+
			"complete before it can be delivered", depth, events)
	}

	// The bus comes back. stop() restores the service on cleanup, so this test restores
	// it explicitly and lets the cleanup be a no-op.
	restart(t, "nats")

	conn, err := waitForNATS(t)
	if err != nil {
		t.Skipf("NATS did not come back: %v", err)
	}
	defer conn.Close()
	js, err := evidence.EnsureStream(ctx, conn)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}

	publisher := &evidence.OutboxPublisher{
		Store: drainer, Publisher: evidence.NewPublisher(js),
		Batch: 500, Owner: "chaos-outage",
	}

	catchUpStart := time.Now()
	deadline := time.Now().Add(60 * time.Second)
	var remaining int64
	for time.Now().Before(deadline) {
		publisher.Drain(ctx)
		remaining, _, err = drainer.Depth(ctx, tenant)
		if err != nil {
			t.Fatalf("depth: %v", err)
		}
		if remaining == 0 {
			break
		}
	}
	catchUp := time.Since(catchUpStart)
	t.Logf("%d events queued during the outage; caught up in %s",
		events, catchUp.Round(time.Millisecond))

	if remaining != 0 {
		t.Errorf("%d events are still queued a minute after the bus returned. An outage "+
			"must cost latency and only latency.", remaining)
	}
	if catchUp > 30*time.Second {
		t.Errorf("the backlog took %s to clear; the analytical plane was that far behind "+
			"the period an incident review reads", catchUp.Round(time.Second))
	}

	// And PostgreSQL, which is the record, never lost anything.
	if _, err := app.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var stored int
	if err := app.QueryRow(ctx,
		`SELECT count(*) FROM evidence_events WHERE tenant_id = $1`, tenant).Scan(&stored); err != nil {
		t.Fatalf("count: %v", err)
	}
	if stored != events {
		t.Errorf("%d of %d events are in the evidence store", stored, events)
	}
}

func chaosPool(t *testing.T, envName, fallback string) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv(envName)
	if dsn == "" {
		dsn = fallback
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no database for %s: %v", envName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no database for %s: %v", envName, err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func waitForNATS(t *testing.T) (*nats.Conn, error) {
	t.Helper()
	url := os.Getenv("NATS_URL")
	if url == "" {
		url = "nats://localhost:4222"
	}
	var last error
	for range 30 {
		conn, err := nats.Connect(url)
		if err == nil {
			return conn, nil
		}
		last = err
		time.Sleep(time.Second)
	}
	return nil, last
}

// restart brings a stopped service back inside the test rather than at cleanup.
func restart(t *testing.T, service string) {
	t.Helper()
	cmd := exec.Command("docker", "compose", "start", service)
	cmd.Dir = "../.."
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start %s: %v\n%s", service, err, out)
	}
}
