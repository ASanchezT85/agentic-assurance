package fleet

import (
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/money"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
)

var origin = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

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

func env(agentID string, side intent.Side, notional float64, offset time.Duration) *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion: intent.SchemaVersion,
		EnvelopeID:    agentID + "_" + offset.String(),
		TenantID:      "tenant_acme",
		ReceivedAt:    origin.Add(offset),
		Agent:         intent.Agent{AgentID: agentID},
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         side,
			OrderType:    intent.OrderMarket,
			Notional:     f(notional),
			TimeInForce:  intent.TIFDay,
		},
	}
}

func instrumentCohort(t *testing.T) Cohort {
	t.Helper()
	c, err := NewCohort("tenant_acme", Predicate{FieldInstrument, "instr_us_equity_00206R102"})
	if err != nil {
		t.Fatalf("cohort: %v", err)
	}
	return c
}

// A cohort is its predicates. Spec section 30 forbids an opaque identifier, so the
// id has to be derivable from the explanation and vice versa.
func TestCohortIsExplainedByItsPredicates(t *testing.T) {
	c, err := NewCohort("tenant_acme",
		Predicate{FieldSide, "BUY"},
		Predicate{FieldInstrument, "instr_x"})
	if err != nil {
		t.Fatalf("cohort: %v", err)
	}

	if got := c.Expression(); got != "instrument_id=instr_x AND side=BUY" {
		t.Errorf("expression = %q", got)
	}

	// Predicate order must not change identity, or the same group would get two ids.
	other, _ := NewCohort("tenant_acme",
		Predicate{FieldInstrument, "instr_x"},
		Predicate{FieldSide, "BUY"})
	if c.ID() != other.ID() {
		t.Errorf("predicate order changed the cohort id: %q vs %q", c.ID(), other.ID())
	}
}

func TestUnexplainableCohortsAreRefused(t *testing.T) {
	if _, err := NewCohort("tenant_acme", Predicate{"vibes", "high"}); err == nil {
		t.Error("a cohort keyed on an unknown field was accepted (spec section 30)")
	}
	if _, err := NewCohort("tenant_acme"); err == nil {
		t.Error("a cohort with no predicates was accepted; that is every intent")
	}
	if _, err := NewCohort("", Predicate{FieldSide, "BUY"}); err == nil {
		t.Error("a cohort with no tenant was accepted")
	}
}

// Directional imbalance is D from spec section 23.
func TestDirectionalImbalance(t *testing.T) {
	cases := []struct {
		name      string
		envelopes []*intent.AgentExecutionEnvelope
		want      float64
	}{
		{
			name: "fully one-directional",
			envelopes: []*intent.AgentExecutionEnvelope{
				env("a", intent.SideBuy, 1000, 0),
				env("b", intent.SideBuy, 1000, time.Second),
			},
			want: 1,
		},
		{
			name: "perfectly balanced",
			envelopes: []*intent.AgentExecutionEnvelope{
				env("a", intent.SideBuy, 1000, 0),
				env("b", intent.SideSell, 1000, time.Second),
			},
			want: 0,
		},
		{
			name: "three to one",
			envelopes: []*intent.AgentExecutionEnvelope{
				env("a", intent.SideBuy, 3000, 0),
				env("b", intent.SideSell, 1000, time.Second),
			},
			want: 0.5,
		},
	}

	w := Window{Start: origin, End: origin.Add(time.Minute)}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Measure(instrumentCohort(t), w, tc.envelopes)
			if math.Abs(m.DirectionalImbalance-tc.want) > 1e-9 {
				t.Errorf("D = %v, want %v", m.DirectionalImbalance, tc.want)
			}
		})
	}
}

// Gross and net must both survive. A cohort that bought and sold 10,000 each is not
// a cohort that did nothing, though their nets are identical (spec section 23).
func TestGrossAndNetBothSurvive(t *testing.T) {
	w := Window{Start: origin, End: origin.Add(time.Minute)}
	m := Measure(instrumentCohort(t), w, []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 10000, 0),
		env("b", intent.SideSell, 10000, time.Second),
	})

	if m.NetNotional != 0 {
		t.Errorf("net = %v, want 0", m.NetNotional)
	}
	if m.GrossNotional != 20000 {
		t.Errorf("gross = %v, want 20000; netting away the flow hides the activity", m.GrossNotional)
	}
}

