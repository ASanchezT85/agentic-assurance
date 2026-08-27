//go:build integration

package security

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/evidence"
)

// INV-006, the database half.
//
// The append-only guarantee is a privilege and a trigger, not a convention in the Go
// code. Both are only real if a real PostgreSQL refuses the write, so this file asks
// it to.
//
// Run with:  make up && make migrate && make test-integration

const evTenant = "tenant_evidence"

func evPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		dsn = "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect (is `make up && make migrate` done?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func uniqueEventID(prefix string) string {
	return prefix + "_" + time.Now().UTC().Format("20060102150405.000000000")
}

func seedEvent(t *testing.T, store *evidence.Store, id, correlationID string, seq int64) evidence.Event {
	t.Helper()
	now := time.Now().UTC()
	e := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       id,
		EventName:     evidence.PolicyEvaluated,
		TenantID:      evTenant,
		AggregateID:   "env_" + correlationID,
		CorrelationID: correlationID,
		OccurredAt:    now.Add(time.Duration(seq) * time.Millisecond),
		ProducedAt:    now,
		Producer:      "assurance-gateway",
		Sequence:      seq,
		Payload:       map[string]any{"action": "ALLOW"},
	}
	recorded, err := store.Append(context.Background(), e)
	if err != nil {
		t.Fatalf("append %s: %v", id, err)
	}
	if !recorded {
		t.Fatalf("append %s reported the event was already present", id)
	}
	return e
}

// The application role must not be able to change a recorded event, however it asks.
func TestDatabaseRefusesToUpdateEvidence(t *testing.T) {
	pool := evPool(t)
	store := evidence.NewStore(pool)
	ctx := context.Background()

	id := uniqueEventID("ev_immutable")
	seedEvent(t, store, id, "corr_immutable", 1)

	// Straight to SQL, bypassing every guard in the Go code. This is the attempt
	// the invariant is actually about: application code can be changed.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", evTenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE evidence_events SET producer = 'someone-else' WHERE event_id = $1`, id); err == nil {
		t.Fatal("PostgreSQL allowed an UPDATE against evidence_events (INV-006)")
	}
}

func TestDatabaseRefusesToDeleteEvidence(t *testing.T) {
	pool := evPool(t)
	store := evidence.NewStore(pool)
	ctx := context.Background()

	id := uniqueEventID("ev_undeletable")
	seedEvent(t, store, id, "corr_undeletable", 1)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", evTenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM evidence_events WHERE event_id = $1`, id); err == nil {
		t.Fatal("PostgreSQL allowed a DELETE against evidence_events (INV-006)")
	}
}

// The privilege is absent, not merely unused. If a future migration grants UPDATE,
// this fails even though the trigger would still block the statement.
func TestApplicationRoleHoldsOnlySelectAndInsert(t *testing.T) {
	pool := evPool(t)
	ctx := context.Background()

	rows, err := pool.Query(ctx, `
		SELECT privilege_type
		  FROM information_schema.role_table_grants
		 WHERE table_name = 'evidence_events' AND grantee = current_user`)
	if err != nil {
		t.Fatalf("read grants: %v", err)
	}
	defer rows.Close()

	held := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatalf("scan: %v", err)
		}
		held[p] = true
	}

	for _, forbidden := range []string{"UPDATE", "DELETE", "TRUNCATE"} {
		if held[forbidden] {
			t.Errorf("the application role holds %s on evidence_events; the privilege must "+
				"be absent, not merely unused (INV-006)", forbidden)
		}
	}
	if !held["INSERT"] || !held["SELECT"] {
		t.Error("the application role cannot record or read evidence")
	}
}

// A correction adds a row and leaves the original exactly as it was.
func TestCorrectionLeavesTheOriginalIntact(t *testing.T) {
	store := evidence.NewStore(evPool(t))
	ctx := context.Background()

	correlationID := "corr_corrected_" + time.Now().UTC().Format("150405.000000000")
	originalID := uniqueEventID("ev_original")
	original := seedEvent(t, store, originalID, correlationID, 1)

	correction := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       uniqueEventID("ev_correction"),
		TenantID:      evTenant,
		OccurredAt:    time.Now().UTC().Add(time.Second),
		ProducedAt:    time.Now().UTC(),
		Producer:      "operator",
		Sequence:      2,
		Payload:       map[string]any{"reason": "policy bundle was misattributed"},
	}
	if err := store.Correct(ctx, correction, originalID); err != nil {
		t.Fatalf("correct: %v", err)
	}

	chain, err := store.Chain(ctx, evTenant, correlationID)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain has %d events, want the original plus the correction", len(chain))
	}

	if chain[0].EventID != original.EventID || chain[0].Producer != original.Producer {
		t.Errorf("the original event changed: %+v", chain[0])
	}
	if chain[0].Payload["action"] != "ALLOW" {
		t.Errorf("the original payload changed: %v", chain[0].Payload)
	}
	if chain[1].EventName != evidence.EvidenceCorrected {
		t.Errorf("the correction is not recorded as one: %s", chain[1].EventName)
	}
	if chain[1].CorrectsEventID != originalID {
		t.Errorf("the correction does not point at the original: %q", chain[1].CorrectsEventID)
	}
}

// A correction pointing at an event that does not exist is refused: it would leave a
// dangling claim that something was wrong, with nothing to compare against.
func TestCorrectionOfAMissingEventIsRefused(t *testing.T) {
	store := evidence.NewStore(evPool(t))

	correction := evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       uniqueEventID("ev_dangling"),
		TenantID:      evTenant,
		CorrelationID: "corr_dangling",
		AggregateID:   "env_dangling",
		OccurredAt:    time.Now().UTC(),
		ProducedAt:    time.Now().UTC(),
		Producer:      "operator",
	}
	if err := store.Correct(context.Background(), correction, "ev_never_existed"); err == nil {
		t.Fatal("a correction of a nonexistent event was recorded (INV-006)")
	}
}

// Evidence is tenant-scoped like everything else.
func TestEvidenceIsTenantScoped(t *testing.T) {
	store := evidence.NewStore(evPool(t))
	ctx := context.Background()

	correlationID := "corr_tenants_" + time.Now().UTC().Format("150405.000000000")
	seedEvent(t, store, uniqueEventID("ev_scoped"), correlationID, 1)

	mine, err := store.Chain(ctx, evTenant, correlationID)
	if err != nil || len(mine) != 1 {
		t.Fatalf("the owning tenant could not read its own evidence: %v (%d events)", err, len(mine))
	}

	theirs, err := store.Chain(ctx, "tenant_someone_else", correlationID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(theirs) != 0 {
		t.Errorf("another tenant read %d evidence events (INV-007)", len(theirs))
	}
}
