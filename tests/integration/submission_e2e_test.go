//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/policy"
)

// The submission path end to end: an HTTP request over the wire, every store in
// PostgreSQL, and a venue at the other end.
//
// The unit tests use in-memory stores and prove the pipeline asks the right questions
// in the right order. They cannot prove that the answers survive a round trip through
// the database, that row level security lets the gateway's own writes through, or that
// what an operator can later read back is the decision that was actually made. This
// does, which is why it needs real infrastructure.
//
// Run with:  make up && make migrate && make test-integration

const e2eToken = "e2e-token-of-at-least-thirty-two-chars"

// A second credential, because registering a signing key is a privilege the submission
// credential must not have.
const e2eRegistrarToken = "e2e-registrar-token-of-at-least-thirty-two-chars"

// Each rig gets its own tenant. A name built from a truncated clock is not unique:
// Truncate zeroes the nanoseconds, so every test in the same second shared a tenant
// and read the others' rows. That surfaced as a double charge against the grant, which
// is exactly what a real idempotency bug would look like.
var rigCounter atomic.Int64

func nextRigID() int64 { return rigCounter.Add(1) }

type e2eRig struct {
	pool     *pgxpool.Pool
	server   *httptest.Server
	broker   *fakebroker.Broker
	tenant   string
	grantID  string
	evidence *evidence.Store

	signingKey ed25519.PrivateKey

	// What a second replica of the same deployment needs. A replica is not a second
	// tenant: same grant, same keys, same policy on disk, same venue — a different
	// process, with its own connection pool, sharing one database. That is where the
	// authority ceiling has to hold, because the only thing the two share is
	// PostgreSQL.
	bundleDir      string
	symbolPath     string
	publicKeyHex   string
	registrarToken string
	grants         *authority.Store
	keys           *identity.KeyStore
	credential     string
	now            func() time.Time
}

// replica starts a second gateway over the same database, as a separate process would.
func (r *e2eRig) replica(t *testing.T) *e2eRig {
	t.Helper()

	// Its own pool, which is the part that matters: two pools cannot coordinate in
	// process memory, so any limit that holds here holds because the database made it
	// hold.
	pool := usagePool(t)

	bundles, err := gateway.NewFileBundles(r.bundleDir, r.publicKeyHex)
	if err != nil {
		t.Fatalf("replica bundles: %v", err)
	}
	// The replica reads the same accepted transitions from the same database, which is
	// what makes two processes agree about which policy is in force.
	bundles.Activations = policy.NewActivationStore(pool)
	symbols, err := gateway.LoadSymbols(r.symbolPath)
	if err != nil {
		t.Fatalf("replica symbols: %v", err)
	}
	usage := authority.NewPostgresUsage(pool)
	pipeline := &gateway.Pipeline{
		Identity: &identity.Verifier{},
		Grants:   gateway.StoreGrants{Store: authority.NewStore(pool)},
		Policies: bundles,
		Usage:    usage,
		Reserve:  usage,
		Keys:     identity.NewKeyStore(pool),
		Execution: &execution.Service{
			Broker: r.broker,
			Store:  execution.NewPostgresStore(pool),
			Now:    r.now,
		},
		Symbols:  symbols,
		Evidence: evidence.NewStore(pool),
		Parent:   gateway.NewParentTracker(intent.DefaultClusterConfig),
		Now:      r.now,
	}
	creds, err := identity.ParseCredentials(r.credential)
	if err != nil {
		t.Fatalf("replica credentials: %v", err)
	}
	srv := httptest.NewServer(gateway.SubmitHandler(pipeline, creds))
	t.Cleanup(srv.Close)

	second := *r
	second.pool = pool
	second.server = srv
	second.evidence = evidence.NewStore(pool)
	return &second
}

