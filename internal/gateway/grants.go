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

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// POST /v1/authority-grants.
//
// Grants were created with SQL by hand. Section 46 lists this endpoint and the store
// has carried Save since Phase 3; nothing served it, so issuing authority meant a psql
// prompt and a hope that the columns were right.
//
// The privilege is separated from submitting an intent, and that separation is the
// point rather than a detail. P-002 says the customer retains final authority. A
// credential that could both submit orders and issue the authority to submit them
// would let an agent raise its own ceiling: INV-002 would still be enforced, against a
// limit the party under it can move.

// GrantIssuer creates and reads authority grants.
type GrantIssuer interface {
	Save(ctx context.Context, g *authority.Grant) error
	Load(ctx context.Context, tenantID, grantID string) (*authority.Grant, error)
}

// grantRequest is what a customer sends to issue authority.
//
// The tenant is deliberately absent: it comes from the credential. A grant is the
// object that decides what an agent may do, and letting the request name whose
// authority it is would be the cross-tenant hole in its most direct form.
type grantRequest struct {
	GrantID     string `json:"grant_id"`
	PrincipalID string `json:"principal_id"`
	AccountID   string `json:"account_id"`
	AgentID     string `json:"agent_id"`
	IssuedBy    string `json:"issued_by"`

	ValidFrom  time.Time `json:"valid_from"`
	ValidUntil time.Time `json:"valid_until"`

	AllowedOperations   []string `json:"allowed_operations"`
	AllowedAssetClasses []string `json:"allowed_asset_classes"`
	AllowedInstruments  []string `json:"allowed_instruments"`
	DeniedInstruments   []string `json:"denied_instruments"`

	// Exact, and refused rather than rounded when a caller sends more precision than
	// the platform keeps: a ceiling silently rounded is a ceiling nobody agreed to.
	PerOrderNotional  money.Amount `json:"per_order_notional"`
	Rolling1hNotional money.Amount `json:"rolling_1h_notional"`
	DailyNotional     money.Amount `json:"daily_notional"`
	MaxOpenOrders     int          `json:"max_open_orders"`

	MarginAllowed   bool `json:"margin_allowed"`
	ShortingAllowed bool `json:"shorting_allowed"`
}

// validate refuses a grant that would not constrain anything.
//
// A grant with no limits is not a permissive grant, it is an absent one: authority
// evaluation would allow every size, and the ceiling would exist only in the mind of
// whoever wrote the request.
func (g grantRequest) validate() []string {
	var problems []string

	require := func(value, name string) {
		if strings.TrimSpace(value) == "" {
			problems = append(problems, name+" is required")
		}
	}
	require(g.GrantID, "grant_id")
	require(g.PrincipalID, "principal_id")
	require(g.AccountID, "account_id")
	require(g.AgentID, "agent_id")
	require(g.IssuedBy, "issued_by")

	if g.ValidUntil.IsZero() {
		problems = append(problems, "valid_until is required; a grant with no expiry "+
			"is authority nobody has to renew, and section 14.3 makes expiry a "+
			"mandatory denial")
	} else if !g.ValidFrom.IsZero() && !g.ValidUntil.After(g.ValidFrom) {
		problems = append(problems, "valid_until must be after valid_from")
	}

	if len(g.AllowedOperations) == 0 {
		problems = append(problems, "allowed_operations is required; a grant that "+
			"permits no operation permits nothing, and one that omits the field "+
			"would be read as permitting everything")
	}
	if len(g.AllowedAssetClasses) == 0 {
		problems = append(problems, "allowed_asset_classes is required")
	}
	if g.PerOrderNotional <= 0 {
		problems = append(problems, "per_order_notional must be positive; a grant "+
			"with no per-order ceiling is an absent ceiling rather than a generous one")
	}
	return problems
}

func (g grantRequest) toGrant(tenantID string, at time.Time) *authority.Grant {
	sides := make([]intent.Side, 0, len(g.AllowedOperations))
	for _, s := range g.AllowedOperations {
		sides = append(sides, intent.Side(strings.ToUpper(strings.TrimSpace(s))))
	}
	classes := make([]intent.AssetClass, 0, len(g.AllowedAssetClasses))
	for _, c := range g.AllowedAssetClasses {
		classes = append(classes, intent.AssetClass(strings.ToUpper(strings.TrimSpace(c))))
	}

	validFrom := g.ValidFrom
	if validFrom.IsZero() {
		validFrom = at
	}

	return &authority.Grant{
		GrantID:             strings.TrimSpace(g.GrantID),
		TenantID:            tenantID,
		PrincipalID:         strings.TrimSpace(g.PrincipalID),
		AccountID:           strings.TrimSpace(g.AccountID),
		AgentID:             strings.TrimSpace(g.AgentID),
		IssuedAt:            at,
		ValidFrom:           validFrom.UTC(),
		ValidUntil:          g.ValidUntil.UTC(),
		AllowedOperations:   sides,
		AllowedAssetClasses: classes,
		AllowedInstruments:  g.AllowedInstruments,
		DeniedInstruments:   g.DeniedInstruments,
		Limits: authority.Limits{
			PerOrderNotional:  g.PerOrderNotional,
			Rolling1hNotional: g.Rolling1hNotional,
			DailyNotional:     g.DailyNotional,
			MaxOpenOrders:     g.MaxOpenOrders,
		},
		Capabilities: authority.Capabilities{
			MarginAllowed:   g.MarginAllowed,
			ShortingAllowed: g.ShortingAllowed,
		},
		Status: authority.StatusActive,
	}
}

