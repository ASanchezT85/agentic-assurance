package fleet

import (
	"fmt"
	"sort"
	"time"
)

// Shadow mode (spec section 42).
//
// Fleet-level controls begin here and, unless a customer says otherwise, stay here.
// The platform records what it would have done; local hard policy keeps enforcing;
// nothing the fleet engine concludes blocks anything on its own.
//
// INV-009 is the rule: fleet intelligence may recommend, customer policy authorizes
// enforcement. This file makes that a property of the types rather than a discipline.
// A Recommendation has no method that enforces, and the only function that produces
// an enforceable Control requires an Authorization that the fleet engine cannot
// construct.

// ControlAction is a fleet-level control (spec section 16).
//
// These four are the ones an order-level policy rule cannot emit: internal/policy
// rejects ISOLATE_COHORT and READ_ONLY with a reason pointing here, because they act
// on a cohort or an account rather than on one order.
type ControlAction string

const (
	ControlThrottle        ControlAction = "THROTTLE"
	ControlRequireApproval ControlAction = "REQUIRE_APPROVAL"
	ControlIsolateCohort   ControlAction = "ISOLATE_COHORT"
	ControlReadOnly        ControlAction = "READ_ONLY"
)

func validControl(a ControlAction) bool {
	switch a {
	case ControlThrottle, ControlRequireApproval, ControlIsolateCohort, ControlReadOnly:
		return true
	}
	return false
}

// Recommendation is what fleet intelligence produces.
//
// It is a statement about what would have happened. There is deliberately no Apply,
// Enforce or Execute method on it: the only route from here to an actual control
// runs through Authorize, which needs something the fleet engine cannot make.
type Recommendation struct {
	RecommendationID string
	TenantID         string
	CohortID         string
	CohortPredicate  string

	// WouldHave is the field spec section 42 asks to be recorded. The name is the
	// point: reading `rec.WouldHave` at a call site makes it hard to mistake this
	// for something that took effect.
	WouldHave ControlAction

	Reason      string
	Window      Window
	GeneratedAt time.Time
}

// Enforced is always false. It exists so that a caller serialising a recommendation
// into evidence or a console cannot omit the distinction by accident.
func (r Recommendation) Enforced() bool { return false }

func (r Recommendation) String() string {
	return fmt.Sprintf("would have %s cohort %s: %s", r.WouldHave, r.CohortPredicate, r.Reason)
}

// Authorization is the customer's decision to let a fleet recommendation bind.
//
// It carries who authorized it and under what policy, because an enforcement action
// nobody signed is one nobody can be asked about later (spec section 36).
//
// The fleet engine cannot produce one of these. Nothing in this package constructs
// an Authorization: the constructor is exported for the customer-controlled plane to
// call, and a test asserts this package never calls it.
type Authorization struct {
	AuthorizedBy   string
	PolicyBundleID string
	Reason         string
	At             time.Time
}

// NewAuthorization is called by the customer-controlled enforcement plane, never by
// fleet intelligence.
func NewAuthorization(authorizedBy, policyBundleID, reason string, at time.Time) (Authorization, error) {
	switch {
	case authorizedBy == "":
		return Authorization{}, fmt.Errorf("an authorization with no author cannot be audited (spec section 36)")
	case policyBundleID == "":
		return Authorization{}, fmt.Errorf("an authorization must name the customer policy that permits it (INV-009)")
	case reason == "":
		return Authorization{}, fmt.Errorf("an authorization with no reason cannot be reviewed later")
	}
	return Authorization{
		AuthorizedBy:   authorizedBy,
		PolicyBundleID: policyBundleID,
		Reason:         reason,
		At:             at.UTC(),
	}, nil
}

// Control is an enforceable fleet action. It can only be produced by Authorize.
type Control struct {
	Recommendation Recommendation
	Authorization  Authorization
	AppliedAt      time.Time
}

// Enforced is always true, which is the whole difference from a Recommendation.
func (c Control) Enforced() bool { return true }

// Authorize turns a recommendation into a control.
//
// This is the only function in the codebase that produces an enforceable fleet
// control, and it cannot be called without an Authorization. That is INV-009
// expressed as a signature: fleet intelligence has nothing to pass here.
func Authorize(r Recommendation, a Authorization, at time.Time) (Control, error) {
	if a.AuthorizedBy == "" || a.PolicyBundleID == "" {
		return Control{}, fmt.Errorf("a fleet control requires a customer authorization "+
			"naming its author and policy bundle (INV-009); recommendation %s was not applied",
			r.RecommendationID)
	}
	if !validControl(r.WouldHave) {
		return Control{}, fmt.Errorf("unknown control action %q", r.WouldHave)
	}
	return Control{Recommendation: r, Authorization: a, AppliedAt: at.UTC()}, nil
}

// Verdict is what actually turned out to be true about a recommendation.
//
// Recorded after the fact by whoever reviewed it. UNREVIEWED is the honest default
// and is excluded from precision rather than counted as either kind of success.
type Verdict string