func newE2ERig(t *testing.T, now time.Time) *e2eRig {
	t.Helper()
	pool := usagePool(t)
	ctx := context.Background()

	tenant := fmt.Sprintf("tenant_e2e_%d_%d", time.Now().UnixNano(), nextRigID())
	grantID := "grant_e2e"

	t.Cleanup(func() {
		purge(t, pool, tenant,
			"authority_usage", "idempotency_records", "evidence_events", "authority_grants")
	})

	// The grant, in the database, the way one would actually arrive.
	grants := authority.NewStore(pool)
	if err := grants.Save(ctx, &authority.Grant{
		GrantID:             grantID,
		TenantID:            tenant,
		PrincipalID:         "prin_e2e",
		AccountID:           "acct_e2e",
		AgentID:             "agent_e2e",
		IssuedAt:            now.Add(-time.Hour),
		ValidFrom:           now.Add(-time.Hour),
		ValidUntil:          now.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits: authority.Limits{
			PerOrderNotional:  money.MustParse("50000"),
			Rolling1hNotional: money.MustParse("10000"),
			MaxOpenOrders:     10,
		},
		Status: authority.StatusActive,
	}); err != nil {
		t.Fatalf("save grant: %v", err)
	}

	// A signed, ACTIVE bundle on disk plus the customer's signed authorization to
	// activate it. Verified by the provider rather than trusted, and the bundle alone
	// is not enough: promotion is a separate act with its own signature.
	dir := t.TempDir()
	activations := newReloadAuthority(t, tenant)
	pub := writeSignedBundle(t, dir, tenant, now, activations)
	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Activations = activations.store

	// Instrument reference data through the real loader, not a map literal.
	symbolPath := filepath.Join(dir, "instruments.json")
	if err := os.WriteFile(symbolPath,
		[]byte(`{"instr_us_equity_00206R102":"AAPL"}`), 0o600); err != nil {
		t.Fatalf("symbols: %v", err)
	}
	symbols, err := gateway.LoadSymbols(symbolPath)
	if err != nil {
		t.Fatalf("load symbols: %v", err)
	}

	// The agent's signing key, registered in the database the way one would arrive.
	// An end-to-end test that skipped signature verification would exercise a
	// pipeline nobody runs.
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	keys := identity.NewKeyStore(pool)
	if _, err := keys.Register(ctx, identity.AgentKey{
		TenantID: tenant, AgentID: "agent_e2e", KeyID: "key_e2e",
		Algorithm: identity.AlgorithmEd25519, PublicKey: pubKey,
		Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register key: %v", err)
	}

	venue := fakebroker.New()
	venue.SetClock(func() time.Time { return now })
	usage := authority.NewPostgresUsage(pool)
	evStore := evidence.NewStore(pool)

	pipeline := &gateway.Pipeline{
		Identity: &identity.Verifier{},
		Grants:   gateway.StoreGrants{Store: grants},
		Policies: bundles,
		Usage:    usage,
		Reserve:  usage,
		Keys:     keys,
		Execution: &execution.Service{
			Broker: venue,
			Store:  execution.NewPostgresStore(pool),
			Now:    func() time.Time { return now },
		},
		Symbols:  symbols,
		Evidence: evStore,
		Parent:   gateway.NewParentTracker(intent.DefaultClusterConfig),
		Now:      func() time.Time { return now },
	}

	// Two credentials: one that submits and one that may register signing keys. They are
	// separate because the platform separates them — a credential that could both submit
	// and decide which key is an agent could act as any agent in the tenant.
	credential := "svc_e2e@" + tenant + "=" + e2eToken +
		",svc_registrar@" + tenant + "=" + e2eRegistrarToken
	creds, err := identity.ParseCredentials(credential)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowKeyRegistrars("svc_registrar")

	mux := http.NewServeMux()
	mux.Handle("POST /v1/intents", gateway.SubmitHandler(pipeline, creds))
	// The rig's clock, not the wall clock. A key registered "now" by a handler reading
	// real time would not yet be valid at the frozen instant the pipeline evaluates
	// signatures against, and the failure would look like a rejected key rather than two
	// clocks. In production both are the same clock and a key is valid from the moment
	// it is registered.
	clock := func() time.Time { return now }
	mux.HandleFunc("POST /v1/agent-keys/revoke",
		gateway.RevokeAgentKeyHandler(keys, evStore, creds, nil, clock))
	mux.HandleFunc("POST /v1/agent-keys",
		gateway.RegisterAgentKeyHandler(keys, evStore, creds, nil, clock))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &e2eRig{pool: pool, server: srv, broker: venue, tenant: tenant,
		grantID: grantID, evidence: evStore, signingKey: privKey,
		bundleDir: dir, symbolPath: symbolPath, publicKeyHex: hex.EncodeToString(pub),
		credential:     credential,
		registrarToken: e2eRegistrarToken,
		grants:         grants,
		keys:           keys,
		now:            func() time.Time { return now }}
}

func writeSignedBundle(t *testing.T, dir, tenant string, now time.Time,
	authority *reloadAuthority) ed25519.PublicKey {
	t.Helper()
	src, err := policy.ParseSource([]byte(`
version: 1
policy: pol_e2e
rules:
  - id: no_extended_hours
    action: DENY
    when:
      extended_hours: true
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 25000
`))
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	bundle, err := policy.Compile(src, tenant, "bundle_e2e", now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	if err := bundle.Sign(priv, "e2e", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, to := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(to, now, "e2e"); err != nil {
			t.Fatalf("transition %s: %v", to, err)
		}
	}
	raw, _ := json.Marshal(bundle)
	if err := os.WriteFile(filepath.Join(dir, tenant+".json"), raw, 0o600); err != nil {
		t.Fatalf("write bundle: %v", err)
	}

	authorization := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          tenant,
		BundleID:          bundle.BundleID,
		BundleContentHash: bundle.ContentHash,
		Action:            policy.ActionActivate,
		Actor:             "e2e-setup",
		AuthorizedAt:      now,
		Nonce:             fmt.Sprintf("e2e_%s_%d", tenant, time.Now().UnixNano()),
	}
	if err := authorization.Sign(authority.priv, authority.keyID); err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	authRaw, _ := json.Marshal(authorization)
	if err := os.WriteFile(filepath.Join(dir, tenant+".activation.json"), authRaw, 0o600); err != nil {
		t.Fatalf("write authorization: %v", err)
	}
	return pub
}

func (r *e2eRig) post(t *testing.T, body string, withCredential bool) (int, map[string]any) {
	t.Helper()
	// The rig used to serve the submit handler at the root, so a bare URL reached it. It
	// serves a mux now — the key endpoints live beside submission — and a request to the
	// root would be a 404 that reads like a refusal.
	req, _ := http.NewRequest(http.MethodPost, r.server.URL+"/v1/intents", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if withCredential {
		req.Header.Set("Authorization", "Bearer "+e2eToken)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("response is not JSON (%d): %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, decoded
}

func (r *e2eRig) envelope(now time.Time, key string, mutate func(map[string]any)) string {
	m := map[string]any{
		"schema_version":  "0.1",
		"envelope_id":     "env_" + key,
		"idempotency_key": key,
		"correlation_id":  "corr_" + key,
		"received_at":     now.Format(time.RFC3339),
		"tenant_id":       r.tenant,
		"principal": map[string]any{
			"principal_id": "prin_e2e", "account_id": "acct_e2e", "principal_type": "INDIVIDUAL",
		},
		"agent": map[string]any{
			"agent_id": "agent_e2e", "agent_type": "EXECUTION", "operator_id": "op_e2e",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"authority_grant_id": r.grantID,
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

	// Signed with the agent's registered key, over the canonical form.
	value, err := identity.SignEnvelope(raw, r.signingKey)
	if err != nil {
		panic(err)
	}
	m["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": "key_e2e", "value": value,
	}
	signed, _ := json.Marshal(m)
	return string(signed)
}

// grantForAgent issues authority for an agent other than the rig's own.
//
// Onboarding needs one: a freshly registered key can sign, and a signature without a grant
// is refused at the next stage, so a test that stopped at the signature would prove half
// the path.
func (r *e2eRig) grantForAgent(t *testing.T, agentID string, now time.Time) string {
	t.Helper()
	grantID := "grant_" + agentID
	err := r.grants.Save(context.Background(), &authority.Grant{
		GrantID:             grantID,
		TenantID:            r.tenant,
		PrincipalID:         "prin_e2e",
		AccountID:           "acct_e2e",
		AgentID:             agentID,
		IssuedAt:            now.Add(-time.Hour),
		ValidFrom:           now.Add(-time.Hour),
		ValidUntil:          now.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits: authority.Limits{
			PerOrderNotional:  money.MustParse("50000"),
			Rolling1hNotional: money.MustParse("100000"),
			MaxOpenOrders:     50,
		},
		Status: authority.StatusActive,
	})
	if err != nil {
		t.Fatalf("grant for %s: %v", agentID, err)
	}
	return grantID
}

// envelopeSignedBy builds an envelope for a named agent, signed with a named key.
//
// The rig's own envelope() always signs as agent_e2e with the key the rig registered
// directly. Onboarding is about a key that arrived over the API, so this one takes both.
func (r *e2eRig) envelopeSignedBy(t *testing.T, now time.Time, key, agentID, keyID,
	grantID string, private ed25519.PrivateKey) string {

	t.Helper()
	m := map[string]any{
		"schema_version":  "0.1",
		"envelope_id":     "env_" + key,
		"idempotency_key": key,
		"correlation_id":  "corr_" + key,
		"received_at":     now.Format(time.RFC3339),
		"tenant_id":       r.tenant,
		"principal": map[string]any{
			"principal_id": "prin_e2e", "account_id": "acct_e2e", "principal_type": "INDIVIDUAL",
		},
		"agent": map[string]any{
			"agent_id": agentID, "agent_type": "EXECUTION", "operator_id": "op_e2e",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"authority_grant_id": grantID,
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102",
			"asset_class":   "EQUITY",
			"side":          "BUY",
			"order_type":    "LIMIT",
			"quantity":      1,
			"limit_price":   100,
			"time_in_force": "DAY",
		},
	}
	raw, _ := json.Marshal(m)

	value, err := identity.SignEnvelope(raw, private)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	m["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": keyID, "value": value,
	}
	signed, _ := json.Marshal(m)
	return string(signed)
}

// The whole path: HTTP in, order at the venue, and every store holding what it should.
func TestSubmissionEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)
	ctx := context.Background()

	key := strings.TrimPrefix(rig.tenant, "tenant_e2e") + "-key"
	status, body := rig.post(t, rig.envelope(now, key, nil), true)

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["accepted"] != true {
		t.Fatalf("accepted = %v: %v", body["accepted"], body)
	}

	// The order reached the venue, exactly once, under our identifier.
	clientOrderID := "coid-" + key
	if n := rig.broker.Submissions(clientOrderID); n != 1 {
		t.Errorf("the venue received %d submissions, want 1", n)
	}
	order, _ := body["order"].(map[string]any)
	if order == nil || order["broker_order_id"] == "" {
		t.Errorf("the response carries no venue order: %v", body)
	}

	// The decision names the bundle that produced it (ADR-010).
	pol, _ := body["policy"].(map[string]any)
	if pol == nil || pol["bundle_hash"] == "" || pol["bundle_id"] != "bundle_e2e" {
		t.Errorf("the decision does not carry its bundle: %v", body["policy"])
	}

	// The idempotency record is RESOLVED in PostgreSQL, not only in memory.
	store := execution.NewPostgresStore(rig.pool)
	record, err := store.Load(ctx, rig.tenant, key)
	if err != nil || record == nil {
		t.Fatalf("idempotency record not found: %v", err)
	}
	if record.State != execution.RecordResolved {
		t.Errorf("record state = %s, want RESOLVED", record.State)
	}
	if record.ClientOrderID != clientOrderID {
		t.Errorf("record client order id = %q, want %q", record.ClientOrderID, clientOrderID)
	}

	// The grant was charged. 190.50 x 10.
	usage := authority.NewPostgresUsage(rig.pool)
	snapshot, err := usage.Usage(ctx, rig.tenant, rig.grantID, now)
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if snapshot.Rolling1hNotional != money.MustParse("1905") {
		t.Errorf("the grant was charged %s, want 1905.00", snapshot.Rolling1hNotional)
	}

	// And the chain of spec section 66 step 19 is readable back out of the database:
	// agent -> intent -> authority -> policy -> broker order -> result.
	chain, err := rig.evidence.Chain(ctx, rig.tenant, "corr_"+key)
	if err != nil {
		t.Fatalf("evidence chain: %v", err)
	}
	want := []evidence.EventName{
		evidence.IntentReceived,
		evidence.IdentityVerified,
		evidence.AuthorityEvaluated,
		evidence.PolicyEvaluated,
		// Capacity held, then the receipt, then what the platform knows after trying.
		// The receipt used to say broker.order.submitted before the broker was called.
		evidence.AuthorityReserved,
		evidence.DecisionCommitted,
		evidence.SubmissionAttempted,
		evidence.AuthorityReservationCommitted,
		evidence.OrderAccepted,
	}
	seen := map[evidence.EventName]bool{}
	for _, e := range chain {
		seen[e.EventName] = true
	}
	for _, name := range want {
		if !seen[name] {
			t.Errorf("the stored evidence chain is missing %s; got %d events", name, len(chain))
		}
	}
}

// A duplicate over HTTP returns the prior outcome and does not reach the venue again,
// with the record in PostgreSQL doing the deduplicating rather than a process cache.
func TestDuplicateSubmissionOverHTTP(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	key := strings.TrimPrefix(rig.tenant, "tenant_e2e") + "-key"
	envelope := rig.envelope(now, key, nil)

	if status, body := rig.post(t, envelope, true); status != http.StatusOK {
		t.Fatalf("first: status = %d: %v", status, body)
	}
	status, body := rig.post(t, envelope, true)
	if status != http.StatusOK {
		t.Fatalf("duplicate: status = %d: %v", status, body)
	}
	if body["replayed"] != true {
		t.Errorf("the duplicate was not marked replayed: %v", body)
	}
	if n := rig.broker.Submissions("coid-" + key); n != 1 {
		t.Errorf("the venue received %d submissions for one idempotency key, want 1", n)
	}

	// And it was not charged twice.
	snapshot, _ := authority.NewPostgresUsage(rig.pool).Usage(
		context.Background(), rig.tenant, rig.grantID, now)
	if snapshot.Rolling1hNotional != money.MustParse("1905") {
		t.Errorf("two submissions of one key charged %s, want 1905.00",
			snapshot.Rolling1hNotional)
	}
}

// Every refusal over the wire, with the status code a client would act on.
func TestRefusalsEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)

	cases := []struct {
		name       string
		credential bool
		mutate     func(map[string]any)
		wantStatus int
		wantStage  string
		wantCode   string
	}{
		{
			name:       "no credential",
			credential: false,
			wantStatus: http.StatusUnauthorized,
			wantStage:  gateway.StageIdentity,
			wantCode:   "ATTESTATION_CLAIM_EXCEEDS_EVIDENCE",
		},
		{
			name:       "malformed envelope",
			credential: true,
			mutate:     func(m map[string]any) { delete(m, "authority_grant_id") },
			wantStatus: http.StatusBadRequest,
			wantStage:  gateway.StageValidation,
			wantCode:   "ENVELOPE_INVALID",
		},
		{
			name:       "grant does not exist",
			credential: true,
			mutate:     func(m map[string]any) { m["authority_grant_id"] = "grant_nope" },
			wantStatus: http.StatusForbidden,
			wantStage:  gateway.StageAuthority,
			wantCode:   "GRANT_UNAVAILABLE",
		},
		{
			name:       "over the grant's per-order limit",
			credential: true,
			mutate: func(m map[string]any) {
				m["intent"].(map[string]any)["quantity"] = 1000
			},
			wantStatus: http.StatusForbidden,
			wantStage:  gateway.StageAuthority,
			wantCode:   "PER_ORDER_LIMIT_EXCEEDED",
		},
		{
			name:       "hard policy denies extended hours",
			credential: true,
			mutate: func(m map[string]any) {
				m["intent"].(map[string]any)["extended_hours"] = true
			},
			wantStatus: http.StatusForbidden,
			wantStage:  gateway.StagePolicy,
			wantCode:   "DENY",
		},
	}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := newE2ERig(t, now)
			key := fmt.Sprintf("idem-deny-%d-%s", i, rig.tenant)

			status, body := rig.post(t, rig.envelope(now, key, tc.mutate), tc.credential)

			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d: %v", status, tc.wantStatus, body)
			}
			if body["accepted"] == true {
				t.Error("a refused intent came back accepted")
			}
			if stage, _ := body["stage"].(string); stage != tc.wantStage {
				t.Errorf("stage = %q, want %q (%v)", stage, tc.wantStage, body["reason"])
			}
			// The code too, not only the stage. A POLICY_UNAVAILABLE and a rule that
			// fired both stop at POLICY, and a test that checked only the stage would
			// pass while the rule never ran.
			if code, _ := body["code"].(string); code != tc.wantCode {
				t.Errorf("code = %q, want %q (%v)", code, tc.wantCode, body["reason"])
			}
			if n := rig.broker.Submissions("coid-" + key); n != 0 {
				t.Errorf("a refused intent reached the venue %d times", n)
			}

			// Nothing was charged against the grant for an order that never went.
			snapshot, _ := authority.NewPostgresUsage(rig.pool).Usage(
				context.Background(), rig.tenant, rig.grantID, now)
			if snapshot.Rolling1hNotional != money.MustParse("0") {
				t.Errorf("a refused intent charged %s against the grant",
					snapshot.Rolling1hNotional)
			}
		})
	}
}

// A venue that takes the order and loses the response must not become a rejection, and
// must not be sent the order twice. INV-004 over the real path.
func TestLostVenueResponseEndToEnd(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	key := strings.TrimPrefix(rig.tenant, "tenant_e2e") + "-key"
	rig.broker.InjectFault("coid-"+key, fakebroker.FaultTimeoutAfterReceipt)

	status, body := rig.post(t, rig.envelope(now, key, nil), true)

	if status >= 500 {
		t.Errorf("a lost venue response became a server error (%d): %v", status, body)
	}
	order, _ := body["order"].(map[string]any)
	if order != nil && order["state"] == "REJECTED" {
		t.Error("a lost response became a rejection; the venue never refused anything")
	}
	if n := rig.broker.Submissions("coid-" + key); n != 1 {
		t.Errorf("the venue received %d submissions after a lost response, want 1", n)
	}
}
