package fleet

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// The intelligence API (spec section 46).
//
// Read-only, and that is structural: there is no handler here that writes anything,
// and the fleet engine is forbidden from submitting orders or modifying customer
// policy (spec section 29). A console reading these cannot cause anything to happen.

// Reader is the slice of ClickHouse the API needs. It is an interface so the handlers
// are testable without a database, and it has one method, because a read-only API
// needs exactly one.
type Reader interface {
	Query(ctx context.Context, sql string) (string, error)
}

// API serves the intelligence surface.
type API struct {
	Store Reader
}

// tenantOf reads the tenant from the request.
//
// A header, and not authentication. Spec section 46 requires an authenticated tenant
// on every endpoint; that arrives with the API surface that carries authentication.
// Saying so here is better than a handler that implies a check it does not perform.
func tenantOf(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
}

// Routes registers the read-only intelligence endpoints.
func (a *API) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/fleet/state", a.fleetState)
	mux.HandleFunc("GET /v1/cohorts", a.cohorts)
	mux.HandleFunc("GET /v1/dependencies", a.dependencies)
}

// escapeLiteral makes a tenant id safe to interpolate.
//
// ClickHouse's HTTP interface has no parameter binding in the form this client uses,
// so the tenant is escaped rather than bound. It is the only user-controlled value
// that reaches a query here, and it is checked against a conservative shape rather
// than merely escaped: an identifier that is not identifier-shaped is a request that
// should be refused, not sanitised.
func safeIdentifier(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

func (a *API) fleetState(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}

	// The most recent measurement per cohort. Stored measurements, not recomputed
	// ones: a view that re-derived them with today's code would show what the
	// current logic concludes rather than what was measured.
	rows := a.query(w, r, fmt.Sprintf(`
		SELECT cohort_id, cohort_predicate, window_start, window_end,
		       intent_count, agent_count, gross_notional, net_notional,
		       directional_imbalance, observed_coverage, verified_coverage,
		       declared_coverage, unknown_coverage
		  FROM assurance.fleet_measurements
		 WHERE tenant_id = '%s'
		 ORDER BY window_start DESC
		 LIMIT 200
		 FORMAT JSONEachRow`, tenant))
	if rows == nil {
		return
	}
	writeRows(w, tenant, rows)
}

func (a *API) cohorts(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}
	rows := a.query(w, r, fmt.Sprintf(`
		SELECT cohort_id, cohort_predicate,
		       count() AS windows,
		       max(window_end) AS last_seen,
		       max(intent_count) AS peak_intents,
		       max(agent_count) AS peak_agents
		  FROM assurance.fleet_measurements
		 WHERE tenant_id = '%s'
		 GROUP BY cohort_id, cohort_predicate
		 ORDER BY last_seen DESC
		 LIMIT 200
		 FORMAT JSONEachRow`, tenant))
	if rows == nil {
		return
	}
	writeRows(w, tenant, rows)
}

func (a *API) dependencies(w http.ResponseWriter, r *http.Request) {
	tenant, ok := a.requireTenant(w, r)
	if !ok {
		return
	}

	// Verification levels are counted separately rather than collapsed into a
	// single "verified" fraction, because a reader needs to see the unknowns
	// (ADR-007, spec section 25).
	rows := a.query(w, r, fmt.Sprintf(`
		SELECT dependency_type, dependency_id,
		       count() AS observations,
		       countIf(verification = 'VERIFIED') AS verified,
		       countIf(verification = 'DECLARED') AS declared,
		       countIf(verification = 'UNKNOWN') AS unknown,
		       uniqExact(agent_id) AS agents,
		       max(observed_at) AS last_seen
		  FROM assurance.dependency_observations
		 WHERE tenant_id = '%s'
		 GROUP BY dependency_type, dependency_id
		 ORDER BY observations DESC
		 LIMIT 200
		 FORMAT JSONEachRow`, tenant))
	if rows == nil {
		return
	}
	writeRows(w, tenant, rows)
}

func (a *API) requireTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	tenant := tenantOf(r)
	if tenant == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-Id is required")
		return "", false
	}
	if !safeIdentifier(tenant) {
		writeError(w, http.StatusBadRequest, "tenant id is not identifier-shaped")
		return "", false
	}
	if a.Store == nil {
		writeError(w, http.StatusServiceUnavailable, "no analytical store is configured")
		return "", false
	}
	return tenant, true
}

func (a *API) query(w http.ResponseWriter, r *http.Request, sql string) []map[string]any {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	raw, err := a.Store.Query(ctx, sql)
	if err != nil {
		// Losing ClickHouse degrades analytics and nothing else (ADR-021). The
		// console shows the outage; no enforcement decision waits on this.
		writeError(w, http.StatusServiceUnavailable, "the analytical store is unavailable")
		return nil
	}

	rows := []map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			writeError(w, http.StatusInternalServerError, "the analytical store returned unreadable rows")
			return nil
		}
		rows = append(rows, row)
	}
	return rows
}

func writeRows(w http.ResponseWriter, tenant string, rows []map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"tenant_id": tenant,
		"count":     len(rows),
		"rows":      rows,
	})
}

func writeError(w http.ResponseWriter, status int, message string) {
	// Deliberately generic. Spec section 45 lists cross-tenant leakage as a threat,
	// and an error that distinguishes "no such cohort here" from "exists elsewhere"
	// is itself a disclosure.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