// Intents whose size cannot be established are counted, not silently excluded.
func TestIndeterminateIntentsAreCounted(t *testing.T) {
	hidden := env("c", intent.SideBuy, 0, 2*time.Second)
	hidden.Intent.Notional = nil
	hidden.Intent.Quantity = q(500)

	w := Window{Start: origin, End: origin.Add(time.Minute)}
	m := Measure(instrumentCohort(t), w, []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
		hidden,
	})

	if m.IntentCount != 2 {
		t.Errorf("intent_count = %d, want 2", m.IntentCount)
	}
	if m.IndeterminateIntents != 1 {
		t.Errorf("indeterminate = %d, want 1", m.IndeterminateIntents)
	}
	if m.GrossNotional != 1000 {
		t.Errorf("gross = %v; the flow excludes what it could not size, and the count "+
			"says so", m.GrossNotional)
	}
}

// Windows are half-open, so consecutive windows neither double-count nor drop.
func TestWindowsAreHalfOpen(t *testing.T) {
	windows := RollingWindows(origin, origin.Add(3*time.Minute), time.Minute)
	if len(windows) != 3 {
		t.Fatalf("got %d windows, want 3", len(windows))
	}

	boundary := origin.Add(time.Minute)
	if windows[0].Contains(boundary) {
		t.Error("the first window contains its own end; consecutive windows would double-count")
	}
	if !windows[1].Contains(boundary) {
		t.Error("no window contains the boundary; an intent landing there would be dropped")
	}
}

// Concentration without coverage is uninterpretable (spec section 25).
func TestConcentrationCarriesCoverage(t *testing.T) {
	// Four intents: two declare model-x, one declares model-y, one declares nothing.
	envelopes := []*intent.AgentExecutionEnvelope{
		withModel(env("a", intent.SideBuy, 1000, 0), "model-x"),
		withModel(env("b", intent.SideBuy, 1000, time.Second), "model-x"),
		withModel(env("c", intent.SideBuy, 1000, 2*time.Second), "model-y"),
		env("d", intent.SideBuy, 1000, 3*time.Second),
	}

	index, coverage := ModelConcentration(envelopes)

	// Three observed declarations: 2/3 and 1/3 shares.
	want := (2.0/3)*(2.0/3) + (1.0/3)*(1.0/3)
	if math.Abs(index-want) > 1e-9 {
		t.Errorf("HHI = %v, want %v", index, want)
	}
	if math.Abs(coverage-0.75) > 1e-9 {
		t.Errorf("coverage = %v, want 0.75", coverage)
	}
}

// An undeclared model must not be counted as a family. Doing so would invent a large
// fictitious competitor and make every real concentration look smaller than it is.
func TestUnknownDeclarationsDoNotBecomeAFamily(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		withModel(env("a", intent.SideBuy, 1000, 0), "model-x"),
		env("b", intent.SideBuy, 1000, time.Second),
		env("c", intent.SideBuy, 1000, 2*time.Second),
	}

	index, coverage := ModelConcentration(envelopes)
	if index != 1 {
		t.Errorf("HHI = %v; one declared family among the observed is full concentration, "+
			"and the two unknowns belong in the coverage instead", index)
	}
	if math.Abs(coverage-1.0/3) > 1e-9 {
		t.Errorf("coverage = %v, want 1/3", coverage)
	}
}

// No observations at all is zero coverage, not zero concentration. They mean
// different things and only one of them is a finding.
func TestNoObservationsIsZeroCoverage(t *testing.T) {
	index, coverage := ModelConcentration([]*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
	})
	if index != 0 || coverage != 0 {
		t.Errorf("index = %v, coverage = %v; both must be zero when nothing was observed",
			index, coverage)
	}
}

// Measurement is deterministic: no clock, no network, no model.
func TestMeasurementIsDeterministic(t *testing.T) {
	w := Window{Start: origin, End: origin.Add(time.Minute)}
	build := func() []*intent.AgentExecutionEnvelope {
		return []*intent.AgentExecutionEnvelope{
			env("a", intent.SideBuy, 1000, 0),
			env("b", intent.SideSell, 400, time.Second),
			env("c", intent.SideBuy, 700, 2*time.Second),
		}
	}

	first := Measure(instrumentCohort(t), w, build())
	for i := 0; i < 50; i++ {
		got := Measure(instrumentCohort(t), w, build())
		if got.DirectionalImbalance != first.DirectionalImbalance ||
			got.GrossNotional != first.GrossNotional ||
			got.NetNotional != first.NetNotional {
			t.Fatalf("run %d differed from the first", i)
		}
	}
}

// A cohort never spans tenants.
func TestCohortsNeverSpanTenants(t *testing.T) {
	other := env("a", intent.SideBuy, 1000, 0)
	other.TenantID = "tenant_globex"

	w := Window{Start: origin, End: origin.Add(time.Minute)}
	m := Measure(instrumentCohort(t), w, []*intent.AgentExecutionEnvelope{other})

	if m.IntentCount != 0 {
		t.Errorf("a cohort counted %d intents from another tenant (INV-007)", m.IntentCount)
	}
}

