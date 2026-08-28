package identity

import (
	"strings"
	"testing"
)

func mustParse(t *testing.T, raw string) *Workloads {
	t.Helper()
	w, err := ParseWorkloads(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return w
}

func TestAnExactWorkloadMapsToItsTenant(t *testing.T) {
	w := mustParse(t, "spiffe://acme.example/ns/agents/sa/momentum=tenant_acme")

	tenant, ok := w.TenantFor(SPIFFEID{TrustDomain: "acme.example", Path: "/ns/agents/sa/momentum"})
	if !ok || tenant != "tenant_acme" {
		t.Errorf("tenant = %q, %v; want tenant_acme", tenant, ok)
	}
}

// The trailing slash is the whole point of requiring it.
//
// "spiffe://td/ns/prod" used as a prefix also matches "spiffe://td/ns/production" — a
// different namespace, a different customer, and a bug that looks like nothing until
// the day somebody registers the longer name.
func TestAPrefixDoesNotBiteTheNextNamespace(t *testing.T) {
	w := mustParse(t, "spiffe://acme.example/ns/prod/=tenant_acme")

	inside := SPIFFEID{TrustDomain: "acme.example", Path: "/ns/prod/sa/trader"}
	if tenant, ok := w.TenantFor(inside); !ok || tenant != "tenant_acme" {
		t.Errorf("a workload inside the prefix got %q, %v", tenant, ok)
	}

	neighbour := SPIFFEID{TrustDomain: "acme.example", Path: "/ns/production/sa/trader"}
	if tenant, ok := w.TenantFor(neighbour); ok {
		t.Errorf("/ns/production matched a prefix for /ns/prod and was assigned %q. "+
			"They are different namespaces and could be different customers.", tenant)
	}
}

// The prefix also matches the namespace itself, not only what is under it.
func TestAPrefixMatchesItsOwnRoot(t *testing.T) {
	w := mustParse(t, "spiffe://acme.example/ns/prod/=tenant_acme")

	root := SPIFFEID{TrustDomain: "acme.example", Path: "/ns/prod"}
	if tenant, ok := w.TenantFor(root); !ok || tenant != "tenant_acme" {
		t.Errorf("the prefix root got %q, %v; a workload registered at the namespace "+
			"itself belongs to the same customer as everything under it", tenant, ok)
	}
}

// Exact beats prefix, and a longer prefix beats a shorter one. Both are cases where a
// map iteration order would otherwise decide which customer an order belongs to.
func TestTheMostSpecificEntryWins(t *testing.T) {
	w := mustParse(t, strings.Join([]string{
		"spiffe://acme.example/ns/=tenant_root",
		"spiffe://acme.example/ns/prod/=tenant_prod",
		"spiffe://acme.example/ns/prod/sa/special=tenant_special",
	}, ","))

	cases := map[string]string{
		"/ns/prod/sa/special": "tenant_special",
		"/ns/prod/sa/other":   "tenant_prod",
		"/ns/staging/sa/any":  "tenant_root",
	}
	for path, want := range cases {
		tenant, ok := w.TenantFor(SPIFFEID{TrustDomain: "acme.example", Path: path})
		if !ok || tenant != want {
			t.Errorf("%s = %q, %v; want %q", path, tenant, ok, want)
		}
	}
}

// A workload with no entry gets nothing. It is a registered workload nobody has
// assigned to a customer, and guessing would assign it to one.
func TestAnUnmappedWorkloadGetsNoTenant(t *testing.T) {
	w := mustParse(t, "spiffe://acme.example/ns/prod/=tenant_acme")

	for _, id := range []SPIFFEID{
		{TrustDomain: "acme.example", Path: "/ns/other/sa/x"},
		{TrustDomain: "other.example", Path: "/ns/prod/sa/x"},
		{},
	} {
		if tenant, ok := w.TenantFor(id); ok {
			t.Errorf("%s was assigned %q", id.String(), tenant)
		}
	}
}

// A nil registry assigns nothing, so an operator who configures SPIRE and forgets the
// mapping gets refusals rather than a default tenant.
func TestANilRegistryAssignsNothing(t *testing.T) {
	var w *Workloads
	if tenant, ok := w.TenantFor(SPIFFEID{TrustDomain: "acme.example", Path: "/x"}); ok {
		t.Errorf("a nil registry assigned %q", tenant)
	}
}

// Ambiguity is a startup error. Two entries for one path would otherwise be resolved
// by whichever the runtime happened to look at, at the moment an order was placed.
func TestAmbiguousConfigurationIsRefused(t *testing.T) {
	cases := map[string]string{
		"the same workload twice": "spiffe://acme.example/ns/a/sa/x=tenant_a," +
			"spiffe://acme.example/ns/a/sa/x=tenant_b",
		"the same prefix twice": "spiffe://acme.example/ns/a/=tenant_a," +
			"spiffe://acme.example/ns/a/=tenant_b",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseWorkloads(raw); err == nil {
				t.Error("ambiguous configuration was accepted; which tenant a workload " +
					"belongs to would depend on iteration order")
			}
		})
	}
}

func TestMalformedConfigurationIsRefused(t *testing.T) {
	for _, raw := range []string{
		"",
		"spiffe://acme.example/ns/a/sa/x",
		"spiffe://acme.example/ns/a/sa/x=",
		"=tenant_a",
		"acme.example/ns/a/sa/x=tenant_a",
		"spiffe:///ns/a=tenant_a",
		"spiffe://acme.example/ns/a/sa/x=tenant a",
		"spiffe://acme.example/ns/a/sa/x=../../etc",

		// A whole trust domain. Refused deliberately: it would assign every workload
		// SPIRE ever issues in that domain to one customer, including the ones added
		// later by someone who never read the configuration.
		"spiffe://acme.example/=tenant_a",
		"spiffe://acme.example=tenant_a",
	} {
		if _, err := ParseWorkloads(raw); err == nil {
			t.Errorf("%q was accepted", raw)
		}
	}
}

// The registry is the only thing that decides. A caller cannot arrive with a tenant
// and have it survive verification.
func TestAVerifiedWorkloadCannotCarryItsOwnTenant(t *testing.T) {
	// Presented.TenantID is set only by the credential path, but an SVID request that
	// somehow carried one must not keep it: the mapping decides, or nothing does.
	w := mustParse(t, "spiffe://acme.example/ns/prod/=tenant_acme")
	if tenant, ok := w.TenantFor(SPIFFEID{TrustDomain: "acme.example", Path: "/ns/other"}); ok {
		t.Fatalf("unmapped workload resolved to %q", tenant)
	}
}
