package incident

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agentic-assurance/internal/evidence"
)

// TimelineEntry is one line of the account (spec section 49).
type TimelineEntry struct {
	At          time.Time
	EventName   evidence.EventName
	Producer    string
	Description string

	// Enforced separates what the system recommended from what was actually done.
	// Spec section 49 ends by requiring both, and a timeline that shows one is
	// answering a different question.
	Enforced bool
	Actor    string
}

// Timeline is an incident reconstructed from evidence.
//
// The reconstruction is the point. The incident object in memory is a convenience;
// the evidence is the record, and a review six months later reads this. If the two
// ever disagree, the evidence is right.
type Timeline struct {
	IncidentID string
	TenantID   string
	Entries    []TimelineEntry

	// Recommended and Applied are the two questions section 49 ends on: what did
	// the system recommend, and what did the customer actually do.
	Recommended []string
	Applied     []string
}

// Reconstruct rebuilds an incident's timeline from evidence events.
//
// It takes events rather than an incident, on purpose: the Phase 10 exit criterion
// is that an incident can be replayed from evidence, and a function that accepted
// the incident would be replaying memory instead.
func Reconstruct(events []evidence.Event) (Timeline, error) {
	if len(events) == 0 {
		return Timeline{}, fmt.Errorf("no evidence: nothing to reconstruct")
	}

	ordered := append([]evidence.Event(nil), events...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].OccurredAt.Equal(ordered[j].OccurredAt) {
			return ordered[i].Sequence < ordered[j].Sequence
		}
		return ordered[i].OccurredAt.Before(ordered[j].OccurredAt)
	})

	t := Timeline{
		IncidentID: ordered[0].AggregateID,
		TenantID:   ordered[0].TenantID,
	}

	for _, e := range ordered {
		entry := TimelineEntry{
			At:        e.OccurredAt.UTC(),
			EventName: e.EventName,
			Producer:  e.Producer,
		}

		switch e.EventName {
		case evidence.IncidentCreated:
			entry.Description = describeCreation(e)

		case evidence.ControlRecommended:
			rec, _ := e.Payload["recommendation"].(string)
			entry.Description = "recommended: " + rec
			entry.Enforced = false
			t.Recommended = append(t.Recommended, rec)

		case evidence.ControlEnforced:
			// The platform acting on a control, not a person applying one. It carries
			// no actor on purpose: an order refused by a standing control is not a
			// human decision, and counting it as one would answer "what the customer
			// did" with a list of automated refusals.
			code, _ := e.Payload["control"].(string)
			entry.Description = "enforced: " + code
			entry.Enforced = true

		case evidence.ControlApplied:
			control, _ := e.Payload["control"].(string)
			actor, _ := e.Payload["actor"].(string)
			entry.Description = "applied: " + control
			entry.Enforced = true
			entry.Actor = actor
			t.Applied = append(t.Applied, control)

		case evidence.IncidentUpdated, evidence.IncidentEscalated, evidence.IncidentClosed:
			actor, _ := e.Payload["actor"].(string)
			action, _ := e.Payload["action"].(string)
			reason, _ := e.Payload["reason"].(string)
			entry.Actor = actor
			entry.Description = fmt.Sprintf("%s by %s: %s", action, actor, reason)

		default:
			entry.Description = string(e.EventName)
		}

		t.Entries = append(t.Entries, entry)
	}

	return t, nil
}

// String renders the timeline in the shape spec section 49 shows.
func (t Timeline) String() string {
	var b strings.Builder
	for _, e := range t.Entries {
		fmt.Fprintf(&b, "%s %s\n", e.At.Format("15:04:05.000"), e.Description)
	}
	return b.String()
}

// AnsweredQuestions reports whether the timeline can answer everything spec section
// 49 requires of it.
//
// It is a checklist rather than prose because "the incident is reconstructable" is
// the Phase 10 exit criterion, and a criterion nobody can evaluate is not one.
func (t Timeline) AnsweredQuestions() map[string]bool {
	answers := map[string]bool{
		"what happened":         false,
		"when":                  false,
		"which cohort":          false,
		"which dependencies":    false,
		"what evidence":         false,
		"what was recommended":  false,
		"what the customer did": false,
	}

	for _, e := range t.Entries {
		if e.EventName == evidence.IncidentCreated {
			answers["what happened"] = true
			answers["which cohort"] = strings.Contains(e.Description, "cohort")
			answers["which dependencies"] = strings.Contains(e.Description, "dependenc")
			answers["what evidence"] = strings.Contains(e.Description, "because")
		}
		if !e.At.IsZero() {
			answers["when"] = true
		}
		if e.EventName == evidence.ControlRecommended {
			answers["what was recommended"] = true
		}
		if e.Actor != "" {
			answers["what the customer did"] = true
		}
	}
	return answers
}

func describeCreation(e evidence.Event) string {
	severity, _ := e.Payload["severity"].(string)
	rule, _ := e.Payload["severity_rule"].(string)
	cohort, _ := e.Payload["cohort"].(string)

	var reasons []string
	if raw, ok := e.Payload["anomalies"].([]any); ok {
		for _, item := range raw {
			a, ok := item.(map[string]any)
			if !ok {
				continue
			}
			observation, _ := a["observation"].(string)
			reasons = append(reasons, observation)
		}
	}

	var deps []string
	if raw, ok := e.Payload["shared_dependencies"].([]any); ok {
		for _, item := range raw {
			if s, ok := item.(string); ok {
				deps = append(deps, s)
			}
		}
	}

	description := fmt.Sprintf("%s incident opened for cohort %s, because %s (rule: %s)",
		severity, cohort, strings.Join(reasons, "; "), rule)

	if len(deps) > 0 {
		description += "; shared dependencies: " + strings.Join(deps, ", ")
	}
	return description
}
