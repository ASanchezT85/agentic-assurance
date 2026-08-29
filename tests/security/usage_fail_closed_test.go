package security

import (
	"context"
	"errors"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/money"
)

// T3-R003: authority that cannot read consumed usage denies.
//
// parseAmount returned zero when a stored amount would not parse, and zero means
// "nothing consumed": malformed authoritative state read as a grant with its full
// capacity available. The column is numeric(20,4) and everything written to it comes
// from Amount.String(), so the condition is unlikely — and "unlikely" is not a property
// a ceiling can be built on. A limit may fail in one direction only.

// unreadableUsage is the repository boundary failing. Direct corruption cannot normally
// be expressed through a numeric column, so the failure is injected where a read error
// would surface.
type unreadableUsage struct{}

func (unreadableUsage) Usage(context.Context, string, string, time.Time) (authority.Snapshot, error) {
	return authority.Snapshot{}, errors.New(`consumed usage "1e5" is not a readable amount`)
}

func TestUnreadableUsageDenies(t *testing.T) {
	g := grantFor("tenant_acme")
	g.Limits = authority.Limits{
		PerOrderNotional:  money.MustParse("50000"),
		Rolling1hNotional: money.MustParse("10000"),
		DailyNotional:     money.MustParse("20000"),
	}

	decision := authority.Evaluate(context.Background(), envelopeFor("tenant_acme"), g,
		unreadableUsage{}, evalAt)

	if decision.Allowed {
		t.Fatal("an order was authorized while consumed usage could not be read. " +
			"Unreadable usage is not usage of zero: the grant's whole remaining " +
			"capacity was offered on the strength of a value nobody could parse (INV-002).")
	}
	if decision.Code != "USAGE_UNAVAILABLE" {
		t.Errorf("denied with %s; an operator needs to know the ledger could not be read "+
			"rather than that a limit was reached", decision.Code)
	}
}
