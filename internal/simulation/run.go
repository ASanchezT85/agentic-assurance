// Package simulation is the control surface over the Digital Twin.
//
// The twin is a Python process (ADR-016) and stays one. This package starts it,
// records what it produced, and serves the two endpoints of spec section 46. It adds
// no deployable: the API is mounted on the fleet engine, because a simulation is
// intelligence and not enforcement, and ADR-011 counts four V0 deployables.
//
// Nothing here can change what production does. A simulation answers "what would
// happen"; only an authorized customer control changes what does happen (INV-009).
// There is no code in this package that writes a policy bundle, an authority grant,
// or an order, which is that invariant expressed as an absence.
package simulation

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is where a run is.
type Status string

const (
	// StatusQueued means accepted and not yet started. A run is queued rather than
	// refused when the engine is busy, because a simulation is expensive and a
	// customer who asked for one should get it late rather than not at all.
	StatusQueued    Status = "QUEUED"
	StatusRunning   Status = "RUNNING"
	StatusCompleted Status = "COMPLETED"
	StatusFailed    Status = "FAILED"

	// StatusCancelled is a run an operator stopped. Terminal, and deliberately not
	// FAILED: nothing went wrong, and a failure count that included cancellations
	// would make the engine look unreliable every time someone changed their mind.
	StatusCancelled Status = "CANCELLED"
)

// Terminal reports whether a status will not change again.
func (s Status) Terminal() bool {
	return s == StatusCompleted || s == StatusFailed || s == StatusCancelled
}

// Request is what a caller asks for.
type Request struct {
	TenantID string `json:"-"`

	// Scenario names a scenario, never a path. See ResolveScenario.
	Scenario string `json:"scenario"`

	// Seed is required and has no default. A run nobody can reproduce is an
	// anecdote, and a seed the platform chose silently is one the caller cannot
	// quote back.
	Seed *int64 `json:"seed"`

	// RequestedBy is the human or service that asked. Spec section 36: humans are
	// audited too, and a simulation is a request against a customer's data.
	//
	// Self-declared, and kept because it is the only place a human name can come
	// from: a credential identifies a service, not the person operating it. It is
	// recorded beside SubmittedBy rather than instead of it.
	RequestedBy string `json:"requested_by"`

	// SubmittedBy is the authenticated credential. Not settable by the caller: it is
	// filled from the transport, and it is the half of "who asked" that is worth
	// anything six months later.
	SubmittedBy string `json:"-"`
}

// Validate checks a request before anything is started.
func (r Request) Validate() error {
	var problems []string

	if strings.TrimSpace(r.TenantID) == "" {
		problems = append(problems, "tenant is required")
	}
	if r.Seed == nil {
		problems = append(problems,
			"seed is required; an unseeded run is not reproducible, and a seed the "+
				"platform picked silently is one the caller cannot quote back")
	}
	if strings.TrimSpace(r.RequestedBy) == "" {
		problems = append(problems, "requested_by is required")
	}
	if _, err := ValidateScenarioName(r.Scenario); err != nil {
		problems = append(problems, err.Error())
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ErrUnsafeScenario is returned for a scenario name that is not a name.
var ErrUnsafeScenario = errors.New("scenario is not a scenario name")

// ValidateScenarioName accepts a bare name and nothing else.
//
// The name becomes a path and then an argument to a process. Accepting a path from an
// API request would let a caller name any readable file on the host and have the
// engine parse it, and accepting a shell-shaped string would be worse. So: letters,
// digits, underscore and dash, and the reserved name "demo". No separators, no dots,
// no traversal, nothing to escape.
//
// Refused rather than sanitised. A name that is not name-shaped is a request that
// should not be honoured, and stripping the bad characters would honour a request the
// caller did not make.
func ValidateScenarioName(name string) (string, error) {
	name = strings.TrimSpace(name)

	if name == "" {
		return "", fmt.Errorf("%w: it is empty", ErrUnsafeScenario)
	}
	if len(name) > 64 {
		return "", fmt.Errorf("%w: it is longer than 64 characters", ErrUnsafeScenario)
	}
	if name == "demo" {
		return name, nil
	}

	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-':
		default:
			return "", fmt.Errorf(
				"%w: %q contains %q. A scenario is named, not located: letters, digits, "+
					"underscore and dash only", ErrUnsafeScenario, name, r)
		}
	}
	return name, nil
}

// Run is a simulation, from request to record.
type Run struct {
	RunID    string `json:"run_id"`
	TenantID string `json:"tenant_id"`

	Scenario    string `json:"scenario"`
	Seed        int64  `json:"seed"`
	RequestedBy string `json:"requested_by"`

	Status Status `json:"status"`

	RequestedAt time.Time  `json:"requested_at"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`

	// ExperimentID and the two hashes come from the engine's own record. They are
	// kept as columns as well as inside the record so a run is findable by what makes
	// it reproducible, not only by the id the platform assigned.
	ExperimentID       string `json:"experiment_id,omitempty"`
	ResultFingerprint  string `json:"result_fingerprint,omitempty"`
	ScenarioSourceHash string `json:"scenario_source_hash,omitempty"`

	// Record is the engine's output, stored whole. Summarising it here would mean
	// deciding today which fields a future question needs.
	Record map[string]any `json:"record,omitempty"`

	// Error is why a failed run failed, in the engine's own words.
	Error string `json:"error,omitempty"`

	// SubmittedBy and CancelledByIdentity are the authenticated credentials behind
	// RequestedBy and CancelledBy. Those two are text the caller typed; these are
	// what the transport established.
	SubmittedBy string `json:"submitted_by,omitempty"`

	CancelledAt         *time.Time `json:"cancelled_at,omitempty"`
	CancelledBy         string     `json:"cancelled_by,omitempty"`
	CancelledByIdentity string     `json:"cancelled_by_identity,omitempty"`
}

// ErrNotCancellable is returned for a run that has already finished.
var ErrNotCancellable = errors.New("the run has already finished")

// ErrNoSuchRun is returned for a run this tenant does not have.
var ErrNoSuchRun = errors.New("no such simulation run")

// Duration is how long the engine ran, or zero while it has not finished.
func (r Run) Duration() time.Duration {
	if r.StartedAt == nil || r.CompletedAt == nil {
		return 0
	}
	return r.CompletedAt.Sub(*r.StartedAt)
}
