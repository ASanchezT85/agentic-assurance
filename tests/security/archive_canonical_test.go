package security

import (
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/retention"
)

// T3-ARCHIVE: an archive covers the whole event, including a payload field named
// "signature".
//
// The content hash canonicalized payloads through the envelope signer's canonicalizer,
// which deletes a top-level field called "signature" because a signature cannot cover
// itself. Correct for an envelope; wrong for a payload. Two archived events differing
// only in that field hashed identically, so the one field most likely to carry an
// attestation was the one the archive did not protect.

func archiveEvent(payload map[string]any) evidence.Event {
	at := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	return evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       "arch_canonical",
		EventName:     evidence.AuthorityEvaluated,
		TenantID:      "tenant_canonical",
		AggregateID:   "env_canonical",
		CorrelationID: "corr_canonical",
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      1,
		Payload:       payload,
	}
}

func hashOf(t *testing.T, payload map[string]any) string {
	t.Helper()
	sum, err := retention.ContentHash(archiveEvent(payload))
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return sum
}

// T3-ARCHIVE-01: a top-level payload signature is covered.
func TestAPayloadSignatureIsCoveredByTheArchiveHash(t *testing.T) {
	a := hashOf(t, map[string]any{"allowed": true, "signature": "VALUE-A"})
	b := hashOf(t, map[string]any{"allowed": true, "signature": "VALUE-B"})

	if a == b {
		t.Errorf("two events differing only in payload.signature hash identically (%s). "+
			"An archive that omits a field cannot prove the event was not edited, and "+
			"the omitted one is where an attestation lives.", a[:16])
	}
}

// T3-ARCHIVE-02: a nested one is covered too.
func TestANestedSignatureIsCoveredByTheArchiveHash(t *testing.T) {
	a := hashOf(t, map[string]any{"auth": map[string]any{"signature": "A"}})
	b := hashOf(t, map[string]any{"auth": map[string]any{"signature": "B"}})

	if a == b {
		t.Errorf("two events differing only in payload.auth.signature hash identically")
	}
}

// T3-ARCHIVE-03: key order is not an edit.
//
// The other half of the property. A hash that changed when a map was re-serialised would
// report tampering on every faithful archive, and an operator who sees a false alarm
// twice stops reading the alarm.
func TestReorderedKeysStillVerify(t *testing.T) {
	first := hashOf(t, map[string]any{"allowed": true, "grant_id": "g1", "signature": "A"})
	second := hashOf(t, map[string]any{"signature": "A", "grant_id": "g1", "allowed": true})

	if first != second {
		t.Errorf("the same payload written in a different key order hashed differently:\n"+
			"  %s\n  %s", first, second)
	}
}

// And the whole-record property the chain depends on: a change anywhere changes the head.
func TestAChangedPayloadBreaksTheChain(t *testing.T) {
	clean := []evidence.Event{archiveEvent(map[string]any{"allowed": true, "signature": "A"})}
	head, err := retention.ChainOver(clean)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}

	tampered := []evidence.Event{archiveEvent(map[string]any{"allowed": true, "signature": "B"})}
	if _, err := retention.Verify(tampered, head); err == nil {
		t.Error("an archive whose payload signature was rewritten verified against the " +
			"original chain head")
	} else if !strings.Contains(err.Error(), "recomputed") {
		t.Errorf("verification failed with %q; it should say where the chain stopped "+
			"being true", err)
	}
}
