package identity

import (
	"crypto/x509"
	"errors"
	"testing"
	"time"
)

const trustDomain = "acme.example"

func newVerifier(ca *testCA) *Verifier {
	return &Verifier{TrustDomain: trustDomain, Bundle: ca.pool}
}

func failureCode(t *testing.T, err error) string {
	t.Helper()
	var f *VerificationFailure
	if !errors.As(err, &f) {
		t.Fatalf("expected *VerificationFailure, got %T: %v", err, err)
	}
	return f.Code
}

// Exit criterion: a valid workload is accepted.
func TestValidSVIDIsAccepted(t *testing.T) {
	ca := newTestCA(t, "acme root")
	v := newVerifier(ca)

	got, err := v.Verify(ca.svid(t, "spiffe://acme.example/ns/agents/sa/momentum"), nil)
	if err != nil {
		t.Fatalf("expected accepted: %v", err)
	}
	if got.ID.String() != "spiffe://acme.example/ns/agents/sa/momentum" {
		t.Errorf("identity = %q", got.ID.String())
	}
	if got.ID.TrustDomain != trustDomain {
		t.Errorf("trust domain = %q", got.ID.TrustDomain)
	}
	if got.Method != methodX509SVID {
		t.Errorf("method = %q", got.Method)
	}
	if got.SerialHex == "" {
		t.Error("serial not recorded; evidence needs to name the specific certificate")
	}
}

// Exit criterion: forged and expired identities are rejected, each for its own
// stated reason.
func TestInvalidSVIDsAreRejected(t *testing.T) {
	ca := newTestCA(t, "acme root")
	forged := newTestCA(t, "attacker root")

	cases := []struct {
		name string
		cert func(t *testing.T) *x509.Certificate
		now  time.Time
		code string
	}{
		{
			name: "signed by an untrusted CA",
			cert: func(t *testing.T) *x509.Certificate {
				return forged.svid(t, "spiffe://acme.example/ns/agents/sa/momentum")
			},
			code: "SVID_UNTRUSTED_ISSUER",
		},
		{
			name: "expired",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.issue(t, svidOptions{
					uris:      []string{"spiffe://acme.example/ns/agents/sa/momentum"},
					notBefore: time.Now().Add(-4 * time.Hour),
					notAfter:  time.Now().Add(-2 * time.Hour),
				})
			},
			code: "SVID_EXPIRED",
		},
		{
			name: "not yet valid",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.issue(t, svidOptions{
					uris:      []string{"spiffe://acme.example/ns/agents/sa/momentum"},
					notBefore: time.Now().Add(2 * time.Hour),
					notAfter:  time.Now().Add(4 * time.Hour),
				})
			},
			code: "SVID_NOT_YET_VALID",
		},
		{
			name: "no SPIFFE URI at all",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.issue(t, svidOptions{uris: []string{"https://acme.example/agent"}})
			},
			code: "SVID_WITHOUT_SPIFFE_ID",
		},
		{
			name: "two SPIFFE identities in one certificate",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.issue(t, svidOptions{uris: []string{
					"spiffe://acme.example/ns/agents/sa/momentum",
					"spiffe://acme.example/ns/admin/sa/root",
				}})
			},
			code: "SVID_MULTIPLE_SPIFFE_IDS",
		},
		{
			name: "another trust domain",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.svid(t, "spiffe://other.example/ns/agents/sa/momentum")
			},
			code: "TRUST_DOMAIN_MISMATCH",
		},
		{
			name: "trust domain ID with no workload path",
			cert: func(t *testing.T) *x509.Certificate {
				return ca.issue(t, svidOptions{uris: []string{"spiffe://acme.example"}})
			},
			code: "SVID_WITHOUT_SPIFFE_ID",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := newVerifier(ca)
			if !tc.now.IsZero() {
				v.Now = func() time.Time { return tc.now }
			}
			_, err := v.Verify(tc.cert(t), nil)
			if err == nil {
				t.Fatalf("expected rejection with %s", tc.code)
			}
			if got := failureCode(t, err); got != tc.code {
				t.Errorf("expected %s, got %s (%v)", tc.code, got, err)
			}
		})
	}
}

