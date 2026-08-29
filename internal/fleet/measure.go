package fleet

import (
	"math"

	"agentic-assurance/internal/intent"
)

// Coverage describes how well sourced a measurement is.
//
// Spec section 28 makes this mandatory and forbids the UI from collapsing it into a
// precise score. It travels with every measurement rather than being computed on
// demand, because a number that arrives without its coverage will be read as if it
// had full coverage.
type Coverage struct {
	// Observed is the fraction of intents that asserted the dependency at all.
	Observed float64
	// Verified, Declared and Unknown partition the observed ones by verification
	// level. They never sum to more than Observed.
	Verified float64
	Declared float64
	Unknown  float64
}

// Measurement is what the fleet engine computes for one cohort in one window.
//
// Phase 8 delivers the counts, the flows and directional imbalance. Temporal burst,
// concentration and abnormal consensus need the baseline engine and arrive in
// Phase 9. The fields are absent rather than present-and-zero: a zero burst score
// reads as "no burst", which is a different claim from "not measured".
type Measurement struct {
	TenantID string
	Cohort   Cohort
	Window   Window

	IntentCount int
	AgentCount  int

	// AuthorizedIntents is how many of them the enforcement plane allowed to reach a
	// venue. The flow figures below cover every decided intent, refused ones
	// included, because the fleet vector measures agentic INTENT: "forty agents all
	// wanted to sell" is the signal, and it is the same signal whether or not they
	// were allowed to.
	//
	// But a gross notional that does not say how much of it reached a market is a
	// number an operator would misread, and this platform's whole discipline is that
	// a figure travels with what it was computed over (ADR-014). So both are here.
	AuthorizedIntents int
	RefusedIntents    int

	// GrossNotional is total flow, NetNotional is directional. Spec section 23
	// requires both to survive: a cohort that bought and sold 10,000 each is not
	// the same as one that did nothing, though their net is identical.
	GrossNotional float64
	NetNotional   float64

	// IndeterminateIntents counts intents whose notional could not be established
	// without a market price (ADR-019). The flows above exclude them, so a caller
	// reading GrossNotional needs this to know whether it is the whole story.
	IndeterminateIntents int

	// DirectionalImbalance is D from spec section 23: |sum(s_i * n_i)| / sum(n_i).
	// 0 is balanced, 1 is fully one-directional.
	DirectionalImbalance float64

	ModelCoverage float64
	FeedCoverage  Coverage
}

// Measure computes a window's measurement for one cohort.
//
// Deterministic and dependency-free: given the same intents it returns the same
// numbers, with no clock, no network and no model. That is what makes a historical
// fleet view reproducible from stored intents.
// Observed is a stored intent together with what the enforcement plane decided.
//
// Separate from the envelope because an envelope is what an agent sent; whether it
// was allowed is something the platform concluded afterwards, and putting it on the
// envelope would let a caller believe an agent had declared it.
type Observed struct {
	Envelope   *intent.AgentExecutionEnvelope
	Authorized bool
}

// Measure computes a window's measurement from envelopes alone.
//
// Every intent is treated as authorized, which is right for callers that only have
// envelopes: a simulation, a scenario, a test. Anything reading from the store should
// use MeasureObserved, so the authorized split is real rather than assumed.
func Measure(c Cohort, w Window, envelopes []*intent.AgentExecutionEnvelope) Measurement {
	observed := make([]Observed, 0, len(envelopes))
	for _, e := range envelopes {
		observed = append(observed, Observed{Envelope: e, Authorized: true})
	}
	return MeasureObserved(c, w, observed)
}

