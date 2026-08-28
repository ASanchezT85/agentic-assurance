package gateway

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// The pipeline is tested against the real engines, not mocks of them. A composition
// root tested against mocks proves the mocks compose.

var at = time.Date(2026, 8, 28, 14, 30, 0, 0, time.UTC)

// --- harness ---

type memGrants map[string]*authority.Grant

func (m memGrants) Load(_ context.Context, tenantID, grantID string) (*authority.Grant, error) {
	g, ok := m[grantID]
	if !ok {
		return nil, errNoGrant
	}
	return g, nil
}

type constError string

func (e constError) Error() string { return string(e) }

const errNoGrant = constError("no such grant")

type memBundles struct{ bundle *policy.Bundle }

func (m memBundles) Active(context.Context, string) (*policy.Bundle, error) {
	return m.bundle, nil
}

type memEvidence struct {
	mu     sync.Mutex
	events []evidence.Event
}

func (m *memEvidence) Append(_ context.Context, e evidence.Event) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, e)
	return true, nil
}

func (m *memEvidence) names() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.events))
	for _, e := range m.events {
		out = append(out, string(e.EventName))
	}
	return out
}

const policySource = `
version: 1
policy: pol_test
rules:
  - id: block_penny_stocks
    action: DENY
    when:
      instrument_id: instr_us_equity_PENNY
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 100000
`

func activeBundle(t *testing.T) *policy.Bundle {
	t.Helper()
	src, err := policy.ParseSource([]byte(policySource))
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_test", "bundle_test", at)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := bundle.Sign(priv, "test", at); err != nil {
		t.Fatalf("sign: %v", err)
	}
	// The full lifecycle, because INV-010 is expressed as data and a test that
	// skipped a step would be testing a bundle production could never produce.
	for _, to := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(to, at, "test"); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	return bundle
}

func grant() *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_test",
		TenantID:            "tenant_test",
		PrincipalID:         "prin_test",
		AccountID:           "acct_test",
		AgentID:             "agent_test",
		IssuedAt:            at.Add(-time.Hour),
		ValidFrom:           at.Add(-time.Hour),
		ValidUntil:          at.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102", "instr_us_equity_PENNY"},
		Limits: authority.Limits{
			PerOrderNotional:  500000,
			Rolling1hNotional: 1000000,
			DailyNotional:     2000000,
			MaxOpenOrders:     50,
		},
		Status: authority.StatusActive,
	}
}

func harness(t *testing.T) (*Pipeline, *fakebroker.Broker, *memEvidence) {
	t.Helper()
	fake := fakebroker.New()
	fake.SetClock(func() time.Time { return at })

	ev := &memEvidence{}
	usage := authority.NewMemoryUsage()
	p := &Pipeline{
		Identity:      &identity.Verifier{},
		Grants:        memGrants{"grant_test": grant()},
		Usage:         usage,
		UsageRecorder: usage,
		Policies:      memBundles{activeBundle(t)},
		Execution:     &execution.Service{Broker: fake, Store: execution.NewMemoryStore(), Now: func() time.Time { return at }},
		Symbols:       StaticSymbols{"instr_us_equity_00206R102": "AAPL", "instr_us_equity_PENNY": "PNY"},
		Evidence:      ev,
		Parent:        NewParentTracker(intent.DefaultClusterConfig),
		Now:           func() time.Time { return at },
	}
	return p, fake, ev
}

func envelope(mutate func(m map[string]any)) []byte {
	m := map[string]any{
		"schema_version":  "0.1",
		"envelope_id":     "env_01J8Z3K9QW",
		"idempotency_key": "idem-01J8Z3K9QW",
		"correlation_id":  "corr_01J8Z3K9QW",
		"received_at":     at.Format(time.RFC3339),
		"tenant_id":       "tenant_test",
		"principal":       map[string]any{"principal_id": "prin_test", "account_id": "acct_test", "principal_type": "INDIVIDUAL"},
		"agent": map[string]any{
			"agent_id":    "agent_test",
			"agent_type":  "EXECUTION",
			"operator_id": "op_test",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"authority_grant_id": "grant_test",
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102",
			"asset_class":   "EQUITY",
			"side":          "BUY",
			"order_type":    "LIMIT",
			"quantity":      10,
			"limit_price":   190.5,
			"time_in_force": "DAY",
		},
	}
	if mutate != nil {
		mutate(m)
	}
	raw, _ := json.Marshal(m)
	return raw
}

func presentedAPI() identity.Presented {
	return identity.Presented{APIIdentity: "svc_test"}
}

// --- the path ---

