package security

import (
	"regexp"
	"strings"
	"testing"
)

// Every setting the code reads is in the example file.
//
// Sixteen were not. Most had drifted since the phase that added them; three were
// credentials added while closing a cross-tenant hole, and those are the ones that
// turn the system off — an operator following .env.example would have brought up a
// platform where every endpoint carrying tenant data answers 401, with nothing in the
// file to say which variable was missing.
//
// The same shape once more: a file written once, code that grew past it, and nothing
// checking. It is cheap to check, so it is checked.
func TestEveryEnvironmentVariableIsInTheExample(t *testing.T) {
	// Read by the process and not something an operator sets for it.
	exempt := map[string]string{
		"PATH":             "inherited, and deliberately the only thing the twin's subprocess gets",
		"PYTHONHASHSEED":   "set by the runner for determinism, not read from the environment",
		"PYTHONIOENCODING": "set by the runner, not read from the environment",

		// Broker credentials, exempt because an older and stronger rule forbids them
		// here: tests/scope_guard_test.go refuses to let .env.example so much as name
		// one, since a file with a slot for a venue secret invites someone to paste a
		// live key into the working tree. The two guards disagreed and that one is
		// right about the thing that matters. They are documented in
		// docs/operations/README.md instead, and belong in a secret manager
		// (spec section 35).
		"ALPACA_BASE_URL":    "documented in docs/operations, never in an example file",
		"ALPACA_KEY_ID":      "documented in docs/operations, never in an example file",
		"ALPACA_SECRET_KEY":  "documented in docs/operations, never in an example file",
		"TRADIER_BASE_URL":   "documented in docs/operations, never in an example file",
		"TRADIER_TOKEN":      "documented in docs/operations, never in an example file",
		"TRADIER_ACCOUNT_ID": "documented in docs/operations, never in an example file",
	}

	// An exemption is only honest if the setting really is documented somewhere.
	operations := readSource(t, repoRoot+"/docs/operations/README.md")
	for name, reason := range exempt {
		if !strings.Contains(reason, "docs/operations") {
			continue
		}
		if !strings.Contains(operations, name) {
			t.Errorf("%s is exempt from .env.example because it is documented in "+
				"docs/operations, and it is not there. An exemption that points at "+
				"nothing is a setting nobody has written down at all.", name)
		}
	}

	example := readSource(t, repoRoot+"/.env.example")
	declared := map[string]bool{}
	for _, line := range strings.Split(example, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, _, ok := strings.Cut(line, "=")
		if ok {
			declared[strings.TrimSpace(name)] = true
		}
	}
	if len(declared) < 10 {
		t.Fatalf(".env.example declares %d settings; the parse is wrong and this guard "+
			"would pass over an empty set", len(declared))
	}

	getenv := regexp.MustCompile(`os\.Getenv\("([A-Z0-9_]+)"\)`)
	nextEnv := regexp.MustCompile(`process\.env\.([A-Z0-9_]+)`)

	read := map[string][]string{}
	for _, path := range goSources(t) {
		for _, m := range getenv.FindAllStringSubmatch(readSource(t, path), -1) {
			read[m[1]] = append(read[m[1]], path)
		}
	}
	for _, path := range consoleSources(t) {
		for _, m := range nextEnv.FindAllStringSubmatch(readSource(t, path), -1) {
			read[m[1]] = append(read[m[1]], path)
		}
	}

	if len(read) < 10 {
		t.Fatalf("found only %d settings in the source; the search is wrong", len(read))
	}

	for name, where := range read {
		if _, ok := exempt[name]; ok {
			continue
		}
		if !declared[name] {
			t.Errorf("%s is read by %s and is not in .env.example. An operator "+
				"configuring from that file gets a process missing something it needs, "+
				"and finds out from a 401 or a warning rather than from the file.",
				name, where[0])
		}
	}
}

// consoleSources returns the console's own TypeScript, excluding build output.
func consoleSources(t *testing.T) []string {
	t.Helper()

	var files []string
	for _, dir := range []string{"/apps/console-web/lib", "/apps/console-web/app"} {
		walkTypeScript(t, repoRoot+dir, &files)
	}
	if len(files) == 0 {
		t.Fatal("no console sources found; the console half of this guard is inspecting nothing")
	}
	return files
}
