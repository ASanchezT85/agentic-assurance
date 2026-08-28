package simulation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store persists simulation runs.
type Store struct{ pool *pgxpool.Pool }

func NewStore(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

func (s *Store) withTenant(ctx context.Context, tenantID string, fn func(pgx.Tx) error) error {
	if tenantID == "" {
		// Without a tenant the RLS setting is empty and every policy fails closed,
		// which is correct and unhelpful. Saying so is better than a permission error.
		return errors.New("a tenant is required")
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Transaction-scoped, never session-scoped: the pool reuses connections, and a
	// session setting would outlive this call and be applied to the next tenant that
	// happened to get the same connection.
	if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", tenantID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

const runColumns = `
	tenant_id, run_id, scenario, seed, requested_by, status,
	requested_at, started_at, completed_at,
	experiment_id, result_fingerprint, scenario_source_hash, record, error,
	cancelled_at, cancelled_by`

// Cancel marks a run cancelled, if it has not already finished.
//
// It reports whether it changed anything. A caller needs to tell "stopped" from
// "already over", because those are different answers to the operator who asked, and
// reporting a completed run as cancelled would erase a result they still have.
//
// The guard is in the WHERE clause rather than in a read followed by a write: the
// engine finishing and the operator cancelling race by nature, and one statement is
// how the database settles it rather than whichever goroutine got there first.
func (s *Store) Cancel(ctx context.Context, tenantID, runID string, at time.Time, by string) (bool, error) {
	if by == "" {
		return false, errors.New("a cancellation without an actor cannot be explained later")
	}

	var changed bool
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE simulation_runs
			SET status = 'CANCELLED', cancelled_at = $3, cancelled_by = $4,
			    completed_at = COALESCE(completed_at, $3)
			WHERE tenant_id = $1 AND run_id = $2 AND status IN ('QUEUED', 'RUNNING')`,
			tenantID, runID, at.UTC(), by)
		if err != nil {
			return err
		}
		changed = tag.RowsAffected() == 1
		return nil
	})
	return changed, err
}

// Status reads only the status column.
//
// Separate from Load because the watchdog calls it every couple of seconds for the
// whole length of a run, and Load pulls the record jsonb with it. A completed
// scenario's record is tens of kilobytes; polling that would move megabytes to answer
// a question about one word.
func (s *Store) Status(ctx context.Context, tenantID, runID string) (Status, error) {
	var status string
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT status FROM simulation_runs WHERE tenant_id = $1 AND run_id = $2`,
			tenantID, runID).Scan(&status)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNoSuchRun
	}
	return Status(status), err
}

func (s *Store) Create(ctx context.Context, run Run) error {
	return s.withTenant(ctx, run.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO simulation_runs
				(tenant_id, run_id, scenario, seed, requested_by, status, requested_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			run.TenantID, run.RunID, run.Scenario, run.Seed, run.RequestedBy,
			string(run.Status), run.RequestedAt.UTC())
		return err
	})
}

func (s *Store) Start(ctx context.Context, tenantID, runID string, at time.Time) error {
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		// Only from QUEUED. A run that already reached a terminal state must not be
		// dragged back to RUNNING by a retry or a second worker.
		_, err := tx.Exec(ctx, `
			UPDATE simulation_runs SET status = 'RUNNING', started_at = $3
			WHERE tenant_id = $1 AND run_id = $2 AND status = 'QUEUED'`,
			tenantID, runID, at.UTC())
		return err
	})
}

