package simulation

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"agentic-assurance/internal/evidence"
)

// Events records the simulation lifecycle.
//
// simulation.started.v1, simulation.completed.v1 and simulation.failed.v1 have been in
// the section 32 catalog since Phase 0 and nothing had ever emitted one. A simulation
// runs against a customer's own configuration on their own infrastructure, and until
// now it left no trace an auditor could find.
type Events struct {
	Store *evidence.Store
	Log   *slog.Logger

	sequence atomic.Int64
}

func NewEvents(store *evidence.Store, log *slog.Logger) *Events {
	return &Events{Store: store, Log: log}
}

func (e *Events) Started(ctx context.Context, run Run) {
	e.append(ctx, run, evidence.SimulationStarted, map[string]any{
		"scenario":     run.Scenario,
		"seed":         run.Seed,
		"requested_by": run.RequestedBy,
	})
}

func (e *Events) Completed(ctx context.Context, run Run) {
	e.append(ctx, run, evidence.SimulationCompleted, map[string]any{
		"scenario":             run.Scenario,
		"seed":                 run.Seed,
		"experiment_id":        run.ExperimentID,
		"result_fingerprint":   run.ResultFingerprint,
		"scenario_source_hash": run.ScenarioSourceHash,
		"duration_ms":          run.Duration().Milliseconds(),
	})
}

func (e *Events) Cancelled(ctx context.Context, run Run) {
	e.append(ctx, run, evidence.SimulationCancelled, map[string]any{
		"scenario":     run.Scenario,
		"seed":         run.Seed,
		"requested_by": run.RequestedBy,
		"cancelled_by": run.CancelledBy,
	})
}

func (e *Events) Failed(ctx context.Context, run Run) {
	e.append(ctx, run, evidence.SimulationFailed, map[string]any{
		"scenario": run.Scenario,
		"seed":     run.Seed,
		"error":    run.Error,
	})
}

// append never lets a failure to record fail a run.
//
// Spec section 17: telemetry unavailable means carry on, not stop. A simulation that
// died because the audit trail was down would be the audit trail causing the outage.
func (e *Events) append(ctx context.Context, run Run, name evidence.EventName, payload map[string]any) {
	if e == nil || e.Store == nil {
		return
	}

	at := time.Now().UTC()
	seq := e.sequence.Add(1)

	if _, err := e.Store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       fmt.Sprintf("%s_%s_%d", run.RunID, name, seq),
		EventName:     name,
		TenantID:      run.TenantID,
		AggregateID:   run.RunID,
		CorrelationID: run.RunID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "fleet-engine",
		Sequence:      seq,
		Payload:       payload,
	}); err != nil && e.Log != nil {
		e.Log.Error("simulation evidence not recorded", "run_id", run.RunID, "err", err)
	}
}
