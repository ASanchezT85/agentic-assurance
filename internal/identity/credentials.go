package identity

import (
	"crypto/subtle"
	"crypto/x509"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Credentials authenticates API callers and says which tenant each one speaks for.
//
// This is not a credential management system and does not pretend to be. It is the
// smallest thing that makes an endpoint honestly authenticated. Rotation, scoping and
// storage belong with a secret manager (spec section 35).
//
// The tenant binding is the part that matters, and it was missing. A credential that
// only proved "this is svc_a" left the tenant to come from the request, so an
// authenticated caller could name any tenant they liked and every tenant-scoped
// lookup after that — the authority grant, the policy bundle, the idempotency record,
// the row level security setting — obediently used it. The database half of INV-007
// was enforced and correct the whole time: it isolated to the tenant it was given, and
// nobody had ever established which tenant that should be.
//
// A credential is issued to a tenant. That is where a caller's tenant comes from now,
// and a request that claims a different one is refused.
type Credentials struct {
	// byToken maps a bearer token to the identity and tenant it authenticates.
	byToken map[string]Caller
}

// Caller is an authenticated API identity and the tenant it speaks for.
type Caller struct {
	Identity string
	TenantID string

	// MayIssueAuthority separates submitting an intent from issuing the authority to
	// submit one. P-002 says the customer retains final authority, and a credential
	// that could do both would let an agent widen its own grant: the ceiling INV-002
	// enforces would be one the party under it can raise.
	//
	// Off by default. An issuer is named explicitly in GATEWAY_GRANT_ISSUERS, so the
	// privilege is something an operator granted rather than something every
	// credential happened to have.
	MayIssueAuthority bool
}

// AllowIssuers marks the named identities as able to issue authority grants.
//
// A separate list rather than a field in the credential string, because the
// credential format is what an operator copies between environments and adding a
// privilege flag to it makes the dangerous case the easy typo.
func (c *Credentials) AllowIssuers(raw string) {
	if c == nil {
		return
	}
	allowed := map[string]bool{}
	for _, name := range strings.Split(raw, ",") {
		if name = strings.TrimSpace(name); name != "" {
			allowed[name] = true
		}
	}
	for token, caller := range c.byToken {
		if allowed[caller.Identity] {
			caller.MayIssueAuthority = true
			c.byToken[token] = caller
		}
	}
}

// Tenants returns every tenant this registry serves, once each.
//
// Used by housekeeping that has to run per tenant because row level security is per
// tenant. A tenant removed from the registry stops being swept, which is the honest
// consequence of having no other list of who exists.
func (c *Credentials) Tenants() []string {
	if c == nil {
		return nil
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(c.byToken))
	for _, caller := range c.byToken {
		if !seen[caller.TenantID] {
			seen[caller.TenantID] = true
			out = append(out, caller.TenantID)
		}
	}
	sort.Strings(out)
	return out
}

// ParseCredentials reads "identity@tenant=token,identity@tenant=token".
func ParseCredentials(raw string) (*Credentials, error) {
	c := &Credentials{byToken: map[string]Caller{}}

	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}

		who, token, ok := strings.Cut(pair, "=")
		if !ok || token == "" {
			return nil, errors.New("malformed credential entry; expected identity@tenant=token")
		}

		name, tenant, ok := strings.Cut(strings.TrimSpace(who), "@")
		if !ok {
			return nil, fmt.Errorf(
				"credential %q names no tenant. A credential that only proves who is "+
					"calling leaves the tenant to come from the request, and an "+
					"authenticated caller could then act on any tenant they name "+
					"(INV-007)", who)
		}
		if !identifierShaped(name) || !identifierShaped(tenant) {
			return nil, fmt.Errorf("credential %q: identity and tenant must be identifier-shaped", who)
		}
		if len(token) < 32 {
			// A short bearer token is a guessable one, and this is the only thing
			// standing between an unauthenticated caller and an authenticated action.
			return nil, fmt.Errorf("credential for %q is shorter than 32 characters", name)
		}

		c.byToken[token] = Caller{Identity: name, TenantID: tenant}
	}

	if len(c.byToken) == 0 {
		return nil, errors.New("no credentials configured")
	}
	return c, nil
}

// Identify returns the caller a bearer token authenticates.
//
// The comparison is constant-time over every configured token rather than a map
// lookup, because a map lookup leaks through timing which prefix was right.
func (c *Credentials) Identify(token string) (Caller, bool) {
	if c == nil || token == "" {
		return Caller{}, false
	}
	var matched Caller
	found := false
	for candidate, caller := range c.byToken {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(token)) == 1 {
			matched, found = caller, true
		}
	}
	return matched, found
}

// FromTransport builds a Presented from what a transport established.
//
// It takes the pieces rather than an *http.Request, because this package is on the
// enforcement path and INV-005 forbids it from importing net/http: local enforcement
// has to survive the loss of everything outside the process, and a package that can
// reach the network is one that might. The guard caught this file the first time and
// it was right to.
//
// It never reads a request body. The body's claimed attestation level and tenant are
// checked against what this returns, never the other way round (INV-001, INV-007).
func FromTransport(authorization string, peerCertificates []*x509.Certificate, creds *Credentials) Presented {
	var p Presented

	// Both, when both are present. Resolve prefers a verified SVID and degrades to
	// whatever else the caller established when it does not verify — and that
	// fallback needs the credential to have been read.
	//
	// This used to return as soon as it saw a certificate, so a caller behind a
	// service mesh, which presents one on every connection, was A0 with a perfectly
	// good bearer token in the request. Nobody hit it while A2 was unreachable;
	// making A2 reachable made it a real lockout.
	if len(peerCertificates) > 0 {
		p.SVID = peerCertificates[0]
		if len(peerCertificates) > 1 {
			p.Intermediates = peerCertificates[1:]
		}
	}

	if token, ok := strings.CutPrefix(authorization, "Bearer "); ok {
		if caller, authenticated := creds.Identify(strings.TrimSpace(token)); authenticated {
			p.APIIdentity = caller.Identity
			p.TenantID = caller.TenantID
			p.MayIssueAuthority = caller.MayIssueAuthority
		}
	}
	return p
}

// RequireTenant checks that a claimed tenant is the one the caller was authenticated
// for.
//
// An established identity with no tenant is refused rather than waved through. That is
// the SVID case today: a workload certificate proves which workload is calling and the
// platform has no mapping from a SPIFFE ID to a customer, so there is nothing to check
// the claim against. Trusting the request in that case would reintroduce exactly the
// hole this function exists to close, and it is better to say the mapping is missing.
func RequireTenant(a Attested, claimed string) error {
	if claimed == "" {
		return errors.New("no tenant was claimed")
	}
	if a.TenantID == "" {
		if !a.SpiffeID.IsZero() {
			return fmt.Errorf(
				"workload %s is not mapped to a tenant. A workload certificate proves "+
					"which workload is calling and says nothing about which customer it "+
					"acts for; add it to the workload registry rather than letting the "+
					"request name one", a.SpiffeID.String())
		}
		return fmt.Errorf(
			"the caller was authenticated as %q and no tenant is established for it",
			a.Method)
	}
	if a.TenantID != claimed {
		// Deliberately does not say which tenant the caller belongs to. Spec section
		// 45 lists cross-tenant leakage as a threat, and an error that names the
		// caller's own tenant next to the one they guessed is a probe with feedback.
		return errors.New("the request names a tenant this caller is not authenticated for")
	}
	return nil
}

func identifierShaped(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}
