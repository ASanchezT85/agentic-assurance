//go:build integration

package integration

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os/exec"
	"strings"
	"testing"

	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
)

// The verifier in internal/identity is built on crypto/x509 rather than a SPIFFE
// library. That is a deliberate trade (see the Phase 1 and Phase 2 commits), and it
// is only safe if the verifier is tested against SVIDs a real SPIRE server issued,
// not only against certificates our own tests minted. Certificates our tests generate
// agree with our assumptions by construction.
//
// Run with:  make up && make test-integration

const (
	spireTrustDomain = "acme.example"
	spireWorkloadID  = "spiffe://acme.example/ns/agents/sa/momentum"
)

// mintSVID asks the running SPIRE server for a real X509-SVID.
func mintSVID(t *testing.T, spiffeID string) (leaf *x509.Certificate, roots []*x509.Certificate) {
	t.Helper()

	cmd := exec.Command("docker", "compose", "exec", "-T", "spire-server",
		"/opt/spire/bin/spire-server", "x509", "mint", "-spiffeID", spiffeID, "-ttl", "1h")
	cmd.Dir = "../.."

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("spire-server x509 mint failed (is `make up` running?): %v\n%s", err, out)
	}

	certs := parseCertificates(t, out)
	if len(certs) < 2 {
		t.Fatalf("expected an SVID and at least one root CA, got %d certificates:\n%s", len(certs), out)
	}
	// mint prints the SVID first, then the private key, then the root CAs.
	return certs[0], certs[1:]
}

func parseCertificates(t *testing.T, raw []byte) []*x509.Certificate {
	t.Helper()
	var out []*x509.Certificate
	rest := raw
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return out
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			t.Fatalf("parse certificate: %v", err)
		}
		out = append(out, cert)
	}
}

func poolOf(certs []*x509.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool
}

func integrationFailureCode(t *testing.T, err error) string {
	t.Helper()
	var f *identity.VerificationFailure
	if !errors.As(err, &f) {
		t.Fatalf("expected *identity.VerificationFailure, got %T: %v", err, err)
	}
	return f.Code
}

// A real SPIRE-issued SVID verifies against the real SPIRE trust bundle.
func TestSPIREIssuedSVIDIsAccepted(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{TrustDomain: spireTrustDomain, Bundle: poolOf(roots)}
	got, err := v.Verify(leaf, nil)
	if err != nil {
		t.Fatalf("a genuine SPIRE SVID was rejected: %v", err)
	}

	if got.ID.String() != spireWorkloadID {
		t.Errorf("identity = %q, want %q", got.ID.String(), spireWorkloadID)
	}
	if got.ID.TrustDomain != spireTrustDomain {
		t.Errorf("trust domain = %q", got.ID.TrustDomain)
	}
	if got.SerialHex == "" {
		t.Error("serial not recorded")
	}
	if got.NotAfter.IsZero() {
		t.Error("expiry not recorded; rotation depends on it")
	}
}

// The same SVID resolves to A2 through the taxonomy, and carries no model identity.
func TestSPIREIssuedSVIDResolvesToA2(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{TrustDomain: spireTrustDomain, Bundle: poolOf(roots)}
	got := v.Resolve(identity.Presented{SVID: leaf})

	if string(got.Level) != "A2" {
		t.Fatalf("level = %s, want A2", got.Level)
	}
	if got.SpiffeID.String() != spireWorkloadID {
		t.Errorf("spiffe id = %q", got.SpiffeID.String())
	}
	if got.Downgrade != nil {
		t.Errorf("a successful verification recorded a downgrade reason: %v", got.Downgrade)
	}
}

// The same certificate against an empty bundle must fail, and fail for the right
// reason. This is what proves the acceptance above came from chain verification
// rather than from the certificate merely looking well formed.
func TestSPIREIssuedSVIDFailsWithoutTheBundle(t *testing.T) {
	leaf, _ := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{TrustDomain: spireTrustDomain, Bundle: x509.NewCertPool()}
	_, err := v.Verify(leaf, nil)
	if err == nil {
		t.Fatal("a real SVID verified against an empty trust bundle")
	}
	if code := integrationFailureCode(t, err); code != "SVID_UNTRUSTED_ISSUER" {
		t.Errorf("expected SVID_UNTRUSTED_ISSUER, got %s", code)
	}
}

// A valid SVID from a trust domain we do not accept is still a rejection.
func TestSPIREIssuedSVIDFromAnotherTrustDomainIsRejected(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{TrustDomain: "someone-else.example", Bundle: poolOf(roots)}
	_, err := v.Verify(leaf, nil)
	if err == nil {
		t.Fatal("an SVID from another trust domain was accepted")
	}
	if code := integrationFailureCode(t, err); code != "TRUST_DOMAIN_MISMATCH" {
		t.Errorf("expected TRUST_DOMAIN_MISMATCH, got %s", code)
	}
}

