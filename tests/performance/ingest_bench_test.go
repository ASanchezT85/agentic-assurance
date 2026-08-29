//go:build integration

// Package performance measures what the platform actually does, and reports it
// whatever the number turns out to be.
//
// Spec section 50.3 sets an MVP benchmark target of 10,000 intents/sec sustained.
// Phase 8's exit criterion is that the target is "attempted and measured", not that
// it is met, and this file is written so the measurement is honest either way: it
// prints the achieved rate and the conditions, and fails only on a floor low enough
// to mean something is broken rather than merely slow.
//
// Run with:  make up && make migrate && go test -tags=integration -bench=. ./tests/performance/
package performance

import (
	"agentic-assurance/internal/money"
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/intent"
)

const benchTenant = "tenant_bench"

func sink() *fleet.Sink {
	base := os.Getenv("CLICKHOUSE_HTTP_URL")
	if base == "" {
		base = "http://localhost:8123"
	}
	user := os.Getenv("CLICKHOUSE_USER")
	if user == "" {
		user = "assurance"
	}
	pass := os.Getenv("CLICKHOUSE_PASSWORD")
	if pass == "" {
		pass = "assurance_dev_only"
	}
	return fleet.NewSink(base, user, pass)
}

// f and q build the exact financial types a decoded envelope carries. Tests may start
// from a float literal for readability; the platform never does.
func f(v float64) *money.Amount {
	a, err := money.FromFloat(v)
	if err != nil {
		panic(err)
	}
	return &a
}

func q(v float64) *money.Quantity {
	x, err := money.QuantityFromFloat(v)
	if err != nil {
		panic(err)
	}
	return &x
}

// buildIntents makes a deterministic stream. The mix matters: a stream of identical
// intents would compress and index far better than a real fleet, and would flatter
// the measurement.
func buildIntents(n int, start time.Time) []*intent.AgentExecutionEnvelope {
	instruments := []string{
		"instr_us_equity_00206R102", "instr_us_equity_88160R101",
		"instr_us_equity_02079K305", "instr_us_equity_594918104",
	}
	agents := []string{"agent_a", "agent_b", "agent_c", "agent_d", "agent_e"}
	feeds := []string{"feed-a", "feed-b", "feed-c"}
	models := []string{"model-x", "model-y", ""}

	out := make([]*intent.AgentExecutionEnvelope, 0, n)
	for i := 0; i < n; i++ {
		side := intent.SideBuy
		if i%3 == 0 {
			side = intent.SideSell
		}
		at := start.Add(time.Duration(i) * time.Millisecond)

		e := &intent.AgentExecutionEnvelope{
			SchemaVersion:    intent.SchemaVersion,
			EnvelopeID:       "env_bench_" + strconv.Itoa(i),
			IdempotencyKey:   "idem_bench_" + strconv.Itoa(i),
			CorrelationID:    "corr_bench_" + strconv.Itoa(i/10),
			ReceivedAt:       at,
			TenantID:         benchTenant,
			AuthorityGrantID: "grant_bench",
			Principal:        intent.Principal{PrincipalID: "principal_bench", AccountID: "account_bench"},
			Agent: intent.Agent{
				AgentID:     agents[i%len(agents)],
				Attestation: intent.Attestation{Level: intent.AttestationA2},
			},
			RuntimeClaims: intent.RuntimeClaims{
				ModelFamily: intent.Claim{Value: models[i%len(models)], Verification: intent.VerificationDeclared},
			},
			Lineage: intent.Lineage{StrategyID: "strategy_bench"},
			Dependencies: []intent.Dependency{{
				Type: intent.DependencyMarketData, ID: feeds[i%len(feeds)],
				Verification: intent.VerificationDeclared, ObservedAt: at,
			}},
			Intent: intent.Intent{
				AssetClass:   intent.AssetEquity,
				InstrumentID: instruments[i%len(instruments)],
				Side:         side,
				OrderType:    intent.OrderMarket,
				Notional:     f(1000 + float64(i%50)*17),
				TimeInForce:  intent.TIFDay,
			},
		}
		out = append(out, e)
	}
	return out
}

