package intent

import (
	"agentic-assurance/internal/canonicaljson"
	"fmt"
)

// Canonical bytes for envelope signing.
//
// An envelope carries a signature field and nothing verified it. The tenant came from
// the transport credential, but `agent_id` was a claim in the body: the platform knew
// which customer was calling and took the agent's word for which agent it was. A
// signature is where that claim stops being caller-supplied data.
//
// # The canonical form, v0.1
//
// Signing pretty-printed JSON signs a formatter. This defines one deterministic
// representation, small enough that an agent in any language can reproduce it:
//
//  1. parse the envelope as JSON, preserving number literals exactly as written;
//  2. remove the top-level "signature" member;
//  3. serialize with object keys sorted by Unicode code point, no insignificant
//     whitespace, and every number emitted verbatim as it appeared in the input.
//
// Numbers are the trap this avoids. Re-encoding 1200 as 1200.0, or 0.1 through a
// float, changes the bytes and breaks a signature that was correct — so the literal
// travels unchanged rather than through a float64.
//
// The algorithm is part of the contract. A change to it is a new version, not a patch,
// and CanonicalVersion is what a producer states it used.
const CanonicalVersion = canonicaljson.Version

// Canonical returns the bytes a signature covers.
//
// The generic canonical form of the envelope, minus its own signature field: a signature
// cannot cover itself. The removal is here, in the domain that needs it, rather than
// inside the canonicalizer — retention hashes event payloads with the same algorithm, and
// a payload with a field called "signature" must keep it.
func Canonical(raw []byte) ([]byte, error) {
	object, err := canonicaljson.CanonicalObject(raw)
	if err != nil {
		return nil, fmt.Errorf("envelope is not a JSON object: %w", err)
	}
	delete(object, "signature")
	return canonicaljson.CanonicalizeObject(object)
}
