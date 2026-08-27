package fleet

import (
	"math"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
)

func window() Window {
	return Window{Start: origin, End: origin.Add(time.Minute)}
}

// Robust statistics do not move when one extreme value arrives. That is the whole
// reason spec section 24 asks for them: with a mean, the first burst hides the
// second.
func TestMedianAndMADResistOutliers(t *testing.T) {
	quiet := []float64{10, 11, 9, 10, 12, 10, 11, 9}
	withBurst := append(append([]float64(nil), quiet...), 5000)

	if before, after := Median(quiet), Median(withBurst); math.Abs(before-after) > 1 {
		t.Errorf("one outlier moved the median from %v to %v", before, after)
	}
	if before, after := MAD(quiet), MAD(withBurst); after > before*2 {
		t.Errorf("one outlier moved the MAD from %v to %v", before, after)
	}

	// The contrast the spec is warning about.
	meanBefore, meanAfter := mean(quiet), mean(withBurst)
	if math.Abs(meanAfter-meanBefore) < 100 {
		t.Fatal("precondition: the outlier should move a mean substantially")
	}
}

// Median must not reorder its caller's slice.
func TestStatisticsDoNotMutateInput(t *testing.T) {
	values := []float64{5, 1, 4, 2, 3}
	original := append([]float64(nil), values...)

	Median(values)
	MAD(values)
	Quantile(values, 0.9)

	for i := range values {
		if values[i] != original[i] {
			t.Fatalf("the input was reordered: %v", values)
		}
	}
}

// A deviation never arrives as a bare number.
func TestDeviationCarriesItsWorkings(t *testing.T) {
	b := NewBaseline(BaselineContext{InstrumentID: "instr_x", MarketSession: "US_REGULAR", HourUTC: 15})
	for i := 0; i < 60; i++ {
		b.Observe(10 + float64(i%3))
	}

	d := b.Compare(80)

	if !d.SufficientData {
		t.Error("60 observations were reported as insufficient")
	}
	if d.SampleSize != 60 {
		t.Errorf("sample_size = %d", d.SampleSize)
	}
	if d.BaselineMedian == 0 || d.MAD == 0 {
		t.Error("the deviation omitted its median or spread")
	}
	if d.RobustScore <= 0 {
		t.Errorf("robust score = %v for an observation far above the median", d.RobustScore)
	}
	if !strings.Contains(d.Explanation, "MAD") {
		t.Errorf("the explanation does not say how the score was produced: %q", d.Explanation)
	}
}

// A thin sample says so instead of producing a confident-looking score.
func TestThinBaselineReportsInsufficientData(t *testing.T) {
	b := NewBaseline(BaselineContext{InstrumentID: "instr_x"})
	for i := 0; i < 4; i++ {
		b.Observe(10)
	}

	d := b.Compare(90)
	if d.SufficientData {
		t.Error("a four-observation baseline reported sufficient data")
	}
	if !strings.Contains(d.Explanation, "below the") {
		t.Errorf("the explanation does not mention the sample size: %q", d.Explanation)
	}
}

// Zero spread must not become infinity. An infinite score reads as certainty.
func TestZeroSpreadDoesNotProduceInfinity(t *testing.T) {
	b := NewBaseline(BaselineContext{InstrumentID: "instr_x"})
	for i := 0; i < 60; i++ {
		b.Observe(10)
	}

	d := b.Compare(90)
	if math.IsInf(d.RobustScore, 0) || math.IsNaN(d.RobustScore) {
		t.Fatalf("robust score = %v", d.RobustScore)
	}
	if !strings.Contains(d.Explanation, "zero spread") {
		t.Errorf("the explanation does not say why no score was computed: %q", d.Explanation)
	}
}

// The Phase 9 exit criterion, stated as a test: no composite score exists.
func TestNoCompositeScoreExists(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
		env("b", intent.SideBuy, 1000, time.Second),
	}
	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, nil)

	// Eight named components, each with its own value, coverage and explanation.
	if got := len(r.Components()); got != 8 {
		t.Fatalf("the vector has %d components, want 8 (spec section 22)", got)
	}
	for _, c := range r.Components() {
		if c.Name == "" {
			t.Error("a component has no name")
		}
		if c.Explanation == "" {
			t.Errorf("%s has no explanation; a number nobody can audit is not explainable "+
				"(ADR-014)", c.Name)
		}
	}
}

