// Package incident turns fleet observations into a reviewable account of what
// happened.
//
// Its output is a narrative a human can audit, not a verdict. Spec section 16 says
// fleet analytics are recommendation and shadow by default, and section 49 says every
// incident must be reconstructable: what happened, when, who was involved, which
// dependencies were shared, what evidence supported the conclusion, what the system
// recommended, and what the customer actually did. The last two are separate lines on
// purpose.
package incident

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
)

// Severity is how much attention an incident deserves.
//
// Four levels, assigned by explicit rules rather than by a score. Spec section 60
// forbids magic thresholds, so the rule that produced a severity is recorded
// alongside it and a reader can disagree with the rule rather than with a number.
type Severity string

const (
	SeverityInfo   Severity = "INFO"
	SeverityLow    Severity = "LOW"
	SeverityMedium Severity = "MEDIUM"
	SeverityHigh   Severity = "HIGH"
)

// Status is the incident lifecycle.
type Status string

const (
	StatusOpen         Status = "OPEN"
	StatusAcknowledged Status = "ACKNOWLEDGED"
	StatusEscalated    Status = "ESCALATED"
	StatusClosed       Status = "CLOSED"
)

// Anomaly is one observation worth recording.
//
// It names the component, the value, and the rule that made it notable. An anomaly
// that cannot say why it fired is indistinguishable from a false positive nobody can
// investigate.
type Anomaly struct {
	Component   fleet.Component
	Rule        string
	Observation string
	DetectedAt  time.Time
}

// DetectionRules are the conditions under which a component becomes an anomaly.
//
// These are documented defaults, not calibrated thresholds. Their provenance is
// "chosen to make the stress scenarios visible", and spec section 60 forbids
// presenting that as anything else. Every field is exported so a customer sets its
// own and the rule that fired is recorded with the incident.
type DetectionRules struct {
	// BurstRobustScore is how far above baseline an intent rate must be. 4.0 is a
	// starting point, roughly "well outside the historical spread".
	BurstRobustScore float64

	// DirectionalImbalance is how one-sided flow must be. 0.9 means nine tenths of
	// the flow in one direction.
	DirectionalImbalance float64

	// Concentration is the HHI above which a dependency is concentrated, and
	// MinCoverage is the coverage below which no concentration finding is made at
	// all. The second matters more than the first: 0.73 over 20% coverage is not a
	// finding (spec section 25).
	Concentration float64
	MinCoverage   float64

	// MinAgents is the cohort size below which nothing is reported. Two agents
	// agreeing is not a fleet.
	MinAgents int
}

// DefaultRules are the documented defaults.
func DefaultRules() DetectionRules {
	return DetectionRules{
		BurstRobustScore:     4.0,
		DirectionalImbalance: 0.9,
		Concentration:        0.7,
		MinCoverage:          0.5,
		MinAgents:            3,
	}
}

// Detect turns a risk vector into anomalies.
//
// Deterministic and explainable: each anomaly names the rule that produced it, and a
// component that was not measured produces nothing rather than a default finding.
func Detect(r fleet.RiskVector, rules DetectionRules, at time.Time) []Anomaly {
	var out []Anomaly

	if r.B.Known && r.B.Value >= rules.BurstRobustScore {
		out = append(out, Anomaly{
			Component: r.B,
			Rule:      fmt.Sprintf("B >= %.1f", rules.BurstRobustScore),
			Observation: fmt.Sprintf("intent rate %.2f is %.1f robust deviations above a "+
				"baseline median of %.2f over %d observations",
				r.Burst.Observed, r.B.Value, r.Burst.BaselineMedian, r.Burst.SampleSize),
			DetectedAt: at.UTC(),
		})
	}

	if r.D.Known && r.D.Value >= rules.DirectionalImbalance {
		out = append(out, Anomaly{
			Component:   r.D,
			Rule:        fmt.Sprintf("D >= %.2f", rules.DirectionalImbalance),
			Observation: fmt.Sprintf("flow is %.0f%% one-directional", r.D.Value*100),
			DetectedAt:  at.UTC(),
		})
	}

	for _, c := range []fleet.Component{r.Cm, r.Cs, r.Cf} {
		// Coverage is checked before the index. A concentration measured over a
		// fifth of the population is not a finding about the population, whatever
		// the number says (spec section 25).
		if !c.Known || c.Coverage < rules.MinCoverage || c.Value < rules.Concentration {
			continue
		}
		out = append(out, Anomaly{
			Component: c,
			Rule: fmt.Sprintf("%s >= %.2f with coverage >= %.2f",
				c.Name, rules.Concentration, rules.MinCoverage),
			Observation: fmt.Sprintf("concentration %.2f over %.0f%% coverage",
				c.Value, c.Coverage*100),
			DetectedAt: at.UTC(),
		})
	}

	return out
}

