// Package packages holds the schema compatibility harness.
//
// It enforces the versioning policy in packages/README.md mechanically, so Phase 1
// and later cannot drift the contracts by accident. Stdlib only, by design: the
// harness must not depend on a schema library whose own version becomes a variable.
package packages

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

type registryEntry struct {
	Contract    string `json:"contract"`
	Package     string `json:"package"`
	File        string `json:"file"`
	Version     string `json:"version"`
	Status      string `json:"status"`
	OwningPhase int    `json:"owning_phase"`
}

type registry struct {
	RegistryVersion string          `json:"registry_version"`
	Schemas         []registryEntry `json:"schemas"`
}

type schemaDoc struct {
	ID         string                     `json:"$id"`
	Title      string                     `json:"title"`
	Required   []string                   `json:"required"`
	Properties map[string]json.RawMessage `json:"properties"`
	// additionalProperties is decoded as a raw message so that `false` and an
	// omitted key are distinguishable.
	AdditionalProperties json.RawMessage `json:"additionalProperties"`
}

var (
	fileNameRe  = regexp.MustCompile(`^(.+)\.v(\d+\.\d+)\.json$`)
	idVersionRe = regexp.MustCompile(`/v(\d+\.\d+)$`)
)

func loadRegistry(t *testing.T) registry {
	t.Helper()
	raw, err := os.ReadFile("schema-registry.json")
	if err != nil {
		t.Fatalf("read registry: %v", err)
	}
	var reg registry
	if err := json.Unmarshal(raw, &reg); err != nil {
		t.Fatalf("parse registry: %v", err)
	}
	if len(reg.Schemas) == 0 {
		t.Fatal("registry lists no schemas")
	}
	return reg
}

// discoverSchemaFiles returns every schema file on disk, relative to packages/.
func discoverSchemaFiles(t *testing.T) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(".", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Base(filepath.Dir(path)) != "schemas" {
			return nil
		}
		if strings.HasSuffix(path, ".json") {
			found = append(found, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	sort.Strings(found)
	return found
}

func loadSchema(t *testing.T, path string) (schemaDoc, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var doc schemaDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s: parse: %v", path, err)
	}
	var loose map[string]any
	if err := json.Unmarshal(raw, &loose); err != nil {
		t.Fatalf("%s: parse: %v", path, err)
	}
	return doc, loose
}

// TestRegistryMatchesDisk enforces policy item 6: the registry and the filesystem
// agree exactly, in both directions.
func TestRegistryMatchesDisk(t *testing.T) {
	reg := loadRegistry(t)
	onDisk := discoverSchemaFiles(t)

	registered := map[string]bool{}
	for _, e := range reg.Schemas {
		registered[e.File] = true
		if _, err := os.Stat(e.File); err != nil {
			t.Errorf("registry lists %s but it does not exist on disk", e.File)
		}
	}
	for _, f := range onDisk {
		if !registered[f] {
			t.Errorf("%s exists on disk but is not in schema-registry.json", f)
		}
	}
}

// TestVersionsAgree enforces policy item 1: filename, $id and the schema_version
// const must all carry the same version.
func TestVersionsAgree(t *testing.T) {
	for _, path := range discoverSchemaFiles(t) {
		doc, loose := loadSchema(t, path)

		m := fileNameRe.FindStringSubmatch(filepath.Base(path))
		if m == nil {
			t.Errorf("%s: filename does not match <contract>.v<major>.<minor>.json", path)
			continue
		}
		fileVersion := m[2]

		idMatch := idVersionRe.FindStringSubmatch(doc.ID)
		if idMatch == nil {
			t.Errorf("%s: $id %q does not end in /v<major>.<minor>", path, doc.ID)
			continue
		}
		if idMatch[1] != fileVersion {
			t.Errorf("%s: filename version %s != $id version %s", path, fileVersion, idMatch[1])
		}

		constVersion, err := schemaVersionConst(loose)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if constVersion != fileVersion {
			t.Errorf("%s: filename version %s != schema_version const %s", path, fileVersion, constVersion)
		}
	}
}

func schemaVersionConst(loose map[string]any) (string, error) {
	props, ok := loose["properties"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no properties object")
	}
	sv, ok := props["schema_version"].(map[string]any)
	if !ok {
		return "", fmt.Errorf("no schema_version property")
	}
	c, ok := sv["const"].(string)
	if !ok {
		return "", fmt.Errorf("schema_version has no string const")
	}
	return c, nil
}

// TestConsumersMayReceiveUnknownProperties enforces policy item 5. A producer running
// ahead of a consumer is normal under ADR-008, so no schema may seal its root.
func TestConsumersMayReceiveUnknownProperties(t *testing.T) {
	for _, path := range discoverSchemaFiles(t) {
		doc, _ := loadSchema(t, path)
		if string(doc.AdditionalProperties) == "false" {
			t.Errorf("%s: additionalProperties:false at the document root breaks forward compatibility", path)
		}
	}
}

// TestMinorVersionsStayCompatible enforces policy item 3. Within one contract, a
// later minor version may not drop a property or newly require one. Removing or
// tightening is a major bump, which lives in its own file and is not compared here.
func TestMinorVersionsStayCompatible(t *testing.T) {
	byContract := map[string][]string{}
	for _, path := range discoverSchemaFiles(t) {
		m := fileNameRe.FindStringSubmatch(filepath.Base(path))
		if m == nil {
			continue
		}
		key := filepath.Dir(path) + "/" + m[1] + "/major=" + strings.Split(m[2], ".")[0]
		byContract[key] = append(byContract[key], path)
	}

	for contract, paths := range byContract {
		if len(paths) < 2 {
			continue
		}
		sort.Strings(paths) // v0.1 < v0.10 is wrong lexically, but minors are single-digit until proven otherwise
		for i := 1; i < len(paths); i++ {
			prev, _ := loadSchema(t, paths[i-1])
			next, _ := loadSchema(t, paths[i])

			for name := range prev.Properties {
				if _, ok := next.Properties[name]; !ok {
					t.Errorf("%s: %s drops property %q present in %s; that is a major bump",
						contract, paths[i], name, paths[i-1])
				}
			}
			prevRequired := map[string]bool{}
			for _, r := range prev.Required {
				prevRequired[r] = true
			}
			for _, r := range next.Required {
				if !prevRequired[r] {
					t.Errorf("%s: %s newly requires %q; that is a major bump", contract, paths[i], r)
				}
			}
		}
	}
}

// TestScaffoldsDeclareTheirOwningPhase keeps Phase 0 honest: a scaffold must say so
// and must name the phase that completes it.
func TestScaffoldsDeclareTheirOwningPhase(t *testing.T) {
	reg := loadRegistry(t)
	for _, e := range reg.Schemas {
		if e.Status != "SCAFFOLD" {
			continue
		}
		_, loose := loadSchema(t, e.File)
		if loose["x-phase-0-scaffold"] != true {
			t.Errorf("%s: registry says SCAFFOLD but the document does not set x-phase-0-scaffold", e.File)
		}
		phase, ok := loose["x-owning-phase"].(float64)
		if !ok || int(phase) != e.OwningPhase {
			t.Errorf("%s: x-owning-phase does not match registry owning_phase %d", e.File, e.OwningPhase)
		}
	}
}
