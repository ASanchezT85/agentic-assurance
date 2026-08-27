package identity

import (
	"crypto/x509"
	"errors"
	"fmt"
	"time"
)

// Verifier validates X509-SVIDs against a trust bundle.
//
// It uses crypto/x509 rather than a SPIFFE library. Verifying a presented SVID is
// chain validation plus a URI SAN check, which the standard library does correctly
// and without becoming the first dependency of the core. Fetching and rotating SVIDs
// through the Workload API is a different job and belongs to the process that needs
// one, not to this verifier.
type Verifier struct {
	// TrustDomain is the only domain this verifier accepts. An SVID from another
	// trust domain is a valid certificate that is not ours, which is a rejection,
	// not a pass.
	TrustDomain string

	// Bundle holds the trust domain's CA certificates.
	Bundle *x509.CertPool

	// Now is injectable so expiry is testable without waiting. Nil means time.Now.
	Now func() time.Time
}

// VerificationFailure explains why an SVID was rejected. The Code is stable and is
// what evidence and metrics key on.
type VerificationFailure struct {
	Code    string
	Reason  string
	wrapped error
}

func (e *VerificationFailure) Error() string {
	if e.wrapped != nil {
		return fmt.Sprintf("svid rejected [%s]: %s: %v", e.Code, e.Reason, e.wrapped)
	}
	return fmt.Sprintf("svid rejected [%s]: %s", e.Code, e.Reason)
}

func (e *VerificationFailure) Unwrap() error { return e.wrapped }

// VerifiedIdentity is the result of a successful verification. Its existence is the
// evidence: there is no way to construct one except by verifying.
type VerifiedIdentity struct {
	ID         SPIFFEID
	Method     string
	NotAfter   time.Time
	VerifiedAt time.Time
	SerialHex  string
}

const methodX509SVID = "SPIFFE_X509_SVID"

func (v *Verifier) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// Verify validates a presented certificate chain and returns the workload identity
// it establishes.
//
// leaf is the workload's certificate. intermediates may be empty. A non-nil error
// means no identity was established, and the caller must treat the workload as
// unauthenticated (INV-001).
func (v *Verifier) Verify(leaf *x509.Certificate, intermediates []*x509.Certificate) (VerifiedIdentity, error) {
	if leaf == nil {
		return VerifiedIdentity{}, &VerificationFailure{
			Code: "SVID_ABSENT", Reason: "no certificate was presented"}
	}
	if v.Bundle == nil {
		// Fail closed. A verifier with no trust bundle cannot establish anything,
		// and must never behave as though everything checks out.
		return VerifiedIdentity{}, &VerificationFailure{
			Code: "TRUST_BUNDLE_UNAVAILABLE", Reason: "no trust bundle is loaded"}
	}
	if v.TrustDomain == "" {
		return VerifiedIdentity{}, &VerificationFailure{
			Code: "TRUST_DOMAIN_UNCONFIGURED", Reason: "no trust domain is configured"}
	}

	id, err := svidURI(leaf)
	if err != nil {
		return VerifiedIdentity{}, err
	}
	if !id.MemberOf(v.TrustDomain) {
		return VerifiedIdentity{}, &VerificationFailure{
			Code:   "TRUST_DOMAIN_MISMATCH",
			Reason: fmt.Sprintf("SVID is from trust domain %q, this verifier accepts %q", id.TrustDomain, v.TrustDomain),
		}
	}

	pool := x509.NewCertPool()
	for _, c := range intermediates {
		pool.AddCert(c)
	}

	now := v.now()
	_, err = leaf.Verify(x509.VerifyOptions{
		Roots:         v.Bundle,
		Intermediates: pool,
		CurrentTime:   now,
		// An SVID carries its identity in the URI SAN, never in a DNS name, so
		// hostname verification is not applicable and DNSName stays empty.
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	})
	if err != nil {
		return VerifiedIdentity{}, classifyVerifyError(err, leaf, now)
	}

	return VerifiedIdentity{
		ID:         id,
		Method:     methodX509SVID,
		NotAfter:   leaf.NotAfter,
		VerifiedAt: now,
		SerialHex:  fmt.Sprintf("%x", leaf.SerialNumber),
	}, nil
}

// svidURI extracts the single SPIFFE URI SAN an SVID must carry.
func svidURI(leaf *x509.Certificate) (SPIFFEID, error) {
	var found []SPIFFEID
	for _, u := range leaf.URIs {
		id, err := spiffeIDFromURL(u)
		if err != nil {
			continue
		}
		found = append(found, id)
	}

	switch len(found) {
	case 0:
		return SPIFFEID{}, &VerificationFailure{
			Code: "SVID_WITHOUT_SPIFFE_ID", Reason: "certificate carries no valid SPIFFE URI SAN"}
	case 1:
		return found[0], nil
	default:
		// Two identities in one certificate is ambiguous, and ambiguity in an
		// identity is indistinguishable from an attempt to be two things at once.
		return SPIFFEID{}, &VerificationFailure{
			Code:   "SVID_MULTIPLE_SPIFFE_IDS",
			Reason: fmt.Sprintf("certificate carries %d SPIFFE IDs, an SVID must carry exactly one", len(found)),
		}
	}
}

// classifyVerifyError turns an x509 verification failure into a stable code, so
// "expired" and "untrusted issuer" are distinguishable in evidence and metrics.
func classifyVerifyError(err error, leaf *x509.Certificate, now time.Time) error {
	var invalid x509.CertificateInvalidError
	if errors.As(err, &invalid) && invalid.Reason == x509.Expired {
		switch {
		case now.After(leaf.NotAfter):
			return &VerificationFailure{
				Code: "SVID_EXPIRED", wrapped: err,
				Reason: "SVID expired at " + leaf.NotAfter.UTC().Format(time.RFC3339)}
		default:
			return &VerificationFailure{
				Code: "SVID_NOT_YET_VALID", wrapped: err,
				Reason: "SVID is not valid until " + leaf.NotBefore.UTC().Format(time.RFC3339)}
		}
	}

	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return &VerificationFailure{
			Code: "SVID_UNTRUSTED_ISSUER", wrapped: err,
			Reason: "SVID does not chain to the configured trust bundle"}
	}

	return &VerificationFailure{
		Code: "SVID_INVALID", wrapped: err, Reason: "chain verification failed"}
}
