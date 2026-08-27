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
