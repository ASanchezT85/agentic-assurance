//go:build integration

package integration

import (
	"agentic-assurance/internal/money"
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/intent"
)

// ADR-015 in a real database.
//
// The in-memory store proves the service's logic. Only PostgreSQL proves the part
// that matters under concurrency: that two simultaneous requests for one idempotency
// key cannot both believe they claimed it. That guarantee is the primary key's, not
// the application's, and a test against a map would confirm the wrong thing.
//
// Run with:  make up && make migrate && make test-integration

const idemTenant = "tenant_idem"

func idemDSN() string {
	if dsn := os.Getenv("POSTGRES_APP_DSN"); dsn != "" {
		return dsn
	}
	return "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
}

func idemPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, idemDSN())
	if err != nil {
		t.Fatalf("connect (is `make up && make migrate` done?): %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func idemService(t *testing.T, key string) (*execution.Service, *fakebroker.Broker) {
	t.Helper()
	fake := fakebroker.New()
	return &execution.Service{
		Broker: fake,
		Store:  execution.NewPostgresStore(idemPool(t)),
		Now:    time.Now,
	}, fake
}

func idemEnvelope(key string) *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:  intent.SchemaVersion,
		EnvelopeID:     "env_" + key,
		IdempotencyKey: key,
		TenantID:       idemTenant,
		ReceivedAt:     time.Now().UTC(),
	}
}

func idemRequest(key string) broker.OrderRequest {
	qty := money.MustParseQuantity("100")
	price := money.MustParse("50")
	return broker.OrderRequest{
		ClientOrderID: "coid_" + key,
		TenantID:      idemTenant,
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      &qty,
		LimitPrice:    &price,
		TimeInForce:   intent.TIFDay,
	}
}

// uniqueKey keeps runs independent without truncating a table other tests may use.
func uniqueKey(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func TestIdempotencyRecordSurvivesInPostgres(t *testing.T) {
	key := uniqueKey("persist")
	svc, fake := idemService(t, key)

	first, err := svc.Submit(context.Background(), idemEnvelope(key), idemRequest(key))
	if err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// A brand new service, as after a restart. Nothing is shared but the database.
	restarted := &execution.Service{
		Broker: fake,
		Store:  execution.NewPostgresStore(idemPool(t)),
		Now:    time.Now,
	}
	second, err := restarted.Submit(context.Background(), idemEnvelope(key), idemRequest(key))
	if err != nil {
		t.Fatalf("after restart: %v", err)
	}

	if n := fake.Submissions("coid_" + key); n != 1 {
		t.Fatalf("%d submissions across a restart; the record must survive (ADR-015, INV-011)", n)
	}
	if !second.Replayed {
		t.Error("the duplicate was not recognised after the restart")
	}
	if second.BrokerOrderID != first.BrokerOrderID {
		t.Errorf("replayed outcome differs: %q vs %q", second.BrokerOrderID, first.BrokerOrderID)
	}
}

// The one that needs a real database: concurrent claims on one key.
func TestConcurrentClaimsOnOneKeyProduceOneSubmission(t *testing.T) {
	key := uniqueKey("concurrent")
	fake := fakebroker.New()

	const callers = 12
	var wg sync.WaitGroup
	wg.Add(callers)

	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			// Each caller gets its own pool and store, as separate gateway
			// processes would.
			svc := &execution.Service{
				Broker: fake,
				Store:  execution.NewPostgresStore(idemPool(t)),
				Now:    time.Now,
			}
			_, _ = svc.Submit(context.Background(), idemEnvelope(key), idemRequest(key))
		}()
	}
	wg.Wait()

	if n := fake.Submissions("coid_" + key); n != 1 {
		t.Fatalf("%d submissions from %d concurrent callers on one idempotency key; "+
			"the primary key must make the claim atomic (INV-004)", n, callers)
	}
}

// A resolved outcome is final. Resolving twice must not rewrite what the first
// caller was told.
func TestResolvedOutcomeCannotBeRewritten(t *testing.T) {
	key := uniqueKey("final")
	store := execution.NewPostgresStore(idemPool(t))
	ctx := context.Background()

	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID:       idemTenant,
		IdempotencyKey: key,
		EnvelopeID:     "env_" + key,
		ClientOrderID:  "coid_" + key,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	original := execution.Outcome{
		State:         broker.StateFilled,
		ClientOrderID: "coid_" + key,
		BrokerOrderID: "venue-1",
	}
	if err := store.Resolve(ctx, idemTenant, key, original, time.Now()); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	overwrite := execution.Outcome{
		State:         broker.StateRejected,
		ClientOrderID: "coid_" + key,
		BrokerOrderID: "venue-2",
	}
	if err := store.Resolve(ctx, idemTenant, key, overwrite, time.Now()); err == nil {
		t.Fatal("a resolved outcome was rewritten; a duplicate would then get a " +
			"different answer than the first caller (spec section 17)")
	}

	rec, err := store.Load(ctx, idemTenant, key)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if rec.Outcome.State != broker.StateFilled || rec.Outcome.BrokerOrderID != "venue-1" {
		t.Errorf("the record changed: %+v", rec.Outcome)
	}
}

// Idempotency records are tenant-scoped like everything else (INV-007).
func TestIdempotencyRecordsAreTenantScoped(t *testing.T) {
	key := uniqueKey("tenants")
	store := execution.NewPostgresStore(idemPool(t))
	ctx := context.Background()

	if _, _, err := store.Claim(ctx, execution.Record{
		TenantID:       idemTenant,
		IdempotencyKey: key,
		EnvelopeID:     "env_" + key,
		ClientOrderID:  "coid_" + key,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := store.Load(ctx, "tenant_someone_else", key); err == nil {
		t.Fatal("another tenant read an idempotency record (INV-007)")
	}

	// And the same key in another tenant is a different record entirely, not a
	// duplicate of this one.
	existing, claimed, err := store.Claim(ctx, execution.Record{
		TenantID:       "tenant_someone_else",
		IdempotencyKey: key,
		EnvelopeID:     "env_other_" + key,
		ClientOrderID:  "coid_other_" + key,
		CreatedAt:      time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("claim in second tenant: %v", err)
	}
	if !claimed || existing != nil {
		t.Error("one tenant's idempotency key blocked another tenant's (INV-007)")
	}
}

// A query with no tenant must fail loudly rather than matching zero rows and looking
// like a new request.
func TestStoreRefusesAnEmptyTenant(t *testing.T) {
	store := execution.NewPostgresStore(idemPool(t))

	if _, err := store.Load(context.Background(), "", "anything"); err != execution.ErrTenantContextMissing {
		t.Fatalf("error = %v, want ErrTenantContextMissing", err)
	}
}
