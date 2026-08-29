//go:build integration

package security

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// INV-007, the database half.
//
// Row level security cannot be proven without a database enforcing it. Two things
// make an RLS test pass while the database isolates nothing:
//
//  1. connecting as a superuser, which PostgreSQL exempts from RLS entirely;
//  2. ENABLE without FORCE, which leaves the table owner exempt.
//
// The migration handles both, and TestApplicationRoleIsNotExemptFromRLS asserts the
// test itself is not running under an exemption.
//
// Run with:  make up && make migrate && make test-integration

const (
	tenantA = "tenant_acme"
	tenantB = "tenant_globex"
)

func appDSN() string {
	if dsn := os.Getenv("POSTGRES_APP_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
}

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, appDSN())
	if err != nil {
		t.Fatalf("connect (is `make up && make migrate` done?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// The test connection must be subject to RLS. If this fails, every other isolation
// assertion in this file is worthless, so it runs first and says so plainly.
func TestApplicationRoleIsNotExemptFromRLS(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()

	var isSuper, bypassRLS bool
	err := pool.QueryRow(ctx,
		`SELECT rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`).
		Scan(&isSuper, &bypassRLS)
	if err != nil {
		t.Fatalf("read role attributes: %v", err)
	}

	if isSuper {
		t.Fatal("the application connects as a superuser; PostgreSQL exempts superusers " +
			"from row level security, so every policy in the migration is inert (INV-007)")
	}
	if bypassRLS {
		t.Fatal("the application role has BYPASSRLS; the policies are inert (INV-007)")
	}

	var enabled, forced bool
	err = pool.QueryRow(ctx,
		`SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'authority_grants'`).
		Scan(&enabled, &forced)
	if err != nil {
		t.Fatalf("read table attributes: %v", err)
	}
	if !enabled {
		t.Fatal("row level security is not enabled on authority_grants (INV-007)")
	}
	if !forced {
		t.Fatal("row level security is enabled but not FORCEd; the table owner is exempt (INV-007)")
	}
}

func seedGrant(t *testing.T, store *authority.Store, tenantID, grantID string) *authority.Grant {
	t.Helper()
	at := time.Now().UTC().Truncate(time.Second)
	g := &authority.Grant{
		GrantID:             grantID,
		TenantID:            tenantID,
		PrincipalID:         "principal_7781",
		AccountID:           "account_4410",
		AgentID:             "agent_momentum_03",
		IssuedAt:            at.Add(-24 * time.Hour),
		ValidFrom:           at.Add(-time.Hour),
		ValidUntil:          at.Add(time.Hour),
		AllowedOperations:   sidesBuy(),
		AllowedAssetClasses: classesEquity(),
		Limits:              authority.Limits{PerOrderNotional: money.MustParse("5000")},
		Status:              authority.StatusActive,
	}
	if err := store.Save(context.Background(), g); err != nil {
		t.Fatalf("seed %s/%s: %v", tenantID, grantID, err)
	}
	t.Cleanup(func() {
		// Best effort: the row is tenant-scoped and harmless if it survives.
		_ = store.Revoke(context.Background(), tenantID, grantID, time.Now(), "test cleanup")
	})
	return g
}

// The load-bearing test. Tenant A stores a grant; tenant B must not be able to read
// it, by its exact primary key.
func TestTenantCannotReadAnotherTenantsGrant(t *testing.T) {
	store := authority.NewStore(openPool(t))
	ctx := context.Background()

	seedGrant(t, store, tenantA, "grant_isolation_probe")

	if _, err := store.Load(ctx, tenantA, "grant_isolation_probe"); err != nil {
		t.Fatalf("the owning tenant could not read its own grant: %v", err)
	}

	got, err := store.Load(ctx, tenantB, "grant_isolation_probe")
	if err == nil {
		t.Fatalf("tenant B read tenant A's grant (INV-007): %+v", got)
	}
	if err != authority.ErrGrantNotFound {
		t.Errorf("expected ErrGrantNotFound, got %v; the failure must not reveal that "+
			"the row exists in another tenant", err)
	}
}

// Writing a grant for another tenant must be refused by the policy's WITH CHECK,
// not merely by application code.
func TestTenantCannotWriteIntoAnotherTenant(t *testing.T) {
	store := authority.NewStore(openPool(t))

	g := seedGrant(t, store, tenantA, "grant_write_probe")

	// Same row, relabelled to tenant B. The store scopes the transaction to the
	// grant's own tenant, so this lands as a tenant B write for a grant_id that
	// already belongs to tenant A.
	g.TenantID = tenantB
	err := store.Save(context.Background(), g)
	if err == nil {
		t.Fatal("a grant was rewritten into another tenant (INV-007)")
	}
}

// Revocation is tenant-scoped too. The failure mode is an operator in one tenant
// revoking a grant in another.
func TestTenantCannotRevokeAnotherTenantsGrant(t *testing.T) {
	store := authority.NewStore(openPool(t))
	ctx := context.Background()

	seedGrant(t, store, tenantA, "grant_revoke_probe")

	if err := store.Revoke(ctx, tenantB, "grant_revoke_probe", time.Now(), "wrong tenant"); err == nil {
		t.Fatal("tenant B revoked tenant A's grant (INV-007)")
	}

	loaded, err := store.Load(ctx, tenantA, "grant_revoke_probe")
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if loaded.Status != authority.StatusActive {
		t.Errorf("the grant was revoked by another tenant; status = %s", loaded.Status)
	}
}

// The tenant setting must not survive into the next use of a pooled connection.
// A leaked GUC is the standard way RLS quietly stops isolating anything.
func TestTenantSettingDoesNotLeakAcrossConnections(t *testing.T) {
	pool := openPool(t)
	store := authority.NewStore(pool)
	ctx := context.Background()

	seedGrant(t, store, tenantA, "grant_leak_probe")
	if _, err := store.Load(ctx, tenantA, "grant_leak_probe"); err != nil {
		t.Fatalf("seed read: %v", err)
	}

	// Reach for the same pooled connection outside any tenant transaction.
	var setting string
	if err := pool.QueryRow(ctx, `SELECT current_setting('app.tenant_id', true)`).Scan(&setting); err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if setting != "" {
		t.Errorf("app.tenant_id survived the transaction as %q; set_config must be transaction-local", setting)
	}
}

// Round-tripping a grant through PostgreSQL must not change what it authorizes.
func TestGrantRoundTripsFaithfully(t *testing.T) {
	store := authority.NewStore(openPool(t))
	ctx := context.Background()

	original := seedGrant(t, store, tenantA, "grant_roundtrip_probe")
	loaded, err := store.Load(ctx, tenantA, "grant_roundtrip_probe")
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if loaded.AgentID != original.AgentID || loaded.PrincipalID != original.PrincipalID {
		t.Errorf("identity fields changed: %+v", loaded)
	}
	if loaded.Limits != original.Limits {
		t.Errorf("limits changed: %+v want %+v", loaded.Limits, original.Limits)
	}
	if len(loaded.AllowedOperations) != len(original.AllowedOperations) {
		t.Errorf("allowed operations changed: %v", loaded.AllowedOperations)
	}
	if !loaded.Active(time.Now()) {
		t.Error("a grant that was active before the round trip is not active after it")
	}
}

func sidesBuy() []intent.Side            { return []intent.Side{intent.SideBuy} }
func classesEquity() []intent.AssetClass { return []intent.AssetClass{intent.AssetEquity} }
