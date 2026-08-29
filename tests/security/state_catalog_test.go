package security

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"agentic-assurance/internal/broker"
)

// Every canonical execution state has an explicit meaning everywhere it matters.
//
// The EXPIRED defect existed because three lists were maintained separately: the broker
// state catalog, the settlement rule for what counts as terminal, and the mapping from
// outcome to evidence event. Two of them ended in a default branch, so a state nobody
// had enumerated became "open forever" in the usage ledger and "accepted" in the record.
//
// This guard is over the catalog rather than over any one switch: a new broker state
// fails until somebody decides what it means.

// stateMeaning is the decision table. A state added to internal/broker with no entry
// here fails, which is the point.
var stateMeaning = map[broker.ExecutionState]struct {
	terminal      bool
	closesOpen    bool
	evidenceEvent string
	reservation   string
}{
	broker.StateUnknown: {
		terminal: false, closesOpen: false,
		evidenceEvent: "broker.order.unknown.v1",
		reservation:   "held until reconciliation resolves it",
	},
	broker.StateAccepted: {
		terminal: false, closesOpen: false,
		evidenceEvent: "broker.order.accepted.v1",
		reservation:   "committed; exposure is standing at the venue",
	},
	broker.StatePartiallyFilled: {
		terminal: false, closesOpen: false,
		evidenceEvent: "broker.order.accepted.v1",
		reservation:   "committed; the rest is still working",
	},
	broker.StateFilled: {
		terminal: true, closesOpen: true,
		evidenceEvent: "broker.order.filled.v1",
		reservation:   "committed; notional spent, open-order count released",
	},
	broker.StateRejected: {
		terminal: true, closesOpen: true,
		evidenceEvent: "broker.order.rejected.v1",
		reservation:   "released; the order does not exist",
	},
	broker.StateCancelled: {
		terminal: true, closesOpen: true,
		evidenceEvent: "broker.order.cancelled.v1",
		reservation:   "committed; notional spent, open-order count released",
	},
	broker.StateExpired: {
		terminal: true, closesOpen: true,
		evidenceEvent: "broker.order.expired.v1",
		reservation:   "committed; notional spent, open-order count released",
	},
}

// canonicalStates reads the states from the source rather than from a list here, so a
// new one cannot be added without this test seeing it.
func canonicalStates(t *testing.T) []broker.ExecutionState {
	t.Helper()

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, repoRoot+"/internal/broker", nil, 0)
	if err != nil {
		t.Fatalf("parse internal/broker: %v", err)
	}

	var states []broker.ExecutionState
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				spec, ok := n.(*ast.ValueSpec)
				if !ok {
					return true
				}
				ident, ok := spec.Type.(*ast.Ident)
				if !ok || ident.Name != "ExecutionState" {
					return true
				}
				for _, value := range spec.Values {
					lit, ok := value.(*ast.BasicLit)
					if !ok {
						continue
					}
					states = append(states, broker.ExecutionState(strings.Trim(lit.Value, `"`)))
				}
				return true
			})
		}
	}

	if len(states) < 5 {
		t.Fatalf("found %d execution states; the walk is not finding the catalog", len(states))
	}
	return states
}

func TestEveryExecutionStateIsMapped(t *testing.T) {
	for _, state := range canonicalStates(t) {
		meaning, decided := stateMeaning[state]
		if !decided {
			t.Errorf("%s is a canonical execution state with no decided meaning. A new "+
				"state must say whether it is terminal, whether it closes the open-order "+
				"count, which evidence event it produces and what happens to the "+
				"reservation — a default branch answering \"accepted\" is how an expired "+
				"order was recorded as one the venue took.", state)
			continue
		}
		if meaning.terminal != state.Terminal() {
			t.Errorf("%s: this table says terminal=%v and broker.Terminal() says %v",
				state, meaning.terminal, state.Terminal())
		}
		if meaning.evidenceEvent == "" || meaning.reservation == "" {
			t.Errorf("%s has an incomplete meaning", state)
		}
	}
}