// The whole point of this package: an envelope arrives and an order reaches a venue,
// through every check in docs/architecture/hot-path.md.
func TestAnAuthorizedIntentReachesTheVenue(t *testing.T) {
	p, fake, ev := harness(t)

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())

	if !result.Accepted {
		t.Fatalf("a valid, authorized, policy-clean intent was refused at %s: %s (%s)",
			result.Stage, result.Code, result.Reason)
	}
	if result.Outcome == nil || result.Outcome.BrokerOrderID == "" {
		t.Fatal("the intent was accepted but nothing reached the venue")
	}
	if fake.Submissions("coid-idem-01J8Z3K9QW") != 1 {
		t.Errorf("the venue received %d submissions, want 1",
			fake.Submissions("coid-idem-01J8Z3K9QW"))
	}

	// The evidence chain of spec section 66 step 19, in order.
	want := []string{
		"agent.intent.received.v1",
		"agent.identity.verified.v1",
		"authority.evaluated.v1",
		"policy.evaluated.v1",
		"broker.order.submitted.v1",
	}
	got := ev.names()
	for _, name := range want {
		if !contains(got, name) {
			t.Errorf("the evidence chain is missing %s; got %v", name, got)
		}
	}
}

// Each stage refuses on its own, and the result names which one did. An operator
// reading a denial must not have to guess which check produced it.
func TestEachStageRefusesAndNamesItself(t *testing.T) {
	cases := []struct {
		name      string
		envelope  []byte
		presented identity.Presented
		wantStage string
	}{
		{
			name:      "malformed envelope",
			envelope:  envelope(func(m map[string]any) { delete(m, "idempotency_key") }),
			presented: presentedAPI(),
			wantStage: StageValidation,
		},
		{
			name:      "nothing authenticated the caller",
			envelope:  envelope(nil),
			presented: identity.Presented{},
			wantStage: StageIdentity,
		},
		{
			name: "the envelope claims more attestation than the transport established",
			envelope: envelope(func(m map[string]any) {
				agent := m["agent"].(map[string]any)
				agent["attestation"] = map[string]any{"level": "A2", "method": "spiffe"}
				agent["workload_identity"] = map[string]any{
					"spiffe_id": "spiffe://tenant.example/ns/prod/sa/agent",
				}
			}),
			presented: presentedAPI(),
			wantStage: StageIdentity,
		},
		{
			name:      "no such grant",
			envelope:  envelope(func(m map[string]any) { m["authority_grant_id"] = "grant_missing" }),
			presented: presentedAPI(),
			wantStage: StageAuthority,
		},
		{
			name: "policy denies the instrument",
			envelope: envelope(func(m map[string]any) {
				m["intent"].(map[string]any)["instrument_id"] = "instr_us_equity_PENNY"
			}),
			presented: presentedAPI(),
			wantStage: StagePolicy,
		},
		{
			name: "no venue symbol for the instrument",
			envelope: envelope(func(m map[string]any) {
				m["intent"].(map[string]any)["instrument_id"] = "instr_us_equity_UNKNOWN"
				m["authority_grant_id"] = "grant_test"
			}),
			presented: presentedAPI(),
			wantStage: StageAuthority, // the grant does not allow it either, and authority runs first
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, fake, _ := harness(t)
			result := p.Submit(context.Background(), tc.envelope, tc.presented)

			if result.Accepted {
				t.Fatal("the intent was accepted")
			}
			if result.Stage != tc.wantStage {
				t.Errorf("stage = %s, want %s (code %s: %s)",
					result.Stage, tc.wantStage, result.Code, result.Reason)
			}
			if result.Code == "" || result.Reason == "" {
				t.Error("a refusal with no code or no reason cannot be acted on")
			}
			if fake.Submissions("coid-idem-01J8Z3K9QW") != 0 {
				t.Error("a refused intent reached the venue")
			}
		})
	}
}

// INV-002. A duplicate returns the prior outcome and does not submit again, and it is
// checked before authority so an expiring grant cannot change the answer given to a
// caller that already got one.
func TestADuplicateReturnsThePriorOutcomeWithoutSubmittingAgain(t *testing.T) {
	p, fake, _ := harness(t)

	first := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if !first.Accepted {
		t.Fatalf("first submission refused: %s", first.Reason)
	}

	// The grant expires between the two calls. The duplicate must still get the
	// answer the first caller was given.
	p.Grants = memGrants{}

	second := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if !second.Replayed {
		t.Error("the duplicate was not marked as replayed")
	}
	if !second.Accepted {
		t.Errorf("the duplicate was refused at %s: %s; a prior outcome must be "+
			"returned unchanged (spec section 17)", second.Stage, second.Reason)
	}
	if n := fake.Submissions("coid-idem-01J8Z3K9QW"); n != 1 {
		t.Errorf("the venue received %d submissions for one idempotency key, want 1", n)
	}
}