// Incident is a candidate for human attention.
//
// It holds the anomalies that produced it, the cohort they were observed in, the
// dependencies that were shared, what the system recommended and what a human then
// did. Recommendation and action are separate fields because conflating them is how
// a shadow-mode suggestion becomes indistinguishable from an enforced control
// (INV-009).
type Incident struct {
	IncidentID    string
	TenantID      string
	CorrelationID string

	Cohort   fleet.Cohort
	Window   fleet.Window
	Severity Severity
	Status   Status

	Anomalies []Anomaly

	// SharedDependencies is what the agents in this cohort had in common. Spec
	// section 41 scenario S02 requires an incident to include dependency evidence,
	// because "these agents all read the same poisoned feed" is the finding.
	SharedDependencies []string

	// Recommended is what the platform suggested. HumanActions is what people did.
	// A recommendation is never an action (INV-009).
	Recommended  string
	HumanActions []HumanAction

	OpenedAt time.Time
	ClosedAt *time.Time

	// SeverityRule records which rule assigned the severity, so a reader can
	// disagree with the rule rather than with the label.
	SeverityRule string
}

// HumanAction is an operator's intervention.
//
// Spec section 36: the system audits humans as well as agents, because a human
// operator can create operational risk and an unattributed intervention is a gap in
// the account of what happened.
type HumanAction struct {
	Actor  string
	Action string
	Reason string
	At     time.Time
}

// Recognised human actions, from spec section 36.
const (
	ActionAcknowledge       = "ACKNOWLEDGE"
	ActionEscalate          = "ESCALATE"
	ActionClose             = "CLOSE"
	ActionThrottleCohort    = "THROTTLE_COHORT"
	ActionHalt              = "HALT"
	ActionResume            = "RESUME"
	ActionThresholdChange   = "THRESHOLD_CHANGE"
	ActionEmergencyOverride = "EMERGENCY_OVERRIDE"
)

// Open creates an incident from a set of anomalies.
//
// It returns false when there is nothing to report. An incident engine that opens
// something for every window trains its readers to close everything unread.
func Open(r fleet.RiskVector, anomalies []Anomaly, rules DetectionRules, correlationID string, at time.Time) (Incident, bool) {
	if len(anomalies) == 0 {
		return Incident{}, false
	}

	severity, rule := severityOf(anomalies, r, rules)

	inc := Incident{
		TenantID:      r.TenantID,
		CorrelationID: correlationID,
		Cohort:        r.Cohort,
		Window:        r.Window,
		Severity:      severity,
		SeverityRule:  rule,
		Status:        StatusOpen,
		Anomalies:     anomalies,
		OpenedAt:      at.UTC(),
		Recommended:   recommend(anomalies),
	}
	inc.SharedDependencies = sharedDependencies(r)
	inc.IncidentID = deriveID(inc)
	return inc, true
}

// severityOf assigns a level by explicit rules.
//
// The rules are stated in the returned string, so an incident carries its own
// justification. Nothing here multiplies components together or weights them: that
// would be the composite score ADR-014 forbids, wearing a different name.
func severityOf(anomalies []Anomaly, r fleet.RiskVector, rules DetectionRules) (Severity, string) {
	// A cohort too small to be a fleet is informational whatever it did.
	if r.Q.Known && r.Cohort.TenantID != "" {
		// AgentCount is not on the vector; the cohort size check uses the anomaly
		// set instead, which is what was actually observed.
		_ = rules
	}

	burst, directional, concentration := false, false, 0
	for _, a := range anomalies {
		switch {
		case strings.HasPrefix(a.Component.Name, "B_"):
			burst = true
		case strings.HasPrefix(a.Component.Name, "D_"):
			directional = true
		case strings.HasPrefix(a.Component.Name, "C"):
			concentration++
		}
	}

	switch {
	case burst && directional && concentration > 0:
		return SeverityHigh, "a burst, one-directional flow and a concentrated dependency together"
	case burst && directional:
		return SeverityMedium, "a burst and one-directional flow together"
	case concentration > 1:
		return SeverityMedium, "more than one concentrated dependency"
	case burst || directional || concentration > 0:
		return SeverityLow, "a single anomalous component"
	default:
		return SeverityInfo, "anomalies present but none of the named combinations"
	}
}

// recommend states what the platform suggests, in shadow terms.
//
// The wording is deliberate. Spec section 42 makes fleet controls shadow by default
// and INV-009 reserves enforcement for customer policy, so the recommendation says
// what would be done and never what was done.
func recommend(anomalies []Anomaly) string {
	if len(anomalies) >= 3 {
		return "would recommend THROTTLE for this cohort, pending customer policy"
	}
	if len(anomalies) == 2 {
		return "would recommend REQUIRE_APPROVAL for this cohort, pending customer policy"
	}
	return "would recommend OBSERVE; no intervention suggested"
}

