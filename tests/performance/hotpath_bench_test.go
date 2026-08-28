//go:build integration

package performance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"os"
	"sort"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// The gateway overhead targets of spec section 50.1: p50 < 2ms, p95 < 5ms,
// p99 < 10ms, excluding external network and broker latency.
//
// # What is measured, and what is not
//
// This times the deterministic enforcement path: decode and validate the envelope,
// evaluate the authority grant, evaluate the compiled policy bundle. That is the
// computation the platform performs on every intent.
//
// It excludes three things, all of them deliberately:
//
//   - Identity verification, which is an X.509 chain check whose cost belongs to
//     crypto/x509 rather than to this code, and which the transport does once per
//     connection rather than once per intent.
//   - The idempotency claim, which is one PostgreSQL round trip and is measured
//     separately below, because a database on the same laptop is not a database in a
//     data centre and mixing the two produces a number that describes neither.
//   - Everything after the synchronous boundary: telemetry, evidence, the broker.
//
// The section 50.1 target says "excluding external network/broker latency", so
// excluding the broker is what it asks for. Excluding the database is a narrower
// reading and is stated here rather than left for someone to discover.

var benchAt = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

func benchGrant() *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_bench",
		TenantID:            "tenant_bench",
		PrincipalID:         "principal_bench",
		AccountID:           "account_bench",
		AgentID:             "agent_bench",
		IssuedAt:            benchAt.Add(-24 * time.Hour),
		ValidFrom:           benchAt.Add(-time.Hour),
		ValidUntil:          benchAt.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity, intent.AssetETF},
		Limits:              authority.Limits{PerOrderNotional: 5000},
		Status:              authority.StatusActive,
	}
}

// benchBundle is the spec's own example policy, walked to production.
func benchBundle(t testing.TB) *policy.Bundle {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/policies/valid/retail_agent_standard.yaml")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	src, err := policy.ParseSource(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_bench", "bundle_bench", benchAt)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := bundle.Sign(priv, "bench", benchAt); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, benchAt, "bench"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	return bundle
}

func benchEnvelopeJSON() []byte {
	return []byte(`{
	  "schema_version": "0.1",
	  "envelope_id": "env_bench",
	  "idempotency_key": "idem_bench",
	  "correlation_id": "corr_bench",
	  "received_at": "2026-08-27T14:00:00Z",
	  "tenant_id": "tenant_bench",
	  "authority_grant_id": "grant_bench",
	  "principal": {"principal_id": "principal_bench", "account_id": "account_bench"},
	  "agent": {
	    "agent_id": "agent_bench",
	    "workload_identity": {"spiffe_id": "spiffe://acme.example/ns/agents/sa/bench"},
	    "attestation": {"level": "A2", "method": "SPIFFE_X509_SVID", "evidence_ref": "att_1"}
	  },
	  "runtime_claims": {
	    "model_provider": {"value": "provider-x", "verification": "DECLARED"},
	    "model_family": {"value": "model-y", "verification": "DECLARED"},
	    "model_version": {"value": "2026-08", "verification": "DECLARED"}
	  },
	  "dependencies": [
	    {"type": "MARKET_DATA", "id": "feed-a", "verification": "DECLARED",
	     "observed_at": "2026-08-27T13:59:59Z"}
	  ],
	  "intent": {
	    "asset_class": "EQUITY", "instrument_id": "instr_us_equity_00206R102",
	    "side": "BUY", "order_type": "MARKET", "notional": 1200,
	    "time_in_force": "DAY", "extended_hours": false
	  },
	  "lineage": {"strategy_id": "strategy_bench"},
	  "context": {"portfolio_snapshot_id": "ps_1", "market_snapshot_id": "ms_1"}
	}`)
}

// percentiles returns p50, p95 and p99 of a latency sample.
func percentiles(samples []time.Duration) (p50, p95, p99 time.Duration) {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	at := func(q float64) time.Duration {
		idx := int(q * float64(len(sorted)-1))
		return sorted[idx]
	}
	return at(0.50), at(0.95), at(0.99)
}

