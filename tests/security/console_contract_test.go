package security

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The console asks for the field the API actually returns.
//
// `read()` takes the collection key as an argument and defaults to "rows". One call
// omitted it against an endpoint that answers with "events", so a chain of ten events
// read as zero and the surface printed "the source answered; it had nothing to report"
// about a response that had everything in it.
//
// That is the worst shape of failure this product can produce. The Console's whole job is
// to distinguish "nothing happened" from "we could not see", and a wrong field name makes
// it state the first while the second is not even true — the source answered, correctly,
// and the console could not read its own request.
//
// The comment above `read()` warns about exactly this ("guessing would turn a renamed
// field into an empty screen that looks like a quiet fleet") and the call below it did it
// anyway. A warning in a comment is not a control.
func TestTheConsoleReadsTheFieldsTheAPIReturns(t *testing.T) {
	source := readSource(t, filepath.Join(repoRoot, "apps", "console-web", "lib", "api.ts"))
	document := openAPIDocument(t)

	// read<T>(`${BASE}/path`, "What", "key")  — the key is optional and defaults to
	// "rows", which is what makes the mistake silent.
	call := regexp.MustCompile(
		`read<[^>]+>\(\s*` + // read<Type>(
			"`\\$\\{[A-Z_]+\\}([^`]*)`" + // `${BASE}/v1/...`
			`\s*,\s*"[^"]*"` + // , "What"
			`(?:\s*,\s*(?://[^\n]*\n\s*)*"([^"]+)")?`, // , /* comments */ "key"
	)

	matches := call.FindAllStringSubmatch(source, -1)
	if len(matches) < 5 {
		t.Fatalf("found %d console reads; the pattern is not matching the calls it is "+
			"meant to guard", len(matches))
	}

	for _, m := range matches {
		path := strings.SplitN(m[1], "?", 2)[0]
		key := m[2]
		if key == "" {
			key = "rows"
		}

		served, described := collectionKeys(document, path)
		if !described {
			// Endpoints the document does not describe are checked by the route-table
			// guard, not here.
			continue
		}
		if len(served) == 0 {
			// A bare array. The console's key is irrelevant and read() would return
			// nothing, so this is worth saying out loud rather than passing over.
			t.Errorf("%s returns a bare array and the console reads it through a "+
				"collection key (%q); it would render every response as empty", path, key)
			continue
		}

		found := false
		for _, k := range served {
			if k == key {
				found = true
			}
		}
		if !found {
			t.Errorf("the console reads %s expecting %q and the endpoint returns %v. "+
				"A wrong key does not fail: it renders a full response as an empty one, "+
				"which is the console saying \"nothing happened\" about something that "+
				"did.", path, key, served)
		}
	}
}

// collectionKeys returns the array-valued properties of a path's 200 response, and
// whether the document describes that path at all.
func collectionKeys(document map[string]any, path string) ([]string, bool) {
	paths, _ := document["paths"].(map[string]any)
	operations, ok := paths[path].(map[string]any)
	if !ok {
		return nil, false
	}
	get, ok := operations["get"].(map[string]any)
	if !ok {
		return nil, false
	}
	responses, _ := get["responses"].(map[string]any)
	ok200, ok := responses["200"].(map[string]any)
	if !ok {
		return nil, false
	}
	content, _ := ok200["content"].(map[string]any)
	json, _ := content["application/json"].(map[string]any)
	schema, ok := json["schema"].(map[string]any)
	if !ok {
		return nil, false
	}

	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		// A bare array: described, with no collection key.
		return nil, true
	}

	var keys []string
	for name, raw := range properties {
		property, _ := raw.(map[string]any)
		if property["type"] == "array" {
			keys = append(keys, name)
		}
	}
	return keys, true
}

func openAPIDocument(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(repoRoot, "docs", "api", "openapi.json"))
	if err != nil {
		t.Fatalf("read openapi.json: %v", err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse openapi.json: %v", err)
	}
	return document
}
