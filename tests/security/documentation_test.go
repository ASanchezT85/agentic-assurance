package security

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// The documentation stops describing a system nobody is running.
//
// Six audit passes changed how callers authenticate, and the documentation kept saying
// the tenant came from a header and that authentication "arrives with the API surface
// that carries it". It had arrived. For two passes the API reference told a reader
// these endpoints were unauthenticated and safe only behind network isolation, and the
// incident runbook handed them a curl that returns 401 — read, by definition, at the
// worst possible moment.
//
// A stale caveat is worse than no caveat. It describes a system nobody is running, and
// it is believed precisely because it sounds careful.

// docFiles returns every Markdown file that documents the running system.
//
// The spec and the phase handoff are excluded: they are the input this was built from,
// they describe a system that did not exist yet on purpose, and rewriting them would
// destroy the record of what was asked for.
func docFiles(t *testing.T) []string {
	t.Helper()

	excluded := map[string]bool{
		"MASTER_BUILD_SPEC.md":              true,
		"PHASE_0_IMPLEMENTATION_HANDOFF.md": true,
	}

	var files []string
	for _, root := range []string{repoRoot + "/docs", repoRoot + "/README.md"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if strings.HasSuffix(d.Name(), ".md") && !excluded[d.Name()] {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}

	if len(files) < 5 {
		t.Fatalf("found %d documentation files; the walk is wrong and this guard would "+
			"pass over almost nothing", len(files))
	}
	return files
}

// No documented command passes the tenant in a header.
//
// Every endpoint that carries tenant data authenticates, and the tenant comes from the
// credential. A curl in the documentation that sends X-Tenant-Id and no credential is
// a command that returns 401 for whoever follows it.
func TestNoDocumentedCommandPassesTheTenantInAHeader(t *testing.T) {
	for _, path := range docFiles(t) {
		for i, line := range strings.Split(readSource(t, path), "\n") {
			if !strings.Contains(line, "curl") && !strings.HasPrefix(strings.TrimSpace(line), "curl") {
				continue
			}
			if strings.Contains(line, "X-Tenant-Id") && !strings.Contains(line, "Authorization") {
				t.Errorf("%s:%d passes the tenant in a header: %s\n"+
					"The tenant comes from the credential; this command returns 401.",
					path, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// The documentation does not promise authentication that already exists.
//
// These phrasings were true when they were written and are false now. Listed as exact
// sentences rather than keywords, because "arrives with" is a perfectly good phrase for
// something that has genuinely not arrived, and a guard that banned it would push
// authors into vaguer prose rather than truer prose.
func TestNoStaleAuthenticationCaveats(t *testing.T) {
	stale := []string{
		"that is not authentication",
		"arrives with the API surface that carries authentication",
		"tenant-scoped by header",
		"reachable only inside the customer's own network",
	}

	for _, path := range docFiles(t) {
		body := readSource(t, path)
		for _, phrase := range stale {
			if !strings.Contains(body, phrase) {
				continue
			}
			// The sentence explaining that the caveat was removed is allowed to quote
			// it. A guard that punished the explanation would teach authors to delete
			// the explanation.
			for _, line := range strings.Split(body, "\n") {
				if strings.Contains(line, phrase) && !strings.Contains(line, "used to") {
					t.Errorf("%s says %q. Every endpoint that carries tenant data "+
						"authenticates, and the tenant comes from the credential "+
						"(ADR-025).", path, phrase)
					break
				}
			}
		}
	}
}

// A measured number is quoted with what it was measured on, or not quoted.
//
// The enforcement path was 11.6 microseconds when it was first measured and 12.5 after
// the cross-tenant check was added. Both are true; a bare number in prose is read as a
// property of the system rather than as a reading from one machine on one day.
func TestMeasuredNumbersSayWhatTheyAre(t *testing.T) {
	// The files that quote latency figures, and the phrase that has to be near them.
	for _, path := range []string{repoRoot + "/README.md", repoRoot + "/docs/operations/README.md"} {
		body := readSource(t, path)
		if !strings.Contains(body, "µs") {
			continue
		}
		// Any statement of the conditions, not one phrasing. The first version of this
		// demanded the literal "development hardware" and flagged a table headed
		// "Measured on the reference machine, 2026-08-28" — which says more, not less.
		// A guard that requires a particular sentence pushes authors toward that
		// sentence rather than toward a true one.
		conditions := []string{"development hardware", "measured on", "reference machine"}
		said := false
		lowered := strings.ToLower(body)
		for _, c := range conditions {
			if strings.Contains(lowered, c) {
				said = true
				break
			}
		}
		if !said {
			t.Errorf("%s quotes a latency in microseconds and never says what it was "+
				"measured on. A number without its conditions is read as a guarantee, "+
				"and this one has already moved from 11.6 to 12.5.", path)
		}
	}
}
