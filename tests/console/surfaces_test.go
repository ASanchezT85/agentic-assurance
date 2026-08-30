//go:build console

package console

import (
	"net/http"
	"strings"
	"testing"
)

// The three states a source can be in must be three different screens.
//
// This is the Console's whole job. An unreachable fleet engine and a quiet fleet are
// different facts, and a surface that renders both the same way tells an operator the
// fleet is quiet when nothing is reporting at all. Every surface has to keep them apart,
// so every surface is checked.
func TestASourceThatCannotBeReadNeverLooksEmpty(t *testing.T) {
	surfaces := []struct {
		path     string
		endpoint string
		on       *string // which fake server answers: set below
	}{
		{path: "/fleet", endpoint: "/v1/fleet/state"},
		{path: "/dependencies", endpoint: "/v1/dependencies"},
		{path: "/incidents", endpoint: "/v1/incidents"},
		{path: "/lab", endpoint: "/v1/simulations"},
		{path: "/controls", endpoint: "/v1/controls"},
		{path: "/flow", endpoint: "/v1/intents"},
	}

	// Unreadable: the store behind the endpoint is down.
	p := newPlatform(t)
	for _, s := range surfaces {
		p.answer(s.endpoint, http.StatusServiceUnavailable, nil)
	}
	c := p.start(t)

	for _, s := range surfaces {
		t.Run("unavailable"+strings.ReplaceAll(s.path, "/", "_"), func(t *testing.T) {
			html := c.page(t, s.path)

			mustContain(t, html, "UNAVAILABLE",
				"The source could not be read and the surface does not say so. An "+
					"operator has to be able to tell that from the page.")
			mustContain(t, html, "Not available.",
				"The unavailable state has no explanation on it.")
			mustContain(t, html, "This is not an empty result",
				"Nothing distinguishes an unreadable source from a quiet one, which "+
					"is the distinction this product exists to make.")
			mustNotContain(t, html, "it had nothing to report",
				"An unreadable source is being described as one that answered.")
		})
	}
}

// An empty source says so, and says it differently.
func TestASourceThatAnsweredWithNothingSaysThat(t *testing.T) {
	p := newPlatform(t)
	p.answer("/v1/fleet/state", http.StatusOK, map[string]any{"rows": []any{}})
	p.answer("/v1/controls", http.StatusOK, map[string]any{"controls": []any{}})
	c := p.start(t)

	for _, path := range []string{"/fleet", "/controls"} {
		t.Run(strings.TrimPrefix(path, "/"), func(t *testing.T) {
			html := c.page(t, path)

			mustContain(t, html, "EMPTY",
				"The source answered with nothing and the surface does not say which "+
					"of the two silent states this is.")
			mustNotContain(t, html, "UNAVAILABLE",
				"A source that answered is being reported as unreachable, which would "+
					"send an operator looking for an outage that is not happening.")
			mustNotContain(t, html, "Not available.",
				"An empty result is being rendered as a failure.")
		})
	}
}

// A route this deployment does not serve is a configuration state, not a number.
//
// The fleet engine leaves the simulation API unrouted when no engine is configured. The
// Console printed "Simulations returned 404", which tells an operator a status code and
// nothing about the cause — the failure the 401 branch beside it exists to avoid.
func TestAnUnroutedEndpointIsNamedRatherThanNumbered(t *testing.T) {
	p := newPlatform(t)
	// Everything else answers; only the simulation API is absent, which is the real
	// shape of this deployment.
	p.answer("/v1/fleet/state", http.StatusOK, map[string]any{"rows": []any{}})
	c := p.start(t)

	html := c.page(t, "/lab")

	mustContain(t, html, "not served by this deployment",
		"A 404 is being reported as a bare status code.")
	mustNotContain(t, html, "returned 404",
		"The page prints a status number at an operator instead of a cause.")
}

// The evidence chain renders the events the endpoint returned.
//
// The regression this whole file exists for. The Console asked for a collection named
// "rows" against an endpoint that answers with "events", so ten events rendered as zero
// and the page said the source had nothing to report — about a response that had
// everything in it. Four structural guards touched that file and none could see it.
func TestTheEvidenceChainRendersTheEventsItWasGiven(t *testing.T) {
	p := newPlatform(t)
	p.answer("/v1/evidence", http.StatusOK, map[string]any{
		"tenant_id": "tenant_console",
		"count":     3,
		"events": []map[string]any{
			{
				"event_id": "e1", "event_name": "agent.intent.received.v1",
				"aggregate_id": "env_1", "correlation_id": "corr_1",
				"occurred_at": "2026-08-30T12:00:00Z", "producer": "assurance-gateway",
				"sequence": 1,
			},
			{
				"event_id": "e2", "event_name": "authority.evaluated.v1",
				"aggregate_id": "env_1", "correlation_id": "corr_1",
				"causation_id": "e1",
				"occurred_at":  "2026-08-30T12:00:01Z", "producer": "assurance-gateway",
				"sequence": 2, "payload": map[string]any{"allowed": true},
			},
			{
				"event_id": "e3", "event_name": "authority.evaluated.v1",
				"aggregate_id": "env_1", "correlation_id": "corr_1",
				"corrects_event_id": "e2",
				"occurred_at":       "2026-08-30T12:05:00Z", "producer": "assurance-gateway",
				"sequence": 3, "payload": map[string]any{"allowed": false},
			},
		},
	})
	c := p.start(t)

	html := c.page(t, "/flow?correlation_id=corr_1")

	mustContain(t, html, "LIVE",
		"A chain of three events is not being reported as live.")
	mustNotContain(t, html, "it had nothing to report",
		"A response carrying three events rendered as an empty one. This is the "+
			"defect the file this test lives beside was written for.")

	for _, event := range []string{"agent.intent.received.v1", "authority.evaluated.v1"} {
		mustContain(t, html, event, "An event in the chain is not on the page.")
	}
	mustContain(t, html, "sequence 2",
		"Sequence is not inspectable, and it is how a reader orders two events "+
			"recorded in the same instant.")
	mustContain(t, html, "caused by e1",
		"Causation is not inspectable, so a reader cannot follow what led to what.")
}

