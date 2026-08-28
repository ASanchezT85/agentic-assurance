package security

import (
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
	"internal/control":   "an authorized fleet control refuses orders on the hot path",
	"internal/broker":    "a venue is where an order actually goes",
	"internal/gateway":   "the composition root is the enforcement path itself",
}

// The intelligence plane is whatever the fleet engine can reach.
//
// Discovered rather than listed. The first version named internal/fleet and
// internal/simulation, and a new package in that binary was invisible to it — the
// same enumerate-your-own-coverage bug these guards exist to catch, reproduced inside
// the guard. Verified by adding a package that reads authority grants and wiring it
// in: the listed version passed.
//
// Transitive, because a process is bounded by everything it can reach and not by what
// its own files happen to name.
func TestTheIntelligencePlaneCannotReachEnforcement(t *testing.T) {
	reachable := dependencyClosure(t, "agentic-assurance/cmd/fleet-engine")

	if len(reachable) < 3 {
		t.Fatalf("the fleet engine reaches only %d local packages; the walk is not "+
			"finding its imports and this guard would pass over nothing", len(reachable))
	}

	for pkg := range reachable {
		trimmed := strings.TrimPrefix(pkg, "agentic-assurance/")
		if why, forbidden := enforcementPackages[trimmed]; forbidden {
			t.Errorf("the fleet engine reaches %s. The intelligence plane recommends "+
				"and never enforces (INV-009), and %s. A simulation answers what would "+
				"happen; only an authorized customer control changes what does.",
				trimmed, why)
		}
	}
}

// The reverse, so a hard decision never depends on the analytical plane being up.
//
// The gateway reaches internal/fleet on purpose: it writes telemetry. What must not
// happen is a policy or authority decision that cannot be made without analytics
// (INV-005).
func TestEnforcementDoesNotDependOnAnalytics(t *testing.T) {
	for _, decider := range []string{
		"agentic-assurance/internal/policy",
		"agentic-assurance/internal/authority",
		"agentic-assurance/internal/identity",
	} {
		for pkg := range dependencyClosure(t, decider) {
			trimmed := strings.TrimPrefix(pkg, "agentic-assurance/")
			if trimmed == "internal/fleet" || trimmed == "internal/simulation" {
				t.Errorf("%s reaches %s; a decision would depend on the analytical "+
					"plane being reachable (INV-005)",
					strings.TrimPrefix(decider, "agentic-assurance/"), trimmed)
			}
		}
	}
}

// The binary itself, because a package can stay clean while the process wires
// something the packages never mention.
func TestTheFleetEngineBinaryCannotReachEnforcement(t *testing.T) {
	for _, file := range goSources(t) {
		if packageOf(file) != "agentic-assurance/cmd/fleet-engine" {
			continue
		}
		for _, imported := range localImports(t, file) {
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

	// Every file the fleet engine can reach, discovered. A mutating route added in a
	// package this guard never heard of is the case the listed version missed.
	reachable := dependencyClosure(t, "agentic-assurance/cmd/fleet-engine")
	reachable["agentic-assurance/cmd/fleet-engine"] = true

	found := map[string]bool{}
	for _, file := range goSources(t) {
		if !reachable[packageOf(file)] {
			continue
		}
		for _, line := range strings.Split(readSource(t, file), "\n") {
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
