package simulation

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"agentic-assurance/internal/identity"
)

// MaxRequestBytes bounds a submission body. A simulation request is a scenario name,
// a seed and a caller; anything larger is not one.
const MaxRequestBytes = 8 << 10

// API serves the simulation endpoints of spec section 46.
//
// POST is the only mutating endpoint in the intelligence plane, and what it mutates is
// the simulation's own record. It cannot change a policy bundle, an authority grant or
// an order: this package has no code that writes any of them (INV-009).
type API struct {
	Runner *Runner
	Store  *Store

	// Credentials authenticate callers and say which tenant each speaks for.
	//
	// The tenant used to come from a header, with a comment saying that a simulation
	// changes nothing about production so a wrong tenant costs a CPU minute. That was
	// wrong in a way the comment hid: a run is stored under a tenant, retrievable by
	// that tenant, and cancellable by them. A header let anyone read another
	// customer's simulation results and cancel their runs.
	//
	// Nil means the endpoints are not served. There is no unauthenticated mode.
	Credentials *identity.Credentials

	// Identity verifies workload certificates. Nil means a bare verifier, which
	// accepts no SVID and therefore supports no A2: an operator who deployed SPIRE
	// would find mutual TLS working on the submission endpoint and silently not here.
	Identity *identity.Verifier
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
	tenant, who, ok := a.requireCaller(w, r)
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

	owned, err := a.Runner.Cancel(ctx, tenant, runID, strings.TrimSpace(req.CancelledBy), who)
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

// presentedFrom adapts an HTTP request to what identity understands.
//
// The adaptation lives here rather than in internal/identity because that package is
// on the enforcement path and INV-005 forbids it from importing net/http.
// verifier is the configured one, or a bare one that accepts no certificate.
//
// A bare verifier is a safe default and a silent one: it fails every SVID closed. The
// field exists so the choice is made at construction rather than by whichever handler
// happened to instantiate one.
func (a *API) verifier() *identity.Verifier {
	if a.Identity != nil {
		return a.Identity
	}
	return &identity.Verifier{}
}

func presentedFrom(r *http.Request, creds *identity.Credentials) identity.Presented {
	var certs []*x509.Certificate
	if r.TLS != nil {
		certs = r.TLS.PeerCertificates
	}
	return identity.FromTransport(r.Header.Get("Authorization"), certs, creds)
}

// requireTenant establishes who is calling and which tenant they speak for.
//
// The tenant comes from the credential, never from the request. A header would let a
// caller name any tenant, and every simulation lookup that follows is scoped by it:
// they would read another customer's results and cancel their runs (INV-007, ADR-025).
func (a *API) requireTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant, _, ok := a.requireCaller(w, r)
	return tenant, ok
}

// requireCaller also returns the authenticated identity, for the audit trail.
func (a *API) requireCaller(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	if a.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no simulation store is configured")
		return "", "", false
	}

	attested := a.verifier().Resolve(presentedFrom(r, a.Credentials))
	if err := identity.RequireExecutable(attested); err != nil {
		writeError(w, http.StatusUnauthorized, "the caller is not authenticated")
		return "", "", false
	}
	if attested.TenantID == "" {
		writeError(w, http.StatusUnauthorized,
			"the caller is authenticated but no tenant is established for it")
		return "", "", false
	}

	// A header, if sent, must agree. Silently ignoring one that disagrees would let a
	// caller believe they were acting on a tenant they were not, and a simulation
	// they think ran for someone else is a wrong answer they will act on.
	if claimed := strings.TrimSpace(r.Header.Get("X-Tenant-Id")); claimed != "" &&
		claimed != attested.TenantID {
		writeError(w, http.StatusForbidden,
			"the request names a tenant this caller is not authenticated for")
		return "", "", false
	}

	return attested.TenantID, attested.APIIdentity, true
}

func (a *API) submit(w http.ResponseWriter, r *http.Request) {
	tenant, who, ok := a.requireCaller(w, r)
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
	req.SubmittedBy = who

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

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
