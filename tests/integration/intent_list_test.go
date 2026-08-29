//go:build integration

package integration

import (
	"context"
	"fmt"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
)

// GET /v1/intents lists refusals too, which is the reason it is built from evidence
// rather than from the idempotency table: that table holds intents that reached a
// venue, so a list built from it would show what was accepted and omit every refusal.
func TestRecentIntentsIncludeRefusals(t *testing.T) {
	store := evidence.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	tenant := fmt.Sprintf("tenant_list_%d", at.UnixNano())

	write := func(envelopeID string, name evidence.EventName, seq int64, payload map[string]any) {
		t.Helper()
		if _, err := store.Append(ctx, evidence.Event{
			SchemaVersion: evidence.SchemaVersion,
			EventID:       fmt.Sprintf("%s_%s_%d", envelopeID, name, seq),
			EventName:     name,
			TenantID:      tenant,
			AggregateID:   envelopeID,
			CorrelationID: "corr_" + envelopeID,
			OccurredAt:    at,
			ProducedAt:    at,
			Producer:      "assurance-gateway",
			Sequence:      seq,
			Payload:       payload,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	accepted := fmt.Sprintf("env_ok_%d", at.UnixNano())
	write(accepted, evidence.IntentReceived, 1, map[string]any{"side": "BUY"})
	write(accepted, evidence.OrderAccepted, 2, map[string]any{"broker_order_id": "b-1"})

	refused := fmt.Sprintf("env_no_%d", at.UnixNano())
	write(refused, evidence.IntentReceived, 1, map[string]any{"side": "SELL"})
	write(refused, evidence.AuthorityEvaluated, 2, map[string]any{"code": "PER_ORDER_LIMIT_EXCEEDED"})

	// Something that is not an intent at all. It writes evidence like everything else,
	// and a list of intents that included it would be a list of aggregates wearing the
	// wrong name.
	control := fmt.Sprintf("ctl_%d", at.UnixNano())
	write(control, evidence.ControlRevoked, 1, map[string]any{"actor": "ops@example"})

	events, err := store.RecentAggregates(ctx, tenant, at.Add(-time.Hour), 50)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}

	seen := map[string]bool{}
	for _, e := range events {
		seen[e.AggregateID] = true
	}
	if !seen[accepted] {
		t.Error("an accepted intent is missing from the list")
	}
	if !seen[refused] {
		t.Error("a refused intent is missing from the list; the list would show only " +
			"what reached a venue")
	}
	if seen[control] {
		t.Error("a revoked control is listed as an intent")
	}
}

// Another tenant's intents are not in this tenant's list (INV-007).
func TestRecentIntentsAreTenantScoped(t *testing.T) {
	store := evidence.NewStore(idemPool(t))
	ctx := context.Background()
	at := time.Now().UTC()
	mine := fmt.Sprintf("tenant_lista_%d", at.UnixNano())
	theirs := fmt.Sprintf("tenant_listb_%d", at.UnixNano())
	envelopeID := fmt.Sprintf("env_iso_%d", at.UnixNano())

	if _, err := store.Append(ctx, evidence.Event{
		SchemaVersion: evidence.SchemaVersion,
		EventID:       envelopeID + "_received",
		EventName:     evidence.IntentReceived,
		TenantID:      mine,
		AggregateID:   envelopeID,
		CorrelationID: "corr_" + envelopeID,
		OccurredAt:    at,
		ProducedAt:    at,
		Producer:      "assurance-gateway",
		Sequence:      1,
	}); err != nil {
		t.Fatalf("append: %v", err)
	}

	events, err := store.RecentAggregates(ctx, theirs, at.Add(-time.Hour), 50)
	if err != nil {
		t.Fatalf("recent: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("another tenant listed %d of this tenant's intents (INV-007)", len(events))
	}
}
