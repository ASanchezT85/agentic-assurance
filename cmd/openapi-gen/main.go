// Command openapi-gen writes docs/api/openapi.json from the routes the binaries
// register and the canonical schemas in packages/.
//
// Spec section 60 asks for OpenAPI generated from the canonical schemas rather than
// hand-written, and for a long time the API reference said none was generated "because
// no endpoint exists yet" — roughly fifteen endpoints ago.
//
// Two rules make this a generator rather than a second copy of the API by hand.
//
// The paths come from the source: every HandleFunc registration in the repository, the
// same way tests/security discovers them. A route nobody wrote down cannot be missing
// from the document, because the document does not have a list of its own.
//
// And a route with no description here is an error, not an omission. The generator
// refuses to write a document that silently leaves an endpoint undescribed, because a
// customer reading it would conclude the endpoint does not exist — which is the exact
// failure the route-table guard exists to catch, and it would reappear here.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// route is what the document says about one endpoint beyond its path.
//
// Deliberately terse. The prose lives in docs/api/README.md, which explains why each
// endpoint behaves as it does; this is the machine-readable contract, and duplicating
// the reasoning would create two accounts that drift.
type route struct {
	Summary  string
	Tag      string
	Request  string // contract name in packages/schema-registry.json, or empty
	Response string

	// Collection names the field a list is returned under, when the handler wraps it.
	//
	// The generator used to declare every list response as a bare JSON array, and two of
	// these handlers answer with an object — {tenant_id, count, events} — so the document
	// described a shape nothing served. The console read that document, asked for the
	// wrong field, and rendered a chain of ten events as "the source answered; it had
	// nothing to report". A document that is generated is not thereby true.
	Collection string

	Statuses map[string]string
}

// described is the metadata for every served route. A served route missing from here
// stops the generator.
var described = map[string]route{
	"POST /v1/intents": {
		Summary: "Submit an agent execution intent through the enforcement plane",
		Tag:     "Enforcement",
		Request: "agent-execution-envelope",
		Statuses: map[string]string{
			"200": "The order reached the venue",
			"202": "Accepted; the outcome is not yet known (INV-004)",
			"400": "The envelope did not validate",
			"401": "Nothing authenticated the caller, or the claim exceeded the evidence",
			"403": "Authority, a fleet control or hard policy refused",
			"413": "The envelope exceeds the accepted size",
			"422": "The intent could not be executed",
		},
	},
	"GET /v1/intents": {
		Summary:  "List recent intents for the caller's tenant, refusals included",
		Tag:      "Enforcement",
		Statuses: map[string]string{"200": "The intents in the window", "401": "Unauthenticated"},
	},
	"GET /v1/intents/{id}": {
		Summary: "The status of one intent by envelope id",
		Tag:     "Enforcement",
		Statuses: map[string]string{
			"200": "Executed, pending, or refused with the chain that refused it",
			"404": "No such intent for this tenant",
		},
	},
	"GET /v1/intents/{id}/evidence": {
		Summary:    "The evidence chain for one intent",
		Tag:        "Evidence",
		Response:   "internal-event",
		Collection: "events",
		Statuses:   map[string]string{"200": "The chain, exactly as stored"},
	},
	"GET /v1/evidence": {
		Summary:    "The evidence chain for one correlation id",
		Tag:        "Evidence",
		Response:   "internal-event",
		Collection: "events",
		Statuses:   map[string]string{"200": "The chain, exactly as stored"},
	},
	"POST /v1/agent-keys": {
		Summary: "Register an agent signing key",
		Tag:     "Identity",
		Statuses: map[string]string{
			"201": "Registered",
			"400": "The key would not be usable, or a private key was sent",
			"401": "Nothing authenticated the caller",
			"403": "This credential may not register signing keys",
			"409": "The agent already has a key under that id and it was not replaced",
			"503": "The key store could not record it, so nothing was registered",
		},
	},
	"POST /v1/agent-keys/revoke": {
		Summary: "Stop trusting an agent signing key",
		Tag:     "Identity",
		Statuses: map[string]string{
			"200": "Revoked; the row is kept so past signatures stay explainable",
			"400": "The revocation names no key, or no author",
			"401": "Nothing authenticated the caller",
			"403": "This credential may not revoke signing keys",
			"503": "The revocation could not be recorded, so the key is still trusted",
		},
	},
	"POST /v1/authority-grants": {
		Summary: "Issue an authority grant",
		Tag:     "Authority",
		Statuses: map[string]string{
			"201": "Issued",
			"400": "The grant would not constrain anything",
			"403": "This credential may not issue authority (P-002)",
			"409": "A grant with this id already exists",
		},
	},
	"POST /v1/authority-grants/{id}/revoke": {
		Summary: "Revoke an authority grant",
		Tag:     "Authority",
		Statuses: map[string]string{
			"200": "Revoked, or already revoked",
			"400": "revoked_by and reason are required",
			"404": "No such grant for this tenant",
		},
	},
	"POST /v1/controls": {
		Summary: "Authorize a fleet control from an incident's recommendation (INV-009)",
		Tag:     "Controls",
		Statuses: map[string]string{
			"201": "Authorized and in force",
			"400": "Not authorizable as written",
			"403": "This credential may not authorize fleet controls",
			"404": "No such incident, or it carries no recommendation",
			"409": "A control with this id already exists",
			"503": "The incident evidence could not be read",
		},
	},
	"GET /v1/controls": {
		Summary:  "List the tenant's fleet controls, in force or not",
		Tag:      "Controls",
		Statuses: map[string]string{"200": "The controls, with in_force computed"},
	},
	"POST /v1/controls/{id}/revoke": {
		Summary: "Lift a fleet control",
		Tag:     "Controls",
		Statuses: map[string]string{
			"200": "Lifted, or already lifted",
			"400": "revoked_by and reason are required",
			"404": "No such control for this tenant",
		},
	},
	"GET /v1/fleet/state": {
		Summary:  "Fleet risk measurements for the caller's tenant",
		Tag:      "Intelligence",
		Statuses: map[string]string{"200": "The measurements in the window"},
	},
	"GET /v1/cohorts": {
		Summary:  "Cohorts observed for the caller's tenant",
		Tag:      "Intelligence",
		Statuses: map[string]string{"200": "The cohorts"},
	},
	"GET /v1/dependencies": {
		Summary:  "Declared dependency concentration for the caller's tenant",
		Tag:      "Intelligence",
		Statuses: map[string]string{"200": "The observations"},
	},
	"GET /v1/incidents": {
		Summary:  "Incidents opened for the caller's tenant",
		Tag:      "Incidents",
		Statuses: map[string]string{"200": "The incidents, newest first"},
	},
	"GET /v1/incidents/{id}": {
		Summary: "One incident with its timeline, reconstructed from evidence",
		Tag:     "Incidents",
		Statuses: map[string]string{
			"200": "The incident, its timeline and which section 49 questions it answers",
			"404": "No such incident for this tenant",
		},
	},
	"POST /v1/simulations": {
		Summary: "Start a Digital Twin experiment",
		Tag:     "Lab",
		Statuses: map[string]string{
			"202": "Accepted and durable; Location carries the run",
			"400": "Not a runnable request",
			"404": "No scenario by that name",
		},
	},
	"GET /v1/simulations": {
		Summary:  "List the tenant's simulation runs",
		Tag:      "Lab",
		Statuses: map[string]string{"200": "The runs, newest first, without their records"},
	},
	"GET /v1/simulations/{id}": {
		Summary: "One simulation run with its record",
		Tag:     "Lab",
		Statuses: map[string]string{
			"200": "The run",
			"404": "No such run for this tenant",
		},
	},
	"POST /v1/simulations/{id}/cancel": {
		Summary: "Cancel a queued or running experiment",
		Tag:     "Lab",
		Statuses: map[string]string{
			"200": "Stopped",
			"400": "cancelled_by is required",
			"404": "No such run for this tenant",
			"409": "The run had already finished",
		},
	},
}

