package incident

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/identity"
)

// API serves the two incident endpoints of spec section 46.
//
// Read-only, and structurally so: there is no handler here that changes an incident,
// because acknowledging or closing one is a human action and the surface for recording
// those is not built. A read-only API cannot be talked into one.
type API struct {
	Store *Store

	// Evidence lets GET /v1/incidents/{id} return the reconstructed timeline. Spec
	// section 49 requires it to be reproducible, and it has been — in Go, from
	// evidence, reachable by nobody.
	Evidence *evidence.Store

	Credentials *identity.Credentials
	Identity    *identity.Verifier
}

// Routes registers the endpoints.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/incidents", a.list)
	mux.HandleFunc("GET /v1/incidents/{id}", a.get)
}

func (a *API) verifier() *identity.Verifier {
	if a.Identity != nil {
		return a.Identity
	}
	return &identity.Verifier{}
}

// presentedFrom adapts an HTTP request to what identity understands.
//
// The adaptation lives here rather than in internal/identity because that package is
// on the enforcement path and INV-005 forbids it from importing net/http.
func presentedFrom(r *http.Request, creds *identity.Credentials) identity.Presented {
	var certs []*x509.Certificate
	if r.TLS != nil {
		certs = r.TLS.PeerCertificates
	}
	return identity.FromTransport(r.Header.Get("Authorization"), certs, creds)
}

// requireTenant establishes which tenant a caller speaks for.
//
// From the credential, never from the request. An incident names which agents were
// involved and what they had in common, which is among the most sensitive things this
// platform holds (INV-007, ADR-025).
func (a *API) requireTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	if a.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no incident store is configured")
		return "", false
	}

	attested := a.verifier().Resolve(presentedFrom(r, a.Credentials))
	if err := identity.RequireExecutable(attested); err != nil {
		writeError(w, http.StatusUnauthorized, "the caller is not authenticated")
		return "", false
	}
	if attested.TenantID == "" {
		writeError(w, http.StatusUnauthorized,
			"the caller is authenticated but no tenant is established for it")
		return "", false
	}
	if claimed := strings.TrimSpace(r.Header.Get("X-Tenant-Id")); claimed != "" &&
		claimed != attested.TenantID {
		writeError(w, http.StatusForbidden,
			"the request names a tenant this caller is not authenticated for")
		return "", false
	}
	return attested.TenantID, true
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}

	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	incidents, err := a.Store.List(ctx, tenant, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the incidents could not be read")
		return
	}

	rows := make([]map[string]any, 0, len(incidents))
	for _, inc := range incidents {
		rows = append(rows, summarise(inc))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenant,
		"count":     len(rows),
		"incidents": rows,
	})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}

	incidentID := strings.TrimSpace(r.PathValue("id"))
	if incidentID == "" {
		writeError(w, http.StatusBadRequest, "an incident id is required")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	inc, err := a.Store.Load(ctx, tenant, incidentID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the incident could not be read")
		return
	}
	if inc == nil {
		// The same answer as one belonging to another tenant (spec section 45).
		writeError(w, http.StatusNotFound, "no such incident")
		return
	}

	body := summarise(*inc)
	body["anomalies"] = inc.Anomalies
	body["human_actions"] = inc.HumanActions

	// The timeline, reconstructed from evidence rather than from the incident.
	// incident.Reconstruct deliberately refuses to take an incident: a function that
	// did would be replaying memory rather than reading the record.
	if a.Evidence != nil {
		chain, err := a.Evidence.Chain(ctx, tenant, inc.CorrelationID)
		if err != nil {
			// The incident is still worth returning. Saying the timeline is missing is
			// better than failing the whole read during the incident it describes.
			body["timeline_error"] = "the evidence chain could not be read"
		} else if timeline, err := Reconstruct(chain); err != nil {
			body["timeline_error"] = "the evidence chain could not be reconstructed"
		} else {
			body["timeline"] = timeline.Entries
			body["recommended_in_timeline"] = timeline.Recommended
			body["applied_in_timeline"] = timeline.Applied

			// Which of section 49's seven questions this chain can actually answer.
			// A timeline that cannot answer them is incomplete evidence, and that is
			// itself the finding rather than something to hide behind a rendering.
			body["answered_questions"] = timeline.AnsweredQuestions()
		}
	}

	writeJSON(w, http.StatusOK, body)
}

// summarise is what a listing shows and what a detail view starts from.
func summarise(inc Incident) map[string]any {
	rules := make([]string, 0, len(inc.Anomalies))
	for _, a := range inc.Anomalies {
		rules = append(rules, a.Rule)
	}

	return map[string]any{
		"incident_id":         inc.IncidentID,
		"tenant_id":           inc.TenantID,
		"correlation_id":      inc.CorrelationID,
		"severity":            string(inc.Severity),
		"severity_rule":       inc.SeverityRule,
		"status":              string(inc.Status),
		"anomaly_rules":       rules,
		"shared_dependencies": inc.SharedDependencies,

		// Recommended and human actions are separate fields and always will be. A
		// recommendation is never an action (INV-009), and a display that merged them
		// would let a reader believe the platform had done something.
		"recommended":  inc.Recommended,
		"window_start": inc.Window.Start.UTC(),
		"window_end":   inc.Window.End.UTC(),
		"opened_at":    inc.OpenedAt.UTC(),
		"closed_at":    inc.ClosedAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