func withModel(e *intent.AgentExecutionEnvelope, family string) *intent.AgentExecutionEnvelope {
	e.RuntimeClaims.ModelFamily = intent.Claim{Value: family, Verification: intent.VerificationDeclared}
	return e
}

// A tenant-wide cohort must be nameable. Rendering no predicates as the empty string
// produced the id "cohort_", which names nothing and collides with itself.
func TestATenantWideCohortHasAName(t *testing.T) {
	c := Cohort{TenantID: "tenant_x"}
	if c.Expression() != "all" {
		t.Errorf("expression = %q, want %q", c.Expression(), "all")
	}
	if c.ID() != "cohort_all" {
		t.Errorf("id = %q, want cohort_all", c.ID())
	}
}

// A measurement says how much of the flow the enforcement plane allowed through.
//
// The flow figures cover every decided intent, refused ones included, because the
// fleet vector measures intent: forty agents wanting to sell is the same signal
// whether or not they were allowed to. A live window made the case for the split:
// gross notional read 800,100 and the four largest orders in it had been refused.
func TestAMeasurementSaysHowMuchWasAuthorized(t *testing.T) {
	w := Window{
		Start: time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 28, 14, 1, 0, 0, time.UTC),
	}
	at := w.Start.Add(time.Second)
	n := func(v float64) *money.Amount {
		a, err := money.FromFloat(v)
		if err != nil {
			panic(err)
		}
		return &a
	}

	env := func(id string, notional float64) *intent.AgentExecutionEnvelope {
		return &intent.AgentExecutionEnvelope{
			EnvelopeID: id, TenantID: "tenant_x", ReceivedAt: at,
			Agent: intent.Agent{AgentID: "agent_" + id},
			Intent: intent.Intent{
				InstrumentID: "instr_1", AssetClass: intent.AssetEquity,
				Side: intent.SideBuy, OrderType: intent.OrderMarket, Notional: n(notional),
			},
		}
	}

	m := MeasureObserved(Cohort{TenantID: "tenant_x"}, w, []Observed{
		{Envelope: env("a", 1000), Authorized: true},
		{Envelope: env("b", 1000), Authorized: true},
		{Envelope: env("c", 100000), Authorized: false},
	})

	if m.IntentCount != 3 {
		t.Errorf("intent count = %d, want 3", m.IntentCount)
	}
	if m.AuthorizedIntents != 2 || m.RefusedIntents != 1 {
		t.Errorf("authorized/refused = %d/%d, want 2/1", m.AuthorizedIntents, m.RefusedIntents)
	}
	// The refused intent is still in the flow, because it is still intent. The split
	// is what stops a reader taking 102,000 for what reached a market.
	if m.GrossNotional != 102000 {
		t.Errorf("gross = %.0f, want 102000; the fleet vector measures intent", m.GrossNotional)
	}
	if m.AuthorizedIntents+m.RefusedIntents != m.IntentCount {
		t.Error("a decided intent was neither authorized nor refused")
	}
}

// Measure over envelopes alone treats every intent as authorized, which is right for
// callers that only have envelopes and must not silently report them all as refused.
func TestMeasureOverEnvelopesAssumesAuthorized(t *testing.T) {
	w := Window{
		Start: time.Date(2026, 8, 28, 14, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 28, 14, 1, 0, 0, time.UTC),
	}
	n := money.MustParse("1000")
	m := Measure(Cohort{TenantID: "tenant_x"}, w, []*intent.AgentExecutionEnvelope{{
		EnvelopeID: "a", TenantID: "tenant_x", ReceivedAt: w.Start.Add(time.Second),
		Agent: intent.Agent{AgentID: "agent_a"},
		Intent: intent.Intent{
			InstrumentID: "instr_1", AssetClass: intent.AssetEquity,
			Side: intent.SideBuy, OrderType: intent.OrderMarket, Notional: &n,
		},
	}})

	if m.AuthorizedIntents != 1 || m.RefusedIntents != 0 {
		t.Errorf("authorized/refused = %d/%d, want 1/0",
			m.AuthorizedIntents, m.RefusedIntents)
	}
}

