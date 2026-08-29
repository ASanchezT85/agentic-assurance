//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/evidence"
)

// outboxPool connects as the publisher role, which is a different role from the one
// every request handler uses.
func outboxPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_OUTBOX_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_OUTBOX_DSN is not set; the publisher role is not being exercised")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as the publisher role: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The cross-tenant read belongs to the publisher, not to the application.
//
// evidence_outbox had one policy, FOR ALL USING (true), granted to assurance_app. The
// reasoning was about the publisher — one process draining every tenant's queue — and
// the grant landed on the role every request handler connects as, so any read of
// evidence_outbox inside a request returned every tenant's rows.
//
// This asserts the split holds where it is enforced, which is PostgreSQL rather than the
// Go code: the application sees its own tenant, the publisher sees all of them.
func TestOnlyThePublisherRoleReadsAcrossTenants(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	mine := fmt.Sprintf("tenant_outbox_mine_%d", now.UnixNano())
	theirs := fmt.Sprintf("tenant_outbox_theirs_%d", now.UnixNano())

	app := evidence.NewStore(idemPool(t))

	for _, tenant := range []string{mine, theirs} {
		event := evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("outbox_role_%s", tenant),
			EventName:     evidence.IntentReceived,
			TenantID:      tenant,
			AggregateID:   "env_role",
			CorrelationID: "corr_role",
			OccurredAt:    now,
			ProducedAt:    now,
			Producer:      "assurance-gateway",
			Sequence:      1,
		}
		if err := app.AppendBatch(ctx, []evidence.Event{event}); err != nil {
			t.Fatalf("append for %s: %v", tenant, err)
		}
	}

	// Asked directly, by tenant, under each role.
	//
	// Not through Unpublished: that reads the head of the queue, and a queue with a
	// backlog answers "how deep is this tenant's row" rather than "may this role see
	// it". The property under test is what row level security permits, so the query is
	// the narrowest one that expresses it.
	visible := func(pool *pgxpool.Pool, role, tenant string) int {
		t.Helper()
		var count int
		err := pool.QueryRow(ctx,
			`SELECT count(*) FROM evidence_outbox WHERE tenant_id = $1`, tenant).Scan(&count)
		if err != nil {
			// A refusal is also a correct answer for the application role.
			t.Logf("%s reading %s: %v", role, tenant, err)
			return 0
		}
		return count
	}

	publisherPool := outboxPool(t)
	appPool := idemPool(t)

	if visible(publisherPool, "publisher", mine) == 0 ||
		visible(publisherPool, "publisher", theirs) == 0 {
		t.Errorf("the publisher cannot see both tenants' rows; it cannot drain a queue " +
			"it cannot read")
	}

	// The application sets no tenant here, and RLS answers with nothing rather than
	// with everything. That is the point: a handler that reads this table by mistake
	// gets an empty result, not another customer's evidence.
	if n := visible(appPool, "application", theirs); n != 0 {
		t.Errorf("the application role saw %d outbox rows for another tenant; the "+
			"cross-tenant exemption is still granted to it (migration 0025)", n)
	}
	if n := visible(appPool, "application", mine); n != 0 {
		t.Errorf("the application role saw %d outbox rows with no tenant set", n)
	}

}
