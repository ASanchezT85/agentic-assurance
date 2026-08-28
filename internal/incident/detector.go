package incident

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
)

// Detector turns a measured window into an incident, when the window warrants one.
//
// This is the wiring that never existed. Detect, Open and Timeline.Reconstruct have
// been here since Phase 10 with tests; no process called them, nothing stored the
// result, and no surface returned it. The engine could reconstruct an incident nobody
// had opened.
//
// It recommends and never enforces. Opening an incident changes what an operator is
// told and nothing about what production does, which is INV-009 at the plane: this
// file cannot reach a policy bundle, an authority grant or a venue.
type Detector struct {
	Store *Store
	Rules DetectionRules

	// Evidence records that an incident was opened. Optional: losing it costs the
	// audit trail, not the incident.
	Evidence *evidence.Store

	Log *slog.Logger
	Now func() time.Time
}

func NewDetector(store *Store, ev *evidence.Store, log *slog.Logger) *Detector {
	return &Detector{Store: store, Rules: DefaultRules(), Evidence: ev, Log: log}
}

func (d *Detector) now() time.Time {
	if d.Now != nil {
		return d.Now().UTC()
	}
	return time.Now().UTC()
}

func (d *Detector) log() *slog.Logger {
	if d.Log != nil {
		return d.Log
	}
	return slog.Default()
}

// Observe examines one window's risk vector.
//
// It never returns an error and never fails a measurement. A detector that could stop
// the producer would mean a bug in incident opening cost the fleet history, and the
// fleet history is what the next incident is reconstructed from.
func (d *Detector) Observe(ctx context.Context, r fleet.RiskVector) {
	if d == nil || d.Store == nil {
		return
	}

	at := d.now()
	anomalies := Detect(r, d.Rules, at)
	if len(anomalies) == 0 {
		return
	}

	// The correlation id ties the incident to the window it came from, so an
	// investigator moving between the incident and the evidence is following one
	// chain rather than joining two.
	correlationID := fmt.Sprintf("incident_%s_%s_%d",
		r.TenantID, r.Cohort.ID(), r.Window.Start.UTC().Unix())

	inc, ok := Open(r, anomalies, d.Rules, correlationID, at)
	if !ok {
		return
	}

	opened, err := d.Store.Open(ctx, inc)
	if err != nil {
		d.log().Error("incident not recorded", "tenant", r.TenantID,
			"cohort", r.Cohort.ID(), "err", err)
		return
	}
	if !opened {
		// This cohort and window already have one. A situation that persists across
		// ten windows is one incident, not ten.
		return
	}

	d.record(ctx, inc, at)
	d.log().Info("incident opened",
		"incident_id", inc.IncidentID, "tenant", inc.TenantID,
		"severity", string(inc.Severity), "anomalies", len(inc.Anomalies),
		"rule", inc.SeverityRule)
}

func (d *Detector) record(ctx context.Context, inc Incident, at time.Time) {
	if d.Evidence == nil {
		return
	}

	names := make([]string, 0, len(inc.Anomalies))
	for _, a := range inc.Anomalies {
		names = append(names, a.Rule)
	}

	_, _ = d.Evidence.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       inc.IncidentID + "_opened",
		EventName:     evidence.IncidentCreated,
		TenantID:      inc.TenantID,
		AggregateID:   inc.IncidentID,
		CorrelationID: inc.CorrelationID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "fleet-engine",
		Sequence:      1,
		Payload: map[string]any{
			"cohort_id":           inc.Cohort.ID(),
			"severity":            string(inc.Severity),
			"severity_rule":       inc.SeverityRule,
			"anomalies":           names,
			"shared_dependencies": inc.SharedDependencies,
			"recommended":         inc.Recommended,
			"window_start":        inc.Window.Start.UTC(),
			"window_end":          inc.Window.End.UTC(),
		},
	})
}
