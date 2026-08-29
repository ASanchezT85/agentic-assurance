package policy

import (
	"agentic-assurance/internal/money"
	"crypto/ed25519"
	"crypto/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
)

var at = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

const fixtureRoot = "../../tests/fixtures/policies"

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

func loadFixture(t *testing.T, rel string) *Source {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureRoot, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	src, err := ParseSource(raw)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	return src
}

func fixtures(t *testing.T, sub string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(fixtureRoot, sub, "*.yaml"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no fixtures in %s", sub)
	}
	sort.Strings(matches)
	return matches
}

func mustCompile(t *testing.T, rel string) *Bundle {
	t.Helper()
	b, err := Compile(loadFixture(t, rel), "tenant_acme", "bundle_1", at)
	if err != nil {
		t.Fatalf("compile %s: %v", rel, err)
	}
	return b
}

func envelope(mutate func(*intent.Intent)) *intent.AgentExecutionEnvelope {
	in := intent.Intent{
		AssetClass:   intent.AssetEquity,
		InstrumentID: "instr_us_equity_00206R102",
		Side:         intent.SideBuy,
		OrderType:    intent.OrderMarket,
		Notional:     f(4200),
		TimeInForce:  intent.TIFDay,
	}
	if mutate != nil {
		mutate(&in)
	}
	return &intent.AgentExecutionEnvelope{
		SchemaVersion: intent.SchemaVersion,
		EnvelopeID:    "env_1",
		TenantID:      "tenant_acme",
		Intent:        in,
	}
}

// Every valid fixture compiles. The spec's own example is one of them.
func TestValidFixturesCompile(t *testing.T) {
	for _, path := range fixtures(t, "valid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			src, err := ParseSource(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if _, err := Compile(src, "tenant_acme", "bundle_1", at); err != nil {
				t.Fatalf("compile: %v", err)
			}
		})
	}
}

// Every invalid fixture is rejected, at parse time or at compile time.
func TestInvalidFixturesAreRejected(t *testing.T) {
	for _, path := range fixtures(t, "invalid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			src, parseErr := ParseSource(raw)
			if parseErr != nil {
				return // rejected at parse time, which is fine
			}
			if _, err := Compile(src, "tenant_acme", "bundle_1", at); err == nil {
				t.Fatal("compiled successfully; this fixture must be rejected")
			}
		})
	}
}

// The behaviour the spec's example is meant to have. If this changes, either the
// semantics moved or the example did.
func TestSpecExampleBehaviour(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")

	cases := []struct {
		name      string
		mutate    func(*intent.Intent)
		want      Action
		decidedBy string
	}{
		{
			name:      "small equity order passes",
			mutate:    func(in *intent.Intent) { in.Notional = f(1000) },
			want:      ActionAllow,
			decidedBy: "NO_RULE_MATCHED",
		},
		{
			name:      "medium equity order needs approval",
			mutate:    func(in *intent.Intent) { in.Notional = f(4200) },
			want:      ActionRequireApproval,
			decidedBy: "LARGE_ORDER_APPROVAL",
		},
		{
			name:      "oversized equity order is denied",
			mutate:    func(in *intent.Intent) { in.Notional = f(6000) },
			want:      ActionDeny,
			decidedBy: "ORDER_MAX_NOTIONAL",
		},
		{
			name:      "options are denied outright",
			mutate:    func(in *intent.Intent) { in.AssetClass = intent.AssetOption; in.Notional = f(100) },
			want:      ActionDeny,
			decidedBy: "OPTIONS_DISABLED",
		},
		{
			name:      "exactly at the ceiling is allowed but needs approval",
			mutate:    func(in *intent.Intent) { in.Notional = f(5000) },
			want:      ActionRequireApproval,
			decidedBy: "LARGE_ORDER_APPROVAL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Evaluate(b, envelope(tc.mutate), at)
			if got.Action != tc.want {
				t.Errorf("action = %s, want %s (matched: %+v)", got.Action, tc.want, got.MatchedRules)
			}
			if got.DecidedBy != tc.decidedBy {
				t.Errorf("decided by %s, want %s", got.DecidedBy, tc.decidedBy)
			}
		})
	}
}

