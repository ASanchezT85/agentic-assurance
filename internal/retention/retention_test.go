package retention

import (
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

func policy(hot, archive int, mode string) Policy {
	return Policy{TenantID: "t", Class: ClassOrderEvidence,
		HotDays: hot, ArchiveDays: archive, DeletionMode: mode}
}

// The order of the rules is the design. Each of these is a way evidence could be
// destroyed that the platform must refuse regardless of configuration.
func TestAHoldOutranksEveryPolicy(t *testing.T) {
	verdict, reason := Decide(policy(30, 30, DeletionAuthorized), 3650, true, true, true)
	if verdict != VerdictKeep {
		t.Errorf("verdict = %s, want KEEP: a hold was active and %s", verdict, reason)
	}
}

func TestAnUnarchivedPartitionIsNeverDestroyed(t *testing.T) {
	verdict, _ := Decide(policy(30, 30, DeletionAuthorized), 3650, false, false, true)
	if verdict != VerdictArchive {
		t.Errorf("verdict = %s, want ARCHIVE: nothing is destroyed before it is archived", verdict)
	}
}

func TestDestructionNeedsAnApprovedAuthorization(t *testing.T) {
	verdict, reason := Decide(policy(30, 30, DeletionAuthorized), 3650, false, true, false)
	if verdict != VerdictKeep {
		t.Errorf("verdict = %s, want KEEP with no authorization: %s", verdict, reason)
	}

	verdict, _ = Decide(policy(30, 30, DeletionAuthorized), 3650, false, true, true)
	if verdict != VerdictDestroy {
		t.Errorf("verdict = %s, want DESTROY once every condition is met", verdict)
	}
}

// The default answer destroys nothing. A tenant with no policy, or one that never chose
// a deletion mode, keeps everything.
func TestTheDefaultKeepsEverything(t *testing.T) {
	if verdict, _ := Decide(Policy{}, 10000, false, true, true); verdict != VerdictKeep {
		t.Errorf("verdict = %s with no policy, want KEEP", verdict)
	}
	if verdict, _ := Decide(policy(30, 30, DeletionNone), 10000, false, true, true); verdict != VerdictKeep {
		t.Errorf("verdict = %s with deletion NONE, want KEEP", verdict)
	}
	if verdict, _ := Decide(policy(30, 0, DeletionAuthorized), 10000, false, true, true); verdict != VerdictKeep {
		t.Errorf("verdict = %s with archive_days 0, want KEEP: zero means indefinitely", verdict)
	}
}

// The chain is what makes an archived month still evidence.
//
// The first version hashed only identity fields, so an archived authority decision
// could be edited from {"allowed": true} to {"allowed": false} and still verify. The
// test that covered it tampered with an aggregate id, which proved only that identity
// tampering is caught — the audit found the gap by reading what the hash covered rather
// than by trusting the test.
func archived(at time.Time) []evidence.Event {
	return []evidence.Event{
		{SchemaVersion: "0.1", EventID: "e1", EventName: evidence.IntentReceived,
			TenantID: "t", AggregateID: "env_1", CorrelationID: "corr_1", Sequence: 1,
			Producer: "assurance-gateway", OccurredAt: at, ProducedAt: at,
			Payload: map[string]any{"side": "BUY", "notional": 1200}},
		{SchemaVersion: "0.1", EventID: "e2", EventName: evidence.AuthorityEvaluated,
			TenantID: "t", AggregateID: "env_1", CorrelationID: "corr_1", CausationID: "e1",
			Sequence: 2, Producer: "assurance-gateway",
			OccurredAt: at.Add(time.Millisecond), ProducedAt: at.Add(time.Millisecond),
			Payload: map[string]any{"allowed": true, "code": "AUTHORITY_OK"}},
		{SchemaVersion: "0.1", EventID: "e3", EventName: evidence.OrderAccepted,
			TenantID: "t", AggregateID: "env_1", CorrelationID: "corr_1", CausationID: "e2",
			Sequence: 3, Producer: "assurance-gateway",
			OccurredAt: at.Add(2 * time.Millisecond), ProducedAt: at.Add(2 * time.Millisecond),
			Payload: map[string]any{"broker_order_id": "b-1"}},
	}
}

func headOf(t *testing.T, events []evidence.Event) string {
	t.Helper()
	head, err := ChainOver(events)
	if err != nil {
		t.Fatalf("chain: %v", err)
	}
	return head
}

// Every field of an immutable record, and the payload above all.
func TestTheChainCoversTheWholeEvent(t *testing.T) {
	at := time.Now().UTC()
	original := archived(at)
	head := headOf(t, original)

	edits := map[string]func(e *evidence.Event){
		"a payload boolean, allowed true to false": func(e *evidence.Event) {
			e.Payload["allowed"] = false
		},
		"a payload number": func(e *evidence.Event) {
			e.Payload["code"] = "AUTHORITY_OK"
			e.Payload["notional"] = 999999
		},
		"the producer":       func(e *evidence.Event) { e.Producer = "somebody-else" },
		"produced_at":        func(e *evidence.Event) { e.ProducedAt = e.ProducedAt.Add(time.Hour) },
		"occurred_at":        func(e *evidence.Event) { e.OccurredAt = e.OccurredAt.Add(time.Hour) },
		"the schema version": func(e *evidence.Event) { e.SchemaVersion = "9.9" },
		"corrects_event_id":  func(e *evidence.Event) { e.CorrectsEventID = "e_invented" },
		"the aggregate":      func(e *evidence.Event) { e.AggregateID = "env_somebody_elses" },
		"the causation link": func(e *evidence.Event) { e.CausationID = "" },
	}

	for what, edit := range edits {
		t.Run(what, func(t *testing.T) {
			tampered := archived(at)
			edit(&tampered[1])

			if headOf(t, tampered) == head {
				t.Errorf("editing %s produced the same chain head; the archive would verify", what)
			}
			if at, err := Verify(tampered, head); err == nil {
				t.Error("Verify accepted a tampered archive")
			} else if at != len(tampered) {
				t.Logf("verification failed at event %d, which is where an investigation starts", at)
			}
		})
	}
}

// Removing, reordering or duplicating an event is just as visible.
func TestTheChainCoversTheSequenceItself(t *testing.T) {
	at := time.Now().UTC()
	original := archived(at)
	head := headOf(t, original)

	shorter := []evidence.Event{original[0], original[2]}
	if headOf(t, shorter) == head {
		t.Error("an archive with an event removed produced the same head")
	}

	reordered := []evidence.Event{original[1], original[0], original[2]}
	if headOf(t, reordered) == head {
		t.Error("a reordered archive produced the same head")
	}

	duplicated := []evidence.Event{original[0], original[1], original[1], original[2]}
	if headOf(t, duplicated) == head {
		t.Error("an archive with an event duplicated produced the same head")
	}
}

// And the other half: honest re-exports must not look like tampering.
func TestTheChainIsStableAcrossReExport(t *testing.T) {
	at := time.Now().UTC()
	first := headOf(t, archived(at))

	if second := headOf(t, archived(at)); second != first {
		t.Error("re-exporting identical evidence produced a different head")
	}

	// The same payload with its keys built in another order. A map has no order in
	// Go, but a payload arriving from JSON might, and canonicalisation is what makes
	// that a non-event.
	reordered := archived(at)
	reordered[1].Payload = map[string]any{"code": "AUTHORITY_OK", "allowed": true}
	if headOf(t, reordered) != first {
		t.Error("key order changed the chain; an honest re-export would look tampered with")
	}

	if failedAt, err := Verify(archived(at), first); err != nil || failedAt != -1 {
		t.Errorf("an untouched archive did not verify: at=%d err=%v", failedAt, err)
	}
}

// An event nobody classified is kept the longest, not the shortest.
func TestAnUnknownEventFallsIntoTheLongestLivedClass(t *testing.T) {
	if got := ClassOf(evidence.EventName("something.new.v1")); got != ClassOrderEvidence {
		t.Errorf("class = %s, want %s", got, ClassOrderEvidence)
	}
	if got := ClassOf(evidence.PolicyEvaluated); got != ClassPolicyDecision {
		t.Errorf("class = %s, want %s", got, ClassPolicyDecision)
	}
}
