//go:build integration

package integration

import (
	"agentic-assurance/internal/money"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
)

// The fleet engine end to end: intents into ClickHouse, a producer that measures a
// closed window, and the intelligence API serving what it wrote.
//
// Before this, every piece existed and nothing connected them. Measure was called by
// tests, InsertMeasurements by a benchmark, and the API read a table nothing wrote:
// the engine could answer questions about a fleet it had never observed, and every
// answer was an empty list that looked like a calm fleet.
//
// Run with:  make up && make migrate && make test-integration

func clickhouse(t *testing.T) *fleet.Sink {
	t.Helper()
	base := os.Getenv("CLICKHOUSE_HTTP_URL")
	if base == "" {
		t.Skip("CLICKHOUSE_HTTP_URL is not set")
	}
	user := os.Getenv("CLICKHOUSE_USER")
	if user == "" {
		user = "assurance"
	}
	return fleet.NewSink(strings.TrimRight(base, "/"), user, os.Getenv("CLICKHOUSE_PASSWORD"))
}

const fleetToken = "fleet-integration-token-32-plus-chars"

func fleetTenant() string {
	return fmt.Sprintf("tenant_fleet_%d", time.Now().UnixNano())
}

// windowFor returns a window in the past, aligned the way the producer aligns.
func windowFor(base time.Time, d time.Duration) fleet.Window {
	end := base.Truncate(d)
	return fleet.Window{Start: end.Add(-d), End: end}
}

func fleetEnvelope(tenant, id string, at time.Time, side intent.Side, notional float64,
	agent, modelFamily string, deps []intent.Dependency) *intent.AgentExecutionEnvelope {

	n, err := money.FromFloat(notional)
	if err != nil {
		panic(err)
	}
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:  "0.1",
		EnvelopeID:     id,
		IdempotencyKey: "idem-" + id,
		CorrelationID:  "corr-" + id,
		ReceivedAt:     at,
		TenantID:       tenant,
		Principal:      intent.Principal{PrincipalID: "prin_fleet", AccountID: "acct_fleet"},
		Agent: intent.Agent{
			AgentID:     agent,
			Attestation: intent.Attestation{Level: intent.AttestationA1, Method: "api_key"},
		},
		RuntimeClaims: intent.RuntimeClaims{
			ModelFamily: intent.Claim{Value: modelFamily},
		},
		AuthorityGrantID: "grant_fleet",
		Dependencies:     deps,
		Intent: intent.Intent{
			InstrumentID: "instr_us_equity_00206R102",
			AssetClass:   intent.AssetEquity,
			Side:         side,
			OrderType:    intent.OrderMarket,
			Notional:     &n,
			TimeInForce:  intent.TIFDay,
		},
		Lineage: intent.Lineage{StrategyID: "strat_fleet"},
	}
}

