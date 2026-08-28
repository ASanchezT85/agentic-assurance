package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
)

// INV-009: fleet intelligence may recommend; customer policy authorizes enforcement.
//
// The failure this prevents is not dramatic. It is a fleet engine that gets good
// enough that somebody wires its recommendations straight through, and a customer
// discovers their orders were throttled by a vendor's heuristic. Spec section 42
// keeps fleet controls in shadow mode by default, and ADR-003 puts final authority
// in the customer's infrastructure.
//
// The guards below are structural where they can be. A rule enforced by the type
// system does not need anyone to remember it.

var shadowAt = time.Date(2026, 8, 27, 14, 32, 4, 0, time.UTC)

func recommendation(id string, action fleet.ControlAction) fleet.Recommendation {
	return fleet.Recommendation{
		RecommendationID: id,
		TenantID:         "tenant_acme",
		CohortID:         "cohort_instrument_id-instr_x_AND_side-BUY",
		CohortPredicate:  "instrument_id=instr_x AND side=BUY",
		WouldHave:        action,
		Reason:           "feed concentration 0.88 over 80% coverage",
		GeneratedAt:      shadowAt,
	}
}

// A recommendation cannot enforce. It has no method that does, and it reports itself
// as unenforced.
func TestARecommendationCannotEnforce(t *testing.T) {
	r := recommendation("rec_1", fleet.ControlThrottle)

	if r.Enforced() {
		t.Fatal("a recommendation reported itself as enforced (INV-009)")
	}
	if !strings.Contains(r.String(), "would have") {
		t.Errorf("a recommendation renders as an action: %q", r.String())
	}
}

// Authorize is the only route to an enforceable control, and it refuses without a
// customer authorization.
func TestEnforcementRequiresACustomerAuthorization(t *testing.T) {
	r := recommendation("rec_1", fleet.ControlIsolateCohort)

	if _, err := fleet.Authorize(r, fleet.Authorization{}, shadowAt); err == nil {
		t.Fatal("a fleet control was applied with no customer authorization (INV-009)")
	}

	// A partial authorization is no authorization: naming an author without naming
	// the policy that permits it is not a customer decision, it is a person acting.
	partial := fleet.Authorization{AuthorizedBy: "operator@acme"}
	if _, err := fleet.Authorize(r, partial, shadowAt); err == nil {
		t.Error("a control was applied under an authorization naming no policy bundle")
	}

	authorization, err := fleet.NewAuthorization("risk-committee@acme", "bundle_7",
		"approved under the standing cohort-isolation policy", shadowAt)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	control, err := fleet.Authorize(r, authorization, shadowAt)
	if err != nil {
		t.Fatalf("a fully authorized control was refused: %v", err)
	}
	if !control.Enforced() {
		t.Error("an authorized control does not report itself as enforced")
	}
	if control.Authorization.PolicyBundleID != "bundle_7" {
		t.Error("the control does not name the policy that authorized it")
	}
}

// An authorization must be attributable and explained, or it is refused outright.
func TestAuthorizationsAreAuditable(t *testing.T) {
	cases := []struct {
		name               string
		by, bundle, reason string
	}{
		{"no author", "", "bundle_7", "because"},
		{"no policy bundle", "operator@acme", "", "because"},
		{"no reason", "operator@acme", "bundle_7", ""},
	}
	for _, tc := range cases {
		if _, err := fleet.NewAuthorization(tc.by, tc.bundle, tc.reason, shadowAt); err == nil {
			t.Errorf("an authorization with %s was accepted (spec section 36)", tc.name)
		}
	}
}

