package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"agentic-assurance/internal/control"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/identity"
)

// POST /v1/controls.
//
// This is where a fleet recommendation becomes something that binds, and it is the
// only place in the platform where that transition happens. INV-009 is the rule:
// fleet intelligence recommends, customer policy authorizes. internal/fleet makes it a
// property of the types — fleet.Authorize needs an Authorization the intelligence
// plane cannot construct — and nothing outside a test ever called it, so every
// recommendation stopped at shadow mode for want of a surface.
//
// It lives on the gateway rather than the fleet engine on purpose. A control is
// enforcement, the fleet engine is the intelligence plane, and an endpoint that let
// that plane produce enforceable controls would be INV-009 broken by deployment
// topology while the type signatures still looked right.

// ControlStore persists an authorized control.
type ControlStore interface {
	Save(ctx context.Context, c control.Control) error
}

// RecommendationSource is how the gateway checks that the platform actually
// recommended what a customer is authorizing.
type RecommendationSource interface {
	ByAggregate(ctx context.Context, tenantID, aggregateID string) ([]evidence.Event, error)
}

type controlRequest struct {
	ControlID  string `json:"control_id"`
	IncidentID string `json:"incident_id"`
	Action     string `json:"action"`

	AgentID   string `json:"agent_id"`
	AccountID string `json:"account_id"`

	// Scope is stated rather than inferred. An empty agent and account means the
	// whole tenant, which is a real thing to authorize and a terrible default, so the
	// caller says "tenant" out loud instead of omitting two fields.
	Scope string `json:"scope"`

	AuthorizedBy   string `json:"authorized_by"`
	PolicyBundleID string `json:"policy_bundle_id"`
	Reason         string `json:"reason"`

	ExpiresAt time.Time `json:"expires_at"`
}

func (c controlRequest) validate() []string {
	var problems []string
	require := func(value, name string) {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, name+" is required")
		}
	}
	require(c.ControlID, "control_id")
	require(c.IncidentID, "incident_id")
	require(c.AuthorizedBy, "authorized_by")
	require(c.PolicyBundleID, "policy_bundle_id")
	require(c.Reason, "reason")

	if c.ExpiresAt.IsZero() {
		problems = append(problems, "expires_at is required; a control nobody has to "+
			"renew is one that throttles an agent forever because of an incident last spring")
	}

	switch strings.ToLower(strings.TrimSpace(c.Scope)) {
	case "tenant":
		if c.AgentID != "" || c.AccountID != "" {
			problems = append(problems, `scope "tenant" names no agent or account`)
		}
	case "agent":
		require(c.AgentID, "agent_id")
	case "account":
		require(c.AccountID, "account_id")
	default:
		problems = append(problems, `scope must be "tenant", "agent" or "account"; `+
			"an omitted scope would read as the whole tenant, which is the widest "+
			"control there is and never something to arrive at by leaving a field out")
	}

	action, ok := control.ParseAction(c.Action)
	if !ok {
		problems = append(problems, "action must be one of THROTTLE, REQUIRE_APPROVAL, "+
			"ISOLATE_COHORT, READ_ONLY (spec section 16)")
	} else if !control.Enforceable(action) {
		problems = append(problems, string(action)+" has no enforcement path in this "+
			"build and is refused rather than stored. A control the platform records "+
			"and does not apply is the shadow-mode confusion this endpoint exists to end")
	}
	return problems
}

