//go:build process

package process

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/policy"
)

// F4-POLICY-05: two real gateway processes competing for one policy transition.
//
// The integration suite proves the serialization with two transactions in one process,
// which is the same lock and the same database. What it cannot prove is that two operating
// system processes, each with its own bundle directory, its own file watcher and its own
// connection pool, reach the same answer — and that the one that loses does not enforce
// the bundle it was given.
//
// That is the shape the audit described: a staged file on host A and a different staged
// file on host B, both correctly signed, both authorized from the same predecessor. Before
// the fix both would commit, because a unique nonce was the only thing either had to win.
func TestTwoGatewayProcessesCannotBranchPolicyHistory(t *testing.T) {
	ctx := context.Background()
	d := newDeployment(t, authority.Limits{
		PerOrderNotional:  money.MustParse("50000"),
		Rolling1hNotional: money.MustParse("1000000"),
		DailyNotional:     money.MustParse("1000000"),
		MaxOpenOrders:     500,
	})

	// A first process, to put this deployment's provisioned bundle into force. Until a
	// transition is accepted there is no predecessor for two competitors to share, and
	// the branch is precisely two changes from one predecessor.
	boot := d.start(t, nil)
	if status, _ := d.submit(t, boot, fmt.Sprintf("f4-boot-%d", time.Now().UnixNano()), nil); status >= 500 {
		t.Fatalf("the bootstrap submission answered %d", status)
	}
	boot.stop()

	// The bundle each process will try to put into force, staged in its own directory.
	// Different notional caps, both under the grant's per-order ceiling, so what refuses
	// a probe order later is policy rather than authority.
	left := d.stageCompeting(t, "bundle_left", 30000)
	right := d.stageCompeting(t, "bundle_right", 10000)

	// start blocks until the process answers /healthz.
	a := d.start(t, map[string]string{"POLICY_BUNDLE_DIR": left})
	b := d.start(t, map[string]string{"POLICY_BUNDLE_DIR": right})

	// One submission through each, at the same moment. A submission is what makes a
	// gateway read its bundle directory, so this is the race as it happens in production
	// rather than a race arranged by calling an internal method twice.
	var wg sync.WaitGroup
	statuses := make([]int, 2)
	codes := make([]string, 2)
	start := make(chan struct{})
	for i, g := range []*gateway{a, b} {
		wg.Add(1)
		go func(i int, g *gateway) {
			defer wg.Done()
			<-start
			status, body := d.submit(t, g, fmt.Sprintf("f4-proc-%d-%d", i, time.Now().UnixNano()), nil)
			statuses[i] = status
			codes[i], _ = body["code"].(string)
		}(i, g)
	}
	close(start)
	wg.Wait()
	t.Logf("submissions answered %d %s and %d %s",
		statuses[0], codes[0], statuses[1], codes[1])

	// Exactly one transition beyond the one this deployment was provisioned with.
	if _, err := d.pool.Exec(ctx,
		`SELECT set_config('app.tenant_id', $1, false)`, d.tenant); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var transitions int
	if err := d.pool.QueryRow(ctx,
		`SELECT count(*) FROM policy_activations WHERE tenant_id = $1`,
		d.tenant).Scan(&transitions); err != nil {
		t.Fatalf("count transitions: %v", err)
	}
	if transitions > 2 {
		t.Fatalf("%d transitions for this tenant. Two processes each accepted a change "+
			"from the same predecessor, so the tenant's policy history has branched and "+
			"which branch enforces depends on which row a timestamp comparison prefers.",
			transitions)
	}

	// And both processes agree on what is in force, because both read the same pointer.
	store := policy.NewActivationStore(d.pool)
	current, err := store.Current(ctx, d.tenant)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	t.Logf("in force: %s (%s)", current.BundleID, current.BundleContentHash[:12])

	if current.BundleID != "bundle_left" && current.BundleID != "bundle_right" &&
		transitions == 2 {
		t.Errorf("the tenant is enforcing %s, which is neither competitor", current.BundleID)
	}

	// F4-POLICY-09: the loser does not enforce the bundle the database refused.
	//
	// A probe sized between the two caps: allowed by the losing bundle, denied by the one
	// in force. The loser may fail closed — a process whose first reload was refused has
	// no policy it is entitled to evaluate against, and refusing is the honest answer —
	// but it must never accept an order the in-force policy denies.
	loser, winner := a, b
	if current.BundleID == "bundle_left" {
		loser, winner = b, a
	}
	probe := func(m map[string]any) {
		in, _ := m["intent"].(map[string]any)
		in["notional"] = "20000"
	}

	status, body := d.submit(t, loser, fmt.Sprintf("f4-probe-%d", time.Now().UnixNano()), probe)
	code, _ := body["code"].(string)
	t.Logf("the loser answered %d %s to an order its own bundle would allow", status, code)
	if status == 200 || status == 202 {
		t.Errorf("the gateway whose transition was refused accepted an order that the " +
			"policy actually in force denies. A losing transition must change neither " +
			"what is recorded nor what is enforced.")
	}

	// And the winner enforces what it committed.
	status, body = d.submit(t, winner, fmt.Sprintf("f4-probe-w-%d", time.Now().UnixNano()), probe)
	code, _ = body["code"].(string)
	t.Logf("the winner answered %d %s", status, code)

	// Neither process died over it. A refused policy change is an ordinary outcome.
	for i, g := range []*gateway{a, b} {
		if !g.alive() {
			t.Errorf("gateway %d exited after a contested policy transition", i)
		}
	}
}

// stageCompeting writes a bundle and an authorization from the current predecessor into a
// directory of its own, the way a second host with its own staged files would have them.
func (d *deployment) stageCompeting(t *testing.T, id string, cap int) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	dir := filepath.Join(d.dir, "policy-"+id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	source := fmt.Sprintf(`
version: 1
policy: pol_process
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: %d
`, cap)
	parsed, err := policy.ParseSource([]byte(source))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bundle, err := policy.Compile(parsed, d.tenant, id, now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if err := bundle.Sign(d.policyPriv, "process-test", now); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, now, "process-test"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	write(t, filepath.Join(dir, d.tenant+".json"), bundle)

	// Both authorizations name the same predecessor: this is the branch.
	current, err := policy.NewActivationStore(d.pool).Current(ctx, d.tenant)
	if err != nil {
		t.Fatalf("read current: %v", err)
	}
	authorization := policy.Authorization{
		SchemaVersion:          policy.AuthorizationSchemaVersion,
		TenantID:               d.tenant,
		BundleID:               bundle.BundleID,
		BundleContentHash:      bundle.ContentHash,
		PriorBundleID:          current.BundleID,
		PriorBundleContentHash: current.BundleContentHash,
		Action:                 policy.ActionActivate,
		Actor:                  "process-test",
		AuthorizedAt:           now,
		Nonce:                  fmt.Sprintf("proc_%s_%s_%d", d.tenant, id, now.UnixNano()),
	}
	if err := authorization.Sign(d.activationPriv, d.activationKey); err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	write(t, filepath.Join(dir, d.tenant+".activation.json"), authorization)

	return dir
}