// INV-004. An ambiguous outcome is 202 and not a failure, and the order is not
// resubmitted.
func TestAnAmbiguousOutcomeIsNotReportedAsFailure(t *testing.T) {
	p, fake, _ := harness(t)
	fake.InjectFault("coid-idem-01J8Z3K9QW", fakebroker.FaultTimeoutAfterReceipt)

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())

	// The venue took the order and the response was lost. Reconciliation found it,
	// so the outcome is known and the order stands. What must never happen is the
	// lost response becoming a rejection, or the order being sent twice (INV-004).
	if result.Outcome == nil {
		t.Fatal("a lost response produced no outcome at all")
	}
	if result.Outcome.State == broker.StateRejected {
		t.Error("a lost response became a rejection; the venue never refused anything")
	}
	if n := fake.Submissions("coid-idem-01J8Z3K9QW"); n != 1 {
		t.Errorf("the venue received %d submissions after a lost response, want 1", n)
	}
}

// An outcome that stays unresolved is 202, not 200 and not 500: the platform accepted
// the intent and does not yet know what the venue did.
func TestAnUnresolvedOutcomeIsAccepted202(t *testing.T) {
	unresolved := Result{Accepted: false, Stage: StageExecution, Code: "OUTCOME_UNKNOWN"}
	if got := statusFor(unresolved); got != http.StatusAccepted {
		t.Errorf("status = %d, want 202; an unknown outcome is neither success nor failure", got)
	}
}

// A hard policy that cannot be read denies (spec section 17).
func TestUnreadablePolicyDenies(t *testing.T) {
	p, _, _ := harness(t)
	p.Policies = failingBundles{}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if result.Accepted {
		t.Fatal("an intent was accepted while hard policy was unavailable")
	}
	if result.Stage != StagePolicy || result.Code != "POLICY_UNAVAILABLE" {
		t.Errorf("stage/code = %s/%s, want POLICY/POLICY_UNAVAILABLE", result.Stage, result.Code)
	}
}

type failingBundles struct{}

func (failingBundles) Active(context.Context, string) (*policy.Bundle, error) {
	return nil, constError("the bundle store is down")
}

// Losing evidence must not lose enforcement. Spec section 17: telemetry unavailable
// means buffer locally, not stop deciding.
func TestAFailingEvidenceSinkDoesNotStopEnforcement(t *testing.T) {
	p, _, _ := harness(t)
	p.Evidence = failingSink{}

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if !result.Accepted {
		t.Errorf("a decision failed because the audit trail was down: %s", result.Reason)
	}
}

type failingSink struct{}

func (failingSink) Append(context.Context, evidence.Event) (bool, error) {
	return false, constError("evidence store unavailable")
}

// --- HTTP surface ---

func TestSubmitEndpointRefusesAnUnauthenticatedCaller(t *testing.T) {
	p, _, _ := harness(t)
	creds, err := ParseCredentials("svc_test=" + strings.Repeat("k", 40))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	srv := httptest.NewServer(SubmitHandler(p, creds))
	t.Cleanup(srv.Close)

	// No credential at all.
	resp, err := http.Post(srv.URL, "application/json", strings.NewReader(string(envelope(nil))))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401; an unauthenticated caller must never produce "+
			"an executable order (INV-001)", resp.StatusCode)
	}
}

func TestSubmitEndpointAcceptsAnAuthenticatedCaller(t *testing.T) {
	p, _, _ := harness(t)
	token := strings.Repeat("k", 40)
	creds, err := ParseCredentials("svc_test=" + token)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	srv := httptest.NewServer(SubmitHandler(p, creds))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest(http.MethodPost, srv.URL, strings.NewReader(string(envelope(nil))))
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	var body map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", resp.StatusCode, body)
	}
	if body["accepted"] != true {
		t.Errorf("accepted = %v", body["accepted"])
	}
	// The bundle that decided travels with the decision.
	pol, _ := body["policy"].(map[string]any)
	if pol == nil || pol["bundle_hash"] == "" {
		t.Error("the response does not carry the bundle that produced the decision (ADR-010)")
	}
}

// A denial must not be a 200 with a field. A client that reads only the status code
// must not read a refusal as an acceptance.
func TestADenialIsNotASuccessStatus(t *testing.T) {
	denied := Result{Accepted: false, Stage: StagePolicy, Code: "DENY"}
	if got := statusFor(denied); got < 400 {
		t.Errorf("a policy denial returned HTTP %d", got)
	}
}