func TestNoCertificateIsRejected(t *testing.T) {
	ca := newTestCA(t, "acme root")
	_, err := newVerifier(ca).Verify(nil, nil)
	if err == nil {
		t.Fatal("expected rejection")
	}
	if got := failureCode(t, err); got != "SVID_ABSENT" {
		t.Errorf("expected SVID_ABSENT, got %s", got)
	}
}

// A verifier without a trust bundle must fail closed. The tempting bug is to treat
// an empty bundle as "nothing to check against, so everything passes".
func TestMissingTrustBundleFailsClosed(t *testing.T) {
	ca := newTestCA(t, "acme root")
	v := &Verifier{TrustDomain: trustDomain}

	_, err := v.Verify(ca.svid(t, "spiffe://acme.example/ns/a/sa/b"), nil)
	if err == nil {
		t.Fatal("a verifier with no trust bundle accepted an SVID")
	}
	if got := failureCode(t, err); got != "TRUST_BUNDLE_UNAVAILABLE" {
		t.Errorf("expected TRUST_BUNDLE_UNAVAILABLE, got %s", got)
	}
}

func TestUnconfiguredTrustDomainFailsClosed(t *testing.T) {
	ca := newTestCA(t, "acme root")
	v := &Verifier{Bundle: ca.pool}

	_, err := v.Verify(ca.svid(t, "spiffe://acme.example/ns/a/sa/b"), nil)
	if err == nil {
		t.Fatal("a verifier with no trust domain accepted an SVID")
	}
	if got := failureCode(t, err); got != "TRUST_DOMAIN_UNCONFIGURED" {
		t.Errorf("expected TRUST_DOMAIN_UNCONFIGURED, got %s", got)
	}
}

// Expiry is checked at verification time, not at issuance. An SVID valid when the
// connection opened is not valid an hour later.
func TestExpiryIsEvaluatedAtVerificationTime(t *testing.T) {
	ca := newTestCA(t, "acme root")
	cert := ca.issue(t, svidOptions{
		uris:      []string{"spiffe://acme.example/ns/a/sa/b"},
		notBefore: time.Now().Add(-time.Hour),
		notAfter:  time.Now().Add(time.Hour),
	})

	v := newVerifier(ca)
	if _, err := v.Verify(cert, nil); err != nil {
		t.Fatalf("should be valid now: %v", err)
	}

	v.Now = func() time.Time { return time.Now().Add(2 * time.Hour) }
	_, err := v.Verify(cert, nil)
	if err == nil {
		t.Fatal("the same SVID verified after its NotAfter")
	}
	if got := failureCode(t, err); got != "SVID_EXPIRED" {
		t.Errorf("expected SVID_EXPIRED, got %s", got)
	}
}

func TestParseSPIFFEID(t *testing.T) {
	valid := []string{
		"spiffe://acme.example/ns/agents/sa/momentum",
		"spiffe://acme.example/a",
		"SPIFFE://ACME.EXAMPLE/a",
	}
	for _, raw := range valid {
		if _, err := ParseSPIFFEID(raw); err != nil {
			t.Errorf("%q should parse: %v", raw, err)
		}
	}

	invalid := []string{
		"https://acme.example/agent",
		"spiffe://acme.example",
		"spiffe:///ns/agents",
		"spiffe://acme.example/",
		"spiffe://acme.example/a//b",
		"spiffe://acme.example/a/../b",
		"spiffe://user@acme.example/a",
		"spiffe://acme.example/a?x=1",
		"spiffe://acme.example/a#frag",
		"spiffe://acme.example:8443/a",
		"",
	}
	for _, raw := range invalid {
		if id, err := ParseSPIFFEID(raw); err == nil {
			t.Errorf("%q should be rejected, got %q", raw, id.String())
		}
	}
}

func TestTrustDomainIsCaseInsensitive(t *testing.T) {
	id, err := ParseSPIFFEID("spiffe://ACME.Example/ns/a")
	if err != nil {
		t.Fatalf("unexpected: %v", err)
	}
	if id.TrustDomain != "acme.example" {
		t.Errorf("trust domain = %q, want lowercased", id.TrustDomain)
	}
	if !id.MemberOf("ACME.EXAMPLE") {
		t.Error("MemberOf should be case insensitive")
	}
	if id.MemberOf("other.example") {
		t.Error("MemberOf matched the wrong trust domain")
	}
}