// Most restrictive wins, and rule order in the file does not change the outcome.
func TestResolutionIsOrderIndependent(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	forward := Evaluate(b, envelope(func(in *intent.Intent) { in.Notional = f(6000) }), at)

	reversed := *b
	reversed.Rules = make([]CompiledRule, len(b.Rules))
	for i, r := range b.Rules {
		reversed.Rules[len(b.Rules)-1-i] = r
	}
	backward := Evaluate(&reversed, envelope(func(in *intent.Intent) { in.Notional = f(6000) }), at)

	if forward.Action != backward.Action {
		t.Errorf("reordering the rules changed the outcome: %s vs %s", forward.Action, backward.Action)
	}
	if forward.Action != ActionDeny {
		t.Errorf("expected DENY, got %s", forward.Action)
	}
}

// A decision must name the exact bundle it came from. Phase 4 exit criterion.
func TestDecisionRecordsTheBundle(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	key, _, _ := signBundle(t, b)
	_ = key

	got := Evaluate(b, envelope(nil), at)

	if got.BundleID != b.BundleID {
		t.Errorf("bundle_id = %q", got.BundleID)
	}
	if got.Version != b.Version {
		t.Errorf("version = %d", got.Version)
	}
	if got.ContentHash == "" || got.ContentHash != b.ContentHash {
		t.Errorf("content hash = %q, bundle has %q", got.ContentHash, b.ContentHash)
	}
	if got.Status != b.Activation.Status {
		t.Errorf("status = %s", got.Status)
	}
}

// No bundle means DENY, not "nothing to check so proceed" (spec section 17).
func TestMissingBundleDenies(t *testing.T) {
	got := Evaluate(nil, envelope(nil), at)
	if got.Action != ActionDeny {
		t.Fatalf("action = %s, want DENY", got.Action)
	}
	if got.DecidedBy != "NO_BUNDLE" {
		t.Errorf("decided by %s", got.DecidedBy)
	}
}

// A notional rule must not be avoidable by omitting the notional.
func TestSizeRuleFiresWhenSizeIsUnknown(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")

	got := Evaluate(b, envelope(func(in *intent.Intent) {
		in.Notional = nil
		in.Quantity = q(1000)
		in.OrderType = intent.OrderMarket
	}), at)

	if got.Action == ActionAllow {
		t.Fatal("an order of unknown size slipped past every notional rule")
	}
}

// A limit order's exposure is bounded by its price, so size-dependent rules are
// evaluable.
func TestLimitOrderNotionalComesFromThePrice(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")

	got := Evaluate(b, envelope(func(in *intent.Intent) {
		in.Notional = nil
		in.Quantity = q(10)
		in.LimitPrice = f(100) // 1,000 notional: under every threshold
		in.OrderType = intent.OrderLimit
	}), at)

	if got.Action != ActionAllow {
		t.Errorf("action = %s, want ALLOW (matched: %+v)", got.Action, got.MatchedRules)
	}
}

func signBundle(t *testing.T, b *Bundle) (ed25519.PrivateKey, ed25519.PublicKey, error) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	if err := b.Sign(priv, "release-engineer", at); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return priv, pub, nil
}

func TestSignAndVerify(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	_, pub, _ := signBundle(t, b)

	if err := b.VerifySignature(pub); err != nil {
		t.Fatalf("a freshly signed bundle did not verify: %v", err)
	}
	if b.Activation.Status != StatusSigned {
		t.Errorf("status = %s, want SIGN", b.Activation.Status)
	}
}

// Tampering with the rules after signing must break verification. Verification
// recomputes the hash rather than trusting the one the bundle carries.
func TestTamperedBundleFailsVerification(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	_, pub, _ := signBundle(t, b)

	b.Rules[0].RequireNotionalLTE = f(1000000)

	if err := b.VerifySignature(pub); err == nil {
		t.Fatal("a bundle whose rules were rewritten after signing still verified")
	}
}

