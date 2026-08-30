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

// Nor for anything else that writes an event where a human reads text.
//
// The import guard below is the obvious half, and it is partly the compiler's anyway: an
// unused import does not build. A mutation sweep put `println("audit:", e.EventID)` inside
// Store.Append — the decision written to stderr, no import required — and every suite
// stayed green. Printing a decision instead of recording it is exactly the failure §51
// describes, and it does not need a logger to happen.
func TestEvidencePackageDoesNotPrint(t *testing.T) {
	banned := []string{"println(", "print(", "fmt.Print", "os.Stdout", "os.Stderr"}

	entries, err := os.ReadDir("../../internal/evidence")
	if err != nil {
		t.Fatalf("read the package: %v", err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := os.ReadFile("../../internal/evidence/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		checked++
		for _, line := range strings.Split(string(raw), "\n") {
			code, _, _ := strings.Cut(line, "//")
			for _, b := range banned {
				if strings.Contains(code, b) {
					t.Errorf("internal/evidence/%s writes to a stream: %q\n\n"+
						"Evidence is recorded, never printed. A decision on stdout is "+
						"sampled, rotated, dropped under pressure and unqueryable — "+
						"which is the difference INV-013 exists to keep.",
						name, strings.TrimSpace(line))
				}
			}
		}
	}
	if checked < 3 {
		t.Fatalf("only %d files were checked; the guard is looking in the wrong place", checked)
	}
}

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
//
// The payload literals themselves, not the files that contain them.
//
// Two earlier versions of this guard were wrong in opposite directions. The first read
// internal/evidence/event.go alone: that file declares the event and does not decide
// what goes in Payload, so a token written into a payload from the pipeline went
// straight past it, verified by writing one. The second read whole files, and flagged
// the ClickHouse client's connection password and then every mention of
// identity.Credentials — correct code, punished, which teaches authors to route around
// guards.
//
// A substring search over files was always going to collide once "credential" became a
// legitimate concept in this codebase. So the guard reads the map literal assigned to
// Payload and nothing else: its keys, and any string constants in it.
func TestEvidenceCarriesNoCredentialFields(t *testing.T) {
	banned := []string{"secretkey", "secret_key", "apikey", "api_key", "password",
		"bearer", "access_token", "private_key", "authorization"}

	payloads := 0

	for _, path := range goSources(t) {
		source := readSource(t, path)
		if !strings.Contains(source, "evidence.Event{") {
			continue
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, source, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok || !isEvidenceEvent(lit.Type) {
				return true
			}
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Payload" {
					continue
				}
				payloads++
				for _, word := range stringsIn(kv.Value) {
					lowered := strings.ToLower(word)
					for _, b := range banned {
						if strings.Contains(lowered, b) {
							t.Errorf("%s: an evidence payload carries %q, which mentions "+
								"%q. Broker secrets are never logged, never returned "+
								"through an API and never in evidence (spec section 35).",
								path, word, b)
						}
					}
				}
			}
			return true
		})
	}

	if payloads == 0 {
		t.Fatal("no evidence payload literal was found; the guard is inspecting nothing " +
			"and would stay green whatever a payload carried")
	}
}

func isEvidenceEvent(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "evidence" && sel.Sel.Name == "Event"
}

// stringsIn returns every string literal in an expression: a payload's keys are
// strings, and so is anything constant it carries.
func stringsIn(expr ast.Expr) []string {
	var out []string
	ast.Inspect(expr, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			out = append(out, strings.Trim(lit.Value, `"`))
		}
		if ident, ok := n.(*ast.Ident); ok {
			out = append(out, ident.Name)
		}
		return true
	})
	return out
}