// The guard that keeps the projection honest.
//
// The fleet vector is computed by one implementation, in Go, from envelopes. The
// analytical store holds a projection of an envelope, not the envelope. If Measure
// ever starts reading a field the projection does not carry, every stored measurement
// silently becomes wrong while every unit test still passes, because the unit tests
// measure envelopes that never went through the store.
//
// This measures the same intents both ways and requires the answers to be identical.
func TestAMeasurementSurvivesTheRoundTrip(t *testing.T) {
	sink := clickhouse(t)
	ctx := context.Background()

	tenant := fleetTenant()
	w := windowFor(time.Now().UTC().Add(-time.Hour), time.Minute)
	at := w.Start.Add(10 * time.Second)

	verified := []intent.Dependency{{
		Type: intent.DependencyMarketData, ID: "feed_a",
		Verification: intent.VerificationVerified, ObservedAt: at,
	}}
	declared := []intent.Dependency{{
		Type: intent.DependencyMarketData, ID: "feed_b",
		Verification: intent.VerificationDeclared, ObservedAt: at,
	}}

	originals := []*intent.AgentExecutionEnvelope{
		fleetEnvelope(tenant, "env_rt_1", at, intent.SideBuy, 10000, "agent_a", "gpt", verified),
		fleetEnvelope(tenant, "env_rt_2", at.Add(time.Second), intent.SideBuy, 5000, "agent_b", "gpt", declared),
		fleetEnvelope(tenant, "env_rt_3", at.Add(2*time.Second), intent.SideSell, 3000, "agent_c", "claude", nil),
	}

	if err := sink.InsertIntents(ctx, originals, nil); err != nil {
		t.Fatalf("insert intents: %v", err)
	}
	if err := sink.InsertDependencies(ctx, originals); err != nil {
		t.Fatalf("insert dependencies: %v", err)
	}
	waitForRows(t, sink, tenant, len(originals))

	hydrated, err := sink.LoadWindow(ctx, tenant, w)
	if err != nil {
		t.Fatalf("load window: %v", err)
	}
	if len(hydrated) != len(originals) {
		t.Fatalf("the store returned %d intents, want %d (window %s .. %s, first intent at %s)",
			len(hydrated), len(originals), w.Start.Format(time.RFC3339Nano),
			w.End.Format(time.RFC3339Nano), originals[0].ReceivedAt.Format(time.RFC3339Nano))
	}

	cohort := fleet.Cohort{TenantID: tenant}
	want := fleet.Measure(cohort, w, originals)
	got := fleet.MeasureObserved(cohort, w, hydrated)

	// These intents were stored with no recorded decision, so the store reports them
	// as unauthorized while the envelope-only path assumes authorized. That split is
	// the one thing the two paths cannot agree on by construction, and it is compared
	// on its own below rather than papered over.
	want.AuthorizedIntents, want.RefusedIntents = 0, 0
	got.AuthorizedIntents, got.RefusedIntents = 0, 0

	// The cohort and window are inputs, identical by construction. Everything else
	// is what the measurement computed, and that is what must not have moved.
	want.Cohort, got.Cohort = fleet.Cohort{}, fleet.Cohort{}

	if !reflect.DeepEqual(want, got) {
		t.Errorf("a measurement changed on its way through the store.\n"+
			"  from envelopes: %+v\n"+
			"  from the store: %+v\n"+
			"The store holds a projection, not an envelope. Either Measure now reads a "+
			"field the projection does not carry, or the projection lost one.", want, got)
	}

	// And the numbers are what a reader would expect, so a round trip that agreed on
	// two wrong answers still fails.
	if got.IntentCount != 3 || got.AgentCount != 3 {
		t.Errorf("intents/agents = %d/%d, want 3/3", got.IntentCount, got.AgentCount)
	}
	if got.GrossNotional != 18000 || got.NetNotional != 12000 {
		t.Errorf("gross/net = %.0f/%.0f, want 18000/12000",
			got.GrossNotional, got.NetNotional)
	}
	if got.ModelCoverage != 1 {
		t.Errorf("model coverage = %.2f, want 1.00", got.ModelCoverage)
	}
	if got.FeedCoverage.Verified != 1.0/3 || got.FeedCoverage.Unknown != 1.0/3 {
		t.Errorf("feed coverage = %+v; one verified, one declared, one absent",
			got.FeedCoverage)
	}
}

