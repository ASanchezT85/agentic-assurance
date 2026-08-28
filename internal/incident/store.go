package incident

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/fleet"
)

// Store persists incidents.
//
// The engine has been able to detect and open one since Phase 10 and nothing kept it.
// An incident that exists only in the memory of the process that noticed it is not an
// incident anyone can be handed.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		return errors.New("a tenant is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Transaction-scoped. A session setting would outlive this call on a pooled
	// connection and be applied to whichever tenant got it next.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const incidentColumns = `
	tenant_id, incident_id, correlation_id, cohort_id, window_start, window_end,
	severity, status, anomalies, shared_dependencies, recommended, severity_rule,
	human_actions, opened_at, closed_at`

// Open records a newly detected incident, and reports whether it was new.
//
// ON CONFLICT DO NOTHING against the cohort-and-window index: the detector runs on
// every measurement, and a cohort that stays anomalous for ten windows is one
// situation. Opening ten incidents for it is how an operator learns to ignore the list.
func (s *Store) Open(ctx context.Context, inc Incident) (bool, error) {
	anomalies, err := json.Marshal(inc.Anomalies)
	if err != nil {
		return false, fmt.Errorf("encode anomalies: %w", err)
	}
	deps, err := json.Marshal(nonNil(inc.SharedDependencies))
	if err != nil {
		return false, fmt.Errorf("encode shared dependencies: %w", err)
	}
	actions, err := json.Marshal(nonNilActions(inc.HumanActions))
	if err != nil {
		return false, fmt.Errorf("encode human actions: %w", err)
	}

	var opened bool
	err = s.withTenant(ctx, inc.TenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO incidents
				(tenant_id, incident_id, correlation_id, cohort_id, window_start,
				 window_end, severity, status, anomalies, shared_dependencies,
				 recommended, severity_rule, human_actions, opened_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)
			ON CONFLICT (tenant_id, cohort_id, window_start) DO NOTHING`,
			inc.TenantID, inc.IncidentID, inc.CorrelationID, inc.Cohort.ID(),
			inc.Window.Start.UTC(), inc.Window.End.UTC(),
			string(inc.Severity), string(inc.Status), anomalies, deps,
			inc.Recommended, inc.SeverityRule, actions, inc.OpenedAt.UTC())
		if err != nil {
			return err
		}
		opened = tag.RowsAffected() == 1
		return nil
	})
	return opened, err
}

// List returns a tenant's incidents, newest first.
func (s *Store) List(ctx context.Context, tenantID string, limit int) ([]Incident, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var out []Incident
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+incidentColumns+` FROM incidents
			 WHERE tenant_id = $1 ORDER BY opened_at DESC LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			inc, err := scanIncident(rows)
			if err != nil {
				return err
			}
			out = append(out, inc)
		}
		return rows.Err()
	})
	return out, err
}

// Load returns one incident, or nil when the tenant has no such incident.
//
// Nil rather than an error distinguishing "not here" from "belongs to someone else":
// spec section 45 lists cross-tenant leakage as a threat.
func (s *Store) Load(ctx context.Context, tenantID, incidentID string) (*Incident, error) {
	var out *Incident
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+incidentColumns+` FROM incidents
			 WHERE tenant_id = $1 AND incident_id = $2`, tenantID, incidentID)
		if err != nil {
			return err
		}
		defer rows.Close()

		if !rows.Next() {
			return rows.Err()
		}
		inc, err := scanIncident(rows)
		if err != nil {
			return err
		}
		out = &inc
		return rows.Err()
	})
	return out, err
}

func scanIncident(rows pgx.Rows) (Incident, error) {
	var (
		inc         Incident
		cohortID    string
		severity    string
		status      string
		anomalies   []byte
		deps        []byte
		actions     []byte
		windowStart time.Time
		windowEnd   time.Time
		closedAt    *time.Time
	)

	if err := rows.Scan(&inc.TenantID, &inc.IncidentID, &inc.CorrelationID, &cohortID,
		&windowStart, &windowEnd, &severity, &status, &anomalies, &deps,
		&inc.Recommended, &inc.SeverityRule, &actions, &inc.OpenedAt, &closedAt); err != nil {
		return Incident{}, err
	}

	inc.Severity = Severity(severity)
	inc.Status = Status(status)
	inc.Window = fleet.Window{Start: windowStart.UTC(), End: windowEnd.UTC()}
	inc.OpenedAt = inc.OpenedAt.UTC()
	if closedAt != nil {
		utc := closedAt.UTC()
		inc.ClosedAt = &utc
	}

	// The cohort is stored as its id. Reconstructing predicates from it would be
	// guessing; the id is what identifies the group and what the fleet API keys on.
	inc.Cohort = fleet.Cohort{TenantID: inc.TenantID}

	if err := json.Unmarshal(anomalies, &inc.Anomalies); err != nil {
		return Incident{}, fmt.Errorf("incident %s has unreadable anomalies: %w", inc.IncidentID, err)
	}
	if err := json.Unmarshal(deps, &inc.SharedDependencies); err != nil {
		return Incident{}, fmt.Errorf("incident %s has unreadable dependencies: %w", inc.IncidentID, err)
	}
	if err := json.Unmarshal(actions, &inc.HumanActions); err != nil {
		return Incident{}, fmt.Errorf("incident %s has unreadable actions: %w", inc.IncidentID, err)
	}
	return inc, nil
}

// StoredCohortID is the cohort an incident was opened for, as stored.
//
// Exposed because the Incident type carries a fleet.Cohort that cannot be rebuilt from
// an id, and a reader comparing an incident to a fleet measurement needs the id itself
// rather than a reconstruction that might not match.
func (s *Store) StoredCohortID(ctx context.Context, tenantID, incidentID string) (string, error) {
	var id string
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT cohort_id FROM incidents WHERE tenant_id = $1 AND incident_id = $2`,
			tenantID, incidentID).Scan(&id)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNilActions(a []HumanAction) []HumanAction {
	if a == nil {
		return []HumanAction{}
	}
	return a
}
