package scenarios

import (
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/incident"
	"agentic-assurance/internal/intent"
)

// Scenarios that exercise the fleet engine and the incident engine.
//
// They run against the real Go engines rather than the Digital Twin. The twin is for
// market dynamics and population scale (ADR-013); what these assert is what the
// platform concludes, and the twin's own assurance engine is explicitly not the
// production one.

// buildCohort is the instrument-and-side cohort every fleet scenario watches.
func buildCohort(t *testing.T, side intent.Side) fleet.Cohort {
	t.Helper()
	c, err := fleet.NewCohort("tenant_acme",
		fleet.Predicate{Field: fleet.FieldInstrument, Value: "instr_us_equity_00206R102"},
		fleet.Predicate{Field: fleet.FieldSide, Value: string(side)})
	if err != nil {
		t.Fatalf("cohort: %v", err)
	}
	return c
}

// fleetAgent builds one intent with full provenance, so concentration scenarios have
// something to concentrate on.
func fleetAgent(id string, side intent.Side, notional float64, offset time.Duration,
	model, feed, strategy string) *intent.AgentExecutionEnvelope {

	e := envelope(child{
		envelopeID: id, agentID: id, notional: notional, offset: offset, side: side,
		strategyID: strategy,
	})
	e.RuntimeClaims.ModelFamily = intent.Claim{Value: model, Verification: intent.VerificationDeclared}
	if feed != "" {
		e.Dependencies = []intent.Dependency{{
			Type: intent.DependencyMarketData, ID: feed,
			Verification: intent.VerificationDeclared, ObservedAt: origin.Add(offset),
		}}
	}
	return e
}

// baselineWith builds a baseline whose median is a given rate, with realistic spread.
func baselineWith(median float64, observations int) *fleet.Baseline {
	b := fleet.NewBaseline(fleet.BaselineContext{
		InstrumentID:  "instr_us_equity_00206R102",
		MarketSession: "US_REGULAR",
		HourUTC:       14,
	})
	for i := 0; i < observations; i++ {
		// A little spread, so MAD is non-zero and a robust score is computable.
		b.Observe(median + float64(i%5)*median*0.05)
	}
	return b
}

// S01 — Correlated stop-loss.
//
// A fast market decline trips similar risk thresholds across a fleet. Expected: the
// directional burst is detected, the synchronisation is visible, and nothing claims
// the agents were malicious.
func TestS01_CorrelatedStopLoss(t *testing.T) {
	// Forty agents all selling within twelve seconds, each running its own strategy
	// and its own model: they agree because the price moved, not because they
	// coordinated.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 40; i++ {
		envelopes = append(envelopes, fleetAgent(
			"agent_stop_"+itoa(i), intent.SideSell, 5000,
			time.Duration(i*300)*time.Millisecond,
			"model_"+itoa(i%4), "feed_"+itoa(i%3), "strategy_"+itoa(i%5)))
	}

	window := fleet.Window{Start: origin, End: origin.Add(15 * time.Second)}
	baseline := baselineWith(0.5, 120) // a quiet instrument

	vector := fleet.ComputeVector(buildCohort(t, intent.SideSell), window, envelopes, baseline, nil)

	if !vector.D.Known || vector.D.Value < 0.99 {
		t.Errorf("D = %v (known=%v); forty same-side sells is one-directional flow",
			vector.D.Value, vector.D.Known)
	}
	if !vector.B.Known || vector.B.Value <= 0 {
		t.Errorf("B = %v (known=%v); forty intents in fifteen seconds against a "+
			"baseline of 0.5/sec is a burst", vector.B.Value, vector.B.Known)
	}

	anomalies := incident.Detect(vector, incident.DefaultRules(), origin)
	if len(anomalies) == 0 {
		t.Fatal("a synchronised sell-off produced no anomalies")
	}

	inc, ok := incident.Open(vector, anomalies, incident.DefaultRules(), "corr_s01", origin)
	if !ok {
		t.Fatal("no incident was opened")
	}

	// The load-bearing assertion. Nothing may accuse these agents of anything: they
	// all hit their own stop-loss because the price fell, and spec section 41 says
	// so in as many words.
	rendered := strings.ToLower(inc.Recommended + " " + inc.SeverityRule)
	for _, a := range anomalies {
		rendered += " " + strings.ToLower(a.Observation+" "+a.Rule)
	}
	for _, accusation := range []string{"malicious", "attack", "manipulation", "abuse", "coordinated"} {
		if strings.Contains(rendered, accusation) {
			t.Errorf("the incident describes correlated stop-losses as %q; agents "+
				"reacting to the same price move are not colluding (spec section 41 S01)", accusation)
		}
	}

	// Strategy and model concentration must stay low: these agents share a trigger,
	// not a dependency. Reporting concentration here would be the false attribution
	// the scenario is about.
	if vector.Cs.Known && vector.Cs.Value > 0.5 {
		t.Errorf("Cs = %v; five strategies across forty agents is not concentrated",
			vector.Cs.Value)
	}
}

