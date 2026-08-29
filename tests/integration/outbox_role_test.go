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
	publisher := evidence.NewStore(outboxPool(t))

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

	// The publisher sees both, because draining every tenant's queue is its job.
	queued, err := publisher.Unpublished(ctx, 500)
	if err != nil {
		t.Fatalf("the publisher role cannot read the outbox: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range queued {
		seen[e.TenantID] = true
	}
	if !seen[mine] || !seen[theirs] {
		t.Fatalf("the publisher did not see both tenants (mine=%v theirs=%v); it cannot "+
			"drain a queue it cannot read", seen[mine], seen[theirs])
	}

	// The application sees neither, because Unpublished sets no tenant and RLS answers
	// with nothing rather than with everything. That is the point: a handler that reads
	// this table by mistake gets an empty result, not another customer's evidence.
	leaked, err := app.Unpublished(ctx, 500)
	if err != nil {
		// A refusal is also a correct answer here.
		t.Logf("the application role was refused outright: %v", err)
		return
	}
	for _, e := range leaked {
		if e.TenantID == mine || e.TenantID == theirs {
			t.Errorf("the application role read outbox rows for %s; the cross-tenant "+
				"exemption is still granted to it (migration 0025)", e.TenantID)
		}
	}
}
