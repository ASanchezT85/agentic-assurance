package incident

import (
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
)

var at = time.Date(2026, 8, 27, 14, 32, 4, 182000000, time.UTC)

func cohort(t *testing.T) fleet.Cohort {
	t.Helper()
	c, err := fleet.NewCohort("tenant_acme",
		fleet.Predicate{Field: fleet.FieldInstrument, Value: "instr_us_equity_00206R102"},
		fleet.Predicate{Field: fleet.FieldSide, Value: "BUY"})
	if err != nil {
		t.Fatalf("cohort: %v", err)
	}
	return c
}

// alarming builds a vector that should trip every rule.
func alarming(t *testing.T) fleet.RiskVector {
	t.Helper()
	return fleet.RiskVector{
		TenantID: "tenant_acme",
		Cohort:   cohort(t),
		Window:   fleet.Window{Start: at, End: at.Add(time.Minute)},
		D: fleet.Component{Name: "D_directional_imbalance", Value: 0.97, Known: true,
			Coverage: 1, Explanation: "|sum(side * notional)| / sum(notional)"},
		B: fleet.Component{Name: "B_temporal_burst", Value: 6.2, Known: true,
			Coverage: 1, Explanation: "(observed - median) / (1.4826 * MAD), over 120 observations"},
		Cf: fleet.Component{Name: "Cf_feed_concentration", Value: 0.88, Known: true,
			Coverage: 0.8, Explanation: "HHI over declared feeds"},
		Q: fleet.Component{Name: "Q_quality", Value: 0.5, Known: true, Coverage: 1,
			Explanation: "4 of 7 components measured"},
		Burst: fleet.Deviation{Observed: 47.3, BaselineMedian: 8.1, MAD: 1.2,
			RobustScore: 6.2, SampleSize: 120, SufficientData: true},
	}
}

// An anomaly must say why it fired. One that cannot is indistinguishable from a
// false positive nobody can investigate.
func TestAnomaliesExplainThemselves(t *testing.T) {
	anomalies := Detect(alarming(t), DefaultRules(), at)
	if len(anomalies) != 3 {
		t.Fatalf("detected %d anomalies, want 3", len(anomalies))
	}
	for _, a := range anomalies {
		if a.Rule == "" {
			t.Errorf("%s has no rule", a.Component.Name)
		}
		if a.Observation == "" {
			t.Errorf("%s has no observation", a.Component.Name)
		}
		if a.Component.Explanation == "" {
			t.Errorf("%s carries no explanation from the component", a.Component.Name)
		}
	}
}

// Unmeasured components produce nothing. A default finding from an absent
// measurement is a fabricated one.
func TestUnknownComponentsProduceNoAnomalies(t *testing.T) {
	r := fleet.RiskVector{
		TenantID: "tenant_acme",
		Cohort:   cohort(t),
		D:        fleet.Component{Name: "D_directional_imbalance"},
		B:        fleet.Component{Name: "B_temporal_burst"},
		Cf:       fleet.Component{Name: "Cf_feed_concentration"},
	}
	if got := Detect(r, DefaultRules(), at); len(got) != 0 {
		t.Errorf("%d anomalies from a vector where nothing was measured", len(got))
	}
}

// Coverage is checked before the index. A concentration over a fifth of the
// population is not a finding about the population (spec section 25).
func TestThinCoverageProducesNoConcentrationFinding(t *testing.T) {
	r := alarming(t)
	r.Cf.Coverage = 0.2 // same 0.88 index, far less of the fleet observed

	for _, a := range Detect(r, DefaultRules(), at) {
		if strings.HasPrefix(a.Component.Name, "Cf") {
			t.Error("a concentration over 20% coverage was reported as a finding " +
				"(spec section 25)")
		}
	}
}

// Severity is assigned by a named rule, and the incident carries the rule.
func TestSeverityCarriesItsRule(t *testing.T) {
	r := alarming(t)
	inc, ok := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)
	if !ok {
		t.Fatal("no incident opened for three anomalies")
	}
	if inc.Severity != SeverityHigh {
		t.Errorf("severity = %s, want HIGH", inc.Severity)
	}
	if inc.SeverityRule == "" {
		t.Error("the severity has no stated rule; a reader can only disagree with a rule")
	}
}

// No anomalies, no incident. An engine that opens something every window trains its
// readers to close everything unread.
func TestQuietWindowsOpenNothing(t *testing.T) {
	r := alarming(t)
	r.D.Value, r.B.Value, r.Cf.Value = 0.1, 0.3, 0.2

	anomalies := Detect(r, DefaultRules(), at)
	if len(anomalies) != 0 {
		t.Fatalf("%d anomalies in a quiet window", len(anomalies))
	}
	if _, ok := Open(r, anomalies, DefaultRules(), "corr_1", at); ok {
		t.Error("an incident was opened with no anomalies")
	}
}

// The recommendation is phrased as a recommendation, always. A shadow-mode
// suggestion that reads like an action is indistinguishable from one (INV-009).
func TestRecommendationIsNeverAnAction(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)

	if !strings.Contains(inc.Recommended, "would recommend") {
		t.Errorf("recommendation reads as an action: %q", inc.Recommended)
	}
	if !strings.Contains(inc.Recommended, "customer policy") {
		t.Errorf("the recommendation does not say who authorizes: %q", inc.Recommended)
	}
}

