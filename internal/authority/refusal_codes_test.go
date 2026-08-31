package authority

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/money"
)

// Two refusals in this package that no test had ever produced.
//
// The ninth audit's coverage census found them: ENVELOPE_ABSENT, which is the fail-closed
// answer when there is nothing to evaluate, and RESERVATION_KEY_REUSED on the in-memory
// path, which is the same refusal the PostgreSQL store makes and the one every unit test
// of the pipeline relies on being there.

func TestEvaluatingNothingIsRefused(t *testing.T) {
	now := time.Now().UTC()
	grant := &Grant{
		GrantID: "grant_1", TenantID: "tenant_1", PrincipalID: "prin_1", AccountID: "acct_1",
		AgentID: "agent_1", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
		Status: StatusActive,
	}

	d := Evaluate(context.Background(), nil, grant, nil, now)
	if d.Allowed {
		t.Fatal("an absent envelope was authorized; there is nothing to authorize")
	}
	if d.Code != "ENVELOPE_ABSENT" {
		t.Errorf("refused with %s, expected ENVELOPE_ABSENT", d.Code)
	}
}

func TestTheMemoryLedgerRefusesAReusedKey(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	usage := NewMemoryUsage()
	grant := &Grant{
		GrantID: "grant_1", TenantID: "tenant_1", PrincipalID: "prin_1", AccountID: "acct_1",
		AgentID: "agent_1", ValidFrom: now.Add(-time.Hour), ValidUntil: now.Add(time.Hour),
		Status: StatusActive,
		Limits: Limits{PerOrderNotional: money.MustParse("10000"),
			Rolling1hNotional: money.MustParse("100000"), MaxOpenOrders: 50},
	}
	first := ReservationIdentity{EnvelopeID: "env_1", PrincipalID: "prin_1", AccountID: "acct_1"}

	if d, err := usage.Reserve(ctx, grant, "key_1", money.MustParse("1000"), first, now); err != nil || !d.Allowed {
		t.Fatalf("the first reservation was refused: %v %s", err, d.Code)
	}

	// The same key, a different envelope and a different amount: a second economic request
	// wearing the first one's key.
	second := ReservationIdentity{EnvelopeID: "env_2", PrincipalID: "prin_1", AccountID: "acct_1"}
	d, err := usage.Reserve(ctx, grant, "key_1", money.MustParse("9000"), second, now)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if d.Allowed {
		t.Fatal("a second request inherited the first one's reservation; the caller would " +
			"be told an amount nobody evaluated had been authorized (INV-002)")
	}
	if d.Code != "RESERVATION_KEY_REUSED" {
		t.Errorf("refused with %s, expected RESERVATION_KEY_REUSED", d.Code)
	}

	// And the identical request is still a retry, which is what makes one safe.
	if d, err := usage.Reserve(ctx, grant, "key_1", money.MustParse("1000"), first, now); err != nil || !d.Allowed {
		t.Errorf("a retry of the same request was refused: %v %s", err, d.Code)
	}
}