// Every component states how it was produced, and an unmeasured one says so rather
// than reporting zero.
func TestUnmeasuredComponentsAreUnknownNotZero(t *testing.T) {
	// No baseline, no market data, and no declarations of any kind.
	envelopes := []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
	}
	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, nil)

	if r.B.Known {
		t.Error("a burst score was produced with no baseline (spec section 24 forbids a fixed threshold)")
	}
	if r.P.Known {
		t.Error("participation was produced with no market data source (ADR-019)")
	}
	if r.Cm.Known {
		t.Error("model concentration was reported with nothing declared")
	}
	if !strings.Contains(r.P.Explanation, "ADR-019") {
		t.Errorf("P does not explain why it is unknown: %q", r.P.Explanation)
	}
	if !strings.Contains(r.Cm.Explanation, "zero coverage") {
		t.Errorf("Cm does not distinguish zero coverage from zero concentration: %q", r.Cm.Explanation)
	}
}

// Q reports how much of the vector was actually measured. A console showing seven
// components where five are unknown, without saying so, presents a guess as a
// reading.
func TestQualityReportsHowMuchWasMeasured(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
	}
	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, nil)

	if !r.Q.Known {
		t.Fatal("Q is itself unknown; quality metadata is mandatory (spec section 28)")
	}
	if r.Q.Value >= 1 {
		t.Errorf("Q = %v while most components are unknown", r.Q.Value)
	}
	if r.Known() >= 8 {
		t.Error("every component reported as known despite no baseline and no market data")
	}
}

// A component is never rendered as a bare number.
func TestComponentsNeverRenderWithoutContext(t *testing.T) {
	unknown := Component{Name: "P_market_participation", Explanation: "no market data"}
	if got := unknown.String(); !strings.Contains(got, "UNKNOWN") {
		t.Errorf("an unknown component rendered as %q", got)
	}

	known := Component{Name: "D_directional_imbalance", Value: 0.73, Known: true,
		Coverage: 0.8, Explanation: "test"}
	got := known.String()
	if !strings.Contains(got, "coverage") {
		t.Errorf("a known component rendered without its coverage: %q; 0.73 over 20%% "+
			"coverage and 0.73 over 95%% are not the same finding", got)
	}
}

// Agreement during a real event is expected, not abnormal. This is the property
// scenario S12 exists to prove, checked here at the component level.
func TestConsensusIsNotAbnormalWithoutABaseline(t *testing.T) {
	// Ten agents, all buying: perfect agreement.
	var envelopes []*intent.AgentExecutionEnvelope
	for i := 0; i < 10; i++ {
		envelopes = append(envelopes, env(string(rune('a'+i)), intent.SideBuy, 1000,
			time.Duration(i)*time.Second))
	}

	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, nil)

	if r.D.Value != 1 {
		t.Fatalf("precondition: D = %v, want 1 for unanimous buying", r.D.Value)
	}
	if r.A.Known {
		t.Error("unanimous agreement was scored as abnormal with no baseline to compare " +
			"against; broad agreement during a real event is ordinary (scenario S12)")
	}
}

// The vector is deterministic.
func TestVectorIsDeterministic(t *testing.T) {
	build := func() []*intent.AgentExecutionEnvelope {
		return []*intent.AgentExecutionEnvelope{
			withModel(env("a", intent.SideBuy, 1000, 0), "model-x"),
			withModel(env("b", intent.SideSell, 400, time.Second), "model-y"),
			withModel(env("c", intent.SideBuy, 700, 2*time.Second), "model-x"),
		}
	}

	first := ComputeVector(instrumentCohort(t), window(), build(), nil, nil)
	for i := 0; i < 50; i++ {
		got := ComputeVector(instrumentCohort(t), window(), build(), nil, nil)
		for j, c := range got.Components() {
			want := first.Components()[j]
			if c.Value != want.Value || c.Known != want.Known || c.Coverage != want.Coverage {
				t.Fatalf("run %d differed on %s", i, c.Name)
			}
		}
	}
}

// Participation uses venue volume when it is available, and only then.
func TestParticipationUsesVenueVolume(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{
		env("a", intent.SideBuy, 1000, 0),
		env("b", intent.SideBuy, 1000, time.Second),
	}
	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, fakeMarket{volume: 100000})

	if !r.P.Known {
		t.Fatalf("participation was not computed with volume available: %s", r.P.Explanation)
	}
	if math.Abs(r.P.Value-0.02) > 1e-9 {
		t.Errorf("P = %v, want 2000/100000", r.P.Value)
	}
}

func TestParticipationIsUnknownWithoutVolume(t *testing.T) {
	envelopes := []*intent.AgentExecutionEnvelope{env("a", intent.SideBuy, 1000, 0)}
	r := ComputeVector(instrumentCohort(t), window(), envelopes, nil, fakeMarket{})

	if r.P.Known {
		t.Error("participation was computed with no volume for the instrument")
	}
}

type fakeMarket struct{ volume float64 }

func (f fakeMarket) VenueVolume(string, Window) (float64, bool) {
	return f.volume, f.volume > 0
}

func mean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}
