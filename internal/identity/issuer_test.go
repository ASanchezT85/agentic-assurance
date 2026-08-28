package identity

import (
	"crypto/x509"
	"testing"
)

const (
	issuerToken = "admin-token-of-at-least-thirty-two-ch"
	agentToken  = "agent-token-of-at-least-thirty-two-ch"
	registry    = "svc_agent@tenant_a=" + agentToken + ",svc_admin@tenant_a=" + issuerToken
)

func mustCreds(t *testing.T) *Credentials {
	t.Helper()
	c, err := ParseCredentials(registry)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return c
}

// Nothing may issue authority until an operator says so by name.
func TestNoCredentialIssuesAuthorityByDefault(t *testing.T) {
	c := mustCreds(t)
	for _, token := range []string{agentToken, issuerToken} {
		if caller, _ := c.Identify(token); caller.MayIssueAuthority {
			t.Errorf("%s may issue with no issuer list configured", caller.Identity)
		}
	}
}

func TestOnlyTheNamedIssuerGetsThePrivilege(t *testing.T) {
	c := mustCreds(t)
	c.AllowIssuers("svc_admin")

	if caller, _ := c.Identify(agentToken); caller.MayIssueAuthority {
		t.Error("the submitting agent may issue authority; it could raise its own ceiling (P-002)")
	}
	if caller, _ := c.Identify(issuerToken); !caller.MayIssueAuthority {
		t.Error("the named issuer may not issue")
	}
}

// The privilege has to survive the trip from the registry to the handler. It did not:
// FromTransport copied the identity and the tenant and dropped this field, so
// GATEWAY_GRANT_ISSUERS was configuration with no effect and every issue was 403.
func TestTheIssuerPrivilegeReachesTheHandler(t *testing.T) {
	c := mustCreds(t)
	c.AllowIssuers("svc_admin")

	v := &Verifier{}
	if !v.Resolve(FromTransport("Bearer "+issuerToken, nil, c)).MayIssueAuthority {
		t.Error("the issuer arrives at the handler without its privilege")
	}
	if v.Resolve(FromTransport("Bearer "+agentToken, nil, c)).MayIssueAuthority {
		t.Error("the agent arrives at the handler able to issue")
	}
}

// A workload certificate proves which process is calling. It says nothing about who
// authorized it to widen a customer's authority, and A2 must not inherit the privilege
// from a bearer token presented on the same connection.
func TestAVerifiedWorkloadNeverIssuesAuthority(t *testing.T) {
	ca := newTestCA(t, "test-ca")
	svid := ca.svid(t, "spiffe://acme.example/ns/prod/agent")

	c := mustCreds(t)
	c.AllowIssuers("svc_admin")

	v := &Verifier{Bundle: ca.pool, TrustDomain: "acme.example",
		Workloads: mustParse(t, "spiffe://acme.example/ns/prod/=tenant_a")}

	got := v.Resolve(FromTransport("Bearer "+issuerToken, []*x509.Certificate{svid}, c))
	if got.MayIssueAuthority {
		t.Error("a verified workload carried the issuer privilege of a bearer token")
	}
}
