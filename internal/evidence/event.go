// Package evidence is the append-only record of what the platform did and why.
//
// Two rules shape everything here. Evidence is never mutated: a correction is a new
// record that references the earlier one (ADR-009, INV-006). And evidence is not
// logs: operational logging is for humans debugging a process, evidence is the
// account of a financial decision, and the two are not interchangeable (INV-013).
package evidence

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// EventName is a name from the catalog in spec section 32.
type EventName string

// The V0 catalog. It is a closed set: an event nobody wrote down here cannot be
// emitted, because a timeline assembled from unnamed events cannot be replayed.
const (
	IntentReceived     EventName = "agent.intent.received.v1"
	IdentityVerified   EventName = "agent.identity.verified.v1"
	IdentityFailed     EventName = "agent.identity.failed.v1"
	AuthorityEvaluated EventName = "authority.evaluated.v1"
	PolicyEvaluated    EventName = "policy.evaluated.v1"
	IntentParentLinked EventName = "intent.parent.linked.v1"

	OrderSubmitted  EventName = "broker.order.submitted.v1"
	OrderUnknown    EventName = "broker.order.unknown.v1"
	OrderAccepted   EventName = "broker.order.accepted.v1"
	OrderRejected   EventName = "broker.order.rejected.v1"
	OrderFilled     EventName = "broker.order.filled.v1"
	OrderCancelled  EventName = "broker.order.cancelled.v1"
	OrderReconciled EventName = "broker.order.reconciled.v1"

	FleetMetricUpdated   EventName = "fleet.metric.updated.v1"
	FleetCohortCreated   EventName = "fleet.cohort.created.v1"
	FleetAnomalyDetected EventName = "fleet.anomaly.detected.v1"

	IncidentCreated   EventName = "incident.created.v1"
	IncidentUpdated   EventName = "incident.updated.v1"
	IncidentEscalated EventName = "incident.escalated.v1"
	IncidentClosed    EventName = "incident.closed.v1"

	ControlRecommended EventName = "control.recommended.v1"
	ControlApplied     EventName = "control.applied.v1"
	ControlRevoked     EventName = "control.revoked.v1"

	PolicyBundleCreated    EventName = "policy.bundle.created.v1"
	PolicyBundleActivated  EventName = "policy.bundle.activated.v1"
	PolicyBundleRolledBack EventName = "policy.bundle.rolled_back.v1"

	SimulationStarted   EventName = "simulation.started.v1"
	SimulationCompleted EventName = "simulation.completed.v1"
	SimulationFailed    EventName = "simulation.failed.v1"

	// EvidenceCorrected is not in the section 32 catalog. It is added here because
	// ADR-009 requires corrections to reference prior evidence rather than rewrite
	// it, and a correction that cannot be expressed as an event would have to be
	// expressed as an UPDATE.
	EvidenceCorrected EventName = "evidence.corrected.v1"
)

var catalog = map[EventName]bool{
	IntentReceived: true, IdentityVerified: true, IdentityFailed: true,
	AuthorityEvaluated: true, PolicyEvaluated: true, IntentParentLinked: true,
	OrderSubmitted: true, OrderUnknown: true, OrderAccepted: true, OrderRejected: true,
	OrderFilled: true, OrderCancelled: true, OrderReconciled: true,
	FleetMetricUpdated: true, FleetCohortCreated: true, FleetAnomalyDetected: true,
	IncidentCreated: true, IncidentUpdated: true, IncidentEscalated: true, IncidentClosed: true,
	ControlRecommended: true, ControlApplied: true, ControlRevoked: true,
	PolicyBundleCreated: true, PolicyBundleActivated: true, PolicyBundleRolledBack: true,
	SimulationStarted: true, SimulationCompleted: true, SimulationFailed: true,
	EvidenceCorrected: true,
}

// Known reports whether a name is in the catalog.
func Known(n EventName) bool { return catalog[n] }

