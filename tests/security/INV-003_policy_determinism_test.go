package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// INV-003: no LLM output can bypass deterministic policy.
//
// The guard has two halves. One is behavioural: evaluation is a pure function, so
// there is nothing for a non-deterministic input to vary. The other is structural:
// the evaluator has no seam through which an external opinion could arrive, and it
// never reaches the YAML parser (spec section 15.2), so policy cannot be re-authored
// at decision time either.

var policyAt = time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

func testBundle(t *testing.T) *policy.Bundle {
	t.Helper()
	raw, err := os.ReadFile("../fixtures/policies/valid/retail_agent_standard.yaml")
	if err != nil {
		t.Fatalf("read policy: %v", err)
	}
	src, err := policy.ParseSource(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	b, err := policy.Compile(src, "tenant_acme", "bundle_1", policyAt)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	return b
}

func policyEnvelope() *intent.AgentExecutionEnvelope {
	return &intent.AgentExecutionEnvelope{
		SchemaVersion: intent.SchemaVersion,
		EnvelopeID:    "env_1",
		TenantID:      "tenant_acme",
		Intent: intent.Intent{
			AssetClass:   intent.AssetEquity,
			InstrumentID: "instr_us_equity_00206R102",
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     ptr(4200.0),
			TimeInForce:  intent.TIFDay,
		},
	}
}

// The same inputs produce the same decision, every time. Anything varying between
// runs would be somewhere for a non-deterministic input to hide.
func TestEvaluationIsDeterministic(t *testing.T) {
	b := testBundle(t)
	env := policyEnvelope()

	first := policy.Evaluate(b, env, policyAt)
	for i := 0; i < 200; i++ {
		got := policy.Evaluate(b, env, policyAt)
		if got.Action != first.Action || got.DecidedBy != first.DecidedBy {
			t.Fatalf("run %d differed: %s/%s vs %s/%s (INV-003)",
				i, got.Action, got.DecidedBy, first.Action, first.DecidedBy)
		}
		if len(got.MatchedRules) != len(first.MatchedRules) {
			t.Fatalf("run %d matched a different rule set (INV-003)", i)
		}
	}
}

// The evaluator's package must not import anything that could fetch an opinion. The
// check is on the import graph rather than on names in strings, because that is what
// actually constrains what the code can do.
func TestPolicyPackageImportsNothingThatCanCallOut(t *testing.T) {
	forbidden := map[string]string{
		"net":       "network access in the policy package would put a remote call on the hot path (ADR-004)",
		"net/http":  "an HTTP client in the policy package is a remote inference call waiting to happen (ADR-004)",
		"os/exec":   "shelling out from policy evaluation is an external decision input (INV-003)",
		"net/rpc":   "remote calls have no place in deterministic policy (ADR-004)",
		"math/rand": "randomness in a financial control makes decisions unreproducible (INV-003)",
	}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../../internal/policy", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse internal/policy: %v", err)
	}

	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				name := strings.Trim(imp.Path.Value, `"`)
				if reason, banned := forbidden[name]; banned {
					t.Errorf("%s imports %q: %s", filepath.Base(path), name, reason)
				}
			}
		}
	}
}

// Spec section 15.2: YAML must not be interpreted on every order. The evaluator and
// the rule matcher must not reach the parser, so policy cannot be re-authored at
// decision time.
func TestEvaluationNeverParsesSource(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/policy/evaluate.go", nil, 0)
	if err != nil {
		t.Fatalf("parse evaluate.go: %v", err)
	}

	for _, imp := range file.Imports {
		name := strings.Trim(imp.Path.Value, `"`)
		if strings.Contains(name, "yaml") || strings.Contains(name, "json") {
			t.Errorf("evaluate.go imports %q; the evaluator must touch compiled "+
				"structures only (spec section 15.2)", name)
		}
	}

	// And no call to the parsing entry points, in case they arrive through a
	// package the file already imports.
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if ident, ok := call.Fun.(*ast.Ident); ok {
			if ident.Name == "ParseSource" || ident.Name == "Compile" {
				t.Errorf("evaluate.go calls %s; compilation happens once, not per order "+
					"(spec section 15.2)", ident.Name)
			}
		}
		return true
	})
}

// A decision carries the bundle that produced it, so an outcome can be reproduced
// from evidence rather than taken on trust.
func TestDecisionIsReproducibleFromItsRecord(t *testing.T) {
	b := testBundle(t)
	got := policy.Evaluate(b, policyEnvelope(), policyAt)

	if got.BundleID == "" || got.Version == 0 {
		t.Fatal("the decision does not name the bundle that produced it (INV-003)")
	}
	if got.DecidedBy == "" {
		t.Error("the decision does not name the rule that produced it")
	}

	// Recompiling the same source must produce the same bundle identity, which is
	// what makes replaying the decision meaningful.
	again := testBundle(t)
	h1, err := b.ComputeHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	h2, err := again.ComputeHash()
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if h1 != h2 {
		t.Error("the same policy source produced two different bundles; a recorded " +
			"decision could not be reproduced from it")
	}
}

// Evaluation takes no interface it could be handed an opinion through. If a future
// change adds one, this fails and names the invariant.
func TestEvaluateTakesNoPluggableInput(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "../../internal/policy/evaluate.go", nil, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "Evaluate" {
			return true
		}
		for _, param := range fn.Type.Params.List {
			if _, isInterface := param.Type.(*ast.InterfaceType); isInterface {
				t.Error("Evaluate accepts an interface parameter; that is a seam an " +
					"external decision input could arrive through (INV-003)")
			}
			if fnType, isFunc := param.Type.(*ast.FuncType); isFunc && fnType != nil {
				t.Error("Evaluate accepts a function parameter; policy must not be " +
					"extensible at decision time (INV-003)")
			}
		}
		return false
	})
}