// MeasureObserved computes a window's measurement for one cohort.
func MeasureObserved(c Cohort, w Window, observed []Observed) Measurement {
	m := Measurement{TenantID: c.TenantID, Cohort: c, Window: w}

	agents := map[string]bool{}
	var signedSum, absSum float64

	feedTotal, feedVerified, feedDeclared, feedUnknown := 0, 0, 0, 0
	modelDeclared := 0

	for _, o := range observed {
		e := o.Envelope
		if e == nil || !c.Matches(e) || !w.Contains(e.ReceivedAt) {
			continue
		}
		m.IntentCount++
		if o.Authorized {
			m.AuthorizedIntents++
		} else {
			m.RefusedIntents++
		}
		agents[e.Agent.AgentID] = true

		if e.RuntimeClaims.ModelFamily.Value != "" {
			modelDeclared++
		}
		countFeeds(e, &feedTotal, &feedVerified, &feedDeclared, &feedUnknown)

		notional, ok := intent.ClusterNotional(e.Intent)
		if !ok {
			m.IndeterminateIntents++
			continue
		}

		sign := 1.0
		if e.Intent.Side == intent.SideSell {
			sign = -1
		}
		// Analytical arithmetic, in floats by design. The exact value is what the
		// enforcement plane decided against; this is what the fleet view aggregates.
		value := notional.Float()
		m.GrossNotional += value
		m.NetNotional += sign * value
		signedSum += sign * value
		absSum += value
	}

	m.AgentCount = len(agents)
	if absSum > 0 {
		m.DirectionalImbalance = math.Abs(signedSum) / absSum
	}

	if m.IntentCount > 0 {
		m.ModelCoverage = float64(modelDeclared) / float64(m.IntentCount)
		m.FeedCoverage = Coverage{
			Observed: float64(feedTotal) / float64(m.IntentCount),
			Verified: float64(feedVerified) / float64(m.IntentCount),
			Declared: float64(feedDeclared) / float64(m.IntentCount),
			Unknown:  float64(feedUnknown) / float64(m.IntentCount),
		}
	}
	return m
}

func countFeeds(e *intent.AgentExecutionEnvelope, total, verified, declared, unknown *int) {
	for _, d := range e.Dependencies {
		if d.Type != intent.DependencyMarketData {
			continue
		}
		*total++
		switch d.Verification {
		case intent.VerificationVerified:
			*verified++
		case intent.VerificationDeclared:
			*declared++
		default:
			*unknown++
		}
		return
	}
	*unknown++
}

// HHI is the Herfindahl-Hirschman concentration of a set of shares (spec section 25).
//
// It returns the index and the coverage it was computed over. Reporting the index
// alone is the failure spec section 25 warns about: 0.73 over 20% coverage and 0.73
// over 95% coverage are not the same finding, and only one of them is worth acting
// on.
func HHI(counts map[string]int, population int) (index float64, coverage float64) {
	if population <= 0 {
		return 0, 0
	}
	observed := 0
	for _, n := range counts {
		observed += n
	}
	if observed == 0 {
		return 0, 0
	}
	for _, n := range counts {
		share := float64(n) / float64(observed)
		index += share * share
	}
	return index, float64(observed) / float64(population)
}

// ModelConcentration computes model-family concentration across a set of intents.
//
// Unknown declarations are excluded from the index and reflected in the coverage:
// counting them as a family would invent a large fictitious competitor and make
// every real concentration look smaller than it is (ADR-006, INV-008).
func ModelConcentration(envelopes []*intent.AgentExecutionEnvelope) (float64, float64) {
	counts := map[string]int{}
	population := 0
	for _, e := range envelopes {
		if e == nil {
			continue
		}
		population++
		if v := e.RuntimeClaims.ModelFamily.Value; v != "" {
			counts[v]++
		}
	}
	return HHI(counts, population)
}

// FeedConcentration is the same measure over market-data dependencies.
func FeedConcentration(envelopes []*intent.AgentExecutionEnvelope) (float64, float64) {
	counts := map[string]int{}
	population := 0
	for _, e := range envelopes {
		if e == nil {
			continue
		}
		population++
		if id := firstDependency(e, intent.DependencyMarketData); id != "" {
			counts[id]++
		}
	}
	return HHI(counts, population)
}
