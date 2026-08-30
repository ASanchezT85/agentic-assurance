//go:build integration

package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
)

// F4-B001: a pruned request must not reach the venue a second time.
//
// ADR-027 says an idempotency key identifies one economic request, permanently. The
// fourth audit found that the platform only enforced the half of that which authority
// could see: PostgresUsage.Reserve refuses a key whose *identity* differs — a different
// envelope, grant, principal or amount — and allows the identical request through as a
// retry, which is correct while the execution record exists and is how a retry keeps the
// capacity it already holds.
//
// After retention prunes the resolved record the two halves disagree. Authority says
// "this is the same request, it may proceed"; the execution store has no record, so
// nothing replays and nothing refuses; and the venue is called again. The platform
// submits twice for one economic request, which is INV-004 stated exactly.
//
// The existing test for this (TestAPrunedRecordDoesNotReopenItsKey) changes the envelope
// and the notional, so it exercises the identity mismatch and never reaches the case the
// audit found.
//
// These drive the whole path — HTTP in, PostgreSQL throughout, a venue at the other end —
// and assert on fakebroker.Submissions rather than on order count. A venue that
// deduplicates client order ids would return one logical order for two submissions; what
// the invariant forbids is the platform sending the second one.

// prune runs retention over this tenant, the way the sweeper does, and proves the record
// is gone.
//
// Through Prune rather than a DELETE, deliberately: what is under test is the contract
// retention leaves behind, and a test that deletes the row itself would be testing a path
// production never takes.
func prune(t *testing.T, rig *e2eRig, key string) {
	t.Helper()
	ctx := context.Background()
	store := execution.NewPostgresStore(rig.pool)

	// A cutoff after every record this rig wrote. The rig freezes its clock, so "older
	// than thirty days" is not a thing that happens during a test.
	deleted, err := store.Prune(ctx, rig.tenant, time.Now().UTC().Add(24*time.Hour))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if deleted == 0 {
		t.Fatal("retention deleted nothing; this test is not exercising what it claims")
	}
	if _, err := store.Load(ctx, rig.tenant, key); err == nil {
		t.Fatal("the record survived the prune; this test is not exercising what it claims")
	}
}

// F4-IDEMPOTENCY-01: the exact same economic request, after the prune.
func TestAPrunedRequestCannotReachTheVenueAgain(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	key := fmt.Sprintf("f4-perm-%d", time.Now().UnixNano())
	coid := "coid-" + key
	body := rig.envelope(now, key, nil)

	if status, decoded := rig.post(t, body, true); status != 200 && status != 202 {
		t.Fatalf("the first submission was refused with %d: %v", status, decoded)
	}
	if got := rig.broker.Submissions(coid); got != 1 {
		t.Fatalf("the first submission reached the venue %d times", got)
	}

	prune(t, rig, key)

	// Byte for byte the same signed request: same key, same envelope, same grant, same
	// principal, same account, same notional.
	status, decoded := rig.post(t, body, true)
	t.Logf("after the prune: %d %v", status, decoded["code"])

	if got := rig.broker.Submissions(coid); got != 1 {
		t.Fatalf("the venue received %d submissions for one economic request. A key the "+
			"platform says is retired caused another venue submission: retention bounds "+
			"storage, and it must not bound what the platform remembers about a key "+
			"(INV-004, ADR-027).", got)
	}
	if status == 200 || status == 202 {
		t.Errorf("the resubmission was accepted with %d. There is no outcome left to "+
			"replay — it was deliberately pruned — so the honest answer is a refusal, "+
			"not an acceptance that implies an order was placed.", status)
	}
	if code, _ := decoded["code"].(string); code != "IDEMPOTENCY_KEY_RETIRED" {
		t.Errorf("refused with %q; a caller needs to know the key is spent rather than "+
			"that something transient failed", code)
	}
}

// F4-IDEMPOTENCY-02: the same key after the prune, carrying a different envelope.
func TestAPrunedKeyIsRetiredForADifferentEnvelope(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	key := fmt.Sprintf("f4-perm2-%d", time.Now().UnixNano())
	coid := "coid-" + key

	if status, decoded := rig.post(t, rig.envelope(now, key, nil), true); status != 200 && status != 202 {
		t.Fatalf("the first submission was refused with %d: %v", status, decoded)
	}
	prune(t, rig, key)

	// Same key, different envelope id. Two intentions under one key.
	other := rig.envelope(now, key, func(m map[string]any) {
		m["envelope_id"] = "env_other_" + key
	})
	status, decoded := rig.post(t, other, true)

	if got := rig.broker.Submissions(coid); got != 1 {
		t.Errorf("the venue received %d submissions", got)
	}
	if status == 200 || status == 202 {
		t.Fatalf("a spent key carried a different envelope to the venue: %d %v", status, decoded)
	}
	t.Logf("refused: %d %v", status, decoded["code"])
}

// F4-IDEMPOTENCY-03: the same envelope after the prune, under a new key.
//
// The unique envelope index lives on idempotency_records, so pruning the record deletes
// the constraint that made an envelope id identify one intent (§12.2).
func TestAPrunedEnvelopeCannotExecuteUnderANewKey(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	key := fmt.Sprintf("f4-env-%d", time.Now().UnixNano())
	envelopeID := "env_" + key

	if status, decoded := rig.post(t, rig.envelope(now, key, nil), true); status != 200 && status != 202 {
		t.Fatalf("the first submission was refused with %d: %v", status, decoded)
	}
	prune(t, rig, key)

	fresh := key + "-fresh"
	reused := rig.envelope(now, fresh, func(m map[string]any) {
		m["envelope_id"] = envelopeID
	})
	status, decoded := rig.post(t, reused, true)

	if got := rig.broker.Submissions("coid-" + fresh); got != 0 {
		t.Fatalf("an envelope that had already been submitted reached the venue again "+
			"under a new key (%d submissions). Pruning the record deleted the unique "+
			"envelope index, so §12.2 held only as long as retention had not run.", got)
	}
	if status == 200 || status == 202 {
		t.Errorf("the reused envelope was accepted with %d: %v", status, decoded)
	}
	if code, _ := decoded["code"].(string); code != "ENVELOPE_REUSED" {
		t.Errorf("refused with %q; the caller reused an envelope id and the refusal "+
			"should say so", code)
	}
}

