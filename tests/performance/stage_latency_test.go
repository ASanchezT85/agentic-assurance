//go:build integration

package performance

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// The stages remediation added to the hot path, timed one at a time.
//
// An end-to-end figure from a load run is the number a customer feels, and it cannot say
// which stage owns it. Three stages went onto the critical path during remediation —
// signature verification, the atomic reservation, and the durable evidence receipt — and
// each one is a candidate for the next regression. Timed separately so a later run can
// say which of them moved.
//
// Local Docker PostgreSQL on a workstation. These numbers describe this machine.

// Signature verification: ed25519 over the canonical envelope.
//
// On the path since envelopes stopped being trusted to name their own agent. Pure
// computation, no database.
func TestSignatureVerificationLatency(t *testing.T) {
	const samples = 20000

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keys := identity.NewMemoryKeys()
	keys.Add(identity.AgentKey{
		TenantID: "tenant_bench", AgentID: "agent_bench", KeyID: "key_bench",
		Algorithm: identity.AlgorithmEd25519, PublicKey: pub,
		Status: "ACTIVE", ValidFrom: benchAt.Add(-time.Hour),
	})

	body := map[string]any{
		"schema_version":     "0.1",
		"envelope_id":        "env_bench",
		"idempotency_key":    "idem_bench",
		"correlation_id":     "corr_bench",
		"tenant_id":          "tenant_bench",
		"authority_grant_id": "grant_bench",
		"received_at":        benchAt.Format(time.RFC3339),
		"principal": map[string]any{
			"principal_id": "prin_bench", "account_id": "acct_bench",
			"principal_type": "INDIVIDUAL",
		},
		"agent": map[string]any{
			"agent_id": "agent_bench", "agent_type": "EXECUTION", "operator_id": "op_bench",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102", "asset_class": "EQUITY",
			"side": "BUY", "order_type": "MARKET", "notional": 1200,
			"time_in_force": "DAY",
		},
	}
	raw, _ := json.Marshal(body)
	value, err := identity.SignEnvelope(raw, priv)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	body["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": "key_bench", "value": value,
	}
	signed, _ := json.Marshal(body)

	// Decoded once. Decoding is measured by the enforcement benchmark above; what is
	// timed here is canonicalisation and the verify.
	env, err := intent.Decode(signed)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	// Timed in batches and divided.
	//
	// One verification takes less than this platform's clock can resolve: the Windows
	// timer moves in steps of about half a millisecond, so per-call timing reported a
	// p50 of exactly zero and a p95 that was the width of one tick. A figure produced
	// by the clock rather than by the code is worse than no figure, because it looks
	// like a measurement.
	const perBatch = 200
	ctx := context.Background()
	latencies := make([]time.Duration, 0, samples/perBatch)
	for range samples / perBatch {
		start := time.Now()
		for range perBatch {
			if err := identity.VerifyEnvelopeSignature(ctx, keys, signed, env, benchAt); err != nil {
				t.Fatalf("verify: %v", err)
			}
		}
		latencies = append(latencies, time.Since(start)/perBatch)
	}

	p50, p95, p99 := percentiles(latencies)
	t.Logf("signature verification over %d samples in batches of %d: p50=%s p95=%s "+
		"p99=%s (canonicalisation and one ed25519 verify, no database; percentiles are "+
		"over batch means, so they understate the tail)",
		samples, perBatch, p50, p95, p99)
}

// The atomic reservation: one serialized transaction per authorization.
//
// This is the stage that replaced a check-then-act, and it is the most expensive thing
// remediation put on the hot path — an advisory lock per grant, two aggregates and an
// insert, inside one transaction. Serialized per grant by construction, so its cost
// under contention is a queue rather than a race.
func TestReservationLatency(t *testing.T) {
	pool := idemPoolForBench(t)
	if pool == nil {
		t.Skip("no PostgreSQL; skipping the reservation latency measurement")
	}

	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_resbench_%d", now.UnixNano())

	grant := benchGrant()
	grant.TenantID = tenant
	grant.Limits = authority.Limits{
		PerOrderNotional:  money.MustParse("50000"),
		Rolling1hNotional: money.MustParse("500000000"),
		DailyNotional:     money.MustParse("500000000"),
		MaxOpenOrders:     1000000,
	}

	usage := authority.NewPostgresUsage(pool)
	const samples = 500
	latencies := make([]time.Duration, 0, samples)

	for i := range samples {
		key := fmt.Sprintf("res_%d_%d", now.UnixNano(), i)
		who := authority.ReservationIdentity{
			EnvelopeID: "env_" + key, PrincipalID: "prin_bench", AccountID: "acct_bench",
		}
		start := time.Now()
		decision, err := usage.Reserve(ctx, grant, key, money.MustParse("1200"), who, now)
		latencies = append(latencies, time.Since(start))
		if err != nil {
			t.Fatalf("reserve: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("reservation %d refused with %s; the limits are wide enough that a "+
				"refusal means the measurement is of something else", i, decision.Code)
		}
	}

	p50, p95, p99 := percentiles(latencies)
	t.Logf("authority reservation over %d samples: p50=%s p95=%s p99=%s "+
		"(advisory lock, two aggregates and an insert in one transaction)",
		samples, p50, p95, p99)
}

// The durable receipt: the decision is committed before anything reaches a venue.
//
// A batch insert of the events accumulated so far, in one transaction with the outbox
// rows. Nothing is sent until it returns, so its latency is on the customer's path.
func TestEvidenceReceiptLatency(t *testing.T) {
	pool := idemPoolForBench(t)
	if pool == nil {
		t.Skip("no PostgreSQL; skipping the evidence receipt measurement")
	}

	ctx := context.Background()
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_evbench_%d", now.UnixNano())
	store := evidence.NewStore(pool)

	// Six events is what a clean submission accumulates before the receipt commits:
	// received, identity, authority, policy, reserved, decision committed.
	const (
		samples    = 300
		perReceipt = 6
	)

	latencies := make([]time.Duration, 0, samples)
	for i := range samples {
		batch := make([]evidence.Event, 0, perReceipt)
		for j := range perReceipt {
			at := now.Add(time.Duration(i*perReceipt+j) * time.Millisecond)
			batch = append(batch, evidence.Event{
				SchemaVersion: evidence.SchemaVersion,
				EventID:       fmt.Sprintf("evb_%d_%d_%d", now.UnixNano(), i, j),
				EventName:     evidence.AuthorityEvaluated,
				TenantID:      tenant,
				AggregateID:   fmt.Sprintf("env_%d", i),
				CorrelationID: fmt.Sprintf("corr_%d", i),
				OccurredAt:    at,
				ProducedAt:    at,
				Producer:      "assurance-gateway",
				Sequence:      int64(j + 1),
				Payload:       map[string]any{"allowed": true, "grant_id": "grant_bench"},
			})
		}
		start := time.Now()
		if err := store.AppendBatch(ctx, batch); err != nil {
			t.Fatalf("receipt: %v", err)
		}
		latencies = append(latencies, time.Since(start))
	}

	p50, p95, p99 := percentiles(latencies)
	t.Logf("evidence receipt over %d samples: p50=%s p95=%s p99=%s "+
		"(%d events and their outbox rows in one transaction)", samples, p50, p95, p99, perReceipt)
}
