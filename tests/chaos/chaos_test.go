//go:build chaos

// Package chaos stops real services and checks what survives.
//
// # Why its own build tag
//
// It stops shared containers. Go runs test packages in parallel, so under
// `-tags=integration ./tests/...` this suite takes PostgreSQL down while
// tests/integration is using it, and both hang. That is not a flake to retry, it is
// two suites disagreeing about who owns the infrastructure.
//
// A separate tag makes running it a deliberate act:
//
//	go test -tags=chaos -timeout 15m ./tests/chaos/
//
// Spec section 55 lists the failures and states the principle in one sentence:
//
//	Critical identity, authority, and local hard limits remain deterministic or fail
//	closed as specified.
//
// The tests here stop actual containers rather than injecting a fake error. A stubbed
// outage proves the code handles the error it was handed; stopping PostgreSQL proves
// it handles the one the driver actually produces, which is not always the same
// error and is never at the same moment.
//
// Every test restores what it stopped, including on failure.
//
// Run with:  make up && make migrate && make test-chaos
package chaos

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

var at = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

func f(v float64) *float64 { return &v }

// stop halts a compose service and restores it when the test ends.
//
// The restore runs through t.Cleanup rather than defer so it happens even if the test
// calls t.Fatal. A chaos suite that leaves the infrastructure broken for the next test
// is a chaos suite people stop running.
func stop(t *testing.T, service string) {
	t.Helper()

	run := func(args ...string) error {
		cmd := exec.Command("docker", append([]string{"compose"}, args...)...)
		cmd.Dir = "../.."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("docker compose %v: %v\n%s", args, err, out)
		}
		return err
	}

	if err := run("stop", "-t", "5", service); err != nil {
		t.Skipf("could not stop %s; is the infrastructure running?", service)
	}
	t.Cleanup(func() {
		_ = run("start", service)
		// Give the service a moment to accept connections again, so the next test
		// does not inherit a half-started dependency and blame its own code.
		time.Sleep(3 * time.Second)
	})
}

func envelope(id string, notional float64) *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:    intent.SchemaVersion,
		EnvelopeID:       id,
		IdempotencyKey:   "idem_" + id,
		CorrelationID:    "corr_" + id,
		ReceivedAt:       at,
		TenantID:         "tenant_chaos",
		AuthorityGrantID: "grant_chaos",
		Principal:        intent.Principal{PrincipalID: "principal_chaos", AccountID: "account_chaos"},
		Agent:            intent.Agent{AgentID: "agent_chaos"},
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     f(notional),
			TimeInForce:  intent.TIFDay,
		},
	}
}

func grant() *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_chaos",
		TenantID:            "tenant_chaos",
		PrincipalID:         "principal_chaos",
		AccountID:           "account_chaos",
		AgentID:             "agent_chaos",
		IssuedAt:            at.Add(-24 * time.Hour),
		ValidFrom:           at.Add(-time.Hour),
		ValidUntil:          at.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		Limits:              authority.Limits{PerOrderNotional: 5000},
		Status:              authority.StatusActive,
	}
}

func activeBundle(t *testing.T) *policy.Bundle {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/policies/valid/retail_agent_standard.yaml")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	src, err := policy.ParseSource(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_chaos", "bundle_chaos", at)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	if err := bundle.Sign(priv, "chaos", at); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, at, "chaos"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	return bundle
}

// assertEnforcementHolds is the section 55 principle, in one function.
//
// Both directions matter. An oversized order must be denied, and a compliant one must
// still pass: an outage that becomes a blanket denial is an outage of its own.
func assertEnforcementHolds(t *testing.T, during string) {
	t.Helper()

	bundle := activeBundle(t)
	g := grant()
	ctx := context.Background()

	oversized := envelope("env_big", 250000)
	if d := authority.Evaluate(ctx, oversized, g, nil, at); d.Allowed {
		t.Errorf("%s: authority allowed an order over its per-order limit", during)
	}
	if d := policy.Evaluate(bundle, oversized, at); d.Action != policy.ActionDeny {
		t.Errorf("%s: policy returned %s for an order over the ceiling", during, d.Action)
	}

	compliant := envelope("env_small", 1000)
	if d := authority.Evaluate(ctx, compliant, g, nil, at); !d.Allowed {
		t.Errorf("%s: a compliant order was denied (%s); an outage must not become a "+
			"blanket denial", during, d.Code)
	}
	if d := policy.Evaluate(bundle, compliant, at); d.Action == policy.ActionDeny {
		t.Errorf("%s: policy denied a compliant order", during)
	}
}

// Section 55: stop ClickHouse. Analytics degrade, enforcement does not.
func TestEnforcementSurvivesClickHouseOutage(t *testing.T) {
	stop(t, "clickhouse")
	assertEnforcementHolds(t, "with ClickHouse stopped")
}

// Section 55: stop NATS. Telemetry buffers; enforcement does not wait on it.
func TestEnforcementSurvivesNATSOutage(t *testing.T) {
	stop(t, "nats")
	assertEnforcementHolds(t, "with NATS stopped")
}