// The structural guard. Nothing in the fleet package may construct an Authorization,
// because a package that could would be able to authorize itself.
func TestFleetIntelligenceCannotAuthorizeItself(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../../internal/fleet", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			// Both node kinds are checked in one pass. An earlier version returned
			// early for anything that was not a CallExpr, which made the literal
			// check unreachable: a composite literal is not a call. The negative
			// test for route two is what found it, which is the argument for
			// writing negative tests for guards as well as for code.
			//
			// NewAuthorization's own body is exempt. It is the constructor, so it is
			// the one place that has to build one, and every other route through
			// this package is closed.
			for _, decl := range file.Decls {
				fn, isFunc := decl.(*ast.FuncDecl)
				if isFunc && fn.Name.Name == "NewAuthorization" {
					continue
				}

				ast.Inspect(decl, func(n ast.Node) bool {
					switch node := n.(type) {
					case *ast.CallExpr:
						if ident, ok := node.Fun.(*ast.Ident); ok && ident.Name == "NewAuthorization" {
							t.Errorf("%s calls NewAuthorization; the fleet engine must not be "+
								"able to authorize its own recommendations (INV-009)", path)
						}
					case *ast.CompositeLit:
						// A populated Authorization literal bypasses the constructor
						// and reaches Authorize with something that looks valid.
						if ident, ok := node.Type.(*ast.Ident); ok &&
							ident.Name == "Authorization" && len(node.Elts) > 0 {
							t.Errorf("%s builds a populated Authorization literal; that is "+
								"self-authorization by another route (INV-009)", path)
						}
					}
					return true
				})
			}
		}
	}
}

// Recording a hypothetical enforces nothing. This is the whole of shadow mode.
func TestRecordingAHypotheticalEnforcesNothing(t *testing.T) {
	ledger := &fleet.Ledger{}
	for i, action := range []fleet.ControlAction{
		fleet.ControlThrottle, fleet.ControlRequireApproval,
		fleet.ControlIsolateCohort, fleet.ControlReadOnly,
	} {
		ledger.Record(recommendation("rec_"+string(rune('a'+i)), action))
	}

	report := ledger.Report()
	if report.Total != 4 {
		t.Fatalf("total = %d, want 4", report.Total)
	}
	if report.Authorized != 0 {
		t.Errorf("%d recommendations were authorized by being recorded (INV-009)", report.Authorized)
	}
	for _, entry := range ledger.Entries() {
		if entry.Control != nil {
			t.Errorf("%s became a control without an authorization",
				entry.Recommendation.RecommendationID)
		}
	}
}

// All four fleet-level controls are expressible as hypotheticals (spec section 42).
func TestAllFourShadowActionsAreRecorded(t *testing.T) {
	ledger := &fleet.Ledger{}
	wanted := []fleet.ControlAction{
		fleet.ControlThrottle, fleet.ControlRequireApproval,
		fleet.ControlIsolateCohort, fleet.ControlReadOnly,
	}
	for i, action := range wanted {
		ledger.Record(recommendation("rec_"+string(rune('a'+i)), action))
	}

	byAction := ledger.Report().ByAction
	for _, action := range wanted {
		if byAction[action] != 1 {
			t.Errorf("would_have_%s was not recorded", strings.ToLower(string(action)))
		}
	}
}

// Precision without its coverage is the failure spec section 28 warns about, applied
// to shadow mode instead of to concentration.
func TestPrecisionIsNeverReportedWithoutCoverage(t *testing.T) {
	ledger := &fleet.Ledger{}
	for i := 0; i < 100; i++ {
		ledger.Record(recommendation("rec_"+itoa(i), fleet.ControlThrottle))
	}

	// Nothing reviewed: precision is unknown, not zero.
	report := ledger.Report()
	if report.Known {
		t.Error("precision was reported with nothing reviewed")
	}
	if !strings.Contains(report.String(), "UNKNOWN") {
		t.Errorf("an unreviewed report reads as a measurement: %q", report.String())
	}

	// Three reviews out of a hundred, all correct. The precision is 1.00 and it
	// means very little, so the coverage has to travel with it.
	for i := 0; i < 3; i++ {
		if err := ledger.Review("rec_"+itoa(i), fleet.VerdictHarmful, "analyst@acme", shadowAt); err != nil {
			t.Fatalf("review: %v", err)
		}
	}

	report = ledger.Report()
	if report.Precision != 1 {
		t.Errorf("precision = %v", report.Precision)
	}
	if report.ReviewCoverage > 0.05 {
		t.Errorf("review coverage = %v, want 3/100", report.ReviewCoverage)
	}
	if !strings.Contains(report.String(), "coverage") {
		t.Errorf("the report states a precision without its coverage: %q", report.String())
	}
}

