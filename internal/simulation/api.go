package simulation

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MaxRequestBytes bounds a submission body. A simulation request is a scenario name,
// a seed and a caller; anything larger is not one.
const MaxRequestBytes = 8 << 10

// API serves the two simulation endpoints of spec section 46.
//
// POST is the only mutating endpoint in the intelligence plane, and what it mutates is
// the simulation's own record. It cannot change a policy bundle, an authority grant or
// an order: this package has no code that writes any of them (INV-009).
type API struct {
	Runner *Runner
	Store  *Store
}

// Routes registers the endpoints.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/simulations", a.submit)
	mux.HandleFunc("GET /v1/simulations", a.list)
	mux.HandleFunc("GET /v1/simulations/{id}", a.get)
	mux.HandleFunc("POST /v1/simulations/{id}/cancel", a.cancel)
}

// cancelRequest names who is stopping the run.
//
// Required, for the same reason the submission records who asked: humans are audited
// too (spec section 36), and "why did this run stop" is a question that should have an
// answer six months later.
type cancelRequest struct {
	CancelledBy string `json:"cancelled_by"`
}

func (a *API) cancel(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}
	if a.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no simulation engine is configured")
		return
	}

	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "a run id is required")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil || len(body) > MaxRequestBytes {
		writeError(w, http.StatusBadRequest, "the request body could not be read")
		return
	}

	var req cancelRequest
	if len(body) > 0 {
		decoder := json.NewDecoder(strings.NewReader(string(body)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "the request is not a cancellation: "+err.Error())
			return
		}
	}
	if strings.TrimSpace(req.CancelledBy) == "" {
		writeError(w, http.StatusBadRequest, "cancelled_by is required")
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	owned, err := a.Runner.Cancel(ctx, tenant, runID, strings.TrimSpace(req.CancelledBy))
	switch {
	case errors.Is(err, ErrNoSuchRun):
		// The same answer as a run belonging to someone else. Distinguishing them is
		// the cross-tenant disclosure of spec section 45.
		writeError(w, http.StatusNotFound, "no such simulation run")
		return
	case errors.Is(err, ErrNotCancellable):
		// 409, not 404 and not 200. The run exists and the request could not be
		// honoured, and a caller told "cancelled" about a run that had already
		// completed would think a result they still have was thrown away.
		writeError(w, http.StatusConflict, "the run has already finished")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, "the run could not be cancelled")
		return
	}

	run, err := a.Store.Load(ctx, tenant, runID)
	if err != nil || run == nil {
		writeJSON(w, http.StatusOK, map[string]any{"run_id": runID, "status": StatusCancelled})
		return
	}

	// engine_stopped says whether the process was killed on the spot, which happens
	// when the cancellation reaches the replica holding it. When it does not, the
	// replica that does is watching the row and stops it within one watchdog
	// interval; engine_stops_within says how long that is.
	//
	// Both are reported rather than collapsed into "cancelled", because an operator
	// cancelling a run to free capacity for a different one needs to know whether the
	// slot is free now or shortly.
	response := map[string]any{
		"run_id":         run.RunID,
		"status":         run.Status,
		"cancelled_at":   run.CancelledAt,
		"cancelled_by":   run.CancelledBy,
		"engine_stopped": owned,
	}
	if !owned {
		response["engine_stops_within"] = a.Runner.Watchdog.String()
	}
	writeJSON(w, http.StatusOK, response)
}

// tenantOf reads the tenant from the request.
//
// A header, and not authentication, exactly as the rest of the intelligence surface
// says of itself. It is defensible here for a different reason than for the reads: a
// simulation runs against the customer's own scenarios and changes nothing about
// production, so the blast radius of a wrong tenant is a wasted CPU minute and a run
// recorded under the wrong name. It is still not authentication, and the endpoint that
// creates orders authenticates properly (ADR-025).
func tenantOf(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
}

func (a *API) requireTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant := tenantOf(r)
	if tenant == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id is required")
		return "", false
	}
	if !identifierShaped(tenant) {
		writeError(w, http.StatusBadRequest, "tenant id is not identifier-shaped")
		return "", false
	}
	if a.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no simulation store is configured")
		return "", false
	}
	return tenant, true
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}
	if a.Runner == nil {
		writeError(w, http.StatusServiceUnavailable, "no simulation engine is configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, MaxRequestBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "the request body could not be read")
		return
	}
	if len(body) > MaxRequestBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "the request is too large")
		return
	}

	var req Request
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	// Unknown fields are refused. A caller who wrote "seeed" would otherwise get a
	// reproducible run of a seed they did not choose, and every retry would return
	// the same wrong answer.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "the request is not a simulation request: "+err.Error())
		return
	}
	req.TenantID = tenant

	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()

	run, err := a.Runner.Submit(ctx, req)
	if err != nil {
		if errors.Is(err, ErrUnsafeScenario) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if strings.HasPrefix(err.Error(), "no scenario named") {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 202, not 200. The run is accepted and durable; it has not happened yet, and a
	// caller that read 200 as "here is your result" would be reading an empty record.
	w.Header().Set("Location", "/v1/simulations/"+run.RunID)
	writeJSON(w, http.StatusAccepted, run)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}
	runID := strings.TrimSpace(r.PathValue("id"))
	if runID == "" {
		writeError(w, http.StatusBadRequest, "a run id is required")
		return
	}

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	run, err := a.Store.Load(ctx, tenant, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the run could not be read")
		return
	}
	if run == nil {
		// Deliberately the same answer whether the run never existed or belongs to
		// someone else. Spec section 45 lists cross-tenant leakage as a threat, and
		// an error that distinguishes the two is itself a disclosure.
		writeError(w, http.StatusNotFound, "no such simulation run")
		return
	}
	writeJSON(w, http.StatusOK, run)
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

	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	runs, err := a.Store.List(ctx, tenant, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the runs could not be read")
		return
	}
	if runs == nil {
		runs = []Run{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenant,
		"count":     len(runs),
		"runs":      runs,
	})
}

func identifierShaped(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
