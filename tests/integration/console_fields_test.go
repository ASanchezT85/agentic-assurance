//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/control"
	"agentic-assurance/internal/fleet"
)

// The console's field names and the gateway's field names are one contract.
//
// They are two independent lists of strings. The gateway builds its responses as
// map[string]any literals in handlers, so there is no Go type to generate a schema from;
// the console declares TypeScript types with the field names it expects; and nothing tied
// the two together. A rename on either side blanks a column, and a blank column on this
// product reads as "nothing happened" rather than as "we lost the field".
//
// One instance of that class already shipped: the console read /v1/evidence expecting a
// collection called "rows" against an endpoint that answers with "events", so a chain of
// ten events rendered as "the source answered; it had nothing to report". A structural
// test now checks collection keys. This checks the fields inside them, against the live
// response rather than against a document either side could be wrong about.
//
// Optional fields are not required to appear: a `field?: T` in the console means the
// console already handles absence.
func TestTheConsoleFieldsExistInLiveResponses(t *testing.T) {
	base := os.Getenv("GATEWAY_URL")
	token := os.Getenv("CONSOLE_API_TOKEN")
	if base == "" || token == "" {
		t.Skip("set GATEWAY_URL and CONSOLE_API_TOKEN against a gateway with a seeded " +
			"tenant; an empty tenant has no row to check a field name against")
	}

	// A control, seeded so this test checks something rather than skipping. Fleet
	// intelligence recommends and a customer authorizes, so a tenant with no control is
	// the ordinary state — and a contract test that only runs on tenants that happen to
	// have one is a contract test that mostly does not run.
	seedControl(t, os.Getenv("CONSOLE_TENANT_ID"))

	types := consoleRowTypes(t)

	cases := []struct {
		path       string
		collection string
		rowType    string
	}{
		{"/v1/intents?limit=5", "intents", "IntentRow"},
		{"/v1/controls", "controls", "ControlRow"},
	}

	for _, c := range cases {
		t.Run(strings.SplitN(strings.TrimPrefix(c.path, "/v1/"), "?", 2)[0], func(t *testing.T) {
			required, ok := types[c.rowType]
			if !ok {
				t.Fatalf("the console declares no type %s; this test is out of date", c.rowType)
			}

			rows := fetchRows(t, base+c.path, token, c.collection)
			if len(rows) == 0 {
				t.Skipf("%s returned no rows; a field name cannot be checked against an "+
					"empty collection", c.path)
			}

			// The union across rows, because an omitempty field is absent from a row
			// that does not have it and present on one that does. A field missing from
			// every row is the finding.
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
					"%v.\n\nA field the console expects and the API does not send renders "+
					"as blank, and a blank cell on this product reads as \"nothing "+
					"happened\" rather than \"the field is gone\".",
					missing, c.path, sortedKeys(seen))
			}
		})
	}
}

// consoleRowTypes extracts each exported row type's non-optional field names from the
// console's API client.
func consoleRowTypes(t *testing.T) map[string][]string {
	t.Helper()

	path := filepath.Join(repoRootFromTest(t), "apps", "console-web", "lib", "api.ts")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read api.ts: %v", err)
	}

	declaration := regexp.MustCompile(`(?s)export type (\w+) = \{(.*?)\n\};`)
	// `name: T` is required; `name?: T` the console already handles as absent.
	field := regexp.MustCompile(`(?m)^\s{2}([a-z_][a-z0-9_]*)(\??):`)

	types := map[string][]string{}
	for _, m := range declaration.FindAllStringSubmatch(string(raw), -1) {
		var required []string
		for _, f := range field.FindAllStringSubmatch(m[2], -1) {
			if f[2] == "" {
				required = append(required, f[1])
			}
		}
		types[m[1]] = required
	}
	if len(types) < 5 {
		t.Fatalf("parsed %d console types; the extraction is wrong", len(types))
	}
	return types
}

func fetchRows(t *testing.T, url, token, collection string) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skipf("%s is not reachable: %v", url, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Skipf("%s answered %d", url, resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		t.Fatalf("parse %s: %v", url, err)
	}

	list, ok := document[collection].([]any)
	if !ok {
		t.Fatalf("%s has no collection %q; it carries %v", url, collection,
			sortedKeys(keysOfDocument(document)))
	}

	rows := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if row, ok := item.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

func keysOfDocument(document map[string]any) map[string]bool {
	out := map[string]bool{}
	for k := range document {
		out[k] = true
	}
	return out
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func repoRootFromTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

var _ = fmt.Sprintf

// seedControl writes one authorized control through the store the gateway reads.
func seedControl(t *testing.T, tenant string) {
	t.Helper()
	if tenant == "" {
		return
	}
	now := time.Now().UTC()
	err := control.NewStore(idemPool(t)).Save(context.Background(), control.Control{
		ControlID:      fmt.Sprintf("ctl_fields_%d", now.UnixNano()),
		TenantID:       tenant,
		IncidentID:     fmt.Sprintf("inc_fields_%d", now.UnixNano()),
		Action:         fleet.ControlThrottle,
		AgentID:        "agent_fields",
		MaxOrders:      10,
		Window:         time.Minute,
		AuthorizedBy:   "ops@example.test",
		PolicyBundleID: "bundle_fields",
		Reason:         "seeded by the console field contract test",
		AppliedAt:      now,
		ExpiresAt:      now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("seed a control: %v", err)
	}
}
