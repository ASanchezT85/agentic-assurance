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
func TestTheChainDetectsATamperedArchive(t *testing.T) {
	at := time.Now().UTC()
	events := []evidence.Event{
		{EventID: "e1", EventName: evidence.IntentReceived, TenantID: "t",
			AggregateID: "env_1", CorrelationID: "corr_1", Sequence: 1, OccurredAt: at},
		{EventID: "e2", EventName: evidence.AuthorityEvaluated, TenantID: "t",
			AggregateID: "env_1", CorrelationID: "corr_1", CausationID: "e1",
			Sequence: 2, OccurredAt: at.Add(time.Millisecond)},
		{EventID: "e3", EventName: evidence.OrderAccepted, TenantID: "t",
			AggregateID: "env_1", CorrelationID: "corr_1", CausationID: "e2",
			Sequence: 3, OccurredAt: at.Add(2 * time.Millisecond)},
	}

	head := ChainOver(events)
	if head == "" {
		t.Fatal("no chain was produced")
	}
	if ChainOver(events) != head {
		t.Error("the chain is not deterministic; a re-export would look like tampering")
	}

	// Somebody edits the middle of the archive.
	tampered := append([]evidence.Event(nil), events...)
	tampered[1].AggregateID = "env_somebody_elses"
	if ChainOver(tampered) == head {
		t.Error("an edited archive produced the same chain head")
	}

	// And removing one is just as visible.
	shorter := []evidence.Event{events[0], events[2]}
	if ChainOver(shorter) == head {
		t.Error("an archive with an event removed produced the same chain head")
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