// TestIngestThroughput is the attempt spec section 50.3 asks for.
//
// It reports the achieved rate rather than asserting the target, because the
// hardware under it is a laptop running Docker Desktop and a number measured there
// says nothing about the MVP benchmark environment. What it does assert is a floor:
// below it, something is wrong rather than slow.
func TestIngestThroughput(t *testing.T) {
	const (
		total     = 50000
		batchSize = 5000
		workers   = 4

		// A floor, not the target. Ingest slower than this means the batching or
		// the connection is broken, which is worth failing over.
		floorPerSecond = 500.0
	)

	s := sink()
	ctx := context.Background()
	envelopes := buildIntents(total, time.Now().UTC().Truncate(time.Second))

	batches := make(chan []*intent.AgentExecutionEnvelope)
	errs := make(chan error, workers)

	var wg sync.WaitGroup
	wg.Add(workers)
	start := time.Now()

	for w := 0; w < workers; w++ {
		go func() {
			defer wg.Done()
			for batch := range batches {
				if err := s.InsertIntents(ctx, batch, nil); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	for i := 0; i < len(envelopes); i += batchSize {
		end := i + batchSize
		if end > len(envelopes) {
			end = len(envelopes)
		}
		batches <- envelopes[i:end]
	}
	close(batches)
	wg.Wait()
	elapsed := time.Since(start)

	select {
	case err := <-errs:
		t.Fatalf("ingest failed: %v", err)
	default:
	}

	rate := float64(total) / elapsed.Seconds()

	// The measurement is the deliverable. It is printed whether it is flattering or
	// not, with the conditions, so the number cannot be quoted without them.
	t.Logf("ingest: %d intents in %s = %.0f intents/sec "+
		"(%d workers, batches of %d, ClickHouse over HTTP, local Docker)",
		total, elapsed.Round(time.Millisecond), rate, workers, batchSize)
	t.Logf("spec section 50.3 MVP target is 10,000/sec sustained; this run reached %.0f%% of it "+
		"on development hardware", rate/10000*100)
	t.Log("READ THIS BEFORE QUOTING THE NUMBER: this measures ClickHouse ingest of " +
		"pre-built envelopes and nothing else. The section 50.3 target is about the " +
		"platform sustaining intents, which also costs envelope validation, identity " +
		"verification, authority evaluation, policy evaluation and a broker call. " +
		"Ingest being fast means ingest is not the bottleneck; it does not mean the " +
		"platform sustains this rate. The end-to-end figure needs the full path, which " +
		"is not wired yet.")

	if rate < floorPerSecond {
		t.Errorf("ingest reached %.0f intents/sec, below the %.0f floor; that is broken "+
			"rather than slow", rate, floorPerSecond)
	}
}

// The rows have to actually be there. A fast write that lands nowhere is not ingest.
func TestIngestedRowsAreQueryable(t *testing.T) {
	s := sink()
	ctx := context.Background()

	marker := fmt.Sprintf("instr_probe_%d", time.Now().UnixNano())
	envelopes := buildIntents(100, time.Now().UTC())
	for _, e := range envelopes {
		e.Intent.InstrumentID = marker
	}

	if err := s.InsertIntents(ctx, envelopes, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.InsertDependencies(ctx, envelopes); err != nil {
		t.Fatalf("insert dependencies: %v", err)
	}

	out, err := s.Query(ctx, fmt.Sprintf(
		"SELECT count() FROM assurance.intents WHERE instrument_id = '%s'", marker))
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got := parseCount(t, out); got != 100 {
		t.Errorf("ClickHouse holds %d of the 100 rows written", got)
	}

	deps, err := s.Query(ctx, fmt.Sprintf(
		"SELECT count() FROM assurance.dependency_observations WHERE envelope_id LIKE 'env_bench_%%' "+
			"AND dependency_id != ''"))
	if err != nil {
		t.Fatalf("query dependencies: %v", err)
	}
	if parseCount(t, deps) == 0 {
		t.Error("no dependency observations were recorded")
	}
}

// Verification levels must survive the round trip exactly. A DECLARED that comes back
// as VERIFIED would make every concentration figure computed from this table a lie
// (INV-008).
func TestVerificationLevelsSurviveIngest(t *testing.T) {
	s := sink()
	ctx := context.Background()

	marker := fmt.Sprintf("feed_probe_%d", time.Now().UnixNano())
	envelopes := buildIntents(9, time.Now().UTC())
	levels := []intent.VerificationLevel{
		intent.VerificationVerified, intent.VerificationDeclared, intent.VerificationUnknown,
	}
	for i, e := range envelopes {
		e.Dependencies[0].ID = marker
		e.Dependencies[0].Verification = levels[i%len(levels)]
	}

	if err := s.InsertDependencies(ctx, envelopes); err != nil {
		t.Fatalf("insert: %v", err)
	}

	for _, level := range levels {
		out, err := s.Query(ctx, fmt.Sprintf(
			"SELECT count() FROM assurance.dependency_observations "+
				"WHERE dependency_id = '%s' AND verification = '%s'", marker, level))
		if err != nil {
			t.Fatalf("query %s: %v", level, err)
		}
		if got := parseCount(t, out); got != 3 {
			t.Errorf("%s: %d rows, want 3; verification levels must survive ingest "+
				"exactly as declared (INV-008)", level, got)
		}
	}
}

func parseCount(t *testing.T, raw string) int {
	t.Helper()
	trimmed := ""
	for _, r := range raw {
		if r >= '0' && r <= '9' {
			trimmed += string(r)
		}
	}
	if trimmed == "" {
		return 0
	}
	n, err := strconv.Atoi(trimmed)
	if err != nil {
		t.Fatalf("unparseable count %q", raw)
	}
	return n
}