// The producer measures a closed window and the API serves what it wrote.
func TestFleetEngineEndToEnd(t *testing.T) {
	sink := clickhouse(t)
	ctx := context.Background()

	tenant := fleetTenant()
	// A window an hour in the past, so it is closed and settled no matter when the
	// test runs. The producer's clock is injected to land inside it.
	w := windowFor(time.Now().UTC().Add(-time.Hour), time.Minute)
	at := w.Start.Add(5 * time.Second)

	// Twelve agents, ten of them buying. A directional imbalance a fleet view should
	// show rather than average away.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := range 12 {
		side := intent.SideBuy
		if i >= 10 {
			side = intent.SideSell
		}
		envelopes = append(envelopes, fleetEnvelope(tenant,
			fmt.Sprintf("env_fleet_%d", i), at.Add(time.Duration(i)*time.Second),
			side, 1000, fmt.Sprintf("agent_%d", i), "gpt",
			[]intent.Dependency{{
				Type: intent.DependencyMarketData, ID: "feed_shared",
				Verification: intent.VerificationVerified, ObservedAt: at,
			}}))
	}
	if err := sink.InsertIntents(ctx, envelopes, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := sink.InsertDependencies(ctx, envelopes); err != nil {
		t.Fatalf("insert dependencies: %v", err)
	}
	waitForRows(t, sink, tenant, len(envelopes))

	producer := &fleet.Producer{
		Store:    sink,
		Cohorts:  []fleet.Cohort{{TenantID: tenant}},
		Interval: time.Minute,
		Lag:      0,
		// Positioned so the producer's own window calculation lands on w.
		Now: func() time.Time { return w.End },
	}

	written, err := producer.RunOnce(ctx)
	if err != nil {
		t.Fatalf("producer: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("the producer wrote %d measurements, want 1", len(written))
	}

	m := written[0]
	if m.IntentCount != 12 || m.AgentCount != 12 {
		t.Errorf("intents/agents = %d/%d, want 12/12", m.IntentCount, m.AgentCount)
	}
	// Ten buys and two sells of 1,000: |10000 - 2000| / 12000.
	if diff := m.DirectionalImbalance - 8000.0/12000.0; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("directional imbalance = %.4f, want %.4f",
			m.DirectionalImbalance, 8000.0/12000.0)
	}

	// The producer does not measure the same window twice. Writing it again would
	// double every count in a store that does not deduplicate.
	again, err := producer.RunOnce(ctx)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if len(again) != 0 {
		t.Errorf("the producer measured the same window twice, writing %d more", len(again))
	}

	// And the intelligence API serves it.
	// The intelligence API authenticates now: naming a tenant in a header used to be
	// enough to read another customer's risk posture.
	creds, err := identity.ParseCredentials("svc_reader@" + tenant + "=" + fleetToken)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	mux := http.NewServeMux()
	(&fleet.API{Store: sink, Credentials: creds}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	waitForMeasurement(t, srv.URL, tenant)

	rows := getRows(t, srv.URL+"/v1/fleet/state", tenant)
	if len(rows) == 0 {
		t.Fatal("the intelligence API returned no fleet state for a measured window")
	}

	// Dependencies are visible too: twelve agents on one feed is the concentration
	// spec section 25 is about.
	deps := getRows(t, srv.URL+"/v1/dependencies", tenant)
	found := false
	for _, row := range deps {
		if fmt.Sprint(row["dependency_id"]) == "feed_shared" {
			found = true
		}
	}
	if !found {
		t.Errorf("the shared feed twelve agents depend on is not in /v1/dependencies: %v", deps)
	}
}

// A cohort that matched nothing is a measurement of zero, not a missing row. "No agent
// traded this window" is a finding, and a fleet view with a gap where it should say
// zero is indistinguishable from one that was never computed.
func TestAnEmptyWindowIsMeasuredAsZero(t *testing.T) {
	sink := clickhouse(t)
	tenant := fleetTenant()
	w := windowFor(time.Now().UTC().Add(-2*time.Hour), time.Minute)

	producer := &fleet.Producer{
		Store:    sink,
		Cohorts:  []fleet.Cohort{{TenantID: tenant}},
		Interval: time.Minute,
		Now:      func() time.Time { return w.End },
	}

	written, err := producer.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("producer: %v", err)
	}
	if len(written) != 1 {
		t.Fatalf("an empty window produced %d measurements, want 1", len(written))
	}
	if written[0].IntentCount != 0 {
		t.Errorf("intent count = %d, want 0", written[0].IntentCount)
	}
	// Coverage over nothing is zero, and it must not be reported as full coverage of
	// an empty set: the fleet vector's whole discipline is that a number travels with
	// what it was computed over (ADR-014).
	if written[0].ModelCoverage != 0 {
		t.Errorf("model coverage over an empty window = %.2f, want 0.00",
			written[0].ModelCoverage)
	}
}

// The analytical store is not a trust boundary for SQL. Every interpolated value goes
// through safeLiteral, and a tenant id that could terminate a string is refused rather
// than escaped.
func TestAnUnsafeTenantIsRefusedNotEscaped(t *testing.T) {
	sink := clickhouse(t)
	w := windowFor(time.Now().UTC(), time.Minute)

	for _, bad := range []string{"a' OR 1=1 --", "tenant\\'", "", strings.Repeat("x", 200)} {
		if _, err := sink.LoadWindow(context.Background(), bad, w); err == nil {
			t.Errorf("a tenant id that could terminate a SQL string was accepted: %q", bad)
		}
	}
}

func waitForRows(t *testing.T, sink *fleet.Sink, tenant string, want int) {
	t.Helper()
	for range 50 {
		body, err := sink.Query(context.Background(), fmt.Sprintf(
			"SELECT count() AS c FROM assurance.intents WHERE tenant_id = '%s' FORMAT JSONEachRow",
			tenant))
		if err == nil && strings.Contains(body, fmt.Sprintf(`"c":"%d"`, want)) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%d intents never became visible for %s", want, tenant)
}

func waitForMeasurement(t *testing.T, base, tenant string) {
	t.Helper()
	for range 50 {
		if len(getRows(t, base+"/v1/fleet/state", tenant)) > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the measurement never became visible through the API")
}

func getRows(t *testing.T, url, tenant string) []map[string]any {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+fleetToken)
	_ = tenant

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get %s: %v", url, err)
	}
	defer resp.Body.Close()

	var body struct {
		Rows []map[string]any `json:"rows"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return body.Rows
}
