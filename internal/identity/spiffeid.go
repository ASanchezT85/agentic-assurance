// Package identity verifies who a workload is.
//
// It answers exactly one question: which workload presented this connection, and how
// strongly is that established. It never answers which model produced the reasoning
// inside that workload (ADR-006, INV-014). Those are different facts and the second
// one is not derivable from the first.
package identity

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// SPIFFEID is a parsed, validated SPIFFE identifier.
//
// The zero value is not a valid identity, and there is no constructor that produces
// one from an unvalidated string except ParseSPIFFEID, which returns an error rather
// than a best guess.
type SPIFFEID struct {
	TrustDomain string
	Path        string
}

func (s SPIFFEID) String() string {
	if s.TrustDomain == "" {
		return ""
	}
	return "spiffe://" + s.TrustDomain + s.Path
}

// IsZero reports whether this is the empty identity.
func (s SPIFFEID) IsZero() bool { return s.TrustDomain == "" }

// MemberOf reports whether this identity belongs to a trust domain.
func (s SPIFFEID) MemberOf(trustDomain string) bool {
	return s.TrustDomain != "" && s.TrustDomain == strings.ToLower(trustDomain)
}

// ErrNotASPIFFEID is returned when a URI is structurally not a SPIFFE identifier.
var ErrNotASPIFFEID = errors.New("not a SPIFFE ID")

// ParseSPIFFEID validates a SPIFFE ID against the shape the specification requires.
//
// The rules are narrow on purpose. Every relaxation here becomes a way to present an
// identity that looks like one workload to us and another to something downstream.
func ParseSPIFFEID(raw string) (SPIFFEID, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return SPIFFEID{}, fmt.Errorf("%w: unparseable: %v", ErrNotASPIFFEID, err)
	}
	return spiffeIDFromURL(u)
}

func spiffeIDFromURL(u *url.URL) (SPIFFEID, error) {
	if !strings.EqualFold(u.Scheme, "spiffe") {
		return SPIFFEID{}, fmt.Errorf("%w: scheme is %q, want spiffe", ErrNotASPIFFEID, u.Scheme)
	}
	if u.User != nil {
		return SPIFFEID{}, fmt.Errorf("%w: userinfo is not permitted", ErrNotASPIFFEID)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return SPIFFEID{}, fmt.Errorf("%w: query and fragment are not permitted", ErrNotASPIFFEID)
	}
	if u.Port() != "" {
		return SPIFFEID{}, fmt.Errorf("%w: a port is not permitted", ErrNotASPIFFEID)
	}

	td := strings.ToLower(u.Hostname())
	if td == "" {
		return SPIFFEID{}, fmt.Errorf("%w: empty trust domain", ErrNotASPIFFEID)
	}

	path := u.Path
	if path == "" {
		// spiffe://example.org identifies the trust domain itself, not a workload.
		// It must never authenticate a caller.
		return SPIFFEID{}, fmt.Errorf("%w: trust domain ID has no workload path", ErrNotASPIFFEID)
	}
	if !strings.HasPrefix(path, "/") {
		return SPIFFEID{}, fmt.Errorf("%w: path must be absolute", ErrNotASPIFFEID)
	}
	if strings.HasSuffix(path, "/") {
		return SPIFFEID{}, fmt.Errorf("%w: path must not end in a separator", ErrNotASPIFFEID)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		switch segment {
		case "":
			return SPIFFEID{}, fmt.Errorf("%w: empty path segment", ErrNotASPIFFEID)
		case ".", "..":
			return SPIFFEID{}, fmt.Errorf("%w: relative path segment %q", ErrNotASPIFFEID, segment)
		}
	}

	return SPIFFEID{TrustDomain: td, Path: path}, nil
}
