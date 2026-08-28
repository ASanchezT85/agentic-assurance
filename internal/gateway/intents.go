package gateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/identity"
)

// The two section 46 endpoints that were listed and never built.
//
// GET /v1/intents/{id} closes the caller's loop. Submission returned an outcome and
// there was no way to ask again: a caller that lost the response, or one asking hours
// later, had only the evidence chain, which answers a different question in a shape
// meant for an auditor.
//
// POST /v1/authority-grants/{id}/revoke is the emergency lever. authority.Store has
// carried Revoke since Phase 3 and nothing exposed it, so cutting an agent's authority
// meant an operator with a psql prompt — during exactly the incident where that is the
// worst way to work.

// IntentStore reads a submitted intent's outcome.
type IntentStore interface {
	LoadByEnvelope(ctx context.Context, tenantID, envelopeID string) (*execution.Record, error)
}

// GrantRevoker revokes an authority grant.
//
// Narrow on purpose: this handler must not be able to create or widen a grant. A
// revocation surface that could also issue authority is a surface that can undo its
// own emergency.
type GrantRevoker interface {
	Load(ctx context.Context, tenantID, grantID string) (*authority.Grant, error)
	Revoke(ctx context.Context, tenantID, grantID string, at time.Time, reason string) error
}

// IntentStatusHandler is GET /v1/intents/{id}.
func IntentStatusHandler(store IntentStore, creds *identity.Credentials,
	verifier *identity.Verifier) http.HandlerFunc {

	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := callerTenant(r, creds, verifier)
		if tenant == "" {
			writeJSON(w, status, errorBody(message))
			return
		}

		envelopeID := strings.TrimSpace(r.PathValue("id"))
		if envelopeID == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("an envelope id is required"))
			return
		}
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no execution store is configured"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()

		record, err := store.LoadByEnvelope(ctx, tenant, envelopeID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the intent could not be read"))
			return
		}
		if record == nil {
			// The same answer whether it never existed or belongs to another tenant.
			writeJSON(w, http.StatusNotFound, errorBody("no such intent"))
			return
		}

		body := map[string]any{
			"envelope_id":     record.EnvelopeID,
			"tenant_id":       record.TenantID,
			"idempotency_key": record.IdempotencyKey,
			"client_order_id": record.ClientOrderID,
			"state":           string(record.State),
			"created_at":      record.CreatedAt.UTC(),
			"updated_at":      record.UpdatedAt.UTC(),
		}

		// PENDING carries no outcome, and saying so is the point. A record that is
		// still pending is one the platform has claimed and not yet resolved, which
		// is different from one that failed and different again from one that filled.
		if record.State == execution.RecordResolved {
			body["outcome"] = map[string]any{
				"state":           string(record.Outcome.State),
				"broker_order_id": record.Outcome.BrokerOrderID,
				"filled_quantity": record.Outcome.FilledQuantity,
				"reject_reason":   record.Outcome.RejectReason,
			}
		}
		writeJSON(w, http.StatusOK, body)
	}
}

// revokeRequest names who is cutting the authority and why.
type revokeRequest struct {
	RevokedBy string `json:"revoked_by"`
	Reason    string `json:"reason"`
}

// RevokeGrantHandler is POST /v1/authority-grants/{id}/revoke.
func RevokeGrantHandler(grants GrantRevoker, evidenceStore *evidence.Store,
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
		if grants == nil {
			writeJSON(w, http.StatusServiceUnavailable, errorBody("no authority store is configured"))
			return
		}

		grantID := strings.TrimSpace(r.PathValue("id"))
		if grantID == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("a grant id is required"))
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil || len(raw) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}

		var req revokeRequest
		if len(raw) > 0 {
			decoder := json.NewDecoder(strings.NewReader(string(raw)))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, errorBody("the request is not a revocation: "+err.Error()))
				return
			}
		}

		// Both required. A revocation without an actor and a reason is an operational
		// mystery six months later, and this is the one action whose whole purpose is
		// to be explained afterwards (spec section 36).
		if strings.TrimSpace(req.RevokedBy) == "" {
			writeJSON(w, http.StatusBadRequest, errorBody("revoked_by is required"))
			return
		}
		if strings.TrimSpace(req.Reason) == "" {
			writeJSON(w, http.StatusBadRequest,
				errorBody("reason is required; a revocation nobody can explain later is one "+
					"nobody can safely undo"))
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		existing, err := grants.Load(ctx, tenant, grantID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the grant could not be read"))
			return
		}
		if existing == nil {
			writeJSON(w, http.StatusNotFound, errorBody("no such authority grant"))
			return
		}
		if existing.Status == authority.StatusRevoked {
			// 200 rather than 409. Revocation is the emergency action, and an operator
			// hitting it twice under pressure should be told it is already done rather
			// than handed an error to interpret.
			writeJSON(w, http.StatusOK, map[string]any{
				"grant_id":     grantID,
				"status":       string(authority.StatusRevoked),
				"already":      true,
				"revoked_at":   existing.RevokedAt,
				"revoked_note": existing.RevocationReason,
			})
			return
		}

		at := now().UTC()
		if err := grants.Revoke(ctx, tenant, grantID, at, strings.TrimSpace(req.Reason)); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the grant could not be revoked"))
			return
		}

		recordRevocation(ctx, evidenceStore, tenant, grantID, existing.AgentID,
			strings.TrimSpace(req.RevokedBy), strings.TrimSpace(req.Reason), at)

		writeJSON(w, http.StatusOK, map[string]any{
			"grant_id":   grantID,
			"status":     string(authority.StatusRevoked),
			"revoked_at": at,
			"revoked_by": strings.TrimSpace(req.RevokedBy),
			"reason":     strings.TrimSpace(req.Reason),
		})
	}
}

// recordRevocation writes the audit event, and never lets its failure fail the
// revocation. Cutting authority is the emergency action; an unavailable audit trail
// must not stand between an operator and it (spec section 17).
func recordRevocation(ctx context.Context, store *evidence.Store,
	tenant, grantID, agentID, by, reason string, at time.Time) {

	if store == nil {
		return
	}
	_, _ = store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       grantID + "_revoked_" + at.Format(time.RFC3339Nano),
		EventName:     evidence.AuthorityRevoked,
		TenantID:      tenant,
		AggregateID:   grantID,
		CorrelationID: grantID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload: map[string]any{
			"grant_id":   grantID,
			"agent_id":   agentID,
			"revoked_by": by,
			"reason":     reason,
		},
	})
}