// IssueControlHandler is POST /v1/controls.
func IssueControlHandler(controls ControlStore, recommendations RecommendationSource,
	evidenceStore *evidence.Store, creds *identity.Credentials,
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
		if controls == nil || recommendations == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no control store is configured"))
			return
		}

		// The same privilege that issues authority, and for the same reason: a control
		// changes what agents may do, and a credential that could both submit orders
		// and authorize the controls over them is an agent adjusting its own leash.
		if verifier == nil {
			verifier = &identity.Verifier{}
		}
		if !verifier.Resolve(presentedFrom(r, creds)).MayIssueAuthority {
			writeJSON(w, http.StatusForbidden, errorBody(
				"this credential may not authorize fleet controls. Fleet intelligence "+
					"recommends and customer policy authorizes (INV-009); name the "+
					"authorizer in GATEWAY_GRANT_ISSUERS"))
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil || len(raw) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}

		var req controlRequest
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("the request is not a control authorization: "+err.Error()))
			return
		}

		if problems := req.validate(); len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "the control cannot be authorized as written",
				"details": problems,
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		// What the platform actually recommended, read from evidence rather than
		// taken from the request. A customer authorizes a recommendation; a request
		// that could describe its own would let an operator apply a fleet control
		// nothing ever suggested and have the record say the platform proposed it.
		rec, err := recommendationFor(ctx, recommendations, tenant, strings.TrimSpace(req.IncidentID))
		if err != nil {
			writeJSON(w, http.StatusNotFound, errorBody(err.Error()))
			return
		}

		at := now().UTC()
		action, _ := control.ParseAction(req.Action)
		rec.WouldHave = action
		rec.TenantID = tenant

		// Through fleet.Authorize, not around it. It is the only function that
		// produces an enforceable control, and a handler that assembled the record
		// itself would leave INV-009 enforced by a type nobody constructs.
		authorization, err := fleet.NewAuthorization(strings.TrimSpace(req.AuthorizedBy),
			strings.TrimSpace(req.PolicyBundleID), strings.TrimSpace(req.Reason), at)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		authorized, err := fleet.Authorize(rec, authorization, at)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}

		if !authorized.AppliedAt.Before(req.ExpiresAt) {
			writeJSON(w, http.StatusBadRequest,
				errorBody("expires_at is not in the future; the control would never bind"))
			return
		}

		stored := control.Control{
			ControlID:      strings.TrimSpace(req.ControlID),
			TenantID:       tenant,
			IncidentID:     strings.TrimSpace(req.IncidentID),
			Action:         action,
			CohortID:       authorized.Recommendation.CohortID,
			AgentID:        strings.TrimSpace(req.AgentID),
			AccountID:      strings.TrimSpace(req.AccountID),
			AuthorizedBy:   authorization.AuthorizedBy,
			PolicyBundleID: authorization.PolicyBundleID,
			Reason:         authorization.Reason,
			AppliedAt:      authorized.AppliedAt,
			ExpiresAt:      req.ExpiresAt.UTC(),
		}
		if err := controls.Save(ctx, stored); err != nil {
			if errors.Is(err, control.ErrControlExists) {
				writeJSON(w, http.StatusConflict, errorBody(
					"a control with this id already exists. A control records what a "+
						"named person authorized at a moment, so its scope and expiry "+
						"are not edited in place: revoke it and authorize another"))
				return
			}
			writeJSON(w, http.StatusInternalServerError, errorBody("the control could not be stored"))
			return
		}

		recordControl(ctx, evidenceStore, stored, at)

		writeJSON(w, http.StatusCreated, map[string]any{
			"control_id":       stored.ControlID,
			"incident_id":      stored.IncidentID,
			"action":           string(stored.Action),
			"cohort_id":        stored.CohortID,
			"agent_id":         stored.AgentID,
			"account_id":       stored.AccountID,
			"authorized_by":    stored.AuthorizedBy,
			"applied_at":       stored.AppliedAt,
			"expires_at":       stored.ExpiresAt,
			"enforced":         authorized.Enforced(),
			"recommendation":   authorized.Recommendation.String(),
			"policy_bundle_id": stored.PolicyBundleID,
		})
	}
}

// recommendationFor rebuilds the recommendation from the incident's evidence.
func recommendationFor(ctx context.Context, source RecommendationSource,
	tenantID, incidentID string) (fleet.Recommendation, error) {

	events, err := source.ByAggregate(ctx, tenantID, incidentID)
	if err != nil || len(events) == 0 {
		return fleet.Recommendation{}, fmt.Errorf(
			"no incident %s for this tenant, so there is nothing to authorize", incidentID)
	}

	rec := fleet.Recommendation{RecommendationID: incidentID}
	found := false
	for _, e := range events {
		switch e.EventName {
		case evidence.IncidentCreated:
			cohort, _ := e.Payload["cohort"].(string)
			rec.CohortPredicate = cohort
			rec.CohortID = cohort
		case evidence.ControlRecommended:
			text, _ := e.Payload["recommendation"].(string)
			rec.Reason = text
			rec.GeneratedAt = e.OccurredAt
			found = true
		}
	}
	if !found {
		return fleet.Recommendation{}, fmt.Errorf(
			"incident %s carries no recommendation; a customer authorizes what the "+
				"platform recommended, and this one recommended nothing", incidentID)
	}
	return rec, nil
}

func recordControl(ctx context.Context, store *evidence.Store, c control.Control, at time.Time) {
	if store == nil {
		return
	}
	_, _ = store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_applied_%d", c.ControlID, at.UnixNano()),
		EventName:     evidence.ControlApplied,
		TenantID:      c.TenantID,
		AggregateID:   c.IncidentID,
		CorrelationID: c.IncidentID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload: map[string]any{
			"control":          string(c.Action),
			"control_id":       c.ControlID,
			"actor":            c.AuthorizedBy,
			"policy_bundle_id": c.PolicyBundleID,
			"reason":           c.Reason,
			"agent_id":         c.AgentID,
			"account_id":       c.AccountID,
			"expires_at":       c.ExpiresAt,
			"enforced":         true,
		},
	})
}