// A body larger than the cap is refused rather than read.
func TestAnOversizedEnvelopeIsRefused(t *testing.T) {
	p, _, _ := harness(t)
	creds, _ := ParseCredentials("svc_test=" + strings.Repeat("k", 40))
	srv := httptest.NewServer(SubmitHandler(p, creds))
	t.Cleanup(srv.Close)

	resp, err := http.Post(srv.URL, "application/json",
		strings.NewReader(strings.Repeat("x", MaxEnvelopeBytes+10)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", resp.StatusCode)
	}
}

func TestCredentialsRefuseShortTokens(t *testing.T) {
	if _, err := ParseCredentials("svc=short"); err == nil {
		t.Error("a guessable bearer token was accepted as the only thing standing " +
			"between an unauthenticated caller and an executable order")
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

// The reason the usage ledger exists. internal/authority named a PostgreSQL-backed
// UsageSource as the hot path's implementation since Phase 3 and there was none;
// nothing noticed because nothing ran the path. A limit that never trips is not a
// limit, so these spend a grant down until each one does.
//
// The open-orders cap is checked first because it is the one that fires first: with
// the venue accepting and holding orders, exposure accumulates faster than notional.

func spendDown(t *testing.T, p *Pipeline, n int) (int, Result) {
	t.Helper()
	for i := 1; i <= n; i++ {
		raw := envelope(func(m map[string]any) {
			m["idempotency_key"] = "idem-" + strconv.Itoa(i)
			m["envelope_id"] = "env_" + strconv.Itoa(i)
		})
		if r := p.Submit(context.Background(), raw, presentedAPI()); !r.Accepted {
			return i, r
		}
	}
	return 0, Result{}
}

func TestTheOpenOrderCapTripsOnceTheGrantIsFull(t *testing.T) {
	p, _, _ := harness(t)

	// The grant allows 50 open orders and the venue holds every one it accepts.
	at51, result := spendDown(t, p, 60)
	if at51 != 51 {
		t.Fatalf("the open-order cap tripped at order %d, want 51 (%s)", at51, result.Code)
	}
	if result.Code != "MAX_OPEN_ORDERS_EXCEEDED" {
		t.Errorf("code = %s, want MAX_OPEN_ORDERS_EXCEEDED", result.Code)
	}
}

func TestARollingLimitTripsOnceTheGrantIsSpent(t *testing.T) {
	p, _, _ := harness(t)

	// A grant with no open-order cap, so the rolling limit is the one under test.
	// 190.50 x 10 = 1,905 per order against a 1,000,000 rolling limit.
	g := grant()
	g.Limits.MaxOpenOrders = 0
	g.Limits.DailyNotional = 0
	p.Grants = memGrants{"grant_test": g}

	const perOrder, limit = 1905.0, 1000000.0
	want := int(math.Floor(limit/perOrder)) + 1

	tripped, result := spendDown(t, p, want+1)
	if tripped != want {
		t.Fatalf("the rolling limit tripped at order %d, want %d (%s: %s)",
			tripped, want, result.Code, result.Reason)
	}
	if result.Code != "ROLLING_LIMIT_EXCEEDED" {
		t.Errorf("code = %s, want ROLLING_LIMIT_EXCEEDED", result.Code)
	}
}

// A replayed submission must not spend the grant twice. Without this a client that
// retries after a lost response burns its own limit down.
func TestAReplayedSubmissionDoesNotSpendTheGrantTwice(t *testing.T) {
	p, _, _ := harness(t)
	usage := p.Usage.(*authority.MemoryUsage)

	for range 3 {
		if r := p.Submit(context.Background(), envelope(nil), presentedAPI()); !r.Accepted {
			t.Fatalf("refused: %s", r.Reason)
		}
	}

	snapshot, err := usage.Usage(context.Background(), "tenant_test", "grant_test", at)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if snapshot.Rolling1hNotional != 1905 {
		t.Errorf("three submissions of one idempotency key spent %.2f, want 1905.00",
			snapshot.Rolling1hNotional)
	}
}

// A duplicate must leave a trace. Without one the chain shows an intent arriving and
// no decision following it, which reads exactly like a request that vanished.
func TestAReplayIsRecordedInEvidence(t *testing.T) {
	p, _, ev := harness(t)

	if r := p.Submit(context.Background(), envelope(nil), presentedAPI()); !r.Accepted {
		t.Fatalf("first submission refused: %s", r.Reason)
	}
	before := len(ev.names())

	if r := p.Submit(context.Background(), envelope(nil), presentedAPI()); !r.Replayed {
		t.Fatal("the duplicate was not replayed")
	}
	if !contains(ev.names()[before:], string(evidence.IntentReplayed)) {
		t.Errorf("the replay left no evidence; the chain after it is %v", ev.names()[before:])
	}
}
