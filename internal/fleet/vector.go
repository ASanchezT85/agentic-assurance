package fleet

import (
	"fmt"
	"math"

	"agentic-assurance/internal/intent"
)

// Component is one element of the Fleet Risk Vector.
//
// Every component carries its own value, its own coverage and its own sentence of
// explanation. That shape is the point: spec section 22 says the vector is
// explainable and ADR-014 forbids a composite score, so a component that could be
// read as a bare number would be the first step back toward one.
type Component struct {
	Name string

	// Value is meaningless without Known. A component that could not be computed
	// reports Known false rather than a zero, because zero directional imbalance
	// and unmeasured directional imbalance are opposite findings.
	Value float64
	Known bool

	// Coverage is the fraction of the population the value was computed over.
	Coverage float64

	// Explanation says how the number was produced, in words. It is not a comment:
	// it travels with the value into storage and into the console, so a reader who
	// has never seen this code can still audit the claim.
	Explanation string
}

// String renders a component the way it must always be presented: never the number
// alone.
func (c Component) String() string {
	if !c.Known {
		return fmt.Sprintf("%s=UNKNOWN (%s)", c.Name, c.Explanation)
	}
	return fmt.Sprintf("%s=%.4f coverage=%.2f (%s)", c.Name, c.Value, c.Coverage, c.Explanation)
}

// RiskVector is R = (D, B, Cm, Cs, Cf, P, A, Q) from spec section 22.
//
// There is no composite field and there will not be one. ADR-014 requires empirical
// calibration and its own ADR before any single number claims to summarise this, and
// a weighted average of these eight with hand-picked weights is exactly the HRI that
// decision forbids.
type RiskVector struct {
	TenantID string
	Cohort   Cohort
	Window   Window

	D  Component // directional imbalance
	B  Component // temporal burst
	Cm Component // model concentration
	Cs Component // strategy concentration
	Cf Component // feed concentration
	P  Component // projected market participation
	A  Component // abnormal consensus

	// Q is quality: coverage and confidence metadata for the vector as a whole
	// (spec section 28). It is a component like the others so it cannot be dropped
	// from a display that shows the rest.
	Q Component

	// Burst carries the full deviation behind B, because a burst score without its
	// median and sample size cannot be argued with.
	Burst Deviation
}

// Components returns the vector in a fixed order for display and storage.
func (r RiskVector) Components() []Component {
	return []Component{r.D, r.B, r.Cm, r.Cs, r.Cf, r.P, r.A, r.Q}
}

// Known reports how many components were actually measured. A vector with two known
// components is a very different artefact from one with eight, and a console that
// renders them identically is lying by omission.
func (r RiskVector) Known() int {
	n := 0
	for _, c := range r.Components() {
		if c.Known {
			n++
		}
	}
	return n
}

// MarketData supplies venue volume for the participation component.
//
// It is an interface with, today, no implementation: ADR-019 makes market data an
// optional adapter that arrives in Phase 8 or later, and P degrades to UNKNOWN
// without it rather than being estimated from our own observed flow.
type MarketData interface {
	// VenueVolume returns traded notional for an instrument in a window.
	VenueVolume(instrumentID string, w Window) (float64, bool)
}

// ComputeVector builds the Fleet Risk Vector for one cohort in one window.
//
// Deterministic, dependency-free except for the optional market data source, and
// free of any model (ADR-004, ADR-022). Given the same intents and the same baseline
// it returns the same vector, which is what makes a historical fleet view
// reproducible.
func ComputeVector(c Cohort, w Window, envelopes []*intent.AgentExecutionEnvelope,
	baseline *Baseline, md MarketData) RiskVector {

	inWindow := make([]*intent.AgentExecutionEnvelope, 0, len(envelopes))
	for _, e := range envelopes {
		if e != nil && c.Matches(e) && w.Contains(e.ReceivedAt) {
			inWindow = append(inWindow, e)
		}
	}

	m := Measure(c, w, envelopes)
	r := RiskVector{TenantID: c.TenantID, Cohort: c, Window: w}

	r.D = directional(m)
	r.B, r.Burst = burst(m, w, baseline)
	modelIndex, modelCoverage := ModelConcentration(inWindow)
	r.Cm = concentration("Cm_model_concentration", modelIndex, modelCoverage)

	strategyIndex, strategyCoverage := strategyConcentration(inWindow)
	r.Cs = concentration("Cs_strategy_concentration", strategyIndex, strategyCoverage)

	feedIndex, feedCoverage := FeedConcentration(inWindow)
	r.Cf = concentration("Cf_feed_concentration", feedIndex, feedCoverage)
	r.P = participation(c, w, m, md)
	r.A = abnormalConsensus(r.D, r.Burst)
	r.Q = quality(m, r)

	return r
}

func directional(m Measurement) Component {
	sized := m.IntentCount - m.IndeterminateIntents
	if sized == 0 {
		return Component{
			Name:        "D_directional_imbalance",
			Explanation: "no intent in the window had a determinable notional, so no flow could be measured",
		}
	}
	return Component{
		Name:        "D_directional_imbalance",
		Value:       m.DirectionalImbalance,
		Known:       true,
		Coverage:    float64(sized) / float64(m.IntentCount),
		Explanation: "|sum(side * notional)| / sum(notional); 0 is balanced, 1 is fully one-directional",
	}
}

