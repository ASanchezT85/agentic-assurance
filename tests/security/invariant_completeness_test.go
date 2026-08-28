package security

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// Phase 15 is the completeness and regression gate (ADR-024).
//
// The earlier phases each proved one invariant in isolation, at rest. This file asks
// two harder questions: is anything missing, and do they still hold under
// concurrency. The chaos suite in tests/chaos asks the third, with real services
// stopped.

// Every invariant in the threat model has a test file, and every test file names an
// invariant in the threat model.
//
// Both directions. A file with no entry is an invariant nobody wrote down; an entry
// with no file is one nobody checks, and the second is the failure that hides for a
// year.
func TestEveryInvariantHasATestAndEveryTestHasAnInvariant(t *testing.T) {
	raw, err := os.ReadFile("../../docs/threat-model/README.md")
	if err != nil {
		t.Fatalf("read threat model: %v", err)
	}
	documented := map[string]bool{}
	for _, match := range regexp.MustCompile(`INV-\d{3}`).FindAllString(string(raw), -1) {
		documented[match] = true
	}
	if len(documented) != 15 {
		t.Fatalf("the threat model documents %d invariants; spec section 44 defines 15",
			len(documented))
	}

	files, err := filepath.Glob("INV-*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	tested := map[string]bool{}
	for _, path := range files {
		id := strings.SplitN(filepath.Base(path), "_", 2)[0]
		if !documented[id] {
			t.Errorf("%s tests %s, which the threat model does not document", path, id)
		}
		tested[id] = true
	}

	for id := range documented {
		if !tested[id] {
			t.Errorf("%s is documented but has no test file; Phase 15 cannot certify "+
				"an invariant nobody checks (ADR-024)", id)
		}
	}

	if len(tested) != 15 {
		t.Errorf("%d of 15 invariants have test files", len(tested))
	}
}

// Every invariant test file must actually assert something.
//
// A file that exists and contains no test function would satisfy the check above and
// prove nothing, which is the exact shape of a completeness gate that has stopped
// working.
func TestEveryInvariantFileContainsAssertions(t *testing.T) {
	files, err := filepath.Glob("INV-*_test.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	for _, path := range files {
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read %s: %v", path, readErr)
		}
		body := string(raw)

		funcs := strings.Count(body, "\nfunc Test")
		if funcs == 0 {
			t.Errorf("%s declares no test functions", path)
		}
		if !strings.Contains(body, "t.Error") && !strings.Contains(body, "t.Fatal") {
			t.Errorf("%s asserts nothing", path)
		}
		if strings.Contains(body, "t.Skip(") && !strings.Contains(body, "build integration") {
			t.Errorf("%s skips unconditionally outside an integration build; a skipped "+
				"invariant is an unchecked one", path)
		}
	}
}

// Enforcement under concurrency.
//
// Every earlier invariant test evaluates one envelope at a time. Production does not,
// and a decision path that is correct sequentially and wrong under load is worse than
// one that is obviously wrong: it passes review and fails in the afternoon.
func TestEnforcementIsCorrectUnderConcurrency(t *testing.T) {
	bundle := activePolicyForLoad(t)
	g := grantFor("tenant_acme")
	ctx := context.Background()
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	const (
		workers      = 32
		perWorker    = 200
		perOrderCeil = 5000.0
	)

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		wrongAllows  int
		wrongDenials int
	)

	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				// Alternate between an order that must pass and one that must not,
				// so a path that always allows and one that always denies both fail.
				oversized := (worker+i)%2 == 0

				env := envelopeFor("tenant_acme")
				env.EnvelopeID = fmt.Sprintf("env_%d_%d", worker, i)
				if oversized {
					env.Intent.Notional = ptr(perOrderCeil + 1)
				} else {
					env.Intent.Notional = ptr(1000.0)
				}

				authDecision := authority.Evaluate(ctx, env, g, nil, at)
				policyDecision := policy.Evaluate(bundle, env, at)
				denied := !authDecision.Allowed || policyDecision.Action == policy.ActionDeny

				mu.Lock()
				switch {
				case oversized && !denied:
					wrongAllows++
				case !oversized && denied:
					wrongDenials++
				}
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()

	if wrongAllows > 0 {
		t.Errorf("%d oversized orders were allowed under concurrency; enforcement must "+
			"be deterministic per envelope", wrongAllows)
	}
	if wrongDenials > 0 {
		t.Errorf("%d compliant orders were denied under concurrency", wrongDenials)
	}
}

// Tenant isolation under concurrent mixed-tenant traffic.
//
// The sequential test proves a cross-tenant grant is refused. This proves that
// evaluating many tenants at once does not let one leak into another's decision,
// which is the failure a shared cache or a package-level variable would produce.
func TestTenantIsolationHoldsUnderConcurrentMixedTraffic(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	tenants := []string{"tenant_acme", "tenant_globex", "tenant_initech", "tenant_umbrella"}
	grants := map[string]*authority.Grant{}
	for _, name := range tenants {
		grants[name] = grantFor(name)
	}

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		leaks    int
		refusals int
	)

	wg.Add(len(tenants))
	for _, tenant := range tenants {
		go func(own string) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				env := envelopeFor(own)
				env.EnvelopeID = fmt.Sprintf("env_%s_%d", own, i)

				// Its own grant must work.
				if d := authority.Evaluate(ctx, env, grants[own], nil, at); !d.Allowed {
					mu.Lock()
					refusals++
					mu.Unlock()
				}

				// Every other tenant's grant must not, and must be refused as a
				// tenant failure rather than as some downstream mismatch.
				for _, other := range tenants {
					if other == own {
						continue
					}
					d := authority.Evaluate(ctx, env, grants[other], nil, at)
					if d.Allowed || d.Code != "GRANT_WRONG_TENANT" {
						mu.Lock()
						leaks++
						mu.Unlock()
					}
				}
			}
		}(tenant)
	}
	wg.Wait()

	if leaks > 0 {
		t.Errorf("%d cross-tenant evaluations did not fail as GRANT_WRONG_TENANT under "+
			"concurrent mixed traffic (INV-007)", leaks)
	}
	if refusals > 0 {
		t.Errorf("%d own-tenant evaluations were refused under load", refusals)
	}
}

// Policy evaluation returns the same answer under load as it does alone.
//
// INV-003 is about determinism, and determinism under one goroutine is a weaker claim
// than the invariant makes.
func TestPolicyIsDeterministicUnderConcurrency(t *testing.T) {
	bundle := activePolicyForLoad(t)
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	env := envelopeFor("tenant_acme")
	env.Intent.Notional = ptr(4200.0)
	expected := policy.Evaluate(bundle, env, at)

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		diffs int
	)

	wg.Add(64)
	for w := 0; w < 64; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				got := policy.Evaluate(bundle, env, at)
				if got.Action != expected.Action || got.DecidedBy != expected.DecidedBy {
					mu.Lock()
					diffs++
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if diffs > 0 {
		t.Errorf("%d of 12800 concurrent evaluations differed from the sequential "+
			"answer (INV-003)", diffs)
	}
}

func activePolicyForLoad(t *testing.T) *policy.Bundle {
	t.Helper()
	b, _ := signed(t)
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := b.Transition(stage, policyAt, "phase-15"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}
	return b
}

var _ = intent.SchemaVersion