// SPIRE caps the SVID lifetime below what we asked for. Recording the real NotAfter
// rather than the requested TTL is what makes expiry handling correct later.
func TestSPIRECapsTheRequestedLifetime(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{TrustDomain: spireTrustDomain, Bundle: poolOf(roots)}
	got, err := v.Verify(leaf, nil)
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if !got.NotAfter.Equal(leaf.NotAfter) {
		t.Errorf("recorded expiry %s does not match the certificate %s",
			got.NotAfter, leaf.NotAfter)
	}
}

// A SPIFFE ID SPIRE will not mint is one we would never see, but the shape rules
// must hold on real output too: the trust domain ID has no workload path.
func TestTrustDomainIDIsNotAWorkloadIdentity(t *testing.T) {
	cmd := exec.Command("docker", "compose", "exec", "-T", "spire-server",
		"/opt/spire/bin/spire-server", "x509", "mint", "-spiffeID", "spiffe://acme.example", "-ttl", "1h")
	cmd.Dir = "../.."
	out, _ := cmd.CombinedOutput()

	if strings.Contains(string(out), "BEGIN CERTIFICATE") {
		t.Error("SPIRE minted an SVID for a bare trust domain ID; the verifier rejects these, and so should the issuer")
	}
}

// A real SVID reaches A2 with a tenant, which is what makes the level usable.
//
// A2 has been buildable and unreachable: the verifier accepted a SPIRE certificate and
// then RequireTenant refused it, because a workload certificate says which workload is
// calling and nothing about which customer it acts for. Falling back to the request
// there would have reintroduced the cross-tenant hole. The registry closes it, and
// this is the half that cannot be tested without a real issuer.
func TestASPIREWorkloadResolvesToItsTenant(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	workloads, err := identity.ParseWorkloads(
		"spiffe://acme.example/ns/agents/=tenant_acme," +
			"spiffe://acme.example/ns/agents/sa/momentum=tenant_momentum")
	if err != nil {
		t.Fatalf("workloads: %v", err)
	}

	v := &identity.Verifier{
		TrustDomain: spireTrustDomain,
		Bundle:      poolOf(roots),
		Workloads:   workloads,
	}

	attested := v.Resolve(identity.Presented{SVID: leaf})

	if attested.Level != intent.AttestationA2 {
		t.Fatalf("level = %s, want A2 (%v)", attested.Level, attested.Downgrade)
	}
	// The exact entry wins over the namespace prefix, on an identifier SPIRE actually
	// issued rather than one this test wrote down.
	if attested.TenantID != "tenant_momentum" {
		t.Errorf("tenant = %q, want tenant_momentum; the exact entry must win over the "+
			"namespace prefix", attested.TenantID)
	}
	if err := identity.RequireExecutable(attested); err != nil {
		t.Errorf("a verified workload is not executable: %v", err)
	}
	if err := identity.RequireTenant(attested, "tenant_momentum"); err != nil {
		t.Errorf("a mapped workload was refused its own tenant: %v", err)
	}
	if err := identity.RequireTenant(attested, "tenant_other"); err == nil {
		t.Error("a mapped workload was allowed to act on another tenant")
	}
}

// A workload SPIRE issued and nobody mapped establishes no tenant, on a real
// certificate. The refusal names it, so an operator can tell a missing registry entry
// from an attack.
func TestAnUnmappedSPIREWorkloadIsRefused(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	workloads, err := identity.ParseWorkloads("spiffe://acme.example/ns/other/=tenant_acme")
	if err != nil {
		t.Fatalf("workloads: %v", err)
	}

	v := &identity.Verifier{
		TrustDomain: spireTrustDomain,
		Bundle:      poolOf(roots),
		Workloads:   workloads,
	}

	attested := v.Resolve(identity.Presented{SVID: leaf})
	if attested.Level != intent.AttestationA2 {
		t.Fatalf("level = %s, want A2", attested.Level)
	}
	if attested.TenantID != "" {
		t.Fatalf("an unmapped workload was assigned %q", attested.TenantID)
	}

	err = identity.RequireTenant(attested, "tenant_acme")
	if err == nil {
		t.Fatal("an unmapped workload was allowed to name a tenant")
	}
	if !strings.Contains(err.Error(), spireWorkloadID) {
		t.Errorf("error = %q; it should name the workload that has no entry", err)
	}
}

// A verified workload cannot keep a tenant that arrived with the request. The registry
// decides or nothing does.
func TestASPIREWorkloadCannotCarryATenant(t *testing.T) {
	leaf, roots := mintSVID(t, spireWorkloadID)

	v := &identity.Verifier{
		TrustDomain: spireTrustDomain,
		Bundle:      poolOf(roots),
		Workloads:   nil,
	}

	attested := v.Resolve(identity.Presented{
		SVID:     leaf,
		TenantID: "tenant_smuggled",
	})

	if attested.TenantID != "" {
		t.Errorf("a tenant that arrived with the request survived verification as %q",
			attested.TenantID)
	}
}
