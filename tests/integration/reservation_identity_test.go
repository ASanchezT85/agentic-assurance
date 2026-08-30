//go:build integration

package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/money"
)

// B-003: can a reservation be inherited by a different intent?
//
// The ledger is keyed by (tenant, idempotency_key) and a repeated key returned ALLOW
// without checking what it was reserved for. These are the audit's exploit paths,
// written to fail before the fix rather than after.

func reservationGrant(tenant, id string, rolling money.Amount, at time.Time) *authority.Grant {
	return &authority.Grant{
		GrantID: id, TenantID: tenant,
		PrincipalID: "prin_res", AccountID: "acct_res", AgentID: "agent_res",
		IssuedAt: at.Add(-time.Hour), ValidFrom: at.Add(-time.Hour), ValidUntil: at.Add(time.Hour),
		Limits: authority.Limits{PerOrderNotional: money.MustParse("100000"), Rolling1hNotional: rolling},
		Status: authority.StatusActive,
	}
}

// T-B003-A: a reservation left behind by a failure that never reached a venue must not
// authorize a different, larger intent later.
func TestAnOrphanReservationDoesNotAuthorizeADifferentIntent(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_orphan_%d", at.UnixNano())
	grant := reservationGrant(tenant, "grant_orphan", money.MustParse("10000"), at)
	key := "K"

	// Intent A reserves 1,000 and never reaches a venue.
	first, err := usage.Reserve(ctx, grant, key, money.MustParse("1000"), authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve A: %v", err)
	}
	if !first.Allowed {
		t.Fatalf("the first reservation was refused: %s", first.Code)
	}

	// Intent B reuses the key with another envelope and eight times the size.
	second, err := usage.Reserve(ctx, grant, key, money.MustParse("8000"), authority.ReservationIdentity{
		EnvelopeID: "env_B", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve B: %v", err)
	}
	if second.Allowed {
		t.Errorf("a reservation of 1,000 for env_A authorized 8,000 for env_B: the "+
			"ledger describes an intent that never executed and the one that did is "+
			"invisible to it (INV-002). code=%s", second.Code)
	}
	if second.Code != "RESERVATION_KEY_REUSED" {
		t.Errorf("code = %s, want RESERVATION_KEY_REUSED", second.Code)
	}
}

// T-B003-B: the same key under a different grant must never inherit the first grant's
// capacity as the second grant's authority.
func TestAReservationIsNotInheritedAcrossGrants(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_cross_%d", at.UnixNano())
	key := "K"

	grantA := reservationGrant(tenant, "grant_A", money.MustParse("10000"), at)
	grantB := reservationGrant(tenant, "grant_B", money.MustParse("10000"), at)

	if d, err := usage.Reserve(ctx, grantA, key, money.MustParse("1000"), authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at); err != nil || !d.Allowed {
		t.Fatalf("reserve under A: %v %s", err, d.Code)
	}

	second, err := usage.Reserve(ctx, grantB, key, money.MustParse("1000"), authority.ReservationIdentity{
		EnvelopeID: "env_A", PrincipalID: "prin_res", AccountID: "acct_res",
	}, at)
	if err != nil {
		t.Fatalf("reserve under B: %v", err)
	}
	if second.Allowed {
		t.Error("a reservation held against grant A was accepted as authority under grant B")
	}
}

// T-B003-D: an idempotency record pruned by retention must not leave a usage row that
// makes a fresh request invisible to rolling accounting.
func TestAStaleUsageRowDoesNotHideFreshUsage(t *testing.T) {
	usage := authority.NewPostgresUsage(idemPool(t))
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-90 * 24 * time.Hour)
	tenant := fmt.Sprintf("tenant_stale_%d", now.UnixNano())
	grant := reservationGrant(tenant, "grant_stale", money.MustParse("10000"), now)
	key := "K"

	// Months ago, under this key.
	if d, err := usage.Reserve(ctx, grant, key, money.MustParse("1000"), authority.ReservationIdentity{
		EnvelopeID: "env_old", PrincipalID: "prin_res", AccountID: "acct_res",
	}, old); err != nil || !d.Allowed {
		t.Fatalf("historical reserve: %v %s", err, d.Code)
	}

	// The same key today, a different envelope. It must be evaluated as what it is: a
	// new request whose notional counts against the current window.
	fresh, err := usage.Reserve(ctx, grant, key, money.MustParse("9500"), authority.ReservationIdentity{
		EnvelopeID: "env_new", PrincipalID: "prin_res", AccountID: "acct_res",
	}, now)
	if err != nil {
		t.Fatalf("fresh reserve: %v", err)
	}
	if fresh.Allowed {
		t.Error("a months-old usage row authorized a fresh 9,500 without counting it; " +
			"the new action is invisible to rolling and daily accounting")
	}
}