// IssueGrantHandler is POST /v1/authority-grants.
func IssueGrantHandler(grants GrantIssuer, evidenceStore *evidence.Store,
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

		// The privilege, checked separately from authentication. Being a valid caller
		// is not being allowed to widen what callers may do.
		if verifier == nil {
			verifier = &identity.Verifier{}
		}
		if !verifier.Resolve(presentedFrom(r, creds)).MayIssueAuthority {
			writeJSON(w, http.StatusForbidden, errorBody(
				"this credential may not issue authority. Issuing is separated from "+
					"submitting so an agent cannot raise its own ceiling (P-002); name "+
					"the issuer in GATEWAY_GRANT_ISSUERS"))
			return
		}

		raw, err := io.ReadAll(io.LimitReader(r.Body, MaxEnvelopeBytes+1))
		if err != nil || len(raw) > MaxEnvelopeBytes {
			writeJSON(w, http.StatusBadRequest, errorBody("the request body could not be read"))
			return
		}

		var req grantRequest
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		// Unknown fields are refused. A misspelled limit would otherwise be silently
		// dropped and the grant issued without the ceiling its author wrote down.
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest,
				errorBody("the request is not an authority grant: "+err.Error()))
			return
		}

		if problems := req.validate(); len(problems) > 0 {
			writeJSON(w, http.StatusBadRequest, map[string]any{
				"error":   "the grant would not constrain anything",
				"details": problems,
			})
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()

		// Refused rather than overwritten. Save upserts, and a PUT-shaped POST would
		// let an issuer widen an existing grant by reissuing it under the same id,
		// which is the same escalation by a slower route.
		existing, err := grants.Load(ctx, tenant, strings.TrimSpace(req.GrantID))
		if err != nil && !errors.Is(err, authority.ErrGrantNotFound) {
			writeJSON(w, http.StatusInternalServerError, errorBody("the grant could not be read"))
			return
		}
		if existing != nil {
			writeJSON(w, http.StatusConflict, errorBody(
				"a grant with this id already exists. Authority is not edited in place: "+
					"revoke it and issue a new one, so the change is two auditable acts "+
					"rather than a silent widening"))
			return
		}

		at := now().UTC()
		grant := req.toGrant(tenant, at)
		if err := grants.Save(ctx, grant); err != nil {
			writeJSON(w, http.StatusInternalServerError, errorBody("the grant could not be issued"))
			return
		}

		recordIssue(ctx, evidenceStore, grant, strings.TrimSpace(req.IssuedBy), at)

		writeJSON(w, http.StatusCreated, map[string]any{
			"grant_id":    grant.GrantID,
			"tenant_id":   grant.TenantID,
			"agent_id":    grant.AgentID,
			"issued_at":   grant.IssuedAt,
			"issued_by":   strings.TrimSpace(req.IssuedBy),
			"valid_until": grant.ValidUntil,
			"status":      string(grant.Status),
		})
	}
}

// recordIssue writes the audit event. Issuing authority is a human action against a
// customer's own configuration (spec section 36), and one that left no record would be
// the hardest thing to reconstruct after an incident.
func recordIssue(ctx context.Context, store *evidence.Store, g *authority.Grant,
	issuedBy string, at time.Time) {

	if store == nil {
		return
	}
	_, _ = store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_issued_%d", g.GrantID, at.UnixNano()),
		EventName:     evidence.AuthorityIssued,
		TenantID:      g.TenantID,
		AggregateID:   g.GrantID,
		CorrelationID: g.GrantID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      nextSequence(),
		Payload: map[string]any{
			"grant_id":            g.GrantID,
			"agent_id":            g.AgentID,
			"principal_id":        g.PrincipalID,
			"account_id":          g.AccountID,
			"issued_by":           issuedBy,
			"valid_until":         g.ValidUntil,
			"per_order_notional":  g.Limits.PerOrderNotional,
			"rolling_1h_notional": g.Limits.Rolling1hNotional,
			"daily_notional":      g.Limits.DailyNotional,
			"max_open_orders":     g.Limits.MaxOpenOrders,
		},
	})
}
