package intent

import (
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

const schemaPath = "../../packages/envelope-schema/schemas/agent-execution-envelope.v0.1.json"

// The published JSON Schema and this package are two statements of one contract.
// Producers integrate against the schema; the gateway enforces the Go code. If they
// drift, external integrations pass their own validation and are then rejected here,
// which is the worst possible place to find out.
//
// Reflection is fine in a test. It is banned on the hot path (spec section 60), which
// is exactly why validation itself is hand-written.

func loadSchema(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse schema: %v", err)
	}
	return doc
}

func schemaStrings(t *testing.T, doc map[string]any, key string) []string {
	t.Helper()
	raw, ok := doc[key].([]any)
	if !ok {
		t.Fatalf("schema has no %q array", key)
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		out = append(out, v.(string))
	}
	sort.Strings(out)
	return out
}

// goJSONFields returns the root-level JSON property names of the envelope struct.
func goJSONFields() []string {
	var out []string
	rt := reflect.TypeOf(AgentExecutionEnvelope{})
	for i := 0; i < rt.NumField(); i++ {
		tag := rt.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		out = append(out, strings.Split(tag, ",")[0])
	}
	sort.Strings(out)
	return out
}

// TestSchemaDeclaresEveryGoField — a field added to the struct without a schema
// entry is a contract change nobody published.
func TestSchemaDeclaresEveryGoField(t *testing.T) {
	doc := loadSchema(t)
	props, ok := doc["properties"].(map[string]any)
	if !ok {
		t.Fatal("schema has no properties object")
	}
	for _, field := range goJSONFields() {
		if _, declared := props[field]; !declared {
			t.Errorf("struct field %q is not declared in the published schema", field)
		}
	}
}

// TestGoDeclaresEverySchemaProperty — the reverse. A property in the schema that the
// struct ignores is a promise to producers that nothing keeps.
func TestGoDeclaresEverySchemaProperty(t *testing.T) {
	doc := loadSchema(t)
	props := doc["properties"].(map[string]any)

	goFields := map[string]bool{}
	for _, f := range goJSONFields() {
		goFields[f] = true
	}
	for name := range props {
		if !goFields[name] {
			t.Errorf("schema declares %q but the struct does not decode it", name)
		}
	}
}

// TestSchemaRequiredFieldsAreActuallyEnforced is the one that matters. For every
// root property the schema marks required, removing it from an otherwise valid
// envelope must cause rejection. A required list nothing enforces is decoration.
func TestSchemaRequiredFieldsAreActuallyEnforced(t *testing.T) {
	doc := loadSchema(t)
	required := schemaStrings(t, doc, "required")

	baseline := validEnvelopeJSON(t)
	if _, err := Decode(mustMarshal(t, baseline)); err != nil {
		t.Fatalf("baseline fixture must be valid: %v", err)
	}

	for _, field := range required {
		t.Run(field, func(t *testing.T) {
			mutated := map[string]any{}
			for k, v := range baseline {
				if k != field {
					mutated[k] = v
				}
			}
			if _, err := Decode(mustMarshal(t, mutated)); err == nil {
				t.Errorf("schema marks %q required, but an envelope without it validated", field)
			}
		})
	}
}

// TestSchemaVersionMatchesTheBuild keeps the const, the filename and the schema in
// step. The compatibility harness in packages/ checks the file against itself; this
// checks it against the code that implements it.
func TestSchemaVersionMatchesTheBuild(t *testing.T) {
	doc := loadSchema(t)
	props := doc["properties"].(map[string]any)
	sv := props["schema_version"].(map[string]any)

	if got := sv["const"].(string); got != SchemaVersion {
		t.Errorf("schema const %q != intent.SchemaVersion %q", got, SchemaVersion)
	}
	if id := doc["$id"].(string); !strings.HasSuffix(id, "/v"+SchemaVersion) {
		t.Errorf("$id %q does not end in /v%s", id, SchemaVersion)
	}
}

func validEnvelopeJSON(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("../../tests/fixtures/envelopes/valid/market_buy_notional.json")
	if err != nil {
		t.Fatalf("read baseline fixture: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parse baseline fixture: %v", err)
	}
	return out
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}