const (
	VerdictUnreviewed Verdict = "UNREVIEWED"
	// VerdictHarmful: the recommendation was right, something was wrong.
	VerdictHarmful Verdict = "HARMFUL"
	// VerdictBenign: the recommendation was wrong, the activity was ordinary.
	VerdictBenign Verdict = "BENIGN"
)

// LedgerEntry is one recorded hypothetical and, eventually, its verdict.
type LedgerEntry struct {
	Recommendation Recommendation
	Verdict        Verdict
	ReviewedBy     string
	ReviewedAt     time.Time

	// Control is set only if a customer authorized this recommendation. Nil means it
	// stayed hypothetical, which is the normal case.
	Control *Control
}

// Ledger records what shadow mode would have done, and supports the retrospective
// analysis spec section 42 requires.
type Ledger struct {
	entries []LedgerEntry
}

// Record adds a hypothetical. Nothing is enforced by recording it.
func (l *Ledger) Record(r Recommendation) {
	l.entries = append(l.entries, LedgerEntry{Recommendation: r, Verdict: VerdictUnreviewed})
}

// Apply records that a customer authorized one of the recommendations.
//
// It returns an error rather than silently ignoring an unknown id: an enforcement
// action recorded against nothing is worse than one that fails loudly.
func (l *Ledger) Apply(recommendationID string, a Authorization, at time.Time) error {
	for i := range l.entries {
		if l.entries[i].Recommendation.RecommendationID != recommendationID {
			continue
		}
		control, err := Authorize(l.entries[i].Recommendation, a, at)
		if err != nil {
			return err
		}
		l.entries[i].Control = &control
		return nil
	}
	return fmt.Errorf("no recommendation %s in the ledger", recommendationID)
}

// Review records what turned out to be true.
func (l *Ledger) Review(recommendationID string, v Verdict, reviewedBy string, at time.Time) error {
	if v != VerdictHarmful && v != VerdictBenign {
		return fmt.Errorf("a review must conclude HARMFUL or BENIGN; %q is not a conclusion", v)
	}
	if reviewedBy == "" {
		return fmt.Errorf("an unattributed review cannot be weighed later (spec section 36)")
	}
	for i := range l.entries {
		if l.entries[i].Recommendation.RecommendationID != recommendationID {
			continue
		}
		l.entries[i].Verdict = v
		l.entries[i].ReviewedBy = reviewedBy
		l.entries[i].ReviewedAt = at.UTC()
		return nil
	}
	return fmt.Errorf("no recommendation %s in the ledger", recommendationID)
}

func (l *Ledger) Entries() []LedgerEntry {
	out := append([]LedgerEntry(nil), l.entries...)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Recommendation.GeneratedAt.Before(out[j].Recommendation.GeneratedAt)
	})
	return out
}

// Report is the comparison spec section 42 asks for.
//
// Precision is computed over reviewed entries only, and the coverage says how many
// that was. A precision of 1.00 over three reviews out of four hundred is not a
// finding about the detector, and reporting it without the coverage would be the
// same failure as reporting concentration without it (spec section 28).
type Report struct {
	Total      int
	Reviewed   int
	Harmful    int
	Benign     int
	Authorized int

	// ByAction counts hypotheticals per control, so a reader can see whether the
	// engine mostly wants to throttle or mostly wants approval.
	ByAction map[ControlAction]int

	// Precision is harmful / reviewed. Known is false when nothing was reviewed:
	// a precision of zero and an unmeasured precision are different facts.
	Precision      float64
	FalsePositive  float64
	Known          bool
	ReviewCoverage float64
}

func (l *Ledger) Report() Report {
	report := Report{Total: len(l.entries), ByAction: map[ControlAction]int{}}

	for _, entry := range l.entries {
		report.ByAction[entry.Recommendation.WouldHave]++
		if entry.Control != nil {
			report.Authorized++
		}
		switch entry.Verdict {
		case VerdictHarmful:
			report.Reviewed++
			report.Harmful++
		case VerdictBenign:
			report.Reviewed++
			report.Benign++
		}
	}

	if report.Total > 0 {
		report.ReviewCoverage = float64(report.Reviewed) / float64(report.Total)
	}
	if report.Reviewed > 0 {
		report.Known = true
		report.Precision = float64(report.Harmful) / float64(report.Reviewed)
		report.FalsePositive = float64(report.Benign) / float64(report.Reviewed)
	}
	return report
}

// String renders the report the way it must always be presented: never the precision
// without the coverage behind it.
func (r Report) String() string {
	if !r.Known {
		return fmt.Sprintf("%d hypotheticals recorded, none reviewed; precision is "+
			"UNKNOWN rather than zero", r.Total)
	}
	return fmt.Sprintf(
		"%d hypotheticals, %d reviewed (%.0f%% coverage): precision %.2f, "+
			"false positives %.2f, %d authorized by customer policy",
		r.Total, r.Reviewed, r.ReviewCoverage*100, r.Precision, r.FalsePositive, r.Authorized)
}