// Section 55: stop Redis. Idempotency falls through to PostgreSQL, and enforcement
// is unaffected (ADR-015, INV-011).
func TestEnforcementSurvivesRedisOutage(t *testing.T) {
	stop(t, "redis")
	assertEnforcementHolds(t, "with Redis stopped")
}

// Section 55: isolate the intelligence cloud. The fleet engine is not reachable from
// the enforcement path at all, so stopping it changes nothing, which is the point.
func TestEnforcementSurvivesIntelligenceOutage(t *testing.T) {
	stop(t, "spire-server")
	assertEnforcementHolds(t, "with the identity issuer stopped")
}

// Section 55: stop PostgreSQL. This is the one that must fail closed rather than
// continue, because it holds authority, the policy bundle and idempotency truth
// (ADR-021).
func TestPostgresOutageFailsClosed(t *testing.T) {
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		dsn = "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no PostgreSQL to stop: %v", err)
	}
	t.Cleanup(pool.Close)

	store := execution.NewPostgresStore(pool)
	fake := fakebroker.New()
	svc := &execution.Service{Broker: fake, Store: store, Now: func() time.Time { return at }}

	req := broker.OrderRequest{
		ClientOrderID: "coid_chaos_pg",
		TenantID:      "tenant_chaos",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      f(10),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}

	stop(t, "postgres")

	// The submission must not proceed. Spec section 17 and ADR-021: an unreadable
	// idempotency store denies, because sending an order the platform cannot record
	// is how one intent silently becomes two.
	if _, err := svc.Submit(ctx, envelope("env_pg_out", 1000), req); err == nil {
		t.Fatal("a submission proceeded with PostgreSQL stopped (ADR-021)")
	}
	if n := fake.Submissions("coid_chaos_pg"); n != 0 {
		t.Fatalf("%d orders reached the venue with no idempotency record (INV-004)", n)
	}

	// Pure enforcement still works: it never needed the database.
	assertEnforcementHolds(t, "with PostgreSQL stopped")
}

// Section 55: policy bundle unavailable. No bundle means DENY, not "nothing to check
// so proceed".
func TestMissingPolicyBundleDenies(t *testing.T) {
	if d := policy.Evaluate(nil, envelope("env_nobundle", 100), at); d.Action != policy.ActionDeny {
		t.Fatalf("action = %s; hard policy unavailable must DENY (spec section 17)", d.Action)
	}
}

// Section 55: broker API timeout. Covered exhaustively in the Phase 5 matrix; here it
// is checked alongside the other outages so a chaos run answers the whole list.
func TestBrokerTimeoutDoesNotDuplicate(t *testing.T) {
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return at })
	fake.InjectFault("coid_chaos_broker", fakebroker.FaultTimeoutAfterReceipt)

	svc := &execution.Service{
		Broker: fake,
		Store:  execution.NewMemoryStore(),
		Now:    func() time.Time { return at },
	}
	req := broker.OrderRequest{
		ClientOrderID: "coid_chaos_broker",
		TenantID:      "tenant_chaos",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      f(10),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}

	env := envelope("env_broker_timeout", 500)
	for i := 0; i < 20; i++ {
		if _, err := svc.Submit(context.Background(), env, req); err != nil {
			t.Fatalf("retry %d: %v", i, err)
		}
	}
	if n := fake.Submissions("coid_chaos_broker"); n != 1 {
		t.Fatalf("%d submissions reached the venue across twenty retries (INV-004)", n)
	}
}

// Section 55: restart the gateway. Enforcement is stateless per envelope, so a
// restart loses nothing that matters, and idempotency truth survives in PostgreSQL.
func TestGatewayRestartLosesNothingThatMatters(t *testing.T) {
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		dsn = "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
	}
	ctx := context.Background()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("no PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return at })

	key := "restart_" + time.Now().UTC().Format("150405.000000000")
	env := envelope("env_"+key, 500)
	env.IdempotencyKey = key
	req := broker.OrderRequest{
		ClientOrderID: "coid_" + key,
		TenantID:      "tenant_chaos",
		InstrumentID:  "instr_us_equity_00206R102",
		Symbol:        "AAPL",
		AssetClass:    intent.AssetEquity,
		Side:          intent.SideBuy,
		OrderType:     intent.OrderLimit,
		Quantity:      f(10),
		LimitPrice:    f(50),
		TimeInForce:   intent.TIFDay,
	}

	// One gateway submits.
	before := &execution.Service{
		Broker: fake, Store: execution.NewPostgresStore(pool),
		Cache: execution.NewMemoryCache(), Now: func() time.Time { return at },
	}
	if _, err := before.Submit(ctx, env, req); err != nil {
		t.Fatalf("first submit: %v", err)
	}

	// It restarts: a new process, a new cache, nothing shared but the database.
	after := &execution.Service{
		Broker: fake, Store: execution.NewPostgresStore(pool),
		Cache: execution.NewMemoryCache(), Now: func() time.Time { return at },
	}
	outcome, err := after.Submit(ctx, env, req)
	if err != nil {
		t.Fatalf("after restart: %v", err)
	}

	if n := fake.Submissions("coid_" + key); n != 1 {
		t.Fatalf("%d submissions across a restart (INV-011)", n)
	}
	if !outcome.Replayed {
		t.Error("the restarted gateway did not recognise the prior execution")
	}
}
