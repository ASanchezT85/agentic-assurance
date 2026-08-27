package security

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/policy"
)

// INV-005: loss of the intelligence cloud cannot disable local hard limits.
//
// The cloud is fleet-engine, ClickHouse, NATS and the console. None of them can be
// reachable from the enforcement path, and the way to be sure is not to unplug them
// in a test but to show there is no wire.

// Authority and policy evaluation both run to completion with nothing outside the
// process available. There is no client to stub, because there is no client.
func TestEnforcementRunsWithNothingExternal(t *testing.T) {
	b, _ := signed(t)
	for _, s := range []policy.Status{policy.StatusSimulated, policy.StatusShadow,
		policy.StatusCanary, policy.StatusActive} {
		if err := b.Transition(s, policyAt, "release-engineer"); err != nil {
			t.Fatalf("staging: %v", err)
		}
	}

	// A 6,000 order: over the policy ceiling and over the grant's per-order limit.
	env := policyEnvelope()
	env.Intent.Notional = ptr(6000.0)
	env.TenantID = "tenant_acme"
	env.AuthorityGrantID = "grant_5521"
	env.Principal.PrincipalID = "principal_7781"
	env.Principal.AccountID = "account_4410"
	env.Agent.AgentID = "agent_momentum_03"

	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	// nil usage source stands for every rolling-limit backend being gone.
	authDecision := authority.Evaluate(context.Background(), env, grantFor("tenant_acme"), nil, at)
	if authDecision.Allowed {
		t.Error("authority allowed an order over its per-order limit with no backend available (INV-005)")
	}

	policyDecision := policy.Evaluate(b, env, at)
	if policyDecision.Action != policy.ActionDeny {
		t.Errorf("policy returned %s with nothing external available; hard limits must "+
			"still enforce (INV-005)", policyDecision.Action)
	}
}

// The enforcement packages must not import the intelligence plane. This is the
// structural half: an import that does not exist cannot fail at 3am.
func TestEnforcementDoesNotImportTheIntelligencePlane(t *testing.T) {
	// internal/fleet and internal/incident are the intelligence-plane contexts.
	// A dependency from enforcement to either would invert the direction the
	// architecture depends on (docs/architecture/container-view.md).
	forbidden := []string{
		"agentic-assurance/internal/fleet",
		"agentic-assurance/internal/incident",
	}

	for _, dir := range []string{"../../internal/policy", "../../internal/authority", "../../internal/identity"} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}

		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					name := strings.Trim(imp.Path.Value, `"`)
					for _, bad := range forbidden {
						if name == bad {
							t.Errorf("%s imports %s; the enforcement plane must not depend "+
								"on the intelligence plane (INV-005, ADR-003)", path, bad)
						}
					}
					// A network client anywhere in enforcement is a cloud dependency
					// waiting to be added.
					if name == "net/http" || name == "net/rpc" {
						t.Errorf("%s imports %s; enforcement must not make remote calls (INV-005)", path, name)
					}
				}
			}
		}
	}
}

// Policy with no bundle loaded denies rather than passing. Losing the distribution
// channel for policy must not become an open door.
func TestPolicyUnavailableDeniesRatherThanAllows(t *testing.T) {
	got := policy.Evaluate(nil, policyEnvelope(), policyAt)
	if got.Action != policy.ActionDeny {
		t.Fatalf("action = %s; hard policy unavailable must DENY (spec section 17, INV-005)", got.Action)
	}
}

// Authority with no usage backend denies a grant whose limits depend on one, rather
// than treating the limits as satisfied.
func TestRollingLimitsWithoutABackendDeny(t *testing.T) {
	at := time.Date(2026, 8, 27, 14, 0, 0, 0, time.UTC)

	g := grantFor("tenant_acme")
	g.Limits = authority.Limits{Rolling1hNotional: 10000}

	got := authority.Evaluate(context.Background(), envelopeFor("tenant_acme"), g, nil, at)
	if got.Allowed {
		t.Fatal("a rolling limit with no backend was treated as satisfied (INV-005)")
	}
	if got.Code != "USAGE_UNAVAILABLE" {
		t.Errorf("got %s", got.Code)
	}
}

// The enforcement plane must not reach ClickHouse. Spec section 59 forbids it from
// the synchronous hard-policy path outright, and the way to be sure is that no
// enforcement package can even name it.
//
// This is the Phase 8 exit criterion "no hot-path dependency on ClickHouse",
// expressed as a property of the import graph rather than as a benchmark that
// happens not to have touched it.
func TestEnforcementCannotReachClickHouse(t *testing.T) {
	for _, dir := range []string{
		"../../internal/policy", "../../internal/authority", "../../internal/identity",
		"../../internal/intent", "../../internal/execution", "../../internal/broker",
	} {
		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", dir, err)
		}

		for _, pkg := range pkgs {
			for path, file := range pkg.Files {
				for _, imp := range file.Imports {
					name := strings.Trim(imp.Path.Value, `"`)
					if name == "agentic-assurance/internal/fleet" ||
						strings.Contains(strings.ToLower(name), "clickhouse") {
						t.Errorf("%s imports %s; ClickHouse and fleet analytics are "+
							"forbidden on the hot path (spec section 59, ADR-021)", path, name)
					}
				}

				raw, readErr := os.ReadFile(path)
				if readErr != nil {
					continue
				}
				if strings.Contains(strings.ToLower(string(raw)), "clickhouse") {
					t.Errorf("%s mentions ClickHouse; no enforcement decision may read "+
						"analytical storage", path)
				}
			}
		}
	}
}
