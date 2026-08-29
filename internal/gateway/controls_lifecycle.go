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
	"agentic-assurance/internal/identity"
)

// Lifting a control, and seeing which ones exist.
//
// POST /v1/controls landed without either, which is the same gap POST
// /v1/authority-grants/{id}/revoke was built to close and it reappeared one endpoint
// later: the store had the columns, the catalog had control.revoked.v1, and nothing
// wrote them. A tenant-wide READ_ONLY control refuses every order in the tenant, so
// the only way to lift one was a psql prompt — during exactly the incident where that
// is the worst way to work, and out of reach entirely for an operator with no database
// access.

// ControlLifecycle lifts a control and lists what a tenant has.
type ControlLifecycle interface {
	Revoke(ctx context.Context, tenantID, controlID, revokedBy string, at time.Time) (bool, error)
	List(ctx context.Context, tenantID string) ([]control.Control, error)

	// Forget drops a throttle's counted window. A scope that was throttled and then
	// released must not carry a spent window into the next incident.
	Forget(ctx context.Context, tenantID, controlID string) error
}

// RevokeControlHandler is POST /v1/controls/{id}/revoke.
func RevokeControlHandler(controls ControlLifecycle, evidenceStore *evidence.Store,
	creds *identity.Credentials, verifier *identity.Verifier, now func() time.Time) http.HandlerFunc {

	if now == nil {
		now = time.Now
	}

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := callerTenant(r, creds, verifier)
		if tenant == "" {
			writeJSON(w, status, errorBody(message))
			return
		}
		if controls == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no control store is configured"))
			return
		}
		if verifier == nil {
			verifier = &identity.Verifier{}
		}
		if !verifier.Resolve(presentedFrom(r, creds)).MayIssueAuthority {
			writeJSON(w, http.StatusForbidden, errorBody(
				"this credential may not change fleet controls (INV-009); name the "+
					"authorizer in GATEWAY_GRANT_ISSUERS"))
			return
		}

		var req struct {
			RevokedBy string `json:"revoked_by"`
			Reason    string `json:"reason"`
		}
		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("the request is not a revocation: "+err.Error()))
			return
		}
		if strings.TrimSpace(req.RevokedBy) == "" || strings.TrimSpace(req.Reason) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody(
				"revoked_by and reason are required; lifting a control is the act whose "+
					"whole purpose is to be explained afterwards (spec section 36)"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		id := r.PathValue("id")
		at := now().UTC()
		already, err := controls.Revoke(ctx, tenant, id, strings.TrimSpace(req.RevokedBy), at)
		if errors.Is(err, control.ErrControlNotFound) {
			writeJSON(w, http.StatusNotFound, errorBody("no control by that id for this tenant"))
			return
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the control could not be revoked"))
			return
		}

		if !already {
			// After the revocation and best effort: a counted window that outlives its
			// control throttles nobody, because the control it belongs to no longer
			// applies. Failing here must not turn a successful revocation into an
			// error an operator retries under pressure.
			_ = controls.Forget(ctx, tenant, id)
			recordControlRevocation(ctx, evidenceStore, tenant, id,
				strings.TrimSpace(req.RevokedBy), strings.TrimSpace(req.Reason), at)
		}

		// 200 with already, not 409, exactly as revoking a grant is. Lifting a control
		// is what an operator reaches for under pressure, and one who hits it twice
		// should be told it is done rather than handed an error to interpret.
		writeJSON(w, http.StatusOK, map[string]any{
			"control_id": id,
			"revoked_at": at,
			"revoked_by": strings.TrimSpace(req.RevokedBy),
			"already":    already,
		})
	}
}

// ListControlsHandler is GET /v1/controls.
//
// Every control, in force or not. A refusal names a control id, and until this existed
// nothing could turn that id into what it was, who authorized it and when it ends.
func ListControlsHandler(controls ControlLifecycle, creds *identity.Credentials,
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
		if controls == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no control store is configured"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		stored, err := controls.List(ctx, tenant)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the controls could not be read"))
			return
		}

		at := now().UTC()
		out := make([]map[string]any, 0, len(stored))
		for _, c := range stored {
			entry := map[string]any{
				"control_id":     c.ControlID,
				"incident_id":    c.IncidentID,
				"action":         string(c.Action),
				"cohort_id":      c.CohortID,
				"agent_id":       c.AgentID,
				"agent_ids":      c.AgentIDs,
				"account_id":     c.AccountID,
				"authorized_by":  c.AuthorizedBy,
				"reason":         c.Reason,
				"applied_at":     c.AppliedAt,
				"expires_at":     c.ExpiresAt,
				"max_orders":     c.MaxOrders,
				"window_seconds": int(c.Window.Seconds()),
				// Computed rather than left to the reader, so nobody has to compare an
				// expiry against their own clock to know whether orders are being
				// refused right now.
				"in_force": c.InForce(at),
			}
			if c.RevokedAt != nil {
				entry["revoked_at"] = c.RevokedAt.UTC()
				entry["revoked_by"] = c.RevokedBy
			}
			out = append(out, entry)
		}
		writeJSON(w, http.StatusOK, map[string]any{"controls": out, "as_of": at})
	}
}

func recordControlRevocation(ctx context.Context, store *evidence.Store,
	tenantID, controlID, revokedBy, reason string, at time.Time) {

	if store == nil {
		return
	}
	_, _ = store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_revoked_%d", controlID, at.UnixNano()),
		EventName:     evidence.ControlRevoked,
		TenantID:      tenantID,
		AggregateID:   controlID,
		CorrelationID: controlID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload: map[string]any{
			"control_id": controlID,
			"actor":      revokedBy,
			"reason":     reason,
		},
	})
}
