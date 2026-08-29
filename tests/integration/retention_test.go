//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
)

// Bounded retention (spec section 19), which the section lists beside a unique envelope
// id and deterministic duplicate handling and which nothing implemented.
//
// Two properties matter more than the window itself: a PENDING record is never pruned
// at any age, and one tenant's sweep never touches another's rows.

func resolved(t *testing.T, store *execution.PostgresStore, tenant, key string, at time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID:       tenant,
		IdempotencyKey: key,
		EnvelopeID:     "env_" + key,
		ClientOrderID:  "coid_" + key,
		CreatedAt:      at,
		UpdatedAt:      at,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := store.Resolve(ctx, tenant, key, execution.Outcome{
		State:         broker.StateFilled,
		BrokerOrderID: "b_" + key,
	}, at); err != nil {
		t.Fatalf("resolve: %v", err)
	}
}

func pending(t *testing.T, store *execution.PostgresStore, tenant, key string, at time.Time) {
	t.Helper()
	if _, _, err := store.Claim(context.Background(), execution.Record{
		TenantID:       tenant,
		IdempotencyKey: key,
		EnvelopeID:     "env_" + key,
		ClientOrderID:  "coid_" + key,
		CreatedAt:      at,
		UpdatedAt:      at,
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}
}

func TestRetentionKeepsWhatMustNotBeForgotten(t *testing.T) {
	store := execution.NewPostgresStore(idemPool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	tenant := fmt.Sprintf("tenant_ret_%d", now.UnixNano())

	oldResolved := fmt.Sprintf("ret_old_%d", now.UnixNano())
	freshResolved := fmt.Sprintf("ret_new_%d", now.UnixNano())
	oldPending := fmt.Sprintf("ret_pending_%d", now.UnixNano())

	resolved(t, store, tenant, oldResolved, old)
	resolved(t, store, tenant, freshResolved, now)
	pending(t, store, tenant, oldPending, old)

	sweeper := &execution.Sweeper{
		Store:  store,
		Keep:   30 * 24 * time.Hour,
		Now:    func() time.Time { return now },
		Tenant: func() []string { return []string{tenant} },
	}
	if deleted := sweeper.Sweep(ctx); deleted != 1 {
		t.Errorf("swept %d records, want 1", deleted)
	}

	if r, _ := store.Load(ctx, tenant, oldResolved); r != nil {
		t.Error("a resolved record older than the window survived the sweep")
	}
	if r, _ := store.Load(ctx, tenant, freshResolved); r == nil {
		t.Error("a record inside the window was pruned; retries would re-execute")
	}

	// The one that matters. A pending record says a submission was claimed and the
	// platform does not know what the venue did; deleting it at any age would turn an
	// unresolved order into an order nobody remembers claiming.
	if r, _ := store.Load(ctx, tenant, oldPending); r == nil {
		t.Error("a PENDING record was pruned; an ambiguous outcome became invisible")
	}
}

func TestRetentionIsTenantScoped(t *testing.T) {
	store := execution.NewPostgresStore(idemPool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	mine := fmt.Sprintf("tenant_reta_%d", now.UnixNano())
	theirs := fmt.Sprintf("tenant_retb_%d", now.UnixNano())

	key := fmt.Sprintf("ret_iso_%d", now.UnixNano())
	resolved(t, store, theirs, key, old)

	sweeper := &execution.Sweeper{
		Store:  store,
		Keep:   30 * 24 * time.Hour,
		Now:    func() time.Time { return now },
		Tenant: func() []string { return []string{mine} },
	}
	if deleted := sweeper.Sweep(ctx); deleted != 0 {
		t.Errorf("sweeping one tenant deleted %d rows of another (INV-007)", deleted)
	}
	if r, _ := store.Load(ctx, theirs, key); r == nil {
		t.Error("another tenant's record was pruned by this tenant's sweep")
	}
}
