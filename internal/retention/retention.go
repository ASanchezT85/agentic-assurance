// Package retention is the customer's policy about how long evidence lives, and the
// machinery that makes acting on it safe.
//
// It does not decide the period. The platform is infrastructure used by regulated
// institutions, and how long a record must exist depends on the entity, the
// jurisdiction and the class of record — encoding one number would be this system
// telling a customer what their obligation is.
//
// What it does decide is what may never happen: evidence under a legal hold is not
// touched, a partition is not destroyed before it is archived and the archive verified,
// and nothing is destroyed without two named people asking for it. Those are properties
// of an assurance platform rather than preferences a configuration can override.
package retention

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"agentic-assurance/internal/evidence"
)

// Class is what a policy is written against.
//
// The class of record rather than the table: a tenant may keep order assurance evidence
// for years and analytical telemetry for weeks, and both are rows in the same place.
type Class string

const (
	ClassOrderEvidence       Class = "ORDER_ASSURANCE_EVIDENCE"
	ClassAuthorityGrant      Class = "AUTHORITY_GRANT"
	ClassPolicyDecision      Class = "POLICY_DECISION"
	ClassHumanControlAction  Class = "HUMAN_CONTROL_ACTION"
	ClassSecurityAttestation Class = "SECURITY_ATTESTATION"
	ClassSimulationRecord    Class = "SIMULATION_RECORD"
	ClassAnalyticalTelemetry Class = "ANALYTICAL_TELEMETRY"
)

// ClassOf maps an event to the class whose policy governs it.
//
// One function rather than a column, because the class is a property of what the event
// is: an authority decision is an authority record whichever table it happens to sit
// in, and a column would let the two drift.
func ClassOf(name evidence.EventName) Class {
	switch name {
	case evidence.AuthorityEvaluated, evidence.AuthorityIssued, evidence.AuthorityRevoked:
		return ClassAuthorityGrant
	case evidence.PolicyEvaluated, evidence.PolicyBundleActivated,
		evidence.PolicyBundleCreated, evidence.PolicyBundleRolledBack:
		return ClassPolicyDecision
	case evidence.ControlApplied, evidence.ControlRevoked, evidence.IncidentEscalated,
		evidence.IncidentClosed, evidence.IncidentUpdated:
		return ClassHumanControlAction
	case evidence.IdentityVerified, evidence.IdentityFailed:
		return ClassSecurityAttestation
	case evidence.SimulationStarted, evidence.SimulationCompleted,
		evidence.SimulationFailed, evidence.SimulationCancelled:
		return ClassSimulationRecord
	case evidence.FleetMetricUpdated, evidence.FleetCohortCreated, evidence.FleetAnomalyDetected:
		return ClassAnalyticalTelemetry
	default:
		// Everything on the order path, and anything new. An unknown class defaulting
		// to the longest-lived one is the safe direction: a record kept too long is a
		// storage bill, and one deleted too early is gone.
		return ClassOrderEvidence
	}
}

// Policy is one tenant's rule for one class.
type Policy struct {
	TenantID    string
	Class       Class
	HotDays     int
	ArchiveDays int
	Destination string

	// DeletionMode is NONE or AUTHORIZED. There is deliberately no value meaning
	// "delete automatically": every destruction passes through an authorization row
	// naming two people.
	DeletionMode string

	UpdatedBy string
	UpdatedAt time.Time
}

// DeletionNone keeps an archived partition forever unless somebody authorizes removing
// it. It is the default because it is the answer that cannot destroy anything.
const DeletionNone = "NONE"

// DeletionAuthorized permits destruction, and only through an approved authorization.
const DeletionAuthorized = "AUTHORIZED"

// Hold pins evidence against every policy above it.
type Hold struct {
	TenantID      string
	HoldID        string
	CorrelationID string
	Reason        string
	PlacedBy      string
	PlacedAt      time.Time
	ReleasedAt    *time.Time
	ReleasedBy    string
}

// Active reports whether a hold still binds.
func (h Hold) Active() bool { return h.ReleasedAt == nil }

// Manifest is what an export produced, and how to prove it was not edited afterwards.
type Manifest struct {
	TenantID    string
	ManifestID  string
	Partition   string
	PeriodStart time.Time
	PeriodEnd   time.Time
	EventCount  int64

	// ChainHead is the last hash of the chain over the exported events. Each event's
	// hash covers the previous one, so changing any row changes every hash after it:
	// an archive that verifies is one nobody rewrote, and one that does not says where
	// it stopped being true.
	ChainHead   string
	Destination string

	ExportedBy string
	ExportedAt time.Time
	VerifiedAt *time.Time
	VerifiedBy string
}