// S02 — Poisoned news.
//
// A shared news dependency serves false information. Expected: source concentration
// is detected, abnormal consensus rises, and the incident carries the dependency
// evidence.
func TestS02_PoisonedNews(t *testing.T) {
	// Thirty agents, five strategies, four models, and one feed between them.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 30; i++ {
		envelopes = append(envelopes, fleetAgent(
			"agent_news_"+itoa(i), intent.SideBuy, 4000,
			time.Duration(i*400)*time.Millisecond,
			"model_"+itoa(i%4), "feed_poisoned", "strategy_"+itoa(i%5)))
	}

	window := fleet.Window{Start: origin, End: origin.Add(15 * time.Second)}
	vector := fleet.ComputeVector(buildCohort(t, intent.SideBuy), window, envelopes,
		baselineWith(0.5, 120), nil)

	if !vector.Cf.Known {
		t.Fatal("feed concentration was not measured")
	}
	if vector.Cf.Value < 0.99 {
		t.Errorf("Cf = %v; thirty agents on one feed is total concentration", vector.Cf.Value)
	}
	if vector.Cf.Coverage < 0.99 {
		t.Errorf("Cf coverage = %v; every agent declared a feed", vector.Cf.Coverage)
	}

	// Model and strategy concentration must stay low, or the finding would be "these
	// agents are similar" rather than "these agents share a feed".
	if vector.Cm.Value > 0.5 {
		t.Errorf("Cm = %v; four models across thirty agents is not concentrated", vector.Cm.Value)
	}

	anomalies := incident.Detect(vector, incident.DefaultRules(), origin)
	inc, ok := incident.Open(vector, anomalies, incident.DefaultRules(), "corr_s02", origin)
	if !ok {
		t.Fatal("no incident was opened for a fleet sharing one poisoned feed")
	}

	// The dependency evidence is the finding. An incident that says "unusual buying"
	// without naming the shared feed sends an investigator looking in the wrong place.
	if len(inc.SharedDependencies) == 0 {
		t.Fatal("the incident carries no dependency evidence (spec section 41 S02)")
	}
	found := false
	for _, dep := range inc.SharedDependencies {
		if strings.Contains(dep, "Cf_feed_concentration") {
			found = true
		}
	}
	if !found {
		t.Errorf("the shared dependencies do not name the feed: %v", inc.SharedDependencies)
	}

	// And it survives into evidence, because the investigator six months later reads
	// that and not this test.
	for _, e := range inc.EventsFor("fleet-engine", 1) {
		if e.EventName != "incident.created.v1" {
			continue
		}
		deps, ok := e.Payload["shared_dependencies"].([]string)
		if !ok || len(deps) == 0 {
			t.Errorf("the created event carries no dependency evidence: %v", e.Payload)
		}
	}
}

// S04 — Model regression.
//
// A model family changes behaviour after an upgrade. Expected: the cohort's
// behaviour deviates from its baseline, and the dependency view identifies the
// common model declaration.
func TestS04_ModelRegression(t *testing.T) {
	// Twenty-five agents, all declaring the same model family, all on different
	// feeds and strategies: the model is the only thing they share.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 25; i++ {
		envelopes = append(envelopes, fleetAgent(
			"agent_model_"+itoa(i), intent.SideBuy, 6000,
			time.Duration(i*400)*time.Millisecond,
			"model_regressed", "feed_"+itoa(i%4), "strategy_"+itoa(i%5)))
	}

	window := fleet.Window{Start: origin, End: origin.Add(12 * time.Second)}
	baseline := baselineWith(0.4, 150)
	vector := fleet.ComputeVector(buildCohort(t, intent.SideBuy), window, envelopes, baseline, nil)

	if !vector.Cm.Known || vector.Cm.Value < 0.99 {
		t.Errorf("Cm = %v (known=%v); one model family across the cohort is total "+
			"concentration", vector.Cm.Value, vector.Cm.Known)
	}
	if vector.Cf.Value > 0.5 {
		t.Errorf("Cf = %v; four feeds is not the shared factor here", vector.Cf.Value)
	}

	// Behaviour deviates from the baseline, which is what makes this a regression
	// rather than an observation.
	if !vector.B.Known {
		t.Fatalf("no burst score: %s", vector.B.Explanation)
	}
	if vector.B.Value < 4 {
		t.Errorf("B = %v; the cohort's rate should stand well outside its baseline", vector.B.Value)
	}

	anomalies := incident.Detect(vector, incident.DefaultRules(), origin)
	namedModel := false
	for _, a := range anomalies {
		if strings.HasPrefix(a.Component.Name, "Cm_") {
			namedModel = true
		}
	}
	if !namedModel {
		t.Error("no anomaly named the model concentration; the dependency view has to " +
			"identify the common declaration (spec section 41 S04)")
	}
}

