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

	// Through EventsFor, which is the incident's own rendering of itself.
	//
	// This used to build one incident.created.v1 by hand, and the hand-built payload
	// was not the one anything reads. It carried cohort_id where the timeline reads
	// cohort, and anomalies as a list of rule names where the timeline reads objects
	// with an observation — so a timeline reconstructed from a real incident lost the
	// cohort and every reason, while the tests passed because they fed it EventsFor.
	//
	// Worse, it never emitted control.recommended.v1 at all. The recommendation lived
	// in a payload field of the creation event, so POST /v1/controls answered "this
	// incident recommended nothing" for every incident the platform actually detected:
	// the authorization path worked only against evidence written by hand.
	//
	// One producer, so there is nothing for the two to disagree about.
	for _, e := range inc.EventsFor("fleet-engine", 1) {
		if _, err := d.Evidence.Append(ctx, e); err != nil {
			d.log().Error("incident evidence not recorded",
				"incident_id", inc.IncidentID, "event", string(e.EventName), "err", err)
		}
	}
}