// CatalogNames returns every known event name. Used by schema and structure tests.
func CatalogNames() []EventName {
	out := make([]EventName, 0, len(catalog))
	for n := range catalog {
		out = append(out, n)
	}
	return out
}

// SchemaVersion is the event envelope contract version.
const SchemaVersion = "0.1"

// Event is one evidence record.
//
// Every field in spec section 32 is here, and the ones that make replay possible
// are required rather than optional: an event without a correlation id cannot be
// placed in a timeline, and an event without a producer cannot be attributed.
type Event struct {
	SchemaVersion string    `json:"schema_version"`
	EventID       string    `json:"event_id"`
	EventName     EventName `json:"event_name"`
	TenantID      string    `json:"tenant_id"`
	AggregateID   string    `json:"aggregate_id"`
	CorrelationID string    `json:"correlation_id"`

	// CausationID names the event that led to this one. Empty for the first event
	// in a chain.
	CausationID string `json:"causation_id,omitempty"`

	OccurredAt time.Time `json:"occurred_at"`
	ProducedAt time.Time `json:"produced_at"`
	Producer   string    `json:"producer"`
	Sequence   int64     `json:"sequence"`

	// CorrectsEventID points at the event this one supersedes. ADR-009: a
	// correction references prior evidence instead of rewriting it, so the earlier
	// record stays exactly as it was recorded.
	CorrectsEventID string `json:"corrects_event_id,omitempty"`

	Payload map[string]any `json:"payload,omitempty"`
}

// ValidationError explains why an event cannot be recorded.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("evidence event invalid: %s: %s", e.Field, e.Reason)
}

// ErrNotFound is returned when no evidence matches a query.
var ErrNotFound = errors.New("no evidence found")

// Validate refuses an event that could not be replayed later.
func (e *Event) Validate() error {
	switch {
	case e.SchemaVersion == "":
		return &ValidationError{"schema_version", "required"}
	case e.SchemaVersion != SchemaVersion:
		return &ValidationError{"schema_version", "this build records " + SchemaVersion}
	case strings.TrimSpace(e.EventID) == "":
		return &ValidationError{"event_id", "required; it is the deduplication key for at-least-once delivery (ADR-008)"}
	case !Known(e.EventName):
		return &ValidationError{"event_name", fmt.Sprintf("%q is not in the section 32 catalog", e.EventName)}
	case strings.TrimSpace(e.TenantID) == "":
		return &ValidationError{"tenant_id", "required on every domain object"}
	case strings.TrimSpace(e.AggregateID) == "":
		return &ValidationError{"aggregate_id", "required"}
	case strings.TrimSpace(e.CorrelationID) == "":
		return &ValidationError{"correlation_id", "required; without it the event cannot be placed in a timeline"}
	case strings.TrimSpace(e.Producer) == "":
		return &ValidationError{"producer", "required; unattributed evidence is not evidence"}
	case e.OccurredAt.IsZero():
		return &ValidationError{"occurred_at", "required"}
	case e.ProducedAt.IsZero():
		return &ValidationError{"produced_at", "required"}
	case e.Sequence < 0:
		return &ValidationError{"sequence", "must not be negative"}
	case e.EventName == EvidenceCorrected && strings.TrimSpace(e.CorrectsEventID) == "":
		return &ValidationError{"corrects_event_id", "a correction must name the event it supersedes (ADR-009)"}
	case e.CorrectsEventID != "" && e.CorrectsEventID == e.EventID:
		return &ValidationError{"corrects_event_id", "an event cannot correct itself"}
	}

	e.OccurredAt = e.OccurredAt.UTC()
	e.ProducedAt = e.ProducedAt.UTC()
	return nil
}

// Subject is the NATS subject an event publishes to.
//
// Tenant is part of the subject so a consumer can be scoped to one tenant by
// subscription rather than by remembering to filter (spec section 34).
func (e *Event) Subject() string {
	return fmt.Sprintf("evidence.%s.%s", e.TenantID, e.EventName)
}