// S08 — Liquidity shock.
//
// Depth deteriorates while order flow continues. Expected: participation and
// liquidity metrics deteriorate, and a shadow recommendation is generated.
func TestS08_LiquidityShock(t *testing.T) {
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 30; i++ {
		envelopes = append(envelopes, fleetAgent(
			"agent_liq_"+itoa(i), intent.SideBuy, 20000,
			time.Duration(i*400)*time.Millisecond,
			"model_"+itoa(i%4), "feed_"+itoa(i%3), "strategy_"+itoa(i%5)))
	}
	window := fleet.Window{Start: origin, End: origin.Add(15 * time.Second)}
	cohort := buildCohort(t, intent.SideBuy)

	// The same flow against a healthy book and a shocked one. Only the venue volume
	// differs, which is what isolates the shock as the cause.
	healthy := fleet.ComputeVector(cohort, window, envelopes, baselineWith(0.5, 120),
		fixedVolume{notional: 600_000_000})
	shocked := fleet.ComputeVector(cohort, window, envelopes, baselineWith(0.5, 120),
		fixedVolume{notional: 6_000_000})

	if !healthy.P.Known || !shocked.P.Known {
		t.Fatalf("participation was not measured: healthy=%q shocked=%q",
			healthy.P.Explanation, shocked.P.Explanation)
	}
	if shocked.P.Value <= healthy.P.Value {
		t.Errorf("participation did not deteriorate: healthy=%v shocked=%v",
			healthy.P.Value, shocked.P.Value)
	}
	if shocked.P.Value < 0.05 {
		t.Errorf("P = %v; the same flow into a hundredth of the volume is a large "+
			"share of it", shocked.P.Value)
	}

	// A recommendation is generated, and it is a recommendation.
	anomalies := incident.Detect(shocked, incident.DefaultRules(), origin)
	inc, ok := incident.Open(shocked, anomalies, incident.DefaultRules(), "corr_s08", origin)
	if !ok {
		t.Fatal("no incident was opened during a liquidity shock")
	}
	if !strings.Contains(inc.Recommended, "would recommend") {
		t.Errorf("the recommendation reads as an action: %q", inc.Recommended)
	}
	if inc.Status != incident.StatusOpen {
		t.Errorf("status = %s; nothing was enforced, so nothing should have moved past open", inc.Status)
	}
}