// T-B003-C: two concurrent requests carrying the same envelope under different keys.
//
// Both may reserve before the idempotency claim establishes one-envelope-per-intent.
// One wins; the loser's reservation must not survive as an orphan key with no
// idempotency record, because that is the row the paths above reuse.
func TestTheLosingSideOfAnEnvelopeRaceHoldsNoCapacity(t *testing.T) {
	now := time.Date(2026, 8, 29, 16, 0, 0, 0, time.UTC)
	rig := newE2ERig(t, now)

	type result struct {
		status int
		body   map[string]any
	}
	results := make([]result, 2)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("race-%d-%d", now.UnixNano(), i)
			body := rig.envelope(now, key, func(m map[string]any) {
				// One envelope id, two keys. Section 12.2: one envelope is one intent.
				m["envelope_id"] = fmt.Sprintf("env_race_%d", now.UnixNano())
			})
			status, decoded := rig.post(t, body, true)
			results[i] = result{status: status, body: decoded}
		}(i)
	}
	wg.Wait()

	accepted := 0
	for _, r := range results {
		if r.status == 200 || r.status == 202 {
			accepted++
		}
	}
	if accepted > 1 {
		t.Errorf("%d of 2 requests with one envelope id executed; section 12.2 says one "+
			"envelope is one intent", accepted)
	}

	// The loser must hold nothing. A reservation with no idempotency record behind it
	// is the orphan the identity check exists to stop being inherited — and it should
	// not exist in the first place.
	var reserved int
	if err := rig.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM authority_usage
		 WHERE tenant_id = $1 AND state = 'RESERVED'`, rig.tenant).Scan(&reserved); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if reserved > accepted {
		t.Errorf("%d reservations are still held for %d accepted orders; the losing side "+
			"of the race left capacity reserved for an order that does not exist", reserved, accepted)
	}
}

// ADR-027: after retention prunes the record, the key is refused rather than reused.
//
// The two lifecycles disagreed. internal/execution/retention.go said pruning reopened the
// key and a later caller got a fresh execution; authority_usage keeps its row forever and
// refused. The platform stated both, and which one a caller met depended on which layer
// answered first — a contract nobody can rely on.
//
// This drives the resolved contract end to end: the record is claimed, resolved, pruned,
// and the key presented again.
func TestAPrunedRecordDoesNotReopenItsKey(t *testing.T) {
	ctx := context.Background()
	pool := idemPool(t)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_reopen_%d", now.UnixNano())
	key := fmt.Sprintf("reopen-%d", now.UnixNano())

	grant := reservationGrant(tenant, "grant_reopen", money.MustParse("10000"), now)
	usage := authority.NewPostgresUsage(pool)
	store := execution.NewPostgresStore(pool)

	// The original request: capacity reserved and an execution record claimed.
	who := authority.ReservationIdentity{
		EnvelopeID: "env_original", PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
	}
	if d, err := usage.Reserve(ctx, grant, key, money.MustParse("1000"), who, now); err != nil || !d.Allowed {
		t.Fatalf("reserve: %v %s", err, d.Code)
	}
	if _, claimed, err := store.Claim(ctx, execution.Record{
		TenantID: tenant, IdempotencyKey: key, EnvelopeID: "env_original",
		ClientOrderID: "coid_" + key, State: execution.RecordPending,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil || !claimed {
		t.Fatalf("claim: claimed=%v err=%v", claimed, err)
	}
	if err := store.Resolve(ctx, tenant, key, execution.Outcome{
		State: broker.StateFilled, BrokerOrderID: "b_" + key,
	}, now); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// Retention prunes it. The record is gone; the platform's memory of the key is not.
	if _, err := pool.Exec(ctx, `SELECT set_config('app.tenant_id', $1, false)`, tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`DELETE FROM idempotency_records WHERE tenant_id = $1 AND idempotency_key = $2`,
		tenant, key); err != nil {
		t.Fatalf("prune: %v", err)
	}
	if _, err := store.Load(ctx, tenant, key); err == nil {
		t.Fatal("the record survived the prune; this test is not exercising what it claims")
	}

	// The same key, later, for a different economic request.
	later := authority.ReservationIdentity{
		EnvelopeID: "env_later", PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
	}
	decision, err := usage.Reserve(ctx, grant, key, money.MustParse("9500"), later,
		now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("reserve after prune: %v", err)
	}
	if decision.Allowed {
		t.Errorf("a pruned record reopened its key: 9,500 was authorized under a key " +
			"the platform had already spent. Retention bounds storage, not what the " +
			"platform remembers about a key (ADR-027).")
	}
	if decision.Code != "RESERVATION_KEY_REUSED" {
		t.Errorf("refused with %s; the reason should name the reuse so a caller knows to "+
			"send a new key rather than retry", decision.Code)
	}
}

// A-6-01: capacity reserved for an order that was never sent comes back.
//
// PostgresUsage.Release is a DELETE, deliberately — a released row is capacity returned
// and removing it leaves the key genuinely free for a later, properly evaluated request.
// assurance_app was never granted DELETE on authority_usage, so every release since the
// table existed has failed with "permission denied" and the capacity stayed held.
//
// The platform had been saying so all along. The pipeline records the failure as evidence
// when Release returns an error, and the evidence store held 56 of them and not one
// successful release. Nobody read it, through five audits, because nothing asked the
// platform what it had already written down about itself.
func TestCapacityIsReturnedWhenNothingWasSent(t *testing.T) {
	ctx := context.Background()
	pool := usagePool(t)
	now := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_release_%d", now.UnixNano())
	key := fmt.Sprintf("release-%d", now.UnixNano())

	grant := reservationGrant(tenant, "grant_release", money.MustParse("10000"), now)
	usage := authority.NewPostgresUsage(pool)
	who := authority.ReservationIdentity{
		EnvelopeID: "env_release", PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
	}

	if d, err := usage.Reserve(ctx, grant, key, money.MustParse("4000"), who, now); err != nil || !d.Allowed {
		t.Fatalf("reserve: %v %s", err, d.Code)
	}

	// Nothing was sent, so the capacity is returned.
	if err := usage.Release(ctx, tenant, key, now); err != nil {
		t.Fatalf("release: %v\n\nCapacity reserved for an order that never reached a "+
			"venue stays consumed for ever. A customer's rolling window fills with "+
			"orders that do not exist, and legitimate ones are refused against a limit "+
			"nobody spent.", err)
	}

	// And the window shows it: a second 4,000 order plus a 6,000 one fit in 10,000 only
	// if the first reservation really went back.
	second := authority.ReservationIdentity{
		EnvelopeID: "env_release_2", PrincipalID: grant.PrincipalID, AccountID: grant.AccountID,
	}
	d, err := usage.Reserve(ctx, grant, key+"-2", money.MustParse("9000"), second, now)
	if err != nil {
		t.Fatalf("reserve after release: %v", err)
	}
	if !d.Allowed {
		t.Errorf("9,000 was refused (%s) against a 10,000 window whose only other "+
			"reservation was released", d.Code)
	}
}
