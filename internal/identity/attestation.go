package identity

import (
	"crypto/x509"
	"fmt"
	"time"

	"agentic-assurance/internal/intent"
)

// Presented is what a caller actually showed up with. It is evidence, not assertion:
// nothing here comes from the envelope body.
type Presented struct {
	// SVID is the workload certificate from the transport layer, if any.
	SVID          *x509.Certificate
	Intermediates []*x509.Certificate

	// APIIdentity is an authenticated application or API identity established by
	// some other means. It reaches A1: we know which registered caller this is,
	// but nothing attests the workload it runs in.
	APIIdentity string
}

// Attested is the level the platform established, together with why it is not
// higher. It is produced only by Resolve, so a caller cannot assemble one by hand
// and hand it to policy.
type Attested struct {
	Level       intent.AttestationLevel
	SpiffeID    SPIFFEID
	APIIdentity string
	Method      string
	VerifiedAt  time.Time
	SerialHex   string

	// Downgrade records why a stronger level was not reached. It is nil when
	// nothing was attempted, and non-nil when something was attempted and failed.
	// Evidence keeps this: "no SVID presented" and "SVID expired" are different
	// operational stories.
	Downgrade *VerificationFailure
}

// levelRank orders the taxonomy so that claims can be compared to evidence.
func levelRank(l intent.AttestationLevel) int {
	switch l {
	case intent.AttestationA0:
		return 0
	case intent.AttestationA1:
		return 1
	case intent.AttestationA2:
		return 2
	case intent.AttestationA3:
		return 3
	default:
		return -1
	}
}

// Resolve establishes the attestation level from evidence alone.
//
// It always returns a result. A0 is a real, permitted state: unknown origin. What A0
// cannot do is authorize an executable order, which is a separate decision made by
// RequireExecutable and, later, by authority evaluation.
//
// A3 is never produced. V0 has no provider attestation mechanism, and manufacturing
// the level from a workload certificate is exactly the inference ADR-006 forbids.
func (v *Verifier) Resolve(p Presented) Attested {
	now := v.now()

	if p.SVID != nil {
		verified, err := v.Verify(p.SVID, p.Intermediates)
		if err == nil {
			return Attested{
				Level:       intent.AttestationA2,
				SpiffeID:    verified.ID,
				APIIdentity: p.APIIdentity,
				Method:      verified.Method,
				VerifiedAt:  verified.VerifiedAt,
				SerialHex:   verified.SerialHex,
			}
		}

		// A presented-but-invalid SVID does not fall back to A2 with a warning. It
		// falls back to whatever the caller independently established, carrying the
		// reason.
		failure, _ := err.(*VerificationFailure)
		return degraded(p, now, failure)
	}

	return degraded(p, now, nil)
}

func degraded(p Presented, now time.Time, failure *VerificationFailure) Attested {
	if p.APIIdentity != "" {
		return Attested{
			Level:       intent.AttestationA1,
			APIIdentity: p.APIIdentity,
			Method:      "AUTHENTICATED_API_IDENTITY",
			VerifiedAt:  now,
			Downgrade:   failure,
		}
	}
	return Attested{
		Level:      intent.AttestationA0,
		Method:     "NONE",
		VerifiedAt: now,
		Downgrade:  failure,
	}
}

// ClaimError is raised when an envelope claims more identity than the platform
// established.
type ClaimError struct {
	Claimed     intent.AttestationLevel
	Established intent.AttestationLevel
	Code        string
}

func (e *ClaimError) Error() string {
	return fmt.Sprintf("attestation claim rejected [%s]: envelope claims %s, evidence establishes %s",
		e.Code, e.Claimed, e.Established)
}

// CheckClaim compares what the envelope says about itself to what was established.
//
// A claim at or below the established level is accepted, and the established level
// is what the rest of the system uses either way: the envelope never raises its own
// attestation. A claim above it is rejected outright rather than quietly lowered,
// because an agent asserting an identity it cannot demonstrate is a security event,
// not a formatting problem.
func CheckClaim(claimed intent.AttestationLevel, established Attested) error {
	claimedRank := levelRank(claimed)
	if claimedRank < 0 {
		return &ClaimError{Claimed: claimed, Established: established.Level,
			Code: "ATTESTATION_LEVEL_INVALID"}
	}
	if claimedRank > levelRank(established.Level) {
		return &ClaimError{Claimed: claimed, Established: established.Level,
			Code: "ATTESTATION_CLAIM_EXCEEDS_EVIDENCE"}
	}
	return nil
}

// RequireExecutable enforces INV-001: an unauthenticated workload can never create
// an executable order.
//
// A0 means unknown origin. It is a legitimate state to observe and record; it is not
// a state from which money moves.
func RequireExecutable(a Attested) error {
	if levelRank(a.Level) < levelRank(intent.AttestationA1) {
		return &ClaimError{Claimed: a.Level, Established: a.Level,
			Code: "UNAUTHENTICATED_WORKLOAD"}
	}
	return nil
}

// ModelClaims returns the runtime claims unchanged.
//
// It exists to make INV-014 a visible, testable seam rather than an absence. A
// verified workload identity says nothing about which model ran inside it, so the
// only correct transformation here is none. If a future phase is tempted to enrich
// these claims from identity evidence, it has to change this function, and the test
// that guards it will say why it must not.
func ModelClaims(_ Attested, claims intent.RuntimeClaims) intent.RuntimeClaims {
	return claims
}
