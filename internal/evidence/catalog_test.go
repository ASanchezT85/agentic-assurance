package evidence

import (
	"encoding/json"
	"os"
	"testing"
)

// The Go catalog and the published schema are two statements of one closed set. If
// they drift, a producer emits an event the schema permits and the store refuses, or
// worse the other way round.
func TestGoCatalogMatchesThePublishedSchema(t *testing.T) {
	raw, err := os.ReadFile("../../packages/event-schema/schemas/internal-event.v0.1.json")
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}

	var doc struct {
		Properties struct {
			EventName struct {
				Enum []string `json:"enum"`
			} `json:"event_name"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}

	inSchema := map[string]bool{}
	for _, name := range doc.Properties.EventName.Enum {
		inSchema[name] = true
		if !Known(EventName(name)) {
			t.Errorf("the schema permits %q but the Go catalog does not know it", name)
		}
	}

	for _, name := range CatalogNames() {
		if !inSchema[string(name)] {
			t.Errorf("the Go catalog knows %q but the published schema does not permit it", name)
		}
	}
}

func TestSubjectIsTenantScoped(t *testing.T) {
	e := Event{TenantID: "tenant_acme", EventName: IntentReceived}
	if got := e.Subject(); got != "evidence.tenant_acme.agent.intent.received.v1" {
		t.Errorf("subject = %q; the tenant must be in the subject so a consumer can be "+
			"scoped by subscription rather than by remembering to filter", got)
	}
}
