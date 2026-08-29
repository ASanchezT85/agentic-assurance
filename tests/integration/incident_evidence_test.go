//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/incident"
)

// From the detector to a reader, through the real store.
//
// Every existing test fed the timeline Incident.EventsFor output. The running detector
// wrote something else: one hand-built incident.created.v1 with cohort_id where the
// timeline reads cohort, anomalies as rule names where it reads objects, and no
// control.recommended.v1 at all — so a real incident reconstructed without its cohort
// or its reasons, and POST /v1/controls answered "this incident recommended nothing"
// for every incident the platform ever detected.
//
// Nothing caught it because nothing ran the producer and the reader in one test.

func alarmingVector(t *testing.T, tenant string, at time.Time) fleet.RiskVector {
	t.Helper()
	cohort, err := fleet.NewCohort(tenant,
		fleet.Predicate{Field: fleet.FieldInstrument, Value: "instr_us_equity_00206R102"},
		fleet.Predicate{Field: fleet.FieldSide, Value: "BUY"})
	if err != nil {
		t.Fatalf("cohort: %v", err)
	}
	return fleet.RiskVector{
		TenantID: tenant,
		Cohort:   cohort,
		Window:   fleet.Window{Start: at.Add(-time.Minute), End: at},
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

func TestADetectedIncidentIsReadableAsATimeline(t *testing.T) {
	pool := idemPool(t)
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_inc_%d", at.UnixNano())

	evidenceStore := evidence.NewStore(pool)
	detector := incident.NewDetector(incident.NewStore(pool), evidenceStore, nil)
	detector.Observe(ctx, alarmingVector(t, tenant, at))

	incidents, err := incident.NewStore(pool).List(ctx, tenant, 5)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(incidents) == 0 {
		t.Fatal("the detector opened no incident from an alarming vector")
	}

	events, err := evidenceStore.ByAggregate(ctx, tenant, incidents[0].IncidentID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}

	timeline, err := incident.Reconstruct(events)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	for question, answered := range timeline.AnsweredQuestions() {
		// Nobody has acted yet, so this one is honestly unanswered.
		if question == "what the customer did" {
			continue
		}
		if !answered {
			t.Errorf("an incident the detector opened cannot answer %q from its own "+
				"evidence (spec section 49)", question)
		}
	}

	if len(timeline.Recommended) == 0 {
		t.Error("the incident carries no recommendation, so nothing can be authorized " +
			"from it through POST /v1/controls")
	}
}