// Human actions are attributed and explained, or refused.
func TestHumanActionsRequireAnActorAndAReason(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)

	if err := inc.Acknowledge("", "looked at it", at); err == nil {
		t.Error("an unattributed acknowledgement was accepted (spec section 36)")
	}
	if err := inc.Acknowledge("operator@acme", "", at); err == nil {
		t.Error("an acknowledgement with no reason was accepted")
	}
	if err := inc.Acknowledge("operator@acme", "reviewing the feed concentration", at); err != nil {
		t.Fatalf("a well-formed acknowledgement was refused: %v", err)
	}
	if inc.Status != StatusAcknowledged {
		t.Errorf("status = %s", inc.Status)
	}
	if len(inc.HumanActions) != 1 || inc.HumanActions[0].Actor != "operator@acme" {
		t.Errorf("the human action was not recorded: %+v", inc.HumanActions)
	}
}

// A closed incident is closed. Reopening is a new incident, not a rewrite of this
// one (ADR-009 in spirit: history is not edited).
func TestClosedIncidentsDoNotReopen(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)

	if err := inc.Close("operator@acme", "feed provider confirmed the outage", at); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := inc.Escalate("someone", "changed my mind", at); err == nil {
		t.Error("a closed incident was escalated")
	}
	if inc.ClosedAt == nil {
		t.Error("the closure time was not recorded")
	}
}

// The Phase 10 exit criterion: an incident can be replayed from evidence alone.
func TestIncidentReplaysFromEvidence(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)

	if err := inc.Acknowledge("operator@acme", "checking the shared feed", at.Add(90*time.Second)); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}
	if err := inc.Close("supervisor@acme", "feed restored, no orders affected", at.Add(20*time.Minute)); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reconstruct from the events alone. The in-memory incident is deliberately not
	// passed in: that would be replaying memory rather than evidence.
	events := inc.EventsFor("fleet-engine", 1)
	timeline, err := Reconstruct(events)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	if timeline.IncidentID != inc.IncidentID {
		t.Errorf("incident id = %q, want %q", timeline.IncidentID, inc.IncidentID)
	}

	answers := timeline.AnsweredQuestions()
	for question, answered := range answers {
		if !answered {
			t.Errorf("the reconstructed timeline cannot answer %q (spec section 49)", question)
		}
	}

	// Recommendation and action stay distinguishable through the round trip.
	if len(timeline.Recommended) == 0 {
		t.Error("the timeline lost what the system recommended")
	}
	if len(timeline.Applied) != 0 {
		t.Errorf("shadow-mode recommendations were reconstructed as applied controls: %v",
			timeline.Applied)
	}

	rendered := timeline.String()
	for _, expected := range []string{"operator@acme", "supervisor@acme", "feed restored"} {
		if !strings.Contains(rendered, expected) {
			t.Errorf("the rendered timeline omits %q:\n%s", expected, rendered)
		}
	}
}

// Every event the incident produces must be valid evidence, or the replay above is
// reconstructing something the store would have rejected.
func TestIncidentEventsAreValidEvidence(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)
	_ = inc.Acknowledge("operator@acme", "checking", at.Add(time.Minute))

	seen := map[string]bool{}
	for _, e := range inc.EventsFor("fleet-engine", 1) {
		if err := e.Validate(); err != nil {
			t.Errorf("%s is not recordable: %v", e.EventName, err)
		}
		if seen[e.EventID] {
			t.Errorf("duplicate event id %s; at-least-once dedup would collapse them", e.EventID)
		}
		seen[e.EventID] = true
	}
}

// Re-running detection over the same window produces the same incident, not a
// duplicate with a new id.
func TestIncidentIDIsStable(t *testing.T) {
	r := alarming(t)
	first, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)
	second, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at.Add(time.Second))

	if first.IncidentID != second.IncidentID {
		t.Errorf("re-detection produced a new incident: %q vs %q",
			first.IncidentID, second.IncidentID)
	}
}

// An empty evidence set cannot be reconstructed into a plausible-looking incident.
func TestReconstructRefusesEmptyEvidence(t *testing.T) {
	if _, err := Reconstruct(nil); err == nil {
		t.Error("a timeline was reconstructed from no evidence")
	}
}

// Out-of-order evidence still reconstructs in the right order. At-least-once
// delivery makes no ordering promise (ADR-008).
func TestReconstructOrdersEvidence(t *testing.T) {
	r := alarming(t)
	inc, _ := Open(r, Detect(r, DefaultRules(), at), DefaultRules(), "corr_1", at)
	_ = inc.Acknowledge("operator@acme", "checking", at.Add(time.Minute))
	_ = inc.Close("operator@acme", "resolved", at.Add(time.Hour))

	events := inc.EventsFor("fleet-engine", 1)
	shuffled := []evidence.Event{events[3], events[0], events[2], events[1]}

	timeline, err := Reconstruct(shuffled)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}
	for i := 1; i < len(timeline.Entries); i++ {
		if timeline.Entries[i].At.Before(timeline.Entries[i-1].At) {
			t.Fatalf("entry %d is earlier than the one before it", i)
		}
	}
}
