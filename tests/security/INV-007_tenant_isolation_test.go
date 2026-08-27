package security

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
)

// INV-007: tenant A cannot observe tenant B data.
//
// This file covers the in-process half: authority evaluation refuses a grant from
// another tenant, and refuses it as a tenant failure rather than as some downstream
// mismatch. The database half lives in INV-007_tenant_isolation_db_test.go behind the
// integration build tag, because row level security cannot be proven without a real
// PostgreSQL enforcing it.

func grantFor(tenantID string) *authority.Grant {
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	return &authority.Grant{
		GrantID:             "grant_5521",
		TenantID:            tenantID,
		PrincipalID:         "principal_7781",
		AccountID:           "account_4410",
		AgentID:             "agent_momentum_03",
		IssuedAt:            at.Add(-24 * time.Hour),
		ValidFrom:           at.Add(-time.Hour),
		ValidUntil:          at.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		Limits:              authority.Limits{PerOrderNotional: 5000},
		Status:              authority.StatusActive,
	}
}

func envelopeFor(tenantID string) *intent.AgentExecutionEnvelope {
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)
	return &intent.AgentExecutionEnvelope{
		SchemaVersion:    intent.SchemaVersion,
		EnvelopeID:       "env_1",
		IdempotencyKey:   "idem_1",
		ReceivedAt:       at,
		TenantID:         tenantID,
		AuthorityGrantID: "grant_5521",
		Principal:        intent.Principal{PrincipalID: "principal_7781", AccountID: "account_4410"},
		Agent:            intent.Agent{AgentID: "agent_momentum_03"},
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

// A grant from another tenant authorizes nothing, even when every other field lines
// up exactly. The dangerous version of this bug is a grant that matches on agent,
// principal, account and limits, and differs only in tenant.
func TestGrantFromAnotherTenantAuthorizesNothing(t *testing.T) {
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	got := authority.Evaluate(context.Background(),
		envelopeFor("tenant_acme"), grantFor("tenant_globex"), nil, at)

	if got.Allowed {
		t.Fatal("a grant belonging to another tenant authorized an order (INV-007)")
	}
	if got.Code != "GRANT_WRONG_TENANT" {
		t.Errorf("expected GRANT_WRONG_TENANT, got %s; a cross-tenant read must be "+
			"reported as one, not as an ordinary mismatch", got.Code)
	}
}

// The tenant check runs before anything else, so a cross-tenant grant cannot be
// masked by an unrelated failure that happens to be checked first.
func TestTenantCheckPrecedesEveryOtherCheck(t *testing.T) {
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	g := grantFor("tenant_globex")
	g.Revoke(at.Add(-time.Hour), "unrelated")
	g.ValidUntil = at.Add(-time.Minute)
	g.AllowedOperations = nil

	got := authority.Evaluate(context.Background(), envelopeFor("tenant_acme"), g, nil, at)
	if got.Code != "GRANT_WRONG_TENANT" {
		t.Errorf("expected GRANT_WRONG_TENANT to win, got %s", got.Code)
	}
}

// A repository call with no tenant must fail loudly. Under row level security it
// would return zero rows, which reads like "not found" and hides the real bug.
func TestStoreRefusesAnEmptyTenant(t *testing.T) {
	store := authority.NewStore(nil)

	_, err := store.Load(context.Background(), "", "grant_5521")
	if err == nil {
		t.Fatal("a query with no tenant was attempted (INV-007)")
	}
	if err != authority.ErrTenantContextMissing {
		t.Errorf("expected ErrTenantContextMissing, got %v", err)
	}
}

// Not-found and belongs-to-another-tenant must be indistinguishable to a caller.
// Telling someone that a grant id exists in a tenant they cannot see is itself a
// cross-tenant disclosure.
func TestNotFoundDoesNotRevealOtherTenants(t *testing.T) {
	if authority.ErrGrantNotFound.Error() == "" {
		t.Fatal("precondition")
	}
	msg := authority.ErrGrantNotFound.Error()
	for _, leak := range []string{"tenant", "other", "exists", "forbidden", "denied"} {
		if contains(msg, leak) {
			t.Errorf("ErrGrantNotFound mentions %q; it must not hint that the row exists elsewhere", leak)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