// A correction is marked, and the event it corrects is still there.
//
// The append-only rule, rendered. An audit trail that hides what was superseded is not
// one, and a reader has to be able to see both the original and the correction.
func TestACorrectionIsMarkedAndTheOriginalRemains(t *testing.T) {
	p := newPlatform(t)
	p.answer("/v1/evidence", http.StatusOK, map[string]any{
		"count": 2,
		"events": []map[string]any{
			{
				"event_id": "original", "event_name": "authority.evaluated.v1",
				"aggregate_id": "env_1", "correlation_id": "corr_1",
				"occurred_at": "2026-08-30T12:00:00Z", "producer": "assurance-gateway",
				"sequence": 1, "payload": map[string]any{"allowed": true},
			},
			{
				"event_id": "correction", "event_name": "authority.evaluated.v1",
				"aggregate_id": "env_1", "correlation_id": "corr_1",
				"corrects_event_id": "original",
				"occurred_at":       "2026-08-30T12:05:00Z", "producer": "assurance-gateway",
				"sequence": 2, "payload": map[string]any{"allowed": false},
			},
		},
	})
	c := p.start(t)

	html := c.page(t, "/flow?correlation_id=corr_1")

	mustContain(t, html, "CORRECTION",
		"A correction is not distinguished from an ordinary recorded event.")
	mustContain(t, html, "Corrects",
		"The correction does not say what it corrects.")
	// Both event ids are present: the original was not replaced by the thing that
	// corrected it.
	mustContain(t, html, "original",
		"The corrected event is gone from the page. A correction adds to the account; "+
			"it does not replace it.")
}

// A recommendation is never rendered as an applied control.
//
// INV-009 in the interface: the platform recommends and a customer authorizes. A screen
// that blurs the two cannot answer the last question an incident review asks.
func TestARecommendationIsNotRenderedAsEnforcement(t *testing.T) {
	p := newPlatform(t)
	p.answer("/v1/evidence", http.StatusOK, map[string]any{
		"count": 2,
		"events": []map[string]any{
			{
				"event_id": "rec", "event_name": "control.recommended.v1",
				"aggregate_id": "inc_1", "correlation_id": "corr_1",
				"occurred_at": "2026-08-30T12:00:00Z", "producer": "fleet-engine",
				"sequence": 1,
				"payload":  map[string]any{"enforced": false, "control": "THROTTLE"},
			},
			{
				"event_id": "applied", "event_name": "control.applied.v1",
				"aggregate_id": "inc_1", "correlation_id": "corr_1",
				"occurred_at": "2026-08-30T12:10:00Z", "producer": "assurance-gateway",
				"sequence": 2,
				"payload": map[string]any{
					"enforced": true, "control": "THROTTLE", "actor": "ops@example.test",
				},
			},
		},
	})
	c := p.start(t)

	html := c.page(t, "/incidents?correlation_id=corr_1")

	mustContain(t, html, "RECOMMENDED ONLY",
		"A non-binding recommendation is not labelled as one.")
	mustContain(t, html, "ENFORCING",
		"A control that took effect is not distinguished from one that was only "+
			"recommended.")
	mustContain(t, html, "ops@example.test",
		"The enforced control does not name who authorized it, so a review cannot "+
			"tell a customer action from a system one.")
}

// Controls that bind are separated from controls that no longer do.
func TestOnlyBindingControlsAreListedAsEnforcing(t *testing.T) {
	p := newPlatform(t)
	p.answer("/v1/controls", http.StatusOK, map[string]any{
		"controls": []map[string]any{
			{
				"control_id": "ctl_binding", "action": "THROTTLE",
				"agent_id": "agent_7", "authorized_by": "ops@example.test",
				"reason": "burst", "applied_at": "2026-08-30T11:00:00Z",
				"expires_at": "2026-08-31T11:00:00Z", "in_force": true,
			},
			{
				"control_id": "ctl_revoked", "action": "READ_ONLY",
				"agent_id": "agent_9", "authorized_by": "ops@example.test",
				"reason": "investigation", "applied_at": "2026-08-29T11:00:00Z",
				"expires_at": "2026-08-30T11:00:00Z", "in_force": false,
				"revoked_at": "2026-08-29T12:00:00Z", "revoked_by": "ops@example.test",
			},
		},
	})
	c := p.start(t)

	html := c.page(t, "/controls")

	mustContain(t, html, "Enforcing now",
		"What binds right now is not separated from what used to.")
	mustContain(t, html, "No longer binding",
		"Historical controls are not marked as historical.")
	mustContain(t, html, "REVOKED",
		"A revoked control is not shown as revoked.")

	// The scope is the agent, and it says so. A list-scoped control rendered as
	// "whole tenant" would tell an operator their entire customer is stopped when one
	// agent is.
	mustContain(t, html, "agent_7", "The control's scope is not shown.")
	mustNotContain(t, html, "whole tenant",
		"An agent-scoped control is being described as tenant-wide, which overstates "+
			"what is stopped.")
}
