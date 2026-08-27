package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// INV-013: audit logs and application logs are not interchangeable.
//
// They differ in every way that matters. Operational logs are for a human debugging
// a process: they are sampled, rotated, dropped under pressure, and nobody minds.
// Evidence is the account of a financial decision: it is complete, ordered,
// attributable, append-only and queryable. Spec section 51 says it outright, and the
// failure mode is a system that writes a decision to stdout and calls it an audit
// trail.

// The evidence package must not reach for a logger. If recording evidence could be
// satisfied by logging, someone eventually would.
func TestEvidencePackageDoesNotLog(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "../../internal/evidence", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	banned := []string{"log", "log/slog"}
	for _, pkg := range pkgs {
		for path, file := range pkg.Files {
			for _, imp := range file.Imports {
				name := strings.Trim(imp.Path.Value, `"`)
				for _, b := range banned {
					if name == b {
						t.Errorf("%s imports %q; evidence is recorded, not logged (INV-013)", path, name)
					}
				}
			}
		}
	}
}

// The reverse: nothing may record evidence by writing to a log. A logger call that
// takes an evidence.Event is the seam this invariant exists to close.
func TestNoPackageLogsAnEvidenceEvent(t *testing.T) {
	dirs := []string{
		"../../internal/evidence", "../../internal/execution", "../../internal/policy",
		"../../internal/authority", "../../internal/identity", "../../internal/intent",
		"../../cmd/assurance-gateway", "../../cmd/fleet-engine",
	}

	for _, dir := range dirs {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}

		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				ast.Inspect(file, func(n ast.Node) bool {
					call, ok := n.(*ast.CallExpr)
					if !ok {
						return true
					}
					sel, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch sel.Sel.Name {
					case "Info", "Warn", "Error", "Debug", "Print", "Printf", "Println":
					default:
						return true
					}
					for _, arg := range call.Args {
						if mentionsEvidenceEvent(arg) {
							t.Errorf("%s logs an evidence event; the audit trail is the "+
								"store, not stdout (INV-013)", path)
						}
					}
					return true
				})
			}
		}
	}
}

func mentionsEvidenceEvent(node ast.Node) bool {
	found := false
	ast.Inspect(node, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if ok && ident.Name == "evidence" && sel.Sel.Name == "Event" {
			found = true
		}
		return true
	})
	return found
}

// The two have different retention rules, and the operations documentation has to
// say so, because the person deciding what to rotate reads that and not this test.
func TestOperationsDocumentsTheDistinction(t *testing.T) {
	raw, err := os.ReadFile("../../docs/operations/README.md")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := strings.ToLower(string(raw))

	for _, phrase := range []string{"evidence", "operational log"} {
		if !strings.Contains(body, phrase) {
			t.Errorf("docs/operations/README.md does not mention %q; the distinction "+
				"between evidence and logs has to be written where operators read (INV-013)", phrase)
		}
	}
}

// Secrets never enter evidence. Spec section 35 forbids them in telemetry payloads,
// and evidence is the most durable payload in the system.
func TestEvidenceCarriesNoCredentialFields(t *testing.T) {
	raw, err := os.ReadFile("../../internal/evidence/event.go")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	body := strings.ToLower(string(raw))

	for _, banned := range []string{"secretkey", "apikey", "password", "credential", "bearer"} {
		if strings.Contains(body, banned) {
			t.Errorf("the evidence event type mentions %q; secrets must never be embedded "+
				"in evidence (spec section 35)", banned)
		}
	}
}
