package gateway

import (
	"context"
	"net/http"
	"strconv"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
)

// GET /v1/intents.
//
// Section 46 lists it and nothing served it, so the Flow console surface asked for a
// correlation id instead: there was no list to show. The obvious source was the
// idempotency table, and it is the wrong one — it holds intents that reached a venue,
// so a list built from it would show what was accepted and silently omit every
// refusal, which is the half of the record an assurance platform exists to keep.
//
// It is built from evidence for that reason, and it summarises nothing: each intent
// carries the events it produced, so a reader sees the refusal rather than this
// endpoint's opinion of it.

// IntentLister reads the recent evidence for a tenant.
type IntentLister interface {
	RecentAggregates(ctx context.Context, tenantID string, since time.Time, limit int) ([]evidence.Event, error)
}

// IntentListHandler is GET /v1/intents.
func IntentListHandler(events IntentLister, creds *identity.Credentials,
	verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := callerTenant(r, creds, verifier)
		if tenant == "" {
			writeJSON(w, status, errorBody(message))
			return
		}
		if events == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no evidence store is configured"))
			return
		}

		limit := 50
		if raw := r.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}

		// A window rather than all of history, and it is stated in the response. A
		// list that quietly covered "some of the past" would be read as "everything",
		// which is the reading that makes an empty page look like a quiet fleet.
		window := 24 * time.Hour
		if raw := r.URL.Query().Get("hours"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				window = time.Duration(parsed) * time.Hour
			}
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		at := now().UTC()
		chain, err := events.RecentAggregates(ctx, tenant, at.Add(-window), limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the intents could not be read"))
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"tenant_id": tenant,
			"since":     at.Add(-window),
			"as_of":     at,
			"intents":   summariseIntents(chain),
		})
	}
}

// summariseIntents folds an ordered event stream into one entry per envelope.
//
// The last event of each chain is reported as it was recorded, with its own code, not
// translated into a verdict this endpoint invents. A summary that can disagree with the
// record eventually does, and then the record is right (ADR-009).
func summariseIntents(chain []evidence.Event) []map[string]any {
	order := make([]string, 0, 16)
	byEnvelope := map[string]map[string]any{}

	for _, e := range chain {
		entry, seen := byEnvelope[e.AggregateID]
		if !seen {
			entry = map[string]any{
				"envelope_id":    e.AggregateID,
				"correlation_id": e.CorrelationID,
				"received_at":    e.OccurredAt.UTC(),
				"events":         []string{},
			}
			byEnvelope[e.AggregateID] = entry
			order = append(order, e.AggregateID)
		}

		entry["events"] = append(entry["events"].([]string), string(e.EventName))
		entry["last_event"] = string(e.EventName)
		entry["last_at"] = e.OccurredAt.UTC()

		switch e.EventName {
		case evidence.IntentReceived:
			copyPayload(entry, e, "instrument_id", "side", "order_type")
		case evidence.AuthorityEvaluated:
			copyPayload(entry, e, "code", "grant_id")
		case evidence.PolicyEvaluated:
			copyPayload(entry, e, "action", "decided_by")
		case evidence.ControlEnforced:
			copyPayload(entry, e, "control", "control_id")
		case evidence.OrderSubmitted, evidence.OrderAccepted, evidence.OrderFilled,
			evidence.OrderRejected, evidence.OrderUnknown:
			copyPayload(entry, e, "broker_order_id", "state", "reason")
		}
	}

	out := make([]map[string]any, 0, len(order))
	for _, id := range order {
		out = append(out, byEnvelope[id])
	}
	return out
}

func copyPayload(entry map[string]any, e evidence.Event, keys ...string) {
	for _, k := range keys {
		if v, ok := e.Payload[k]; ok && v != nil && v != "" {
			entry[k] = v
		}
	}
}
