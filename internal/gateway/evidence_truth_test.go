package gateway

import (
	"context"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

// Evidence may not assert a venue fact the platform does not have.
//
// The receipt used to record broker.order.submitted.v1 before the broker was called.
// Everything downstream — a customer's audit export, a regulator's reconstruction, an
// incident timeline — then read an intention as a submission, and the two differ in
// exactly the cases that matter: the claim refused, the receipt would not commit, the
// process died between the two.
//
// These names are claims about a venue. Nothing may write one on a path where no order
// left the platform.
var venueClaims = []evidence.EventName{
	evidence.OrderSubmitted,
	evidence.OrderAccepted,
	evidence.OrderRejected,
	evidence.SubmissionAttempted,
}

func claimed(names []string) []string {
	var found []string
	for _, n := range names {
		for _, c := range venueClaims {
			if n == string(c) {
				found = append(found, n)
			}
		}
	}
	return found
}

// failingEvidence refuses the receipt, which is the case that produced the defect:
// the decision cannot be committed, so nothing is sent.
type failingEvidence struct{ memEvidence }

func (f *failingEvidence) AppendBatch(context.Context, []evidence.Event) error {
	return constError("the evidence store is unavailable")
}

func TestEvidenceDoesNotClaimAnUnsentOrder(t *testing.T) {
	t.Run("the receipt cannot commit", func(t *testing.T) {
		p, fake, _ := harness(t)
		ev := &failingEvidence{}
		p.Evidence = ev

		result := p.Submit(context.Background(), envelope(nil), presentedAPI())

		if result.Accepted {
			t.Fatal("an intent was accepted although its decision was never recorded")
		}
		orders, err := fake.GetOrders(context.Background(), at.Add(-time.Hour))
		if err != nil {
			t.Fatalf("read the venue: %v", err)
		}
		if len(orders) != 0 {
			t.Fatalf("%d orders reached the venue after the receipt failed", len(orders))
		}
		if got := claimed(ev.names()); len(got) > 0 {
			t.Errorf("evidence claims %v although no order was sent", got)
		}
	})

	t.Run("the envelope was already used", func(t *testing.T) {
		p, _, ev := harness(t)

		first := p.Submit(context.Background(), envelope(nil), presentedAPI())
		if !first.Accepted {
			t.Fatalf("the first submission was refused: %s", first.Code)
		}
		before := len(ev.all())

		// Same envelope, a different idempotency key: the claim refuses at the durable
		// boundary and the venue is never called.
		reused := envelope(func(m map[string]any) {
			m["idempotency_key"] = "idem-01J8Z3K9QX"
		})
		second := p.Submit(context.Background(), reused, presentedAPI())
		if second.Accepted {
			t.Fatal("a reused envelope was accepted")
		}

		after := ev.names()[before:]
		for _, n := range after {
			if n == string(evidence.SubmissionAttempted) || n == string(evidence.OrderSubmitted) {
				t.Errorf("evidence records %s for a refused claim; got %v", n, after)
			}
		}
	})
}

// A successful reservation leaves a record of its own.
//
// authority_usage is a row that moves from RESERVED to COMMITTED or RELEASED, so it
// answers where a reservation ended and not what was authorized at the moment of
// authorizing. Capacity held against a customer's grant is a decision about their money
// and belongs in the append-only account.
func TestAReservationIsEvidence(t *testing.T) {
	p, _, ev := harness(t)

	result := p.Submit(context.Background(), envelope(nil), presentedAPI())
	if !result.Accepted {
		t.Fatalf("refused at %s: %s", result.Stage, result.Code)
	}

	var reserved *evidence.Event
	var settled bool
	all := ev.all()
	for i, e := range all {
		switch e.EventName {
		case evidence.AuthorityReserved:
			reserved = &all[i]
		case evidence.AuthorityReservationCommitted, evidence.AuthorityReservationReleased:
			settled = true
		}
	}
	if reserved == nil {
		t.Fatalf("capacity was held with no evidence of it; got %v", ev.names())
	}
	if !settled {
		t.Fatalf("the reservation was settled with no evidence of it; got %v", ev.names())
	}

	// The amount held, not merely the fact that something was.
	if _, ok := reserved.Payload["reserved_notional"]; !ok {
		t.Errorf("the reservation record does not say how much was held: %v", reserved.Payload)
	}
}