// The intelligence API returns a customer's risk posture: directional imbalance, gross
// and net flow, agent count, and which models and feeds a fleet depends on. Naming a
// tenant in a header used to be enough to read all of it.
func TestTheIntelligenceAPIRequiresAuthentication(t *testing.T) {
	const token = "fleet-reader-token-of-32-plus-chars-ok"
	creds, err := identity.ParseCredentials("svc_reader@tenant_a=" + token)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}

	mux := http.NewServeMux()
	(&API{Store: stubReader{}, Credentials: creds}).Routes(mux)

	cases := []struct {
		name       string
		auth       string
		tenant     string
		wantStatus int
	}{
		{"no credential", "", "", http.StatusUnauthorized},
		{"header only", "", "tenant_a", http.StatusUnauthorized},
		{"wrong token", "Bearer " + token + "x", "", http.StatusUnauthorized},
		{"credential", "Bearer " + token, "", http.StatusOK},
		{"credential and agreeing header", "Bearer " + token, "tenant_a", http.StatusOK},
		{"credential and another tenant", "Bearer " + token, "tenant_victim", http.StatusForbidden},
	}

	for _, path := range []string{"/v1/fleet/state", "/v1/cohorts", "/v1/dependencies"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				req := httptest.NewRequest(http.MethodGet, path, nil)
				if tc.auth != "" {
					req.Header.Set("Authorization", tc.auth)
				}
				if tc.tenant != "" {
					req.Header.Set("X-Tenant-Id", tc.tenant)
				}
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, req)

				if rec.Code != tc.wantStatus {
					t.Errorf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
				}
			})
		}
	}
}

// With no credentials configured the endpoints refuse. There is no unauthenticated
// mode, and an operator who forgets the registry gets a closed door rather than an
// open one.
func TestTheIntelligenceAPIRefusesWithoutCredentials(t *testing.T) {
	mux := http.NewServeMux()
	(&API{Store: stubReader{}}).Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/state", nil)
	req.Header.Set("Authorization", "Bearer anything-at-all-thirty-two-characters")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

type stubReader struct{}

func (stubReader) Query(context.Context, string) (string, error) { return "", nil }

// The intelligence API supports workload certificates too.
//
// It used to construct a bare verifier inline, which accepts no SVID: mutual TLS
// worked on the submission endpoint and silently not here, so a caller with only a
// certificate could place orders and not read its own fleet. The verifier is a field
// now, so the choice is made at construction rather than by whichever handler
// instantiated one.
func TestTheIntelligenceAPIAcceptsAWorkloadCertificate(t *testing.T) {
	// No credentials at all: the certificate is the only way in.
	workloads, err := identity.ParseWorkloads("spiffe://acme.example/ns/readers/=tenant_a")
	if err != nil {
		t.Fatalf("workloads: %v", err)
	}

	root, leaf := issueSVID(t, "acme.example", "/ns/readers/sa/console")

	pool := x509.NewCertPool()
	pool.AddCert(root)

	api := &API{
		Store: stubReader{},
		Identity: &identity.Verifier{
			TrustDomain: "acme.example",
			Bundle:      pool,
			Workloads:   workloads,
		},
	}
	mux := http.NewServeMux()
	api.Routes(mux)

	req := httptest.NewRequest(http.MethodGet, "/v1/fleet/state", nil)
	req.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", rec.Code, rec.Body.String())
	}

	// And a workload nobody mapped is refused, with the same certificate machinery.
	unmapped := &API{
		Store: stubReader{},
		Identity: &identity.Verifier{
			TrustDomain: "acme.example",
			Bundle:      pool,
			Workloads:   mustWorkloads(t, "spiffe://acme.example/ns/other/=tenant_a"),
		},
	}
	other := http.NewServeMux()
	unmapped.Routes(other)

	req2 := httptest.NewRequest(http.MethodGet, "/v1/fleet/state", nil)
	req2.TLS = &tls.ConnectionState{PeerCertificates: []*x509.Certificate{leaf}}
	rec2 := httptest.NewRecorder()
	other.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("an unmapped workload got %d, want 401", rec2.Code)
	}
}

func mustWorkloads(t *testing.T, raw string) *identity.Workloads {
	t.Helper()
	w, err := identity.ParseWorkloads(raw)
	if err != nil {
		t.Fatalf("workloads: %v", err)
	}
	return w
}

// issueSVID mints a self-signed CA and a workload certificate under it.
func issueSVID(t *testing.T, trustDomain, path string) (root, leaf *x509.Certificate) {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: trustDomain},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca: %v", err)
	}
	root, err = x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("leaf key: %v", err)
	}
	uri, err := url.Parse("spiffe://" + trustDomain + path)
	if err != nil {
		t.Fatalf("uri: %v", err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		URIs:         []*url.URL{uri},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("leaf: %v", err)
	}
	leaf, err = x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	return root, leaf
}
