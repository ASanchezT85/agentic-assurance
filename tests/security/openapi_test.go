package security

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The generated OpenAPI document, checked against the generator and the routes.
//
// A document generated once and committed is a document that is wrong from the next
// endpoint onwards, which is the failure this repository has now made four times in
// prose. So the check is not that the file exists: it is that regenerating it produces
// exactly what is committed.

func openAPIPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRoot, "docs", "api", "openapi.json")
}

// TestTheOpenAPIDocumentIsCurrent regenerates and compares.
func TestTheOpenAPIDocumentIsCurrent(t *testing.T) {
	committed, err := os.ReadFile(openAPIPath(t))
	if err != nil {
		t.Fatalf("no generated document: %v (run `go run ./cmd/openapi-gen`)", err)
	}

	// Regenerated somewhere else and compared, rather than in place: a check that
	// overwrote the file it was verifying would pass by construction.
	target := filepath.Join(t.TempDir(), "openapi.json")

	// Absolute, because the generator resolves -root against its own working
	// directory and repoRoot is relative to this package.
	absolute, err := filepath.Abs(repoRoot)
	if err != nil {
		t.Fatalf("root: %v", err)
	}

	cmd := exec.Command("go", "run", "./cmd/openapi-gen", "-root", absolute, "-out", target)
	cmd.Dir = absolute
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generator failed: %v\n%s", err, out)
	}

	regenerated, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the generator wrote nothing: %v", err)
	}

	if string(committed) != string(regenerated) {
		t.Error("docs/api/openapi.json is not what the generator produces. A document " +
			"generated once and committed is wrong from the next endpoint onwards; run " +
			"`go run ./cmd/openapi-gen` and commit the result.")
	}
}

// Every served route is in the document, which is the property the generator exists
// for and is worth asserting against the artefact rather than against the code that
// wrote it.
func TestTheOpenAPIDocumentCoversEveryServedRoute(t *testing.T) {
	raw, err := os.ReadFile(openAPIPath(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var doc struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("the generated document is not JSON: %v", err)
	}

	for route := range servedRoutes(t) {
		method, path, _ := strings.Cut(route, " ")
		operations, ok := doc.Paths[path]
		if !ok {
			t.Errorf("%s is served and is not in the OpenAPI document", route)
			continue
		}
		if _, ok := operations[strings.ToLower(method)]; !ok {
			t.Errorf("%s is served and the document describes %s without it", route, path)
		}
	}
}

// Every operation says what it is and what it answers. A path with an empty summary or
// no responses is an endpoint a reader routes around.
func TestEveryOperationIsDescribed(t *testing.T) {
	raw, err := os.ReadFile(openAPIPath(t))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	var doc struct {
		Paths map[string]map[string]struct {
			Summary   string         `json:"summary"`
			Responses map[string]any `json:"responses"`
		} `json:"paths"`
		Components struct {
			SecuritySchemes map[string]any `json:"securitySchemes"`
			Schemas         map[string]any `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse: %v", err)
	}

	for path, operations := range doc.Paths {
		for method, op := range operations {
			if strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s %s has no summary", strings.ToUpper(method), path)
			}
			if len(op.Responses) == 0 {
				t.Errorf("%s %s documents no response", strings.ToUpper(method), path)
			}
		}
	}

	if len(doc.Components.SecuritySchemes) == 0 {
		t.Error("the document declares no security scheme; every endpoint that carries " +
			"tenant data authenticates (INV-007) and a document that omits that " +
			"describes an open API")
	}
	// The schemas come from packages/, which is the point of generating rather than
	// writing this by hand (spec section 60).
	if _, ok := doc.Components.Schemas["agent-execution-envelope"]; !ok {
		t.Error("the canonical envelope schema is not embedded; the document is not " +
			"generated from packages/")
	}
}