// S12 — Normal consensus.
//
// A legitimate material event causes broad rational agreement. Expected: consensus
// may be high, the system does not automatically label it abnormal, and the false
// intervention rate is measured.
//
// This is the scenario that keeps the product honest. Every other scenario rewards
// detection; this one punishes over-detection, and a fleet engine that fails it is
// one that would cry wolf on every earnings day.
func TestS12_NormalConsensus(t *testing.T) {
	// A real event: 60 agents, all buying, all within twenty seconds, across four
	// models, three feeds and five strategies. They agree because something
	// happened, and nothing about them is concentrated.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 60; i++ {
		envelopes = append(envelopes, fleetAgent(
			"agent_news_event_"+itoa(i), intent.SideBuy, 3000,
			time.Duration(i*300)*time.Millisecond,
			"model_"+itoa(i%4), "feed_"+itoa(i%3), "strategy_"+itoa(i%5)))
	}

	window := fleet.Window{Start: origin, End: origin.Add(20 * time.Second)}
	baseline := baselineWith(0.3, 200)
	vector := fleet.ComputeVector(buildCohort(t, intent.SideBuy), window, envelopes, baseline, nil)

	// Consensus is real and the system says so. Suppressing D would be the opposite
	// failure: hiding a fact because it is inconvenient.
	if !vector.D.Known || vector.D.Value < 0.99 {
		t.Fatalf("D = %v; sixty same-side buys is unanimous agreement and the system "+
			"must still report it", vector.D.Value)
	}

	// But abnormal consensus must not rise. The burst explains the agreement.
	if vector.A.Known && vector.A.Value > 0.1 {
		t.Errorf("A = %v with an explanation of %q; agreement during a real event is "+
			"ordinary, and the burst already accounts for it (spec section 41 S12)",
			vector.A.Value, vector.A.Explanation)
	}

	// The agreement itself must not be reported as an anomaly. The burst may be, and
	// should be: activity really did spike, and saying so is useful. What the system
	// must not do is treat the agreement as the finding.
	anomalies := incident.Detect(vector, incident.DefaultRules(), origin)
	for _, a := range anomalies {
		if strings.HasPrefix(a.Component.Name, "D_") {
			t.Errorf("one-directional flow was reported as an anomaly during a "+
				"legitimate event: %s (%s). The burst already explains the agreement, "+
				"and flagging it is the over-detection S12 exists to prevent",
				a.Observation, a.Rule)
		}
		if strings.HasPrefix(a.Component.Name, "C") {
			t.Errorf("a concentration anomaly was reported for a well-diversified "+
				"fleet: %s", a.Observation)
		}
	}

	inc, ok := incident.Open(vector, anomalies, incident.DefaultRules(), "corr_s12", origin)
	if !ok {
		return // nothing reported at all is also a correct outcome here
	}

	if inc.Severity != incident.SeverityLow && inc.Severity != incident.SeverityInfo {
		t.Errorf("normal consensus opened a %s incident; the rule was %q",
			inc.Severity, inc.SeverityRule)
	}
	if !strings.Contains(inc.Recommended, "OBSERVE") {
		t.Errorf("normal consensus produced %q; observing a spike is right, "+
			"intervening on it is not", inc.Recommended)
	}
}

// The false intervention rate spec section 41 asks to be measured.
//
// Twenty windows of ordinary, well-diversified activity at a range of intensities.
// Every one that produces a throttle recommendation is a false intervention.
func TestS12_FalseInterventionRateIsMeasured(t *testing.T) {
	const windows = 20

	interventions := 0
	for run := 0; run < windows; run++ {
		agents := 20 + run*4 // 20 to 96 agents: quiet days and busy ones

		var envelopes []*intent.AgentExecutionEnvelope
		for i := 0; i < agents; i++ {
			// Buys and sells both, as ordinary two-way activity has.
			side := intent.SideBuy
			if i%3 == 0 {
				side = intent.SideSell
			}
			envelopes = append(envelopes, fleetAgent(
				"agent_normal_"+itoa(run)+"_"+itoa(i), side, 3000,
				time.Duration(i*200)*time.Millisecond,
				"model_"+itoa(i%5), "feed_"+itoa(i%4), "strategy_"+itoa(i%6)))
		}

		window := fleet.Window{Start: origin, End: origin.Add(20 * time.Second)}
		vector := fleet.ComputeVector(buildCohort(t, intent.SideBuy), window, envelopes,
			baselineWith(0.3, 200), nil)

		anomalies := incident.Detect(vector, incident.DefaultRules(), origin)
		inc, opened := incident.Open(vector, anomalies, incident.DefaultRules(),
			"corr_fp_"+itoa(run), origin)
		if opened && strings.Contains(inc.Recommended, "THROTTLE") {
			interventions++
		}
	}

	rate := float64(interventions) / float64(windows)
	t.Logf("false intervention rate: %d of %d ordinary windows produced a throttle "+
		"recommendation (%.0f%%)", interventions, windows, rate*100)

	// The number is the deliverable; the bound stops it silently becoming useless.
	// A fleet engine that recommends throttling on a quarter of ordinary days is one
	// its operators will learn to ignore.
	if rate > 0.25 {
		t.Errorf("false intervention rate is %.0f%%; at that rate the recommendations "+
			"train operators to ignore them", rate*100)
	}
}

// fixedVolume is a market data source with a constant venue notional.
type fixedVolume struct{ notional float64 }

func (f fixedVolume) VenueVolume(string, fleet.Window) (float64, bool) {
	return f.notional, f.notional > 0
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