var registration = regexp.MustCompile(`HandleFunc\(\s*"((?:GET|POST|PUT|DELETE) )?(/v1/[^"]*)"`)

func main() {
	// The repository to scan, and where to write. Separate, because the check that
	// keeps this document current regenerates it somewhere else and compares: a
	// generator that could only write next to what it read could not be verified
	// without overwriting the thing under test.
	root := flag.String("root", ".", "repository root to scan for route registrations")
	out := flag.String("out", "", "file to write (default <root>/docs/api/openapi.json)")
	flag.Parse()

	target := *out
	if target == "" {
		target = filepath.Join(*root, "docs", "api", "openapi.json")
	}

	routes, err := servedRoutes(*root)
	if err != nil {
		fail(err)
	}
	if len(routes) < 10 {
		fail(fmt.Errorf("found %d routes; the walk is not finding registrations", len(routes)))
	}

	doc, err := build(*root, routes)
	if err != nil {
		fail(err)
	}

	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fail(err)
	}
	encoded = append(encoded, '\n')

	if err := os.WriteFile(target, encoded, 0o644); err != nil {
		fail(err)
	}
	fmt.Printf("wrote %s: %d paths\n", target, len(routes))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "openapi-gen:", err)
	os.Exit(1)
}

// servedRoutes returns "METHOD /path" for every registration in the repository.
func servedRoutes(root string) ([]string, error) {
	seen := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "node_modules", ".git", ".next", "vendor", ".venv", ".gotmp", ".bin":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		source, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range registration.FindAllStringSubmatch(string(source), -1) {
			method := strings.TrimSpace(m[1])
			if method == "" {
				method = "GET"
			}
			seen[method+" "+m[2]] = true
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := make([]string, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Strings(out)
	return out, nil
}

func build(root string, routes []string) (map[string]any, error) {
	schemas, err := components(root)
	if err != nil {
		return nil, err
	}

	// A description for a route nobody serves any more.
	//
	// The generator already refuses an undescribed route. The reverse is quieter and
	// just as wrong: an entry left behind after an endpoint is deleted keeps appearing
	// in the document, and a customer builds against a path that answers 404. Checked
	// here rather than left for a reader to notice, because nobody rereads a map of
	// nineteen entries.
	served := map[string]bool{}
	for _, r := range routes {
		served[r] = true
	}
	for r := range described {
		if !served[r] {
			return nil, fmt.Errorf("%s is described in cmd/openapi-gen and no binary "+
				"registers it; delete the entry rather than publishing a path that "+
				"answers 404", r)
		}
	}

	paths := map[string]any{}
	for _, r := range routes {
		method, path, _ := strings.Cut(r, " ")
		meta, described := described[r]
		if !described {
			// Refused rather than emitted bare. A path with no summary reads as an
			// endpoint nobody finished, and a customer would route around it.
			return nil, fmt.Errorf("%s is served and has no description in cmd/openapi-gen; "+
				"add one rather than publishing a document that leaves it blank", r)
		}

		operation := map[string]any{
			"summary":     meta.Summary,
			"tags":        []string{meta.Tag},
			"operationId": operationID(method, path),
			"responses":   responses(meta, schemas),
		}
		if meta.Request != "" {
			operation["requestBody"] = map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{"$ref": "#/components/schemas/" + meta.Request},
					},
				},
			}
		}
		if strings.Contains(path, "{id}") {
			operation["parameters"] = []any{map[string]any{
				"name": "id", "in": "path", "required": true,
				"schema": map[string]any{"type": "string"},
			}}
		}

		entry, ok := paths[path].(map[string]any)
		if !ok {
			entry = map[string]any{}
			paths[path] = entry
		}
		entry[strings.ToLower(method)] = operation
	}

	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "Agentic Order-Flow Assurance Platform",
			"version": "0.1",
			"description": "Generated from the routes the binaries register and the " +
				"canonical schemas in packages/ (spec section 60). Do not edit by hand: " +
				"run `go run ./cmd/openapi-gen`. Prose and the reasoning behind each " +
				"endpoint live in docs/api/README.md.",
		},
		// Every endpoint that carries tenant data authenticates, and the tenant comes
		// from the credential rather than from a header or a body field (INV-007).
		"security": []any{map[string]any{"bearerAuth": []any{}}},
		"paths":    paths,
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"bearerAuth": map[string]any{
					"type":        "http",
					"scheme":      "bearer",
					"description": "identity@tenant=token from the credential registry. The tenant comes from the credential; a header naming another is 403.",
				},
			},
			"schemas": schemas,
		},
	}, nil
}

