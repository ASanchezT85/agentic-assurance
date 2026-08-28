package simulation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// Runner starts the Digital Twin and records what it produced.
//
// The engine is executed as a process with an argument vector, never through a shell.
// That is not a precaution to be relaxed later: the scenario name and the seed come
// from an HTTP request, and a shell between here and the engine would turn a string
// field into remote execution on the customer's own infrastructure.
type Runner struct {
	// Python is the interpreter. Repo (its working directory) must contain the
	// simulator package, because the engine is invoked as `-m simulator.engine`.
	Python string
	Repo   string

	// ScenarioDir holds the scenario files a caller may name. A name is resolved to
	// exactly one file inside this directory and the result is checked to still be
	// inside it, so a name that somehow escaped validation still cannot reach out.
	ScenarioDir string

	Store *Store

	// Evidence records what happened. Optional: losing it costs the audit trail, not
	// the run.
	Evidence EvidenceSink

	// Timeout bounds one run. A scenario with an absurd step count would otherwise
	// hold a slot forever, and the slot is the scarce thing.
	Timeout time.Duration

	// Watchdog is how often a running engine checks whether its own run has been
	// cancelled somewhere else.
	//
	// Cancellation is in-process: the replica holding the engine kills it. With more
	// than one fleet engine a cancellation can land on a replica that does not hold
	// the run, and without this the row would say CANCELLED while an engine nobody
	// could reach kept a slot until its timeout. The row is the authority; this is
	// how the owner finds out.
	//
	// Polling rather than LISTEN/NOTIFY or a message on the bus. The truth is already
	// in the row, so asking the row needs no second system to be up, and a kill that
	// is late by one interval on a run measured in minutes is not worth a dependency
	// on the notification path being healthy.
	Watchdog time.Duration

	// Concurrency caps how many engines run at once. A simulation is CPU-bound and
	// this process also serves the intelligence API; without a cap a burst of
	// requests would starve the reads that operators depend on during an incident,
	// which is exactly when they are looking.
	Concurrency int

	Log *slog.Logger
	Now func() time.Time

	slots chan struct{}

	// inFlight holds a cancel function per run this process owns. Keyed by tenant
	// and run id, because a run id is only unique within a tenant.
	mu       sync.Mutex
	inFlight map[string]context.CancelFunc
}

// EvidenceSink records simulation lifecycle events.
type EvidenceSink interface {
	Started(ctx context.Context, run Run)
	Completed(ctx context.Context, run Run)
	Failed(ctx context.Context, run Run)
	Cancelled(ctx context.Context, run Run)
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) log() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Prepare sets defaults and checks that the runner can actually run something.
//
// Called at construction rather than on the first request, so a misconfigured engine
// is a startup warning instead of a 500 the first time a customer asks.
func (r *Runner) Prepare() error {
	if r.Timeout <= 0 {
		r.Timeout = 5 * time.Minute
	}
	if r.Concurrency <= 0 {
		r.Concurrency = 2
	}
	if r.Watchdog <= 0 {
		r.Watchdog = 2 * time.Second
	}
	r.slots = make(chan struct{}, r.Concurrency)
	r.inFlight = map[string]context.CancelFunc{}

	if r.Python == "" {
		return errors.New("no python interpreter configured")
	}
	if r.Store == nil {
		return errors.New("no run store configured; a simulation nobody can retrieve is a log line")
	}
	if info, err := os.Stat(r.ScenarioDir); err != nil || !info.IsDir() {
		return fmt.Errorf("scenario directory %q is not a directory", r.ScenarioDir)
	}
	return nil
}

// ResolveScenario turns a validated name into the argument the engine takes.
//
// "demo" is the engine's built-in and is passed through. Every other name becomes one
// file inside ScenarioDir, and the resolved path is checked to still be inside it.
// The name has already been validated to contain no separators; this is the second
// check, because the first one being right is not something to assume about the code
// path that reaches a filesystem.
func (r *Runner) ResolveScenario(name string) (string, error) {
	name, err := ValidateScenarioName(name)
	if err != nil {
		return "", err
	}
	if name == "demo" {
		return "demo", nil
	}

	dir, err := filepath.Abs(r.ScenarioDir)
	if err != nil {
		return "", fmt.Errorf("scenario directory cannot be resolved: %w", err)
	}
	path := filepath.Join(dir, name+".json")

	rel, err := filepath.Rel(dir, path)
	if err != nil || rel != name+".json" {
		return "", fmt.Errorf("%w: %q does not resolve inside the scenario directory",
			ErrUnsafeScenario, name)
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return "", fmt.Errorf("no scenario named %q", name)
	}
	return path, nil
}