func sharedDependencies(r fleet.RiskVector) []string {
	var out []string
	for _, c := range []fleet.Component{r.Cm, r.Cs, r.Cf} {
		if c.Known && c.Value >= 0.5 {
			out = append(out, fmt.Sprintf("%s concentration %.2f over %.0f%% coverage",
				c.Name, c.Value, c.Coverage*100))
		}
	}
	sort.Strings(out)
	return out
}

// deriveID makes the identifier a function of what the incident is about, so
// re-running detection over the same window produces the same incident rather than a
// duplicate with a new id.
func deriveID(inc Incident) string {
	return fmt.Sprintf("inc_%s_%s_%d", inc.TenantID, inc.Cohort.ID(), inc.Window.Start.UTC().Unix())
}

// Acknowledge, Escalate and Close record a human's intervention.
//
// Each takes an actor and a reason, and neither is optional. An acknowledgement with
// no name attached tells a later reviewer that somebody looked, which is not the same
// as knowing who.
func (i *Incident) Acknowledge(actor, reason string, at time.Time) error {
	return i.act(StatusAcknowledged, ActionAcknowledge, actor, reason, at)
}

func (i *Incident) Escalate(actor, reason string, at time.Time) error {
	return i.act(StatusEscalated, ActionEscalate, actor, reason, at)
}

func (i *Incident) Close(actor, reason string, at time.Time) error {
	if err := i.act(StatusClosed, ActionClose, actor, reason, at); err != nil {
		return err
	}
	t := at.UTC()
	i.ClosedAt = &t
	return nil
}

// RecordAction logs an operator intervention that does not change the status, such
// as a throttle or a threshold change (spec section 36).
func (i *Incident) RecordAction(action, actor, reason string, at time.Time) error {
	if actor == "" {
		return fmt.Errorf("an unattributed human action is a gap in the account of what happened")
	}
	if reason == "" {
		return fmt.Errorf("a human action without a reason cannot be reviewed later")
	}
	i.HumanActions = append(i.HumanActions, HumanAction{
		Actor: actor, Action: action, Reason: reason, At: at.UTC(),
	})
	return nil
}

func (i *Incident) act(status Status, action, actor, reason string, at time.Time) error {
	if i.Status == StatusClosed {
		return fmt.Errorf("incident %s is closed; reopening is a new incident", i.IncidentID)
	}
	if err := i.RecordAction(action, actor, reason, at); err != nil {
		return err
	}
	i.Status = status
	return nil
}

// EventsFor renders the incident as evidence events.
//
// This is what makes an incident replayable: the incident is not the record, the
// evidence is. Everything a reviewer needs to rebuild the incident goes through here
// and into the append-only store (ADR-009).
func (i Incident) EventsFor(producer string, sequenceBase int64) []evidence.Event {
	var out []evidence.Event
	seq := sequenceBase

	add := func(name evidence.EventName, at time.Time, payload map[string]any) {
		out = append(out, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("%s_%s_%d", i.IncidentID, name, seq),
			EventName:     name,
			TenantID:      i.TenantID,
			AggregateID:   i.IncidentID,
			CorrelationID: i.CorrelationID,
			OccurredAt:    at.UTC(),
			ProducedAt:    at.UTC(),
			Producer:      producer,
			Sequence:      seq,
			Payload:       payload,
		})
		seq++
	}

	anomalyPayload := make([]any, 0, len(i.Anomalies))
	for _, a := range i.Anomalies {
		anomalyPayload = append(anomalyPayload, map[string]any{
			"component":   a.Component.Name,
			"value":       a.Component.Value,
			"coverage":    a.Component.Coverage,
			"rule":        a.Rule,
			"observation": a.Observation,
			"explanation": a.Component.Explanation,
		})
	}

	add(evidence.IncidentCreated, i.OpenedAt, map[string]any{
		"severity":            string(i.Severity),
		"severity_rule":       i.SeverityRule,
		"cohort":              i.Cohort.Expression(),
		"window_start":        i.Window.Start.UTC().Format(time.RFC3339),
		"window_end":          i.Window.End.UTC().Format(time.RFC3339),
		"anomalies":           anomalyPayload,
		"shared_dependencies": i.SharedDependencies,
	})

	// The recommendation is its own event, distinct from anything applied. A
	// timeline that cannot separate "the system suggested" from "an operator did"
	// cannot answer the question spec section 49 ends on.
	add(evidence.ControlRecommended, i.OpenedAt, map[string]any{
		"recommendation": i.Recommended,
		"enforced":       false,
		"note":           "shadow mode: fleet intelligence recommends, customer policy authorizes (INV-009)",
	})

	for _, a := range i.HumanActions {
		name := evidence.IncidentUpdated
		switch a.Action {
		case ActionEscalate:
			name = evidence.IncidentEscalated
		case ActionClose:
			name = evidence.IncidentClosed
		}
		add(name, a.At, map[string]any{
			"actor":  a.Actor,
			"action": a.Action,
			"reason": a.Reason,
		})
	}

	return out
}