func burst(m Measurement, w Window, baseline *Baseline) (Component, Deviation) {
	observed := RateOf(m.IntentCount, w)

	if baseline == nil {
		return Component{
			Name:        "B_temporal_burst",
			Explanation: "no baseline for this context; spec section 24 forbids a fixed global threshold, so no burst score is produced",
		}, Deviation{Observed: observed}
	}

	d := baseline.Compare(observed)
	if !d.SufficientData || d.MAD == 0 {
		return Component{
			Name:        "B_temporal_burst",
			Explanation: d.Explanation,
		}, d
	}
	return Component{
		Name:        "B_temporal_burst",
		Value:       d.RobustScore,
		Known:       true,
		Coverage:    1,
		Explanation: d.Explanation,
	}, d
}

func concentration(name string, index, coverage float64) Component {
	if coverage == 0 {
		return Component{
			Name:        name,
			Explanation: "nothing was declared, so there is no concentration to measure; this is zero coverage, not zero concentration",
		}
	}
	return Component{
		Name:        name,
		Value:       index,
		Known:       true,
		Coverage:    coverage,
		Explanation: "Herfindahl-Hirschman index over declared values; unknowns are excluded from the index and reflected in coverage",
	}
}

func strategyConcentration(envelopes []*intent.AgentExecutionEnvelope) (float64, float64) {
	counts := map[string]int{}
	population := 0
	for _, e := range envelopes {
		population++
		if e.Lineage.StrategyID != "" {
			counts[e.Lineage.StrategyID]++
		}
	}
	return HHI(counts, population)
}

// participation is P from spec section 22.
//
// Without a market data source it is UNKNOWN, never estimated. ADR-019 is explicit:
// our own observed flow must not be substituted for market volume, because doing so
// reports our order book back to us as if it were the market.
func participation(c Cohort, w Window, m Measurement, md MarketData) Component {
	name := "P_market_participation"

	if md == nil {
		return Component{
			Name: name,
			Explanation: "no market data source is configured; participation is UNKNOWN " +
				"and is never estimated from our own flow (ADR-019)",
		}
	}

	instrument := ""
	for _, p := range c.Predicates {
		if p.Field == FieldInstrument {
			instrument = p.Value
		}
	}
	if instrument == "" {
		return Component{
			Name:        name,
			Explanation: "the cohort is not scoped to one instrument, so participation has no denominator",
		}
	}

	volume, ok := md.VenueVolume(instrument, w)
	if !ok || volume <= 0 {
		return Component{
			Name:        name,
			Explanation: "the market data source has no volume for this instrument and window",
		}
	}
	return Component{
		Name:        name,
		Value:       m.GrossNotional / volume,
		Known:       true,
		Coverage:    1,
		Explanation: "cohort gross notional / venue traded notional in the same window",
	}
}

// abnormalConsensus is A from spec section 26: observed consensus minus what the
// context would lead you to expect.
//
// V0 computes it from the two components it has rather than from a model, and it is
// deliberately conservative. Agreement is not abnormal by itself: scenario S12 exists
// precisely to prove that a legitimate event producing broad rational agreement is
// not flagged, so consensus only becomes abnormal when it coincides with a burst the
// baseline did not expect.
func abnormalConsensus(d Component, burst Deviation) Component {
	name := "A_abnormal_consensus"

	if !d.Known {
		return Component{Name: name, Explanation: "directional imbalance is unknown, so consensus cannot be assessed"}
	}
	if !burst.SufficientData || burst.MAD == 0 {
		return Component{
			Name: name,
			Explanation: "consensus was observed but there is no baseline to say whether it is " +
				"expected here; high agreement during a real event is ordinary (scenario S12)",
		}
	}

	// Expected consensus rises with the burst: when something happens, agents agree,
	// and that is the normal case. Abnormality is agreement in excess of what the
	// activity level explains.
	expected := math.Min(1, math.Max(0, burst.RobustScore/10))
	value := math.Max(0, d.Value-expected)

	return Component{
		Name:     name,
		Value:    value,
		Known:    true,
		Coverage: d.Coverage,
		Explanation: "observed directional imbalance minus the agreement the burst level " +
			"already explains; agreement during a real event is expected, not abnormal",
	}
}

// quality is Q from spec section 22, and section 28 makes it mandatory.
//
// It reports how much of the vector was actually measured. A console showing seven
// components where five are UNKNOWN, without saying so, presents a guess as a
// reading.
func quality(m Measurement, r RiskVector) Component {
	measurable := []Component{r.D, r.B, r.Cm, r.Cs, r.Cf, r.P, r.A}
	known := 0
	for _, c := range measurable {
		if c.Known {
			known++
		}
	}

	coverage := 0.0
	if m.IntentCount > 0 {
		coverage = float64(m.IntentCount-m.IndeterminateIntents) / float64(m.IntentCount)
	}

	return Component{
		Name:     "Q_quality",
		Value:    float64(known) / float64(len(measurable)),
		Known:    true,
		Coverage: coverage,
		Explanation: fmt.Sprintf("%d of %d components measured; %d of %d intents had a "+
			"determinable notional", known, len(measurable),
			m.IntentCount-m.IndeterminateIntents, m.IntentCount),
	}
}