// Submit accepts a request, records it as QUEUED, and runs it in the background.
//
// It returns as soon as the run is durable. A simulation takes seconds to minutes and
// an HTTP request that waited for one would time out at every layer between the
// caller and here.
func (r *Runner) Submit(ctx context.Context, req Request) (Run, error) {
	if err := req.Validate(); err != nil {
		return Run{}, err
	}
	if _, err := r.ResolveScenario(req.Scenario); err != nil {
		return Run{}, err
	}

	at := r.now()
	run := Run{
		RunID:       fmt.Sprintf("sim_%d_%s", at.UnixNano(), req.Scenario),
		TenantID:    req.TenantID,
		Scenario:    req.Scenario,
		Seed:        *req.Seed,
		RequestedBy: req.RequestedBy,
		SubmittedBy: req.SubmittedBy,
		Status:      StatusQueued,
		RequestedAt: at,
	}

	if err := r.Store.Create(ctx, run); err != nil {
		return Run{}, fmt.Errorf("the run could not be recorded: %w", err)
	}

	// Detached from the request's context. A caller hanging up must not kill a
	// simulation the platform has already promised to run and told them the id of.
	// Cancellation is explicit, through Cancel, and never a side effect of a socket
	// closing.
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))

	// Registered before the goroutine starts, so a run cancelled while it is still
	// queued is stopped rather than starting a moment later. Without this the window
	// between "accepted" and "running" is one where cancellation silently does
	// nothing, and it is exactly the window a busy engine spends most of its time in.
	r.register(run, cancel)

	go func() {
		defer r.unregister(run)
		r.execute(runCtx, run, cancel)
	}()

	return run, nil
}

func inFlightKey(tenantID, runID string) string { return tenantID + "/" + runID }

func (r *Runner) register(run Run, cancel context.CancelFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inFlight[inFlightKey(run.TenantID, run.RunID)] = cancel
}

func (r *Runner) unregister(run Run) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.inFlight, inFlightKey(run.TenantID, run.RunID))
}

// Cancel stops a run.
//
// The store settles it first, in one statement, because the engine finishing and the
// operator cancelling race by nature and the database is what decides rather than
// whichever goroutine got there first. Only then is the process killed.
//
// Owned reports whether this replica held the run and therefore stopped the engine on
// the spot. When it did not, the replica that does holds a watchdog on the row and
// stops it within one Watchdog interval, so the CPU comes back either way and the
// caller is told which of the two they got rather than being left to assume.
func (r *Runner) Cancel(ctx context.Context, tenantID, runID, by, byIdentity string) (owned bool, err error) {
	if r.Store == nil {
		return false, errors.New("no run store configured")
	}

	existing, err := r.Store.Load(ctx, tenantID, runID)
	if err != nil {
		return false, err
	}
	if existing == nil {
		return false, ErrNoSuchRun
	}

	changed, err := r.Store.Cancel(ctx, tenantID, runID, r.now(), by, byIdentity)
	if err != nil {
		return false, err
	}
	if !changed {
		// It finished between the read and the write, or it was already terminal.
		// Reporting it as cancelled would erase a result the operator still has.
		return false, ErrNotCancellable
	}

	r.mu.Lock()
	cancel, held := r.inFlight[inFlightKey(tenantID, runID)]
	r.mu.Unlock()
	if held {
		cancel()
	}

	if r.Evidence != nil {
		at := r.now()
		existing.Status = StatusCancelled
		existing.CancelledAt = &at
		existing.CancelledBy = by
		existing.CancelledByIdentity = byIdentity
		r.Evidence.Cancelled(ctx, *existing)
	}

	r.log().Info("simulation cancelled", "run_id", runID, "by", by,
		"identity", byIdentity, "owned", held)
	return held, nil
}

// watch kills the engine when the run's row says it was cancelled elsewhere.
//
// It returns a function that stops it. A watchdog that outlived its run would keep
// querying for a row whose answer cannot change, once per interval, forever.
func (r *Runner) watch(ctx context.Context, run Run, kill context.CancelFunc) func() {
	done := make(chan struct{})
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		ticker := time.NewTicker(r.Watchdog)
		defer ticker.Stop()

		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}

			// Its own timeout, and its own errors are swallowed. A database blip
			// must not kill a running simulation: the failure mode of a watchdog
			// that fails open is a late kill, and of one that fails closed is a run
			// destroyed by an unrelated outage.
			pollCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			status, err := r.Store.Status(pollCtx, run.TenantID, run.RunID)
			cancel()

			if err != nil {
				continue
			}
			if status == StatusCancelled {
				r.log().Info("simulation cancelled elsewhere; stopping the engine",
					"run_id", run.RunID)
				kill()
				return
			}
		}
	}()

	return func() {
		close(done)
		<-stopped
	}
}

