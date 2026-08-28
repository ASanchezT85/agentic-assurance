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