// The retrospective analysis spec section 42 requires: precision and false positives
// over reviewed hypotheticals.
func TestRetrospectiveAnalysis(t *testing.T) {
	ledger := &fleet.Ledger{}
	for i := 0; i < 10; i++ {
		ledger.Record(recommendation("rec_"+itoa(i), fleet.ControlThrottle))
	}

	// Six were real problems, four were ordinary activity.
	for i := 0; i < 6; i++ {
		_ = ledger.Review("rec_"+itoa(i), fleet.VerdictHarmful, "analyst@acme", shadowAt)
	}
	for i := 6; i < 10; i++ {
		_ = ledger.Review("rec_"+itoa(i), fleet.VerdictBenign, "analyst@acme", shadowAt)
	}

	report := ledger.Report()
	if report.Precision != 0.6 {
		t.Errorf("precision = %v, want 0.6", report.Precision)
	}
	if report.FalsePositive != 0.4 {
		t.Errorf("false positive rate = %v, want 0.4", report.FalsePositive)
	}
	if report.ReviewCoverage != 1 {
		t.Errorf("review coverage = %v, want 1", report.ReviewCoverage)
	}
	t.Logf("shadow report: %s", report)
}

// A review must conclude something, and must be attributable.
func TestReviewsMustConclude(t *testing.T) {
	ledger := &fleet.Ledger{}
	ledger.Record(recommendation("rec_1", fleet.ControlThrottle))

	if err := ledger.Review("rec_1", fleet.VerdictUnreviewed, "analyst@acme", shadowAt); err == nil {
		t.Error("a review concluding nothing was accepted")
	}
	if err := ledger.Review("rec_1", fleet.VerdictHarmful, "", shadowAt); err == nil {
		t.Error("an unattributed review was accepted (spec section 36)")
	}
	if err := ledger.Review("rec_missing", fleet.VerdictHarmful, "analyst@acme", shadowAt); err == nil {
		t.Error("a review of a recommendation that does not exist was accepted")
	}
}

// Applying a control to a recommendation that does not exist fails loudly. An
// enforcement action recorded against nothing is worse than one that errors.
func TestApplyingToAnUnknownRecommendationFails(t *testing.T) {
	ledger := &fleet.Ledger{}
	authorization, err := fleet.NewAuthorization("operator@acme", "bundle_7", "approved", shadowAt)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	if err := ledger.Apply("rec_never_recorded", authorization, shadowAt); err == nil {
		t.Error("a control was applied against a recommendation that was never made")
	}
}

// The end-to-end shape of shadow mode: recorded, reviewed, and only then authorized
// by a customer.
func TestShadowToEnforcementRequiresACustomerInTheLoop(t *testing.T) {
	ledger := &fleet.Ledger{}
	ledger.Record(recommendation("rec_1", fleet.ControlIsolateCohort))

	// A month of shadow operation, reviewed.
	if err := ledger.Review("rec_1", fleet.VerdictHarmful, "analyst@acme", shadowAt); err != nil {
		t.Fatalf("review: %v", err)
	}
	if ledger.Report().Authorized != 0 {
		t.Fatal("reviewing a recommendation authorized it (INV-009)")
	}

	// The customer decides.
	authorization, err := fleet.NewAuthorization("risk-committee@acme", "bundle_7",
		"a month of shadow data showed the isolation rule is right", shadowAt)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	if err := ledger.Apply("rec_1", authorization, shadowAt); err != nil {
		t.Fatalf("apply: %v", err)
	}

	entries := ledger.Entries()
	if entries[0].Control == nil {
		t.Fatal("the authorized recommendation did not become a control")
	}
	if entries[0].Control.Authorization.AuthorizedBy != "risk-committee@acme" {
		t.Error("the control does not name who authorized it")
	}
	if ledger.Report().Authorized != 1 {
		t.Error("the report does not distinguish authorized controls from hypotheticals")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
