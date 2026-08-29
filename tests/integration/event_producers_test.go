//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/gateway"
)

// An event type with no producer is a promise nobody keeps.
//
// policy.bundle.activated.v1 was in the catalog, in the schema, in the documentation and
// in nothing that ran: enforcement changed and the customer's evidence said nothing.
// That is not a bug in a producer, it is the absence of one, and no test of any existing
// producer could have found it.
//
// So this drives the platform and collects what actually appears. It is deliberately not
// a list of source files: a producer that exists and is never reached is the defect being
// guarded against, and grepping for the constant would find it and call it covered.

func TestMandatoryEventsHaveProducersThatRun(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	rig := newE2ERig(t, now)

	produced := map[evidence.EventName]bool{}
	collect := func(correlation string) {
		chain, err := rig.evidence.Chain(ctx, rig.tenant, correlation)
		if err != nil {
			t.Fatalf("read %s: %v", correlation, err)
		}
		for _, e := range chain {
			produced[e.EventName] = true
		}
	}

	// 1. A clean submission. Reservation, decision, attempt and outcome.
	key := fmt.Sprintf("prod-ok-%d", now.UnixNano())
	body := rig.envelope(now, key, func(m map[string]any) {
		m["envelope_id"] = "env_" + key
		m["correlation_id"] = "corr_" + key
		intent := m["intent"].(map[string]any)
		delete(intent, "quantity")
		delete(intent, "limit_price")
		intent["order_type"] = "MARKET"
		intent["notional"] = 1000.0
	})
	if status, decoded := rig.post(t, body, true); status != 200 && status != 202 {
		t.Fatalf("the clean submission was refused with %d: %v", status, decoded)
	}
	collect("corr_" + key)

	// 2. An order the venue lets expire. The state that was being recorded as accepted.
	expiring := fmt.Sprintf("prod-exp-%d", now.UnixNano())
	rig.broker.InjectFault("coid-"+expiring, fakebroker.FaultExpire)
	expiringBody := rig.envelope(now, expiring, func(m map[string]any) {
		m["envelope_id"] = "env_" + expiring
		m["correlation_id"] = "corr_" + expiring
		intent := m["intent"].(map[string]any)
		delete(intent, "quantity")
		delete(intent, "limit_price")
		intent["order_type"] = "MARKET"
		intent["notional"] = 1000.0
	})
	rig.post(t, expiringBody, true)
	collect("corr_" + expiring)

	// 3. A policy activation and a rollback, through the provider the gateway uses.
	dir := t.TempDir()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	policyTenant := rig.tenant
	authority := newReloadAuthority(t, policyTenant)
	writeBundle(t, dir, policyTenant, "bundle_prod_a", priv, now, allowOrdinaryOrders, authority)

	bundles, err := gateway.NewFileBundles(dir, hex.EncodeToString(pub))
	if err != nil {
		t.Fatalf("bundles: %v", err)
	}
	bundles.Activations = authority.store
	// A refused activation leaves the previous policy in force and returns no error, so
	// without this a staging mistake in the fixture would look like a missing producer.
	bundles.Report = func(tenant string, err error) { t.Fatalf("activation refused: %v", err) }
	if _, err := bundles.Active(ctx, policyTenant); err != nil {
		t.Fatalf("activate: %v", err)
	}
	writeBundle(t, dir, policyTenant, "bundle_prod_b", priv, now, denyEverything, authority)
	if _, err := bundles.Active(ctx, policyTenant); err != nil {
		t.Fatalf("activate the replacement: %v", err)
	}
	// The retreat, authorized as a rollback: the customer names both sides of the
	// transition rather than the platform inferring one from what it happens to
	// remember.
	writeRollback(t, dir, policyTenant, "bundle_prod_a", priv, now, allowOrdinaryOrders,
		authority, "bundle_prod_b")
	if _, err := bundles.Active(ctx, policyTenant); err != nil {
		t.Fatalf("roll back: %v", err)
	}
	collect("bundle_prod_a")
	collect("bundle_prod_b")

	// The events an operator, an auditor or an incident review cannot do without.
	mandatory := []struct {
		name evidence.EventName
		why  string
	}{
		{evidence.PolicyBundleActivated,
			"a change to what the platform denies, with nothing recording that it changed"},
		{evidence.PolicyBundleRolledBack,
			"a retreat during an incident, indistinguishable from a release"},
		{evidence.AuthorityReserved,
			"capacity held against a customer's grant with no append-only record of it"},
		{evidence.SubmissionAttempted,
			"the only event that says the platform tried to reach a venue"},
		{evidence.OrderExpired,
			"the state that used to be recorded as one the venue accepted"},
		{evidence.DecisionCommitted,
			"the receipt: the decision that permitted an order, committed before it was sent"},
		{evidence.AuthorityReservationCommitted,
			"where a reservation ended, as evidence rather than as an overwritten row"},
	}

	for _, m := range mandatory {
		if !produced[m.name] {
			t.Errorf("nothing produced %s. Declaring an event and never emitting it "+
				"leaves %s. Produced in this run: %v", m.name, m.why, keysOf(produced))
		}
	}
}

func keysOf(m map[evidence.EventName]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, string(k))
	}
	return out
}
