package intent

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const fixtureRoot = "../../tests/fixtures/envelopes"

func fixtureFiles(t *testing.T, sub string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(fixtureRoot, sub, "*.json"))
	if err != nil {
		t.Fatalf("glob %s: %v", sub, err)
	}
	if len(matches) == 0 {
		t.Fatalf("no fixtures in %s; the harness would pass vacuously", sub)
	}
	sort.Strings(matches)
	return matches
}

// TestValidFixturesAreAccepted — every envelope in valid/ must decode and validate.
func TestValidFixturesAreAccepted(t *testing.T) {
	for _, path := range fixtureFiles(t, "valid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			env, err := Decode(raw)
			if err != nil {
				t.Fatalf("expected valid, got: %v", err)
			}
			if env.SchemaVersion != SchemaVersion {
				t.Errorf("schema_version = %q, want %q", env.SchemaVersion, SchemaVersion)
			}
			if !env.ReceivedAt.Equal(env.ReceivedAt.UTC()) {
				t.Error("received_at was not normalized to UTC")
			}
		})
	}
}

// TestInvalidFixturesAreRejectedForTheStatedReason — rejecting for the wrong reason
// is not a pass. Each fixture declares its codes in a sibling .codes file.
func TestInvalidFixturesAreRejectedForTheStatedReason(t *testing.T) {
	for _, path := range fixtureFiles(t, "invalid") {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}

			expected := expectedCodes(t, path)

			_, err = Decode(raw)
			if err == nil {
				t.Fatalf("expected rejection with %v, got a valid envelope", expected)
			}
			verrs, ok := err.(ValidationErrors)
			if !ok {
				t.Fatalf("expected ValidationErrors, got %T", err)
			}
			for _, code := range expected {
				if !verrs.Has(code) {
					t.Errorf("expected code %q; got %v", code, verrs.Codes())
				}
			}
		})
	}
}

func expectedCodes(t *testing.T, jsonPath string) []string {
	t.Helper()
	codesPath := strings.TrimSuffix(jsonPath, ".json") + ".codes"
	raw, err := os.ReadFile(codesPath)
	if err != nil {
		t.Fatalf("every invalid fixture needs a .codes file: %v", err)
	}
	var out []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		t.Fatalf("%s is empty", codesPath)
	}
	return out
}

// TestEveryInvalidFixtureHasCodes guards the harness itself: a fixture without a
// .codes file would silently assert nothing but "it failed somehow".
func TestEveryInvalidFixtureHasCodes(t *testing.T) {
	for _, path := range fixtureFiles(t, "invalid") {
		codesPath := strings.TrimSuffix(path, ".json") + ".codes"
		if _, err := os.Stat(codesPath); err != nil {
			t.Errorf("%s has no .codes file", filepath.Base(path))
		}
	}
}
