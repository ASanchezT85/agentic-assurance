package incident

import (
	"strings"
	"testing"

	"agentic-assurance/internal/evidence"
)

// The detector's evidence has to be the evidence everything else reads.
//
// It was not. The detector hand-built one incident.created.v1 whose payload carried
// cohort_id where the timeline reads cohort, and anomalies as rule names where the
// timeline reads objects with an observation — and it never emitted
// control.recommended.v1 at all. Every test passed, because every test fed the
// timeline EventsFor output, which nothing in the running system produced.
//
// So this test starts where the detector starts and ends where a reader ends.

func detectorIncident(t *testing.T) Incident {
	t.Helper()

	// The same alarming vector the rest of this package tests against, so this proves
	// something about the detector rather than about a fixture written to pass.
	r := alarming(t)
	anomalies := Detect(r, DefaultRules(), at)
	if len(anomalies) == 0 {
		t.Fatal("the fixture triggers no anomaly; this test would prove nothing")
	}
	inc, ok := Open(r, anomalies, DefaultRules(), "corr_1", at)
	if !ok {
		t.Fatal("no incident opened from an anomalous vector")
	}
	return inc
}

// What the detector writes is what an operator reads, whole.
func TestTheDetectorsEvidenceReconstructsTheIncident(t *testing.T) {
	inc := detectorIncident(t)

	// Exactly what Detector.record appends.
	events := inc.EventsFor("fleet-engine", 1)

	timeline, err := Reconstruct(events)
	if err != nil {
		t.Fatalf("reconstruct: %v", err)
	}

	for question, answered := range timeline.AnsweredQuestions() {
		// "what the customer did" is legitimately unanswered: nobody has acted yet.
		if question == "what the customer did" {
			continue
		}
		if !answered {
			t.Errorf("the detector's own evidence cannot answer %q (spec section 49)", question)
		}
	}
}

// The recommendation has to be its own event, because that is what a customer
// authorizes. It used to be a payload field of the creation event, so POST /v1/controls
// answered "this incident recommended nothing" for every real incident.
func TestTheDetectorEmitsTheRecommendationAsItsOwnEvent(t *testing.T) {
	inc := detectorIncident(t)

	var recommendation *evidence.Event
	for i, e := range inc.EventsFor("fleet-engine", 1) {
		if e.EventName == evidence.ControlRecommended {
			recommendation = &inc.EventsFor("fleet-engine", 1)[i]
		}
	}
	if recommendation == nil {
		t.Fatal("no control.recommended.v1; nothing can be authorized from this incident")
	}

	text, _ := recommendation.Payload["recommendation"].(string)
	if strings.TrimSpace(text) == "" {
		t.Error("the recommendation event carries no recommendation")
	}
	if enforced, _ := recommendation.Payload["enforced"].(bool); enforced {
		t.Error("a recommendation is marked enforced; the platform recommends and the " +
			"customer authorizes (INV-009)")
	}
}

// The gateway reads the cohort out of the creation event to scope the control it
// stores. A payload key nobody agrees on is how the previous version of this went
// wrong, so the reader's key is asserted rather than assumed.
func TestTheCreationEventNamesTheCohortWhereReadersLookForIt(t *testing.T) {
	inc := detectorIncident(t)

	for _, e := range inc.EventsFor("fleet-engine", 1) {
		if e.EventName != evidence.IncidentCreated {
			continue
		}
		cohort, _ := e.Payload["cohort"].(string)
		if strings.TrimSpace(cohort) == "" {
			t.Fatal(`incident.created.v1 has no "cohort"; the timeline and the control ` +
				`endpoint both read that key`)
		}
		return
	}
	t.Fatal("no incident.created.v1 among the detector's events")
}