// Rewriting the hash to match the tampered rules must not help: the signature is
// over the hash, and the hash is recomputed from the contents.
func TestTamperedBundleWithRecomputedHashStillFails(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	_, pub, _ := signBundle(t, b)

	b.Rules[0].RequireNotionalLTE = f(1000000)
	recomputed, err := b.ComputeHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	b.ContentHash = recomputed

	if err := b.VerifySignature(pub); err == nil {
		t.Fatal("rewriting both the rules and the hash defeated the signature")
	}
}

// The same source must always produce the same hash, or the signature means nothing.
func TestHashIsDeterministic(t *testing.T) {
	first := mustCompile(t, "valid/retail_agent_standard.yaml")
	second := mustCompile(t, "valid/retail_agent_standard.yaml")

	h1, err := first.ComputeHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h2, err := second.ComputeHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("two compilations of one source hashed differently:\n  %s\n  %s", h1, h2)
	}
}

// Different policies must not collide.
func TestDifferentPoliciesHashDifferently(t *testing.T) {
	a := mustCompile(t, "valid/retail_agent_standard.yaml")
	b := mustCompile(t, "valid/observe_only.yaml")

	ha, _ := a.ComputeHash()
	hb, _ := b.ComputeHash()
	if ha == hb {
		t.Error("two different policies produced the same content hash")
	}
}

// Promotion is a pipeline, and lifecycle metadata is outside the hash so a promotion
// does not change the bundle's identity.
func TestPromotionDoesNotChangeIdentity(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	_, pub, _ := signBundle(t, b)
	before := b.ContentHash

	for _, s := range []Status{StatusSimulated, StatusShadow, StatusCanary, StatusActive} {
		if err := b.Transition(s, at, "release-engineer"); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
	}

	if b.ContentHash != before {
		t.Error("promoting a bundle changed its content hash")
	}
	if err := b.VerifySignature(pub); err != nil {
		t.Errorf("an activated bundle no longer verifies: %v", err)
	}
	if !b.Enforcing() {
		t.Error("an ACTIVE bundle does not report as enforcing")
	}
	if b.Activation.ActivatedAt == nil || b.Activation.ActivatedBy == "" {
		t.Error("activation metadata was not recorded")
	}
}

// Shadow and canary are not production enforcement.
func TestOnlyActiveEnforces(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	signBundle(t, b)

	for _, s := range []Status{StatusSimulated, StatusShadow, StatusCanary} {
		if err := b.Transition(s, at, "release-engineer"); err != nil {
			t.Fatalf("transition to %s: %v", s, err)
		}
		if b.Enforcing() {
			t.Errorf("a bundle in %s reported itself as enforcing production", s)
		}
	}
}

func TestRollbackNeedsAReason(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	signBundle(t, b)

	if err := b.Rollback(at, "operator", ""); err == nil {
		t.Fatal("a rollback with no reason was accepted")
	}
	if err := b.Rollback(at, "operator", "canary error rate"); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if b.Activation.Status != StatusRolledBack {
		t.Errorf("status = %s", b.Activation.Status)
	}
	if b.Activation.RolledBackBy == "" || b.Activation.RollbackReason == "" {
		t.Error("rollback was not audited")
	}
}

// A rolled-back bundle is terminal. Production policy is never edited in place; a new
// version is created instead (spec section 43).
func TestRolledBackIsTerminal(t *testing.T) {
	b := mustCompile(t, "valid/retail_agent_standard.yaml")
	signBundle(t, b)
	if err := b.Rollback(at, "operator", "regression"); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	for _, s := range []Status{StatusActive, StatusShadow, StatusCanary, StatusDraft} {
		if err := b.Transition(s, at, "operator"); err == nil {
			t.Errorf("a rolled-back bundle was moved to %s", s)
		}
	}
}

func TestUnknownFieldsInSourceAreRejected(t *testing.T) {
	_, err := ParseSource([]byte("version: 1\npolicy: p\nrules:\n  - id: R\n    actn: DENY\n"))
	if err == nil {
		t.Fatal("a misspelled key was accepted; it would silently disable the rule")
	}
}