// components embeds the canonical schemas, read from the registry rather than named
// here: the registry is what says which file is the contract.
func components(root string) (map[string]any, error) {
	raw, err := os.ReadFile(filepath.Join(root, "packages", "schema-registry.json"))
	if err != nil {
		return nil, err
	}
	var registry struct {
		Schemas []struct {
			Contract string `json:"contract"`
			File     string `json:"file"`
			Status   string `json:"status"`
		} `json:"schemas"`
	}
	if err := json.Unmarshal(raw, &registry); err != nil {
		return nil, err
	}

	out := map[string]any{}
	for _, entry := range registry.Schemas {
		if entry.Status != "ACTIVE" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, "packages", filepath.FromSlash(entry.File)))
		if err != nil {
			return nil, err
		}
		var schema map[string]any
		if err := json.Unmarshal(body, &schema); err != nil {
			return nil, fmt.Errorf("%s: %w", entry.File, err)
		}
		// $schema is a JSON Schema keyword and not an OpenAPI one. Left out rather
		// than passed through, so a validator does not reject the document over a
		// dialect declaration that means nothing here.
		delete(schema, "$schema")
		out[entry.Contract] = schema
	}
	return out, nil
}

func responses(meta route, schemas map[string]any) map[string]any {
	out := map[string]any{}
	for code, description := range meta.Statuses {
		response := map[string]any{"description": description}
		if code == "200" && meta.Response != "" {
			if _, ok := schemas[meta.Response]; ok {
				items := map[string]any{
					"type":  "array",
					"items": map[string]any{"$ref": "#/components/schemas/" + meta.Response},
				}
				var schema map[string]any = items
				if meta.Collection != "" {
					// The handler wraps the list. Described as it is served, not as it
					// would be tidier to serve.
					schema = map[string]any{
						"type": "object",
						"properties": map[string]any{
							"tenant_id":     map[string]any{"type": "string"},
							"count":         map[string]any{"type": "integer"},
							meta.Collection: items,
						},
						"required": []string{"count", meta.Collection},
					}
				}
				response["content"] = map[string]any{
					"application/json": map[string]any{"schema": schema},
				}
			}
		}
		out[code] = response
	}
	return out
}

func operationID(method, path string) string {
	cleaned := strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(strings.TrimPrefix(path, "/v1/"))
	return strings.ToLower(method) + "_" + cleaned
}
