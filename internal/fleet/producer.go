package fleet

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// The measurement producer.
//
// Everything before this phase could measure a fleet if someone handed it the
// intents. Nothing handed it any: Measure was called by tests, InsertMeasurements by
// a benchmark, and the intelligence API read a table nothing wrote. The engine could
// answer questions about a fleet it never observed.
//
// The producer closes each window on a tick, measures every configured cohort over
// it, and writes the results. It reads and writes the analytical store only. It
// cannot submit an order, and it cannot change a customer's policy: spec section 29
// and INV-009 both say the intelligence plane recommends, and only an authorized
// customer control enforces.

// Producer turns stored intents into fleet measurements.
type Producer struct {
	Store   *Sink
	Cohorts []Cohort

	// Interval is both the tick and the width of the window measured. A window
	// narrower than the tick would leave gaps nothing ever measured; a wider one
	// would double-count intents across consecutive windows, and every robust
	// statistic downstream would be computed over a population that overlaps itself.
	Interval time.Duration

	// Lag holds each window open before measuring it, because an intent decided at
	// 14:59:59.9 can land in the analytical store after 15:00:00. Measuring a window
	// the instant it closes systematically undercounts its tail.
	Lag time.Duration

	Log *slog.Logger
	Now func() time.Time

	// lastMeasured is the end of the most recent window written, per tenant.
	lastMeasured map[string]time.Time
}

func (p *Producer) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

func (p *Producer) log() *slog.Logger {
	if p.Log != nil {
		return p.Log
	}
	return slog.Default()
}

// Window returns the most recent window that is closed and settled.
//
// Aligned to the interval rather than to the moment the producer happened to start,
// so two replicas measure the same boundaries and a restart does not shift every
// window by however long the process was down.
func (p *Producer) window(at time.Time) Window {
	end := at.Add(-p.Lag).Truncate(p.Interval)
	return Window{Start: end.Add(-p.Interval), End: end}
}

// RunOnce measures the current window for every cohort and writes the results.
//
// It returns what it wrote so a caller can act on it, and an error only if the store
// could not be reached. A cohort that matched nothing is a real measurement of zero
// intents, not a failure: "no agent traded this window" is a finding.
func (p *Producer) RunOnce(ctx context.Context) ([]Measurement, error) {
	if p.Store == nil {
		return nil, fmt.Errorf("no analytical store is configured")
	}
	if len(p.Cohorts) == 0 {
		return nil, nil
	}
	if p.lastMeasured == nil {
		p.lastMeasured = map[string]time.Time{}
	}

	w := p.window(p.now())

	// Group cohorts by tenant, so a window is read from the store once however many
	// cohorts are defined over it.
	byTenant := map[string][]Cohort{}
	for _, c := range p.Cohorts {
		byTenant[c.TenantID] = append(byTenant[c.TenantID], c)
	}

	var written []Measurement
	for tenant, cohorts := range byTenant {
		if last, ok := p.lastMeasured[tenant]; ok && !w.End.After(last) {
			// Already measured. Writing it again would double every count in a
			// store that does not deduplicate.
			continue
		}

		observed, err := p.Store.LoadWindow(ctx, tenant, w)
		if err != nil {
			return written, fmt.Errorf("load window for %s: %w", tenant, err)
		}

		measurements := make([]Measurement, 0, len(cohorts))
		for _, c := range cohorts {
			measurements = append(measurements, MeasureObserved(c, w, observed))
		}
		if err := p.Store.InsertMeasurements(ctx, measurements); err != nil {
			return written, fmt.Errorf("write measurements for %s: %w", tenant, err)
		}

		p.lastMeasured[tenant] = w.End
		written = append(written, measurements...)
	}
	return written, nil
}

// Run measures on a ticker until the context is cancelled.
//
// A failed tick is logged and the loop continues. The analytical plane is explicitly
// allowed to be behind or unavailable without stopping anything (INV-005): a producer
// that exited on a ClickHouse outage would turn a telemetry problem into a permanent
// gap in the fleet history, which is the outcome it exists to prevent.
func (p *Producer) Run(ctx context.Context) {
	ticker := time.NewTicker(p.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			written, err := p.RunOnce(ctx)
			if err != nil {
				p.log().Error("fleet measurement failed", "err", err)
				continue
			}
			if len(written) > 0 {
				p.log().Info("fleet measured",
					"cohorts", len(written), "window_end", written[0].Window.End)
			}
		}
	}
}
