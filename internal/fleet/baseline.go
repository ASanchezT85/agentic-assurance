package fleet

import (
	"math"
	"sort"
	"time"
)

// Robust statistics, because fleet data is not well behaved.
//
// Spec section 24 asks for median, MAD, quantiles and EWMA rather than mean and
// standard deviation, and the reason is concrete: one 50,000-intent burst in an hour
// of quiet moves a mean enough that the next burst looks normal. A median does not
// move, so the second burst is still visible.

// Median returns the middle value. It sorts a copy: a statistic that reorders its
// caller's data is a bug waiting for a second reader.
func Median(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// MAD is the median absolute deviation: the median of |x - median(x)|.
//
// It is the robust analogue of standard deviation. Unlike the standard deviation it
// does not grow when a single extreme value arrives, which is exactly the property a
// burst detector needs.
func MAD(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	m := Median(values)
	deviations := make([]float64, len(values))
	for i, v := range values {
		deviations[i] = math.Abs(v - m)
	}
	return Median(deviations)
}

// madToSigma converts MAD to a standard-deviation-equivalent scale.
//
// 1.4826 is the standard consistency constant for normally distributed data. Fleet
// data is not normal, so this makes the score comparable to a z-score in
// interpretation only. It is named RobustScore rather than ZScore for that reason:
// calling it a z-score would imply a distribution nobody has verified.
const madToSigma = 1.4826

// Quantile returns the value at p in [0, 1], by linear interpolation.
func Quantile(values []float64, p float64) float64 {
	if len(values) == 0 {
		return 0
	}
	if p <= 0 {
		return minOf(values)
	}
	if p >= 1 {
		return maxOf(values)
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)

	pos := p * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))
	if lower == upper {
		return sorted[lower]
	}
	weight := pos - float64(lower)
	return sorted[lower]*(1-weight) + sorted[upper]*weight
}

// PercentileOf returns the fraction of the sample at or below a value.
func PercentileOf(values []float64, x float64) float64 {
	if len(values) == 0 {
		return 0
	}
	atOrBelow := 0
	for _, v := range values {
		if v <= x {
			atOrBelow++
		}
	}
	return float64(atOrBelow) / float64(len(values))
}

// EWMA is an exponentially weighted moving average with the given smoothing factor.
type EWMA struct {
	Alpha float64
	value float64
	ready bool
}

func NewEWMA(alpha float64) *EWMA {
	if alpha <= 0 || alpha > 1 {
		alpha = 0.2
	}
	return &EWMA{Alpha: alpha}
}

func (e *EWMA) Observe(x float64) {
	if !e.ready {
		e.value, e.ready = x, true
		return
	}
	e.value = e.Alpha*x + (1-e.Alpha)*e.value
}

func (e *EWMA) Value() (float64, bool) { return e.value, e.ready }

// BaselineContext is what a baseline is conditioned on.
//
// Spec section 24 forbids a fixed global threshold, and this is why: 40 intents a
// second is unremarkable for a liquid name at the open and extraordinary for a thin
// one at midday. A baseline that ignores context compares those two.
type BaselineContext struct {
	InstrumentID  string
	MarketSession string
	HourUTC       int
}

// Baseline is a historical sample for one context.
//
// It stores observations rather than only summary statistics so that a percentile
// can be computed later and so the sample size is visible. A deviation computed from
// four observations is not a finding, and Confidence says so.
type Baseline struct {
	Context      BaselineContext
	Observations []float64

	// MinObservations is the sample size below which this baseline reports low
	// confidence. Thirty is a documented default, not a calibrated threshold: its
	// provenance is convention, and spec section 60 forbids pretending otherwise.
	MinObservations int
}

func NewBaseline(ctx BaselineContext) *Baseline {
	return &Baseline{Context: ctx, MinObservations: 30}
}

func (b *Baseline) Observe(ratePerSecond float64) {
	b.Observations = append(b.Observations, ratePerSecond)
}

// Deviation is a burst measurement, reported with everything needed to disagree
// with it (spec section 24).
type Deviation struct {
	Observed       float64
	BaselineMedian float64
	MAD            float64
	RobustScore    float64
	Percentile     float64
	SampleSize     int
	SufficientData bool

	// Explanation states in words how the score was produced, so a reader who has
	// never opened this package can still audit the claim.
	Explanation string
}

// Compare measures an observation against the baseline.
//
// It never returns a bare number. A score without its sample size, its median and
// its spread cannot be argued with, and a fleet-risk figure nobody can argue with is
// one nobody should act on.
func (b *Baseline) Compare(observed float64) Deviation {
	d := Deviation{
		Observed:       observed,
		SampleSize:     len(b.Observations),
		SufficientData: len(b.Observations) >= b.MinObservations,
	}

	if len(b.Observations) == 0 {
		d.Explanation = "no baseline observations; nothing to compare against"
		return d
	}

	d.BaselineMedian = Median(b.Observations)
	d.MAD = MAD(b.Observations)
	d.Percentile = PercentileOf(b.Observations, observed)

	switch {
	case d.MAD > 0:
		d.RobustScore = (observed - d.BaselineMedian) / (madToSigma * d.MAD)
		d.Explanation = "(observed - median) / (1.4826 * MAD), over " +
			itoa(d.SampleSize) + " observations"
	case observed == d.BaselineMedian:
		d.Explanation = "the baseline has no spread and the observation equals its median"
	default:
		// Every historical observation was identical. Dividing by zero spread would
		// produce infinity, which reads as certainty; the honest answer is that the
		// baseline cannot scale this deviation.
		d.Explanation = "the baseline has zero spread, so no scaled deviation can be " +
			"computed; the raw difference is " + ftoa(observed-d.BaselineMedian)
	}

	if !d.SufficientData {
		d.Explanation += "; sample of " + itoa(d.SampleSize) + " is below the " +
			itoa(b.MinObservations) + " needed for confidence"
	}
	return d
}

// RateOf returns intents per second for a window.
func RateOf(intentCount int, w Window) float64 {
	seconds := w.Duration().Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(intentCount) / seconds
}

// SessionOf is a coarse market-session label.
//
// It is deliberately crude and US-centric, and it is wrong for other venues. It
// exists so that baselines are conditioned on something rather than nothing, and it
// must be replaced by real venue calendars before anyone enforces on a burst score.
func SessionOf(t time.Time) string {
	switch h := t.UTC().Hour(); {
	case h >= 13 && h < 14:
		return "US_OPEN"
	case h >= 14 && h < 20:
		return "US_REGULAR"
	case h >= 20 && h < 21:
		return "US_CLOSE"
	case h >= 8 && h < 13:
		return "US_PREMARKET"
	default:
		return "US_OVERNIGHT"
	}
}

func minOf(values []float64) float64 {
	m := values[0]
	for _, v := range values {
		if v < m {
			m = v
		}
	}
	return m
}

func maxOf(values []float64) float64 {
	m := values[0]
	for _, v := range values {
		if v > m {
			m = v
		}
	}
	return m
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func ftoa(f float64) string {
	whole := int(f)
	frac := int(math.Abs(f-float64(whole)) * 100)
	return itoa(whole) + "." + itoa(frac)
}
