// Package fleet computes asynchronous fleet intelligence.
//
// Everything here is off the hot path. Spec section 29 forbids the fleet engine from
// submitting orders, modifying customer hard policy, or becoming required for
// identity and authority enforcement, and INV-005 requires local enforcement to
// survive its total absence. ADR-022 extends the no-LLM rule to this package
// regardless of call timing: an asynchronously produced number is exactly as
// unexplainable as a synchronous one.
package fleet

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"agentic-assurance/internal/intent"
)

// Cohort is a group of intents sharing explicit properties.
//
// Spec section 30 is unambiguous: every cohort must be explainable by explicit
// predicates, and no opaque identifier is acceptable in V0. The predicate is
// therefore not documentation attached to a cohort, it is the cohort's identity, and
// the id is derived from it.
type Cohort struct {
	TenantID   string
	Predicates []Predicate
}

// Predicate is one condition. Field and Value are plain strings so a cohort can be
// printed, stored and compared without the reader needing this package.
type Predicate struct {
	Field string
	Value string
}

// Recognised predicate fields. A cohort keyed on something not listed here cannot be
// explained to a reader who was not present when it was written.
const (
	FieldInstrument  = "instrument_id"
	FieldSide        = "side"
	FieldStrategy    = "strategy_id"
	FieldModelFamily = "model_family"
	FieldFeed        = "market_data_feed"
	FieldPrincipal   = "principal_id"
	FieldAgent       = "agent_id"
	FieldAssetClass  = "asset_class"
	FieldAttestation = "attestation_level"
)

var knownFields = map[string]bool{
	FieldInstrument: true, FieldSide: true, FieldStrategy: true, FieldModelFamily: true,
	FieldFeed: true, FieldPrincipal: true, FieldAgent: true, FieldAssetClass: true,
	FieldAttestation: true,
}

// NewCohort builds a cohort, refusing any predicate that cannot be explained.
func NewCohort(tenantID string, predicates ...Predicate) (Cohort, error) {
	if tenantID == "" {
		return Cohort{}, fmt.Errorf("a cohort belongs to exactly one tenant")
	}
	if len(predicates) == 0 {
		return Cohort{}, fmt.Errorf("a cohort with no predicates is every intent, which explains nothing")
	}
	for _, p := range predicates {
		if !knownFields[p.Field] {
			return Cohort{}, fmt.Errorf("unknown cohort field %q; a cohort must be explainable "+
				"by explicit predicates (spec section 30)", p.Field)
		}
		if strings.TrimSpace(p.Value) == "" {
			return Cohort{}, fmt.Errorf("predicate %s has no value", p.Field)
		}
	}

	sorted := append([]Predicate(nil), predicates...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Field != sorted[j].Field {
			return sorted[i].Field < sorted[j].Field
		}
		return sorted[i].Value < sorted[j].Value
	})
	return Cohort{TenantID: tenantID, Predicates: sorted}, nil
}

// Expression renders the cohort as its predicate list.
//
// This is the cohort's identity and its explanation at once. Reading it tells you
// exactly which intents are in the group, with no lookup and no model.
func (c Cohort) Expression() string {
	parts := make([]string, 0, len(c.Predicates))
	for _, p := range c.Predicates {
		parts = append(parts, p.Field+"="+p.Value)
	}
	return strings.Join(parts, " AND ")
}

// ID is derived from the expression, so two cohorts with the same predicates are the
// same cohort, in any process and on any day.
func (c Cohort) ID() string {
	return "cohort_" + strings.NewReplacer(" ", "_", "=", "-").Replace(c.Expression())
}

// Matches reports whether an intent belongs to this cohort.
func (c Cohort) Matches(e *intent.AgentExecutionEnvelope) bool {
	if e == nil || e.TenantID != c.TenantID {
		return false
	}
	for _, p := range c.Predicates {
		if fieldValue(e, p.Field) != p.Value {
			return false
		}
	}
	return true
}

func fieldValue(e *intent.AgentExecutionEnvelope, field string) string {
	switch field {
	case FieldInstrument:
		return e.Intent.InstrumentID
	case FieldSide:
		return string(e.Intent.Side)
	case FieldStrategy:
		return e.Lineage.StrategyID
	case FieldModelFamily:
		return e.RuntimeClaims.ModelFamily.Value
	case FieldFeed:
		return firstDependency(e, intent.DependencyMarketData)
	case FieldPrincipal:
		return e.Principal.PrincipalID
	case FieldAgent:
		return e.Agent.AgentID
	case FieldAssetClass:
		return string(e.Intent.AssetClass)
	case FieldAttestation:
		return string(e.Agent.Attestation.Level)
	}
	return ""
}

func firstDependency(e *intent.AgentExecutionEnvelope, t intent.DependencyType) string {
	for _, d := range e.Dependencies {
		if d.Type == t {
			return d.ID
		}
	}
	return ""
}

// Window is a closed-open time range: [Start, End).
//
// Half-open so that consecutive windows neither overlap nor drop an intent that
// lands exactly on a boundary. An intent counted twice inflates a burst; one dropped
// hides it.
type Window struct {
	Start time.Time
	End   time.Time
}

func (w Window) Contains(t time.Time) bool {
	return !t.Before(w.Start) && t.Before(w.End)
}

func (w Window) Duration() time.Duration { return w.End.Sub(w.Start) }

// RollingWindows produces consecutive windows of the given size covering [from, to).
func RollingWindows(from, to time.Time, size time.Duration) []Window {
	if size <= 0 || !from.Before(to) {
		return nil
	}
	var out []Window
	for start := from.UTC(); start.Before(to.UTC()); start = start.Add(size) {
		end := start.Add(size)
		if end.After(to.UTC()) {
			end = to.UTC()
		}
		out = append(out, Window{Start: start, End: end})
	}
	return out
}
