//go:build integration

package integration

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/incident"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// The same contract, for the surfaces the fleet engine feeds.
//
// The gateway's half of this is checked against whatever a seeded tenant happens to have.
// The fleet engine's collections cannot be checked that way: a measurement appears only
// after a window closes, an incident only after a rule fires, and a test that waits for
// either is a test that skips on a quiet afternoon. A contract test that quietly checks
// nothing is the failure it exists to prevent.
//
// So this seeds one row of each kind into the analytical store and asks the engine for it.
// The field names are not declared anywhere in Go: the handlers pass ClickHouse's
// JSONEachRow output through, so the contract is the column list inside a SQL string. That
// is precisely the kind of contract that drifts without anyone noticing, because renaming
// a column and its SELECT is a self-consistent change that leaves the console reading a
// field nobody sends.

func TestTheConsoleFleetFieldsExistInLiveResponses(t *testing.T) {
	base := os.Getenv("FLEET_ENGINE_URL")
	token := os.Getenv("CONSOLE_API_TOKEN")
	// The tenant comes from the credential, never from the request, so the seed has to
	// land in the tenant the token names. Passing it in is the only way for a test to
	// know which one that is without holding the credential registry.
	tenant := os.Getenv("CONSOLE_TENANT_ID")
	if base == "" || token == "" || tenant == "" {
		t.Skip("set FLEET_ENGINE_URL, CONSOLE_API_TOKEN and CONSOLE_TENANT_ID (the tenant " +
			"that token names) against a running fleet engine")
	}

	ctx := context.Background()
	sink := clickhouse(t)
	now := time.Now().UTC()

	seedMeasurement(t, ctx, sink, tenant, now)
	seedDependency(t, ctx, sink, tenant, now)

	types := consoleRowTypes(t)

	cases := []struct {
		path       string
		collection string
		rowType    string
	}{
		{"/v1/fleet/state", "rows", "FleetMeasurement"},
		{"/v1/dependencies", "rows", "DependencyRow"},
		{"/v1/cohorts", "rows", "CohortRow"},
	}

	for _, c := range cases {
		name := strings.ReplaceAll(strings.TrimPrefix(c.path, "/v1/"), "/", "_")
		t.Run(name, func(t *testing.T) {
			required, ok := types[c.rowType]
			if !ok {
				t.Fatalf("the console declares no type %s; this test is out of date", c.rowType)
			}

			rows := fetchTenantRows(t, base+c.path, token, tenant, c.collection)
			if len(rows) == 0 {
				t.Fatalf("%s returned no rows for a tenant this test seeded. Either the "+
					"seed did not land or the endpoint does not read what was written — "+
					"and both are worth knowing, which is why this fails rather than "+
					"skips.", c.path)
			}

			seen := map[string]bool{}
			for _, row := range rows {
				for key := range row {
					seen[key] = true
				}
			}

			var missing []string
			for _, field := range required {
				if !seen[field] {
					missing = append(missing, field)
				}
			}
			sort.Strings(missing)

			if len(missing) > 0 {
				t.Errorf("the console reads %v from %s and no row carries them. Present: "+
					"%v.\n\nThese field names live in a SQL column list on one side and a "+
					"TypeScript type on the other, with nothing tying them together. A "+
					"field the console expects and the engine does not send renders as "+
					"blank, and a blank cell on this product reads as \"nothing "+
					"happened\".", missing, c.path, sortedKeys(seen))
			}
		})
	}
}

// seedMeasurement writes one measurement so the fleet surface has something to answer
// with, without waiting for a window to close.
func seedMeasurement(t *testing.T, ctx context.Context, sink *fleet.Sink, tenant string,
	now time.Time) {

	t.Helper()
	err := sink.InsertMeasurements(ctx, []fleet.Measurement{{
		TenantID: tenant,
		Cohort: fleet.Cohort{
			TenantID:   tenant,
			Predicates: []fleet.Predicate{{Field: "agent_id", Value: "agent_fields"}},
		},
		Window: fleet.Window{
			Start: now.Add(-time.Minute),
			End:   now,
		},
		IntentCount:          3,
		AgentCount:           2,
		AuthorizedIntents:    2,
		RefusedIntents:       1,
		GrossNotional:        1200,
		NetNotional:          400,
		DirectionalImbalance: 0.33,
		ModelCoverage:        1,
		FeedCoverage: fleet.Coverage{
			Observed: 1, Verified: 0.5, Declared: 0.5, Unknown: 0,
		},
	}})
	if err != nil {
		t.Fatalf("seed a measurement: %v", err)
	}
}