// F4-IDEMPOTENCY-04: a quantity-sized MARKET order, whose permanence cannot rest on a
// monetary reservation.
//
// authority_usage was proposed as the universal tombstone. It cannot be: an order sized
// in shares at market has no notional the platform can determine before the venue prices
// it, so the row that would remember the key is the row that does not exist. Permanence
// has to live in the idempotency domain.
func TestPermanenceDoesNotDependOnAMonetaryReservation(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	store := execution.NewPostgresStore(pool)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_f4mkt_%d", now.UnixNano())
	key := fmt.Sprintf("f4-mkt-%d", now.UnixNano())
	envelopeID := "env_" + key

	// Claimed, resolved and pruned with no authority row anywhere. This is the shape of a
	// quantity-sized market order under a grant that caps orders rather than money: the
	// platform cannot price it before the venue does, so there is no reservation to
	// remember it by.
	if _, claimed, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: key, EnvelopeID: envelopeID,
		ClientOrderID: "coid-" + key, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if err := store.Resolve(ctx, tenant, key, execution.Outcome{
		State: broker.StateFilled, BrokerOrderID: "b_" + key,
	}, now); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var reservations int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM authority_usage WHERE tenant_id = $1 AND idempotency_key = $2`,
		tenant, key).Scan(&reservations); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reservations != 0 {
		t.Fatalf("this tenant has %d reservations; the test is not exercising the "+
			"no-reservation case", reservations)
	}

	if _, err := store.Prune(ctx, tenant, now.Add(time.Hour)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// The same key, and then the same envelope under a new key. Nothing monetary can
	// refuse either one.
	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: key, EnvelopeID: envelopeID,
		ClientOrderID: "coid-" + key, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, execution.ErrKeyRetired) {
		t.Errorf("re-claiming a pruned key with no reservation gave %v; permanent identity "+
			"must not depend on an amount the platform may not have", err)
	}

	fresh := key + "-fresh"
	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: fresh, EnvelopeID: envelopeID,
		ClientOrderID: "coid-" + fresh, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, execution.ErrEnvelopeReused) {
		t.Errorf("re-claiming a pruned envelope under a new key gave %v; §12.2 must not "+
			"expire with retention", err)
	}
}

// F4-IDEMPOTENCY-05: the tombstone and the deletion are one transaction.
//
// If they were two, a failure between them leaves a key whose outcome is gone and whose
// identity nothing remembers — which is the F4-B001 defect, arriving through the very
// mechanism built to prevent it. Injected by revoking the INSERT privilege on the
// tombstone table for the duration: the write fails, and the record must survive.
func TestRetentionIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	store := execution.NewPostgresStore(pool)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_f4atomic_%d", now.UnixNano())
	key := fmt.Sprintf("f4-atomic-%d", now.UnixNano())

	if _, claimed, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: key, EnvelopeID: "env_" + key,
		ClientOrderID: "coid-" + key, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if err := store.Resolve(ctx, tenant, key, execution.Outcome{
		State: broker.StateFilled, BrokerOrderID: "b_" + key,
	}, now); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	admin := ownerPool(t)
	if _, err := admin.Exec(ctx,
		`REVOKE INSERT ON idempotency_tombstones FROM assurance_app`); err != nil {
		t.Skipf("cannot revoke the tombstone insert to inject the failure: %v", err)
	}
	restored := false
	restore := func() {
		if restored {
			return
		}
		restored = true
		if _, err := admin.Exec(ctx,
			`GRANT INSERT ON idempotency_tombstones TO assurance_app`); err != nil {
			t.Fatalf("restore the tombstone insert: %v", err)
		}
	}
	t.Cleanup(restore)

	if _, err := store.Prune(ctx, tenant, now.Add(time.Hour)); err == nil {
		t.Error("retention reported success while it could not write the tombstone")
	}
	restore()

	// The record is still there, which is the property: nothing was forgotten.
	if _, err := store.Load(ctx, tenant, key); err != nil {
		t.Fatalf("the record was deleted although its identity was never retired: %v.\n\n"+
			"A key whose outcome is gone and whose identity nothing remembers is the "+
			"defect this table exists to prevent, arriving through the mechanism built "+
			"to prevent it.", err)
	}

	// And with the privilege back, retention completes.
	deleted, err := store.Prune(ctx, tenant, now.Add(time.Hour))
	if err != nil || deleted != 1 {
		t.Fatalf("retention after recovery deleted %d: %v", deleted, err)
	}
	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: key, EnvelopeID: "env_" + key,
		ClientOrderID: "coid-" + key, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); !errors.Is(err, execution.ErrKeyRetired) {
		t.Errorf("after a successful prune the key gave %v", err)
	}
}

// ownerPool connects as the database owner, which is what can revoke a privilege from the
// application role. The application's own credential cannot, by design.
func ownerPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_OWNER_DSN")
	if dsn == "" {
		// The migrations run as this role; the compose file fixes the credential and
		// scripts/migrate.sh uses it. Defaulting keeps a failure-injection test from
		// silently not running in the environment it was written for.
		dsn = "postgres://assurance:assurance_dev_only@localhost:5432/assurance?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no owner connection to inject the failure with: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("no owner connection to inject the failure with: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
