package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The enforcement points a mutation sweep found undefended inside the gate.
//
// Twelve mutations were applied one at a time, each removing one thing the platform says
// it enforces, and the fast suites — unit, security, scenarios, contract — were run against
// each. Seven were caught. Five were not:
//
//	the policy activation signature check
//	the predecessor check on a policy transition        (F4-B002)
//	the tombstone check on an idempotency claim         (F4-B001)
//	the outbox enqueue on a recorded event              (A-5-01)
//	the issuer privilege on creating a grant            (P-002)
//
// The last of those now has a behavioural test beside this one. The other four are defended
// by the integration suite, which the gate deliberately does not run: it needs PostgreSQL,
// ClickHouse, NATS and SPIRE. So a change that removes any of them passes `make verify` and
// every check a contributor sees before pushing, and fails hours later somewhere else — or,
// as A-5-01 and A-6-01 both did, not at all until an audit went looking.
//
// This is the cheap half of the answer: a structural guard that the call is still there.
// It cannot say the check is correct — the integration tests do that — and it can say that
// somebody deleted it. Written down explicitly, for the same reason the INV-007 route guard
// keeps its map by hand: a guard that resolves names loosely is a guard that passes by
// seeing nothing.
func TestTheEnforcementPointsAreStillCalled(t *testing.T) {
	points := []struct {
		file      string
		within    string
		must      string
		guarantee string
		proved    string
	}{
		{
			file:      "internal/gateway/http.go",
			within:    "func (f *FileBundles) authorize(",
			must:      "authorization.Verify(key.PublicKey)",
			guarantee: "a bundle enforces only when the customer authorized the activation (INV-009)",
			proved:    "tests/integration/policy_activation_test.go",
		},
		{
			file:      "internal/policy/activation_store.go",
			within:    "func (s *ActivationStore) Accept(",
			must:      "checkPredecessor(a, hasCurrent, currentID, currentHash)",
			guarantee: "a transition names the policy it replaces, by id and by content (F4-B002)",
			proved:    "tests/integration/policy_transition_cas_test.go",
		},
		{
			file:      "internal/execution/pgstore.go",
			within:    "func (s *PostgresStore) Claim(",
			must:      "FROM idempotency_tombstones",
			guarantee: "a pruned request cannot reach a venue again (F4-B001, ADR-027)",
			proved:    "tests/integration/idempotency_permanence_test.go",
		},
		{
			file:      "internal/evidence/store.go",
			within:    "func (s *Store) Append(",
			must:      "enqueue(ctx, tx, []Event{e})",
			guarantee: "every recorded event is owed to the bus (A-5-01)",
			proved:    "tests/integration/administrative_events_reach_the_bus_test.go",
		},
	}

	for _, p := range points {
		t.Run(strings.TrimSuffix(filepath.Base(p.file), ".go"), func(t *testing.T) {
			body := functionBody(t, p.file, p.within)
			if !strings.Contains(body, p.must) {
				t.Errorf("%s no longer contains %q.\n\nIt is what enforces: %s.\n"+
					"The behaviour is proved in %s, which the gate does not run — so "+
					"without this line and without that suite, nothing here would have "+
					"noticed.", p.within, p.must, p.guarantee, p.proved)
			}
		})
	}
}

// functionBody returns the source of one function, from its signature to the next
// top-level closing brace.
func functionBody(t *testing.T, file, signature string) string {
	t.Helper()

	// Relative to this package, as the other structural guards here read the source.
	raw, err := os.ReadFile(filepath.Join("..", "..", file))
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	source := string(raw)
	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("%s does not contain %q; this guard is out of date, which is worse "+
			"than it failing", file, signature)
	}
	rest := source[start:]
	if end := strings.Index(rest, "\n}\n"); end > 0 {
		return rest[:end]
	}
	return rest
}