// TestGatewayOverheadTargets measures the section 50.1 targets and reports them.
//
// It asserts the targets rather than only logging, because unlike the ingest
// throughput number these are engineering targets the spec states outright, and a
// laptop is a slower environment than the one they describe. Failing here means the
// path got slow, not that the hardware is modest.
func TestGatewayOverheadTargets(t *testing.T) {
	const (
		warmup  = 2000
		samples = 20000
	)

	bundle := benchBundle(t)
	grant := benchGrant()
	raw := benchEnvelopeJSON()
	ctx := context.Background()

	run := func() {
		env, err := intent.Decode(raw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if d := authority.Evaluate(ctx, env, grant, nil, benchAt); !d.Allowed {
			t.Fatalf("authority denied the benchmark envelope: %s", d.Code)
		}
		if d := policy.Evaluate(bundle, env, benchAt); d.Action == policy.ActionDeny {
			t.Fatalf("policy denied the benchmark envelope: %s", d.Reason)
		}
	}

	for i := 0; i < warmup; i++ {
		run()
	}

	// Each sample times a batch and divides.
	//
	// A first version timed one call per sample and reported p50=0s and p95=0s. That
	// was not a fast path, it was a clock: on Windows the timer granularity is
	// coarser than a single evaluation, so most samples rounded to zero and the
	// percentiles were measuring the clock rather than the code. Batching puts each
	// sample well above the tick.
	const perSample = 200

	latencies := make([]time.Duration, 0, samples/perSample)
	for i := 0; i < samples/perSample; i++ {
		start := time.Now()
		for j := 0; j < perSample; j++ {
			run()
		}
		latencies = append(latencies, time.Since(start)/perSample)
	}

	p50, p95, p99 := percentiles(latencies)

	t.Logf("gateway overhead over %d evaluations in %d batches of %d: p50=%s p95=%s p99=%s",
		samples, len(latencies), perSample, p50, p95, p99)
	t.Log("measured: envelope decode and validation, authority evaluation, policy " +
		"evaluation. Excludes identity verification (per connection, not per intent), " +
		"the idempotency round trip (measured separately), and everything past the " +
		"synchronous boundary.")

	for _, target := range []struct {
		name  string
		got   time.Duration
		limit time.Duration
	}{
		{"p50", p50, 2 * time.Millisecond},
		{"p95", p95, 5 * time.Millisecond},
		{"p99", p99, 10 * time.Millisecond},
	} {
		if target.got > target.limit {
			t.Errorf("%s = %s, above the section 50.1 target of %s",
				target.name, target.got, target.limit)
		}
	}
}

// The idempotency claim, measured on its own.
//
// It is the one hot-path step that touches a database, and ADR-015 predicted this
// would be the largest single item in the latency budget. Measuring it separately is
// what makes that claim checkable rather than a guess in a comment.
func TestIdempotencyClaimLatency(t *testing.T) {
	pool := idemPoolForBench(t)
	if pool == nil {
		t.Skip("no PostgreSQL; skipping the idempotency latency measurement")
	}

	const samples = 500
	store := newBenchStore(pool)
	ctx := context.Background()

	latencies := make([]time.Duration, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		if err := store.claim(ctx, fmt.Sprintf("bench_%d_%d", time.Now().UnixNano(), i)); err != nil {
			t.Fatalf("claim: %v", err)
		}
		latencies = append(latencies, time.Since(start))
	}

	p50, p95, p99 := percentiles(latencies)
	t.Logf("idempotency claim over %d samples: p50=%s p95=%s p99=%s "+
		"(local Docker PostgreSQL, one transaction per claim)", samples, p50, p95, p99)
	t.Log("ADR-015 predicted this would be the largest single item in the section 50.1 " +
		"budget. Compare it against the enforcement figure above: if the sum exceeds " +
		"the target, the remedy is batching or a local write-ahead log, never moving " +
		"idempotency truth into Redis.")
}
