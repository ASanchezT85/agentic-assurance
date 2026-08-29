//go:build integration

package integration

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/authority"
)

// The usage ledger against the real database.
//
// internal/authority named a PostgreSQL-backed UsageSource as the hot path's
// implementation since Phase 3 and there was none. The in-memory one proves the
// arithmetic; only this proves the window boundaries survive a round trip through
// numeric and timestamptz, and that row level security actually isolates it.
//
// Run with:  make up && make migrate && make test-integration

func usagePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		t.Skip("POSTGRES_APP_DSN is not set")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// purge deletes a tenant's rows.
//
// In a transaction with set_config local, never session-scoped. pgxpool reuses
// connections: a session-scoped app.tenant_id outlives the test that set it and the
// next writer on that connection fails row level security for a tenant it never
// named. That is a test bug that reads exactly like a product bug.
func purge(t *testing.T, pool *pgxpool.Pool, tenant string, tables ...string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenant); err != nil {
		return
	}
	for _, table := range tables {
		_, _ = tx.Exec(ctx, "DELETE FROM "+table+" WHERE tenant_id = $1", tenant)
	}
	_ = tx.Commit(ctx)
}

func TestPostgresUsageWindowsAndIsolation(t *testing.T) {
	pool := usagePool(t)
	usage := authority.NewPostgresUsage(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Second)
	// Anchored mid-afternoon UTC so "an hour ago" and "today" are different windows.
	// At 00:30 UTC they are nearly the same, and a test that passed only then would
	// be proving very little.
	now = time.Date(now.Year(), now.Month(), now.Day(), 15, 0, 0, 0, time.UTC)

	// Unique per run. The name used to be derived from the anchored clock, so every
	// run wrote under the same tenant with the same idempotency keys — and Record's
	// ON CONFLICT DO NOTHING then silently dropped today's rows on top of yesterday's,
	// which a run interrupted before its cleanup had left behind. The test measured
	// windows against data from another day and reported zero.
	tenant := fmt.Sprintf("tenant_usage_%d", time.Now().UnixNano())
	other := tenant + "_other"
	grant := "grant_usage"

	entries := []authority.Entry{
		// Inside both windows, still open.
		{TenantID: tenant, GrantID: grant, IdempotencyKey: "in-hour", Notional: 1000,
			SubmittedAt: now.Add(-10 * time.Minute), Open: true},
		// Earlier today but outside the rolling hour, and closed.
		{TenantID: tenant, GrantID: grant, IdempotencyKey: "earlier-today", Notional: 500,
			SubmittedAt: now.Add(-5 * time.Hour), Open: false},
		// Yesterday: outside both windows.
		{TenantID: tenant, GrantID: grant, IdempotencyKey: "yesterday", Notional: 9999,
			SubmittedAt: now.Add(-30 * time.Hour), Open: false},
		// Another grant in the same tenant must not be counted.
		{TenantID: tenant, GrantID: "grant_other", IdempotencyKey: "other-grant", Notional: 7777,
			SubmittedAt: now.Add(-1 * time.Minute), Open: true},
		// Another tenant entirely (INV-007).
		{TenantID: other, GrantID: grant, IdempotencyKey: "other-tenant", Notional: 4444,
			SubmittedAt: now.Add(-1 * time.Minute), Open: true},
	}
	for _, e := range entries {
		if err := usage.Record(ctx, e); err != nil {
			t.Fatalf("record %s: %v", e.IdempotencyKey, err)
		}
	}
	t.Cleanup(func() {
		purge(t, pool, tenant, "authority_usage")
		purge(t, pool, other, "authority_usage")
	})

	snapshot, err := usage.Usage(ctx, tenant, grant, now)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}

	if snapshot.Rolling1hNotional != 1000 {
		t.Errorf("rolling hour = %.2f, want 1000.00; only the entry inside the hour "+
			"counts", snapshot.Rolling1hNotional)
	}
	if snapshot.DailyNotional != 1500 {
		t.Errorf("daily = %.2f, want 1500.00; today is both entries and excludes "+
			"yesterday", snapshot.DailyNotional)
	}
	if snapshot.OpenOrders != 1 {
		t.Errorf("open orders = %d, want 1; a closed entry is no longer exposure",
			snapshot.OpenOrders)
	}
}

// Closing an entry removes it from open exposure without changing what it spent. A
// filled order still consumed the grant.
func TestClosingAnEntryKeepsItsNotional(t *testing.T) {
	pool := usagePool(t)
	usage := authority.NewPostgresUsage(pool)
	ctx := context.Background()

	now := time.Now().UTC()
	tenant := "tenant_close_" + now.Format("150405.000000000")
	t.Cleanup(func() { purge(t, pool, tenant, "authority_usage") })

	entry := authority.Entry{TenantID: tenant, GrantID: "g", IdempotencyKey: "k",
		Notional: 2500, SubmittedAt: now.Add(-time.Minute), Open: true}
	if err := usage.Record(ctx, entry); err != nil {
		t.Fatalf("record: %v", err)
	}
	if err := usage.Close(ctx, tenant, "k", now); err != nil {
		t.Fatalf("close: %v", err)
	}

	snapshot, err := usage.Usage(ctx, tenant, "g", now)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if snapshot.OpenOrders != 0 {
		t.Errorf("open orders = %d, want 0", snapshot.OpenOrders)
	}
	if snapshot.Rolling1hNotional != 2500 {
		t.Errorf("rolling hour = %.2f, want 2500.00; a filled order still spent the grant",
			snapshot.Rolling1hNotional)
	}
}

// A replayed submission must not spend the grant twice, enforced by the primary key
// rather than by the caller remembering to check.
func TestRecordingTheSameKeyTwiceSpendsOnce(t *testing.T) {
	pool := usagePool(t)
	usage := authority.NewPostgresUsage(pool)
	ctx := context.Background()

	now := time.Now().UTC()
	tenant := "tenant_dup_" + now.Format("150405.000000000")
	t.Cleanup(func() { purge(t, pool, tenant, "authority_usage") })

	entry := authority.Entry{TenantID: tenant, GrantID: "g", IdempotencyKey: "k",
		Notional: 100, SubmittedAt: now.Add(-time.Minute), Open: true}
	for range 3 {
		if err := usage.Record(ctx, entry); err != nil {
			t.Fatalf("record: %v", err)
		}
	}

	snapshot, _ := usage.Usage(ctx, tenant, "g", now)
	if snapshot.Rolling1hNotional != 100 {
		t.Errorf("three records of one key spent %.2f, want 100.00",
			snapshot.Rolling1hNotional)
	}
}
