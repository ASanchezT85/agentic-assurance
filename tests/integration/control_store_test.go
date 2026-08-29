//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/control"
	"agentic-assurance/internal/fleet"
)

// A control is enforcement state, so its tenant isolation is INV-007 on a new table
// rather than a repeat of an old proof: row level security is per table, and a table
// added without a policy isolates nothing while every other one does.

func controlFixture(tenant, id string, at time.Time) control.Control {
	return control.Control{
		ControlID:      id,
		TenantID:       tenant,
		IncidentID:     "inc_" + id,
		Action:         fleet.ControlReadOnly,
		CohortID:       "cohort_test",
		AuthorizedBy:   "riesgo@example",
		PolicyBundleID: "bundle_v1",
		Reason:         "integration test",
		AppliedAt:      at,
		ExpiresAt:      at.Add(time.Hour),
	}
}

func TestAControlBindsUntilItIsRevoked(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_ctl_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_%d", at.UnixNano())

	if err := store.Save(ctx, controlFixture(tenant, id, at)); err != nil {
		t.Fatalf("save: %v", err)
	}

	inForce, err := store.InForce(ctx, tenant, at)
	if err != nil {
		t.Fatalf("in force: %v", err)
	}
	if len(inForce) != 1 {
		t.Fatalf("%d controls in force, want 1", len(inForce))
	}

	already, err := store.Revoke(ctx, tenant, id, "ops@example", at.Add(time.Minute))
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if already {
		t.Error("a first revocation reported the control was already revoked")
	}

	// Twice, because an operator lifting a control under pressure will hit it twice
	// and must be told it is done rather than handed an error.
	already, err = store.Revoke(ctx, tenant, id, "ops@example", at.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("second revoke: %v", err)
	}
	if !already {
		t.Error("a second revocation did not report the control was already revoked")
	}

	after, err := store.InForce(ctx, tenant, at.Add(3*time.Minute))
	if err != nil {
		t.Fatalf("in force after revoke: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("%d controls still in force after revocation", len(after))
	}

	// Still listed, because a denial names a control id and an operator asking what it
	// was deserves an answer rather than an empty list.
	listed, err := store.List(ctx, tenant)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(listed) != 1 || listed[0].RevokedAt == nil || listed[0].RevokedBy != "ops@example" {
		t.Errorf("the revoked control is not listed with its author: %+v", listed)
	}
}

func TestControlsAreTenantScoped(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	mine := fmt.Sprintf("tenant_ctl_a_%d", at.UnixNano())
	theirs := fmt.Sprintf("tenant_ctl_b_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_iso_%d", at.UnixNano())

	if err := store.Save(ctx, controlFixture(mine, id, at)); err != nil {
		t.Fatalf("save: %v", err)
	}

	other, err := store.InForce(ctx, theirs, at)
	if err != nil {
		t.Fatalf("in force: %v", err)
	}
	if len(other) != 0 {
		t.Error("another tenant read a control that constrains this one (INV-007)")
	}

	// And cannot lift it. A control another tenant could revoke is an outage anyone
	// can cause, and one they could confirm the existence of is a disclosure.
	if _, err := store.Revoke(ctx, theirs, id, "attacker", at); err == nil {
		t.Error("another tenant revoked this tenant's control (INV-007)")
	}
}

func TestAControlIdIsNotReused(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_ctl_dup_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_dup_%d", at.UnixNano())

	if err := store.Save(ctx, controlFixture(tenant, id, at)); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Widening by reissue is the escalation this refusal exists to stop: the second
	// control could name a longer expiry or a narrower scope, with nobody having
	// authorized the change.
	wider := controlFixture(tenant, id, at)
	wider.ExpiresAt = at.Add(30 * 24 * time.Hour)
	if err := store.Save(ctx, wider); err == nil {
		t.Fatal("a control id was silently reused")
	}
}

// An empty tenant must fail loudly. Row level security would return zero rows, which
// reads as "no controls are in force" and would unenforce every one of them.
func TestControlStoreRefusesAnEmptyTenant(t *testing.T) {
	store := control.NewStore(idemPool(t))
	if _, err := store.InForce(context.Background(), "", time.Now()); err != control.ErrTenantContextMissing {
		t.Fatalf("err = %v, want ErrTenantContextMissing", err)
	}
}

func throttleFixture(tenant, id string, at time.Time, max int) control.Control {
	c := controlFixture(tenant, id, at)
	c.Action = fleet.ControlThrottle
	c.MaxOrders = max
	c.Window = time.Minute
	return c
}

// The counter is the whole of THROTTLE. What is checked here is that it counts, that a
// replay does not spend the window twice, and that the window is a window.
func TestAThrottleCountsWhatItAllowed(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_thr_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_thr_%d", at.UnixNano())

	c := throttleFixture(tenant, id, at, 2)
	if err := store.Save(ctx, c); err != nil {
		t.Fatalf("save: %v", err)
	}

	for i := 1; i <= 2; i++ {
		allowed, used, err := store.Consume(ctx, tenant, c, fmt.Sprintf("key_%d", i), at)
		if err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
		if !allowed || used != i {
			t.Fatalf("order %d: allowed=%v used=%d", i, allowed, used)
		}
	}

	allowed, used, err := store.Consume(ctx, tenant, c, "key_3", at)
	if err != nil {
		t.Fatalf("consume 3: %v", err)
	}
	if allowed {
		t.Error("a third order passed a throttle of two per minute")
	}
	if used != 2 {
		t.Errorf("the refusal counted %d orders, want 2", used)
	}

	// A replay holds the slot it already has. Counting it again would let a duplicate
	// submission spend the window twice, which turns a throttle into a smaller one for
	// anyone who retries.
	allowed, _, err = store.Consume(ctx, tenant, c, "key_1", at)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !allowed {
		t.Error("a replayed submission was throttled on a slot it already holds")
	}

	// And the window moves. Same control, an hour later, empty again.
	allowed, used, err = store.Consume(ctx, tenant, c, "key_later", at.Add(time.Hour))
	if err != nil {
		t.Fatalf("later: %v", err)
	}
	if !allowed || used != 1 {
		t.Errorf("after the window: allowed=%v used=%d, want true/1", allowed, used)
	}
}

// Revoking releases the scope. A window that outlived its control would throttle a
// scope nothing constrains any more.
func TestRevokingAThrottleForgetsItsWindow(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_thrf_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_thrf_%d", at.UnixNano())

	c := throttleFixture(tenant, id, at, 1)
	if err := store.Save(ctx, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, _, err := store.Consume(ctx, tenant, c, "key_1", at); err != nil {
		t.Fatalf("consume: %v", err)
	}
	if err := store.Forget(ctx, tenant, id); err != nil {
		t.Fatalf("forget: %v", err)
	}

	allowed, used, err := store.Consume(ctx, tenant, c, "key_2", at)
	if err != nil {
		t.Fatalf("after forget: %v", err)
	}
	if !allowed || used != 1 {
		t.Errorf("the window survived the revocation: allowed=%v used=%d", allowed, used)
	}
}

// A tenant cannot spend, or see, another tenant's window.
func TestThrottleCountersAreTenantScoped(t *testing.T) {
	store := control.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	mine := fmt.Sprintf("tenant_thra_%d", at.UnixNano())
	theirs := fmt.Sprintf("tenant_thrb_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_thriso_%d", at.UnixNano())

	c := throttleFixture(mine, id, at, 1)
	if err := store.Save(ctx, c); err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, _, err := store.Consume(ctx, mine, c, "key_1", at); err != nil {
		t.Fatalf("consume: %v", err)
	}

	// The other tenant sees an empty window, which is the point: their orders are not
	// rationed by a control that belongs to someone else.
	allowed, used, err := store.Consume(ctx, theirs, c, "key_1", at)
	if err != nil {
		t.Fatalf("other tenant: %v", err)
	}
	if !allowed || used != 1 {
		t.Errorf("another tenant saw this window: allowed=%v used=%d", allowed, used)
	}
}

// The counter does not grow forever.
//
// One row per order a throttle allows, kept for good, is a table that grows with
// traffic and is only ever read for the last few minutes of it: a throttle on a busy
// tenant would leave millions of rows nobody queries and make its own count slower as
// it aged. Consuming prunes what is two windows old.
func TestAThrottleForgetsWhatFellOutOfItsWindow(t *testing.T) {
	pool := idemPool(t)
	store := control.NewStore(pool)
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_prune_%d", at.UnixNano())
	id := fmt.Sprintf("ctl_prune_%d", at.UnixNano())

	c := throttleFixture(tenant, id, at, 5)
	if err := store.Save(ctx, c); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Two orders long ago, one now.
	for i, when := range []time.Time{at.Add(-2 * time.Hour), at.Add(-time.Hour), at} {
		if _, _, err := store.Consume(ctx, tenant, c, fmt.Sprintf("key_%d", i), when); err != nil {
			t.Fatalf("consume %d: %v", i, err)
		}
	}

	// Counted inside a transaction that sets the tenant. Row level security answers
	// zero rows to a connection that never named one, which reads exactly like
	// "pruned" — this test passed against a store that pruned nothing until the
	// setting was there.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	var rows int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM fleet_control_usage WHERE control_id = $1`, id).Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("%d usage rows remain, want 1; the window is not being pruned", rows)
	}
}
