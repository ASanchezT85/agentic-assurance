package security

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// INV-009 at the plane, not at the type.
//
// The existing INV-009 test proves that a fleet Recommendation cannot be enforced
// without a customer Authorization. That is the right check on the right type, and it
// says nothing about what the intelligence *process* can reach.
//
// It said nothing while that process grew two POST endpoints. internal/fleet still
// carries the comment "Read-only, and that is structural: there is no handler here
// that writes anything", and it is still true of that file — the fleet engine binary
// now serves POST /v1/simulations and POST /v1/simulations/{id}/cancel. A true
// statement about one file, read as a statement about the plane, which is the exact
// shape of the bug that made INV-007 exploitable.
//
// So the guard is on imports, because that is what actually bounds a process. The
// intelligence plane may mutate a simulation's own record and nothing else: it cannot
// reach a policy bundle, an authority grant, an idempotency record or a venue, because
// it cannot import the packages that own them.

// enforcementPackages own the things that change what production does.
var enforcementPackages = map[string]string{
	"internal/policy":    "a policy bundle decides what production enforces",
	"internal/authority": "an authority grant decides what an agent may do",
	"internal/execution": "an idempotency record is authoritative control state",
	"internal/broker":    "a venue is where an order actually goes",
	"internal/gateway":   "the composition root is the enforcement path itself",
}

// intelligencePackages are the ones the fleet engine is built from.
var intelligencePackages = []string{"internal/fleet", "internal/simulation"}

func TestTheIntelligencePlaneCannotReachEnforcement(t *testing.T) {
	for _, pkg := range intelligencePackages {
		for _, file := range goFiles(t, "../../"+pkg) {
			for _, imported := range importsOf(t, file) {
				trimmed := strings.TrimPrefix(imported, "agentic-assurance/")
				if why, forbidden := enforcementPackages[trimmed]; forbidden {
					t.Errorf("%s imports %s. The intelligence plane recommends and never "+
						"enforces (INV-009), and %s. A simulation answers what would "+
						"happen; only an authorized customer control changes what does.",
						file, trimmed, why)
				}
			}
		}
	}
}

// The binary itself, because a package can stay clean while the process wires
// something the packages never mention.
func TestTheFleetEngineBinaryCannotReachEnforcement(t *testing.T) {
	for _, file := range goFiles(t, "../../cmd/fleet-engine") {
		for _, imported := range importsOf(t, file) {
			trimmed := strings.TrimPrefix(imported, "agentic-assurance/")
			if why, forbidden := enforcementPackages[trimmed]; forbidden {
				t.Errorf("cmd/fleet-engine imports %s. %s, and the intelligence plane "+
					"must not be able to (INV-009).", trimmed, why)
			}
		}
	}
}

// Every mutating route the intelligence plane serves, written down.
//
// The list is the point. A POST added to this plane has to be added here too, and a
// reviewer looking at the diff is asked the question the comment in internal/fleet
// stopped asking: does this change what production does, or only what we know about it?
func TestEveryMutatingIntelligenceRouteIsAccountedFor(t *testing.T) {
	permitted := map[string]string{
		"POST /v1/simulations":             "creates a simulation run record",
		"POST /v1/simulations/{id}/cancel": "stops a simulation this plane started",
	}

	found := map[string]bool{}
	for _, dir := range []string{"../../internal/fleet", "../../internal/simulation", "../../cmd/fleet-engine"} {
		for _, file := range goFiles(t, dir) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			for _, line := range strings.Split(string(raw), "\n") {
				for _, verb := range []string{"POST ", "PUT ", "PATCH ", "DELETE "} {
					marker := `"` + verb
					idx := strings.Index(line, marker)
					if idx < 0 || !strings.Contains(line, "HandleFunc") {
						continue
					}
					rest := line[idx+1:]
					route := rest[:strings.Index(rest, `"`)]
					found[route] = true
				}
			}
		}
	}

	for route := range found {
		if _, ok := permitted[route]; !ok {
			t.Errorf("the intelligence plane serves %q and this test does not list it. "+
				"Either it only changes what we know about production, in which case "+
				"add it with the reason, or it changes production, in which case it "+
				"does not belong on this plane (INV-009).", route)
		}
	}
	for route := range permitted {
		if !found[route] {
			t.Errorf("%q is listed as permitted and no longer exists; a list nobody "+
				"prunes stops being read", route)
		}
	}
}

func goFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	if len(files) == 0 {
		t.Fatalf("%s has no non-test Go files; the guard is looking in the wrong place", dir)
	}
	return files
}

func importsOf(t *testing.T, path string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var out []string
	for _, spec := range parsed.Imports {
		out = append(out, strings.Trim(spec.Path.Value, `"`))
	}
	return out
}