func (s *Store) Complete(ctx context.Context, run Run, at time.Time) error {
	encoded, err := json.Marshal(run.Record)
	if err != nil {
		return fmt.Errorf("the engine's record could not be stored: %w", err)
	}

	return s.withTenant(ctx, run.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE simulation_runs
			SET status = 'COMPLETED', completed_at = $3, experiment_id = $4,
			    result_fingerprint = $5, scenario_source_hash = $6, record = $7
			WHERE tenant_id = $1 AND run_id = $2
			  AND status NOT IN ('COMPLETED', 'CANCELLED')`,
			run.TenantID, run.RunID, at.UTC(), nullIfEmpty(run.ExperimentID),
			nullIfEmpty(run.ResultFingerprint), nullIfEmpty(run.ScenarioSourceHash),
			encoded)
		return err
	})
}

func (s *Store) Fail(ctx context.Context, tenantID, runID string, at time.Time, reason string) error {
	if reason == "" {
		reason = "the engine failed without saying why"
	}
	return s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE simulation_runs SET status = 'FAILED', completed_at = $3, error = $4
			WHERE tenant_id = $1 AND run_id = $2
			  AND status NOT IN ('COMPLETED', 'CANCELLED')`,
			tenantID, runID, at.UTC(), reason)
		return err
	})
}

// Load returns one run, or nil when the tenant has no such run.
//
// Nil rather than an error that distinguishes "no such run here" from "exists in
// another tenant": spec section 45 lists cross-tenant leakage as a threat, and an
// error that tells them apart is itself the disclosure.
func (s *Store) Load(ctx context.Context, tenantID, runID string) (*Run, error) {
	var run *Run
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+runColumns+` FROM simulation_runs
			 WHERE tenant_id = $1 AND run_id = $2`, tenantID, runID)
		if err != nil {
			return err
		}
		defer rows.Close()

		if !rows.Next() {
			return rows.Err()
		}
		loaded, err := scanRun(rows)
		if err != nil {
			return err
		}
		run = &loaded
		return rows.Err()
	})
	return run, err
}

// List returns a tenant's runs, newest first.
func (s *Store) List(ctx context.Context, tenantID string, limit int) ([]Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}

	var runs []Run
	err := s.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx,
			`SELECT `+runColumns+` FROM simulation_runs
			 WHERE tenant_id = $1 ORDER BY requested_at DESC LIMIT $2`, tenantID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			run, err := scanRun(rows)
			if err != nil {
				return err
			}
			// The record is omitted from a listing. It is the largest thing here by
			// far, and a list of fifty runs would be megabytes of detail nobody asked
			// for; GET /v1/simulations/{id} returns it.
			run.Record = nil
			runs = append(runs, run)
		}
		return rows.Err()
	})
	return runs, err
}

func scanRun(rows pgx.Rows) (Run, error) {
	var (
		run          Run
		status       string
		startedAt    *time.Time
		completedAt  *time.Time
		experimentID *string
		fingerprint  *string
		sourceHash   *string
		record       []byte
		failure      *string
		cancelledAt  *time.Time
		cancelledBy  *string
	)

	if err := rows.Scan(&run.TenantID, &run.RunID, &run.Scenario, &run.Seed,
		&run.RequestedBy, &status, &run.RequestedAt, &startedAt, &completedAt,
		&experimentID, &fingerprint, &sourceHash, &record, &failure,
		&cancelledAt, &cancelledBy); err != nil {
		return Run{}, err
	}

	// Normalised to UTC on the way out. Everything is written with at.UTC(), and
	// pgx returns a timestamptz in the connection's timezone: without this a run
	// carries requested_at in Z (built in Go) beside cancelled_at at -04:00 (read
	// back from the database). The same instant, and a reader comparing the two as
	// text is misled.
	run.Status = Status(status)
	run.RequestedAt = run.RequestedAt.UTC()
	run.StartedAt = utcOrNil(startedAt)
	run.CompletedAt = utcOrNil(completedAt)
	run.ExperimentID = deref(experimentID)
	run.ResultFingerprint = deref(fingerprint)
	run.ScenarioSourceHash = deref(sourceHash)
	run.Error = deref(failure)
	run.CancelledAt = utcOrNil(cancelledAt)
	run.CancelledBy = deref(cancelledBy)

	if len(record) > 0 {
		if err := json.Unmarshal(record, &run.Record); err != nil {
			return Run{}, fmt.Errorf("run %s has an unreadable record: %w", run.RunID, err)
		}
	}
	return run, nil
}

func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := t.UTC()
	return &utc
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
