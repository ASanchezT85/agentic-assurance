package security

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentic-assurance/internal/identity"
)

// INV-007, the transport half.
//
// The database half was enforced from the start: row level security, FORCE, and a
// non-superuser role, all with a test. It isolated correctly to whichever tenant the
// platform gave it, and nothing established which tenant that should be.
//
// The submission endpoint authenticated the caller and then read the tenant from the
// request body. A caller authenticated as svc_a could submit an envelope naming
// tenant_b and, with a grant id from that tenant, have the order evaluated against
// tenant_b's grant and tenant_b's policy and placed at the venue. Every lookup after
// the identity check takes the envelope's tenant: the grant, the policy bundle, the
// idempotency record, and the app.tenant_id that RLS itself keys on.
//
// The simulation API had the same shape with a header instead of a body field: a run
// is stored, retrieved and cancelled by tenant, so a header let anyone read another
// customer's results and stop their runs.
//
// A credential is issued to a tenant now. These tests are the guard.

const tenantToken = "a-token-of-at-least-thirty-two-characters"

func credentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials("svc_a@tenant_a=" + tenantToken)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	return creds
}

// A credential names the tenant it speaks for. One that does not is refused at
// startup, because it would leave the tenant to come from the request.
func TestACredentialMustNameItsTenant(t *testing.T) {
	for _, raw := range []string{
		"svc_a=" + tenantToken,
		"svc_a@=" + tenantToken,
		"@tenant_a=" + tenantToken,
		"svc_a@tenant a=" + tenantToken,
		"svc_a@tenant_a=short",
		"",
	} {
		if _, err := identity.ParseCredentials(raw); err == nil {
			t.Errorf("%q was accepted as a credential", raw)
		}
	}

	if _, err := identity.ParseCredentials("svc_a@tenant_a=" + tenantToken); err != nil {
		t.Errorf("a well-formed credential was refused: %v", err)
	}
}

// The tenant a caller is authenticated for comes from the transport, never from the
// request.
func TestTheTenantComesFromTheCredential(t *testing.T) {
	creds := credentials(t)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tenantToken)
	// A header naming a different tenant. It must not be what establishes anything.
	req.Header.Set("X-Tenant-Id", "tenant_victim")

	attested := (&identity.Verifier{}).Resolve(
		identity.FromTransport(req.Header.Get("Authorization"), nil, creds))

	if attested.TenantID != "tenant_a" {
		t.Fatalf("established tenant = %q, want tenant_a", attested.TenantID)
	}
	if attested.APIIdentity != "svc_a" {
		t.Errorf("identity = %q, want svc_a", attested.APIIdentity)
	}
}

// A claim to another tenant is refused, and the refusal does not name the caller's own
// tenant. An error that printed both would be a probe with feedback (spec section 45).
func TestAClaimToAnotherTenantIsRefused(t *testing.T) {
	creds := credentials(t)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tenantToken)
	attested := (&identity.Verifier{}).Resolve(
		identity.FromTransport(req.Header.Get("Authorization"), nil, creds))

	err := identity.RequireTenant(attested, "tenant_victim")
	if err == nil {
		t.Fatal("a caller authenticated for tenant_a was allowed to act on tenant_victim")
	}
	if strings.Contains(err.Error(), "tenant_a") {
		t.Errorf("the refusal names the caller's own tenant: %q. An attacker guessing "+
			"tenant names would learn one from every refusal", err)
	}

	if err := identity.RequireTenant(attested, "tenant_a"); err != nil {
		t.Errorf("the caller's own tenant was refused: %v", err)
	}
}

// An identity with no established tenant is refused rather than trusted.
//
// This is the workload-certificate case: an SVID proves which workload is calling, and
// the platform has no mapping from a SPIFFE ID to a customer. Falling back to the
// request would reintroduce exactly the hole this closes.
func TestAnIdentityWithNoTenantCannotClaimOne(t *testing.T) {
	attested := identity.Attested{
		Level:  "A2",
		Method: "x509-svid",
	}

	err := identity.RequireTenant(attested, "tenant_a")
	if err == nil {
		t.Fatal("an identity with no established tenant was allowed to name one")
	}
	if !strings.Contains(err.Error(), "mapping") {
		t.Errorf("error = %q; the refusal should say what is missing, or an operator "+
			"cannot tell a misconfiguration from an attack", err)
	}
}

// An unauthenticated caller establishes nothing, including a tenant.
func TestAnUnauthenticatedCallerHasNoTenant(t *testing.T) {
	creds := credentials(t)

	for _, header := range []string{"", "Bearer wrong-token", "Basic " + tenantToken} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		req.Header.Set("X-Tenant-Id", "tenant_a")

		attested := (&identity.Verifier{}).Resolve(
			identity.FromTransport(req.Header.Get("Authorization"), nil, creds))
		if attested.TenantID != "" {
			t.Errorf("header %q established tenant %q", header, attested.TenantID)
		}
		if err := identity.RequireExecutable(attested); err == nil {
			t.Errorf("header %q produced an executable identity", header)
		}
	}
}