// ChainHash folds one event into a hash chain.
//
// Over the fields that make an event what it is rather than over its stored bytes: a
// re-export must produce the same chain, and a JSON encoder that reorders keys between
// versions would otherwise look like tampering.
func ChainHash(previous string, e evidence.Event) string {
	sum := sha256.New()
	fmt.Fprintf(sum, "%s\n%s\n%s\n%s\n%s\n%s\n%s\n%d\n%s\n",
		previous, e.EventID, string(e.EventName), e.TenantID, e.AggregateID,
		e.CorrelationID, e.CausationID, e.Sequence,
		e.OccurredAt.UTC().Format(time.RFC3339Nano))
	return hex.EncodeToString(sum.Sum(nil))
}

// ChainOver computes the head hash for a sequence of events.
func ChainOver(events []evidence.Event) string {
	head := ""
	for _, e := range events {
		head = ChainHash(head, e)
	}
	return head
}

// Plan is what a retention pass would do, and why.
//
// Produced without touching anything. A retention system whose first run is also its
// first destructive act is one nobody can review, and the plan is what an operator
// takes to their compliance function.
type Plan struct {
	TenantID string
	AsOf     time.Time
	Actions  []Action
}

// Action is one partition and what the policy says about it.
type Action struct {
	Partition   string
	PeriodStart time.Time
	PeriodEnd   time.Time
	EventCount  int64
	AgeDays     int

	// What would happen: KEEP, ARCHIVE, or DESTROY. Never applied by producing this.
	Verdict string
	Reason  string
}

const (
	VerdictKeep    = "KEEP"
	VerdictArchive = "ARCHIVE"
	VerdictDestroy = "DESTROY"
)

// Decide is the whole retention decision for one partition, in one place.
//
// Separated from the database so the rules can be read and tested as rules. The order
// is the point: a hold outranks a policy, an unarchived partition is never destroyed,
// and destruction needs an authorization that exists rather than a mode that permits.
func Decide(policy Policy, ageDays int, held bool, archived bool, authorized bool) (string, string) {
	switch {
	case held:
		return VerdictKeep, "a legal hold is active for this tenant; holds outrank every policy"
	case policy.HotDays <= 0:
		return VerdictKeep, "no retention policy is configured for this class"
	case ageDays < policy.HotDays:
		return VerdictKeep, fmt.Sprintf("inside the hot window (%d of %d days)", ageDays, policy.HotDays)
	case !archived:
		return VerdictArchive, fmt.Sprintf(
			"past the hot window (%d of %d days) and not yet archived", ageDays, policy.HotDays)
	case policy.DeletionMode != DeletionAuthorized:
		return VerdictKeep, "archived; deletion is not authorized for this class"
	case policy.ArchiveDays <= 0:
		return VerdictKeep, "archived; this class is kept indefinitely"
	case ageDays < policy.HotDays+policy.ArchiveDays:
		return VerdictKeep, fmt.Sprintf("inside the archive window (%d of %d days)",
			ageDays, policy.HotDays+policy.ArchiveDays)
	case !authorized:
		return VerdictKeep, "past every window, and no approved deletion authorization exists"
	default:
		return VerdictDestroy, fmt.Sprintf("past every window (%d days) with an approved authorization",
			ageDays)
	}
}

// Growth is what a tenant's evidence is costing.
type Growth struct {
	TenantID   string
	Partition  string
	EventCount int64
	Bytes      int64
	OldestAt   time.Time
	NewestAt   time.Time
}

// Store is the interface the planner needs. Declared here so the rules above can be
// tested without a database.
type Store interface {
	Policies(ctx context.Context, tenantID string) (map[Class]Policy, error)
	ActiveHolds(ctx context.Context, tenantID string) ([]Hold, error)
	Partitions(ctx context.Context) ([]PartitionInfo, error)
	Manifest(ctx context.Context, tenantID, partition string) (*Manifest, error)
	ApprovedAuthorization(ctx context.Context, tenantID, manifestID string) (bool, error)
}

// PartitionInfo is one month of evidence as the database sees it.
type PartitionInfo struct {
	Name        string
	PeriodStart time.Time
	PeriodEnd   time.Time
	EventCount  int64
	Bytes       int64
}
