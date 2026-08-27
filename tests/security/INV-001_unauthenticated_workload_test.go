package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/url"
	"testing"
	"time"

	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
)

// INV-001: an unauthenticated workload can never create an executable order.
//
// A0 means unknown origin. It is a legitimate thing to observe and record. What it
// must never be is a state from which money moves. The two ways this invariant gets
// broken quietly are letting an envelope assert its own attestation level, and
// treating a presented-but-invalid certificate as good enough.

func claimCode(t *testing.T, err error) string {
	t.Helper()
	var ce *identity.ClaimError
	if !errors.As(err, &ce) {
		t.Fatalf("expected *identity.ClaimError, got %T: %v", err, err)
	}
	return ce.Code
}

// selfSignedCert is any certificate that will not chain to a configured bundle. It
// stands in for whatever an attacker presents.
func selfSignedCert(t *testing.T, spiffeID string) *x509.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	u, err := url.Parse(spiffeID)
	if err != nil {
		t.Fatalf("uri: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(99),
		Subject:      pkix.Name{CommonName: "impostor"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		URIs:         []*url.URL{u},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return cert
}

func emptyBundleVerifier() *identity.Verifier {
	return &identity.Verifier{TrustDomain: "acme.example", Bundle: x509.NewCertPool()}
}

func TestNothingPresentedResolvesToA0(t *testing.T) {
	got := emptyBundleVerifier().Resolve(identity.Presented{})

	if got.Level != intent.AttestationA0 {
		t.Fatalf("level = %s, want A0", got.Level)
	}
	if err := identity.RequireExecutable(got); err == nil {
		t.Fatal("an unauthenticated workload was allowed to create an executable order (INV-001)")
	} else if code := claimCode(t, err); code != "UNAUTHENTICATED_WORKLOAD" {
		t.Errorf("wrong reason: %s", code)
	}
}

// A certificate that does not chain to the bundle establishes nothing. The failure
// mode this guards is accepting the SPIFFE ID out of an unverified certificate,
// which is trusting the attacker to say who they are.
func TestUnverifiableSVIDEstablishesNothing(t *testing.T) {
	forged := selfSignedCert(t, "spiffe://acme.example/ns/agents/sa/momentum")

	got := emptyBundleVerifier().Resolve(identity.Presented{SVID: forged})

	if got.Level != intent.AttestationA0 {
		t.Fatalf("level = %s, want A0; a forged SVID must not reach A2 (INV-001)", got.Level)
	}
	if !got.SpiffeID.IsZero() {
		t.Errorf("the SPIFFE ID from an unverified certificate leaked through: %q", got.SpiffeID.String())
	}
	if got.Downgrade == nil {
		t.Error("no reason recorded; evidence must distinguish 'none presented' from 'presented and rejected'")
	}
	if err := identity.RequireExecutable(got); err == nil {
		t.Fatal("a forged SVID produced an executable identity (INV-001)")
	}
}

// An authenticated API identity reaches A1 and no further. Knowing which registered
// caller this is says nothing about the workload it runs in.
func TestAPIIdentityReachesA1Only(t *testing.T) {
	got := emptyBundleVerifier().Resolve(identity.Presented{APIIdentity: "app_7781"})

	if got.Level != intent.AttestationA1 {
		t.Fatalf("level = %s, want A1", got.Level)
	}
	if !got.SpiffeID.IsZero() {
		t.Error("an API identity produced a workload identity out of nothing")
	}
	if err := identity.RequireExecutable(got); err != nil {
		t.Errorf("A1 should be executable: %v", err)
	}
}

// The envelope does not get to assert its own attestation. This is the one that
// matters: Phase 1 validates the shape of agent.attestation.level, and Phase 2 has
// to make sure that shape is never mistaken for evidence.
func TestEnvelopeCannotClaimMoreThanWasEstablished(t *testing.T) {
	cases := []struct {
		name        string
		claimed     intent.AttestationLevel
		presented   identity.Presented
		wantRefused bool
	}{
		{"A2 claimed with nothing presented", intent.AttestationA2, identity.Presented{}, true},
		{"A1 claimed with nothing presented", intent.AttestationA1, identity.Presented{}, true},
		{"A2 claimed with only an API identity", intent.AttestationA2, identity.Presented{APIIdentity: "app_1"}, true},
		{"A3 claimed with only an API identity", intent.AttestationA3, identity.Presented{APIIdentity: "app_1"}, true},
		{"A1 claimed with an API identity", intent.AttestationA1, identity.Presented{APIIdentity: "app_1"}, false},
		{"A0 claimed with nothing presented", intent.AttestationA0, identity.Presented{}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			established := emptyBundleVerifier().Resolve(tc.presented)
			err := identity.CheckClaim(tc.claimed, established)

			if tc.wantRefused {
				if err == nil {
					t.Fatalf("claim %s was accepted against established %s (INV-001)",
						tc.claimed, established.Level)
				}
				if code := claimCode(t, err); code != "ATTESTATION_CLAIM_EXCEEDS_EVIDENCE" {
					t.Errorf("wrong reason: %s", code)
				}
				return
			}
			if err != nil {
				t.Fatalf("claim %s should be accepted against established %s: %v",
					tc.claimed, established.Level, err)
			}
		})
	}
}

// A claim never raises the established level, even when it is accepted.
func TestAcceptedClaimDoesNotRaiseTheEstablishedLevel(t *testing.T) {
	established := emptyBundleVerifier().Resolve(identity.Presented{APIIdentity: "app_1"})

	if err := identity.CheckClaim(intent.AttestationA0, established); err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if established.Level != intent.AttestationA1 {
		t.Errorf("established level changed to %s after checking a claim", established.Level)
	}
}

func TestUnknownClaimedLevelIsRejected(t *testing.T) {
	established := emptyBundleVerifier().Resolve(identity.Presented{APIIdentity: "app_1"})

	err := identity.CheckClaim(intent.AttestationLevel("A9"), established)
	if err == nil {
		t.Fatal("an unknown attestation level was accepted")
	}
	if code := claimCode(t, err); code != "ATTESTATION_LEVEL_INVALID" {
		t.Errorf("wrong reason: %s", code)
	}
}