func (r *Runner) execute(ctx context.Context, run Run, cancelThis context.CancelFunc) {
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-ctx.Done():
		// Cancelled while queued. The store already says CANCELLED; nothing started,
		// so there is nothing to stop and nothing to record.
		return
	}

	if ctx.Err() != nil {
		return
	}

	started := r.now()
	run.Status = StatusRunning
	run.StartedAt = &started
	if err := r.Store.Start(ctx, run.TenantID, run.RunID, started); err != nil {
		r.log().Error("simulation start not recorded", "run_id", run.RunID, "err", err)
	}
	if r.Evidence != nil {
		r.Evidence.Started(ctx, run)
	}

	// The watchdog runs for exactly as long as the engine does.
	stopWatch := r.watch(ctx, run, cancelThis)
	record, err := r.invoke(ctx, run)
	stopWatch()
	completed := r.now()
	run.CompletedAt = &completed

	if err != nil {
		if ctx.Err() != nil {
			// Stopped on purpose. Fail would be refused by the store anyway, since
			// CANCELLED is terminal, but writing nothing is clearer than writing
			// something the database is expected to reject.
			r.log().Info("simulation stopped", "run_id", run.RunID)
			return
		}

		run.Status = StatusFailed
		run.Error = err.Error()
		if storeErr := r.Store.Fail(ctx, run.TenantID, run.RunID, completed, err.Error()); storeErr != nil {
			r.log().Error("simulation failure not recorded", "run_id", run.RunID, "err", storeErr)
		}
		if r.Evidence != nil {
			r.Evidence.Failed(ctx, run)
		}
		r.log().Error("simulation failed", "run_id", run.RunID, "err", err)
		return
	}

	run.Status = StatusCompleted
	run.Record = record
	run.ExperimentID, _ = record["experiment_id"].(string)
	run.ResultFingerprint, _ = record["result_fingerprint"].(string)
	run.ScenarioSourceHash, _ = record["scenario_source_hash"].(string)

	if err := r.Store.Complete(ctx, run, completed); err != nil {
		r.log().Error("simulation result not recorded", "run_id", run.RunID, "err", err)
	}
	if r.Evidence != nil {
		r.Evidence.Completed(ctx, run)
	}
	r.log().Info("simulation completed", "run_id", run.RunID,
		"fingerprint", run.ResultFingerprint, "took", completed.Sub(started))
}

// invoke runs the engine and parses its record.
func (r *Runner) invoke(ctx context.Context, run Run) (map[string]any, error) {
	scenario, err := r.ResolveScenario(run.Scenario)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, r.Timeout)
	defer cancel()

	// An argument vector, never a command string. Every element here is either a
	// literal or a value that has been validated to a shape with nothing to escape.
	cmd := exec.CommandContext(ctx, r.Python,
		"-m", "simulator.engine",
		"--scenario", scenario,
		"--seed", strconv.FormatInt(run.Seed, 10),
	)
	cmd.Dir = r.Repo

	// A deliberately small environment. The engine needs none of the platform's
	// credentials, and passing the parent's environment would hand a subprocess the
	// database and broker secrets it has no use for (spec section 35).
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PYTHONHASHSEED=0",
		"PYTHONIOENCODING=utf-8",
	}

	var stderr limitedBuffer
	stderr.limit = 8 << 10
	cmd.Stderr = &stderr

	stdout, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("the engine did not finish within %s", r.Timeout)
		}
		detail := stderr.String()
		if detail == "" {
			detail = err.Error()
		}
		return nil, fmt.Errorf("the engine refused or failed: %s", detail)
	}

	var record map[string]any
	if err := json.Unmarshal(stdout, &record); err != nil {
		return nil, fmt.Errorf("the engine's output is not a record: %w", err)
	}
	if _, ok := record["result_fingerprint"].(string); !ok {
		// A record with no fingerprint cannot be compared to another run, which is
		// the only thing a simulation result is for.
		return nil, errors.New("the engine returned a record with no result_fingerprint")
	}
	return record, nil
}

// limitedBuffer keeps the first N bytes and drops the rest.
//
// The engine's stderr is stored and returned through an API. An engine in a loop
// could otherwise write until this process runs out of memory, which would turn a
// broken scenario into an outage of the intelligence plane.
type limitedBuffer struct {
	limit int
	data  []byte
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - len(b.data); room > 0 {
		if len(p) > room {
			b.data = append(b.data, p[:room]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *limitedBuffer) String() string { return string(b.data) }