// seedDependency writes one dependency observation, through the same path the ingest
// writes it.
func seedDependency(t *testing.T, ctx context.Context, sink *fleet.Sink, tenant string,
	now time.Time) {

	t.Helper()
	notional := money.MustParse("1200")
	envelope := &intent.AgentExecutionEnvelope{
		SchemaVersion:  "0.1",
		EnvelopeID:     fmt.Sprintf("env_fields_%d", now.UnixNano()),
		IdempotencyKey: fmt.Sprintf("idem_fields_%d", now.UnixNano()),
		CorrelationID:  fmt.Sprintf("corr_fields_%d", now.UnixNano()),
		TenantID:       tenant,
		ReceivedAt:     now,
		Principal:      intent.Principal{PrincipalID: "prin_fields", AccountID: "acct_fields"},
		Agent:          intent.Agent{AgentID: "agent_fields"},
		Dependencies: []intent.Dependency{{
			Type:         "MARKET_DATA",
			ID:           "feed-fields",
			Verification: "DECLARED",
			ObservedAt:   now,
		}},
		Intent: intent.Intent{
			InstrumentID: "instr_us_equity_00206R102",
			AssetClass:   intent.AssetEquity,
			Side:         intent.SideBuy,
			OrderType:    intent.OrderMarket,
			Notional:     &notional,
			TimeInForce:  intent.TIFDay,
		},
	}
	if err := sink.InsertDependencies(ctx, []*intent.AgentExecutionEnvelope{envelope}); err != nil {
		t.Fatalf("seed a dependency: %v", err)
	}
	// Cohorts are derived from observed intents, so the tenant needs one.
	if err := sink.InsertIntents(ctx, []*intent.AgentExecutionEnvelope{envelope}, nil); err != nil {
		t.Fatalf("seed an intent: %v", err)
	}
}

// fetchTenantRows asks an endpoint for one tenant's rows.
//
// The tenant comes from the credential, so the seeded tenant has to be the one the
// credential names. Where it is not, the rows belong to somebody else and the test says
// so rather than checking a stranger's fields.
func fetchTenantRows(t *testing.T, url, token, tenant, collection string) []map[string]any {
	t.Helper()

	rows := fetchRows(t, url, token, collection)
	var mine []map[string]any
	for _, row := range rows {
		if id, ok := row["tenant_id"].(string); !ok || id == tenant {
			mine = append(mine, row)
		}
	}
	if len(rows) > 0 && len(mine) == 0 {
		t.Fatalf("%s answered with rows for another tenant only. The credential names a "+
			"different tenant from the one this test seeded, so nothing here is being "+
			"checked.", url)
	}
	return mine
}

var _ = http.StatusOK

// Incidents, seeded through the store rather than waited for.
//
// An incident appears when a detection rule fires over a measured window, which a test
// cannot arrange on demand without fabricating the fleet behaviour that would cause one.
// Seeding the record directly checks what this test is about — that the fields the
// console reads are the fields the API sends — without pretending to have reproduced an
// incident.
func TestTheConsoleIncidentFieldsExistInLiveResponses(t *testing.T) {
	base := os.Getenv("FLEET_ENGINE_URL")
	token := os.Getenv("CONSOLE_API_TOKEN")
	tenant := os.Getenv("CONSOLE_TENANT_ID")
	if base == "" || token == "" || tenant == "" {
		t.Skip("set FLEET_ENGINE_URL, CONSOLE_API_TOKEN and CONSOLE_TENANT_ID")
	}

	ctx := context.Background()
	now := time.Now().UTC()

	store := incident.NewStore(idemPool(t))
	opened, err := store.Open(ctx, incident.Incident{
		IncidentID:    fmt.Sprintf("inc_fields_%d", now.UnixNano()),
		TenantID:      tenant,
		CorrelationID: fmt.Sprintf("corr_inc_fields_%d", now.UnixNano()),
		Cohort: fleet.Cohort{
			TenantID:   tenant,
			Predicates: []fleet.Predicate{{Field: "agent_id", Value: "agent_fields"}},
		},
		Window:   fleet.Window{Start: now.Add(-time.Minute), End: now},
		Severity: incident.SeverityHigh,
		Status:   incident.StatusOpen,
		Anomalies: []incident.Anomaly{{
			Rule:        "a burst of one-directional flow",
			Observation: "seeded by the console field contract test",
			DetectedAt:  now,
		}},
		SharedDependencies: []string{"feed-fields"},
		Recommended:        "THROTTLE",
		SeverityRule:       "three anomalies including a concentrated dependency",
		OpenedAt:           now,
	})
	if err != nil {
		t.Fatalf("seed an incident: %v", err)
	}
	if !opened {
		t.Fatal("the incident was not opened; nothing is being checked")
	}

	required, ok := consoleRowTypes(t)["IncidentRow"]
	if !ok {
		t.Fatal("the console declares no type IncidentRow; this test is out of date")
	}

	rows := fetchTenantRows(t, base+"/v1/incidents", token, tenant, "incidents")
	if len(rows) == 0 {
		t.Fatal("/v1/incidents returned no rows for a tenant this test seeded")
	}

	seen := map[string]bool{}
	for _, row := range rows {
		for key := range row {
			seen[key] = true
		}
	}

	var missing []string
	for _, field := range required {
		if !seen[field] {
			missing = append(missing, field)
		}
	}
	sort.Strings(missing)

	if len(missing) > 0 {
		t.Errorf("the console reads %v from /v1/incidents and no row carries them. "+
			"Present: %v.\n\nThe incident response is assembled as a map literal, so "+
			"these names exist as string constants on one side and TypeScript fields on "+
			"the other.", missing, sortedKeys(seen))
	}
}
