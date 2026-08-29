package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// T3-MONEY-06: financial values on the decision path are exact types, structurally.
//
// The second remediation made authority arithmetic exact and the wire boundary stayed
// float64, so the amount an agent signed was converted to binary64 and back before
// anything decided about it. 900000000000.0002 and 900000000000.0003 collapse into one
// float; an agent signing the second had the first authorized.
//
// A test over behaviour cannot catch the reintroduction, because a new float field is
// harmless until the day a value needs the precision. This reads the source.
//
// The analytical plane is exempt by design: a risk score off in the twelfth decimal is a
// risk score, and internal/fleet converts at its own boundary.

// decisionPackages are the packages where a financial value is decided upon rather than
// reported.
var decisionPackages = []string{
	"internal/intent",
	"internal/authority",
	"internal/policy",
	"internal/execution",
	"internal/broker",
}

// financialNames are the fields whose type carries an economic value the platform
// decides about.
//
// The distinction is instruction versus observation. broker.Fill.Quantity and
// broker.Position.Quantity are a venue's report of what happened: nothing is authorized
// against them, they arrive as JSON numbers from someone else's system, and forcing them
// into an exact type would claim a precision the venue never promised. What must be
// exact is what the platform authorizes and submits.
var financialNames = map[string]bool{
	"Notional": true, "Quantity": true, "LimitPrice": true, "StopPrice": true,
	"PerOrderNotional": true, "Rolling1hNotional": true, "DailyNotional": true,
	"GrossNotional": true, "NetNotional": true,
	"WhenNotionalGT": true, "WhenNotionalGTE": true, "WhenNotionalLT": true,
	"WhenNotionalLTE": true, "RequireNotionalLTE": true, "RequireNotionalGTE": true,
	"NotionalGT": true, "NotionalGTE": true, "NotionalLT": true, "NotionalLTE": true,
}

// reportedByVenue are the records a broker fills in. See financialNames.
var reportedByVenue = map[string]bool{
	"Fill": true, "BrokerOrder": true, "Position": true, "Account": true,
	"Outcome": true,
}

func TestNoFinancialFloatsOnTheDecisionPath(t *testing.T) {
	for _, pkg := range decisionPackages {
		dir := filepath.Join(repoRoot, filepath.FromSlash(pkg))
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
			// Tests may still start from a float literal for readability; they convert
			// through money before anything sees it, and a test fixture is not the
			// executable path.
			return !strings.HasSuffix(info.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", pkg, err)
		}

		for _, parsed := range pkgs {
			for path, file := range parsed.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					spec, ok := n.(*ast.TypeSpec)
					if !ok {
						return true
					}
					if reportedByVenue[spec.Name.Name] {
						// A venue's report of what happened, not an instruction.
						return false
					}
					structType, ok := spec.Type.(*ast.StructType)
					if !ok {
						return true
					}
					for _, field := range structType.Fields.List {
						if !isFloat(field.Type) {
							continue
						}
						for _, name := range field.Names {
							if !financialNames[name.Name] {
								continue
							}
							t.Errorf("%s: %s.%s is a float64. A financial value the "+
								"platform decides about must be money.Amount or "+
								"money.Quantity: the wire literal is what an agent "+
								"signed, and binary64 cannot represent every amount the "+
								"platform supports.",
								filepath.Base(path), spec.Name.Name, name.Name)
						}
					}
					return true
				})
			}
		}
	}
}

// money.FromFloat converts faithfully from a value that may already be wrong. It has a
// place at the analytical edges and none on the path that authorizes an order.
func TestFromFloatIsNotOnTheDecisionPath(t *testing.T) {
	for _, pkg := range decisionPackages {
		dir := filepath.Join(repoRoot, filepath.FromSlash(pkg))
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(info fs.FileInfo) bool {
			return !strings.HasSuffix(info.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", pkg, err)
		}

		for _, parsed := range pkgs {
			for path, file := range parsed.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					ident, ok := sel.X.(*ast.Ident)
					if !ok || ident.Name != "money" {
						return true
					}
					if sel.Sel.Name == "FromFloat" || sel.Sel.Name == "QuantityFromFloat" {
						t.Errorf("%s calls money.%s. Converting from a float that has "+
							"already lost information produces an exact record of the "+
							"wrong number; parse the decimal literal instead.",
							filepath.Base(path), sel.Sel.Name)
					}
					return true
				})
			}
		}
	}
}

func isFloat(expr ast.Expr) bool {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name == "float64" || t.Name == "float32"
	case *ast.StarExpr:
		return isFloat(t.X)
	case *ast.ArrayType:
		return isFloat(t.Elt)
	}
	return false
}
