package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Console's tokens are the design system's tokens.
//
// apps/console-web/app/tokens.css is a copy, because Next builds from its own directory
// and reaching outside it for a stylesheet is fragile. A copy is a thing that drifts: the
// design system says one colour and the product renders another, and nobody notices
// because both files look right on their own.
//
// The brand authority states the rule — product code consumes approved tokens rather than
// inventing a parallel palette — and this is what keeps it true.
func TestTheConsoleTokensMatchTheDesignSystem(t *testing.T) {
	source := readSource(t, filepath.Join(repoRoot, "design", "exoryn", "02-tokens", "tokens.css"))
	copied := readSource(t, filepath.Join(repoRoot, "apps", "console-web", "app", "tokens.css"))

	// The copy carries a header explaining what it is. Everything after it must be the
	// design system's file, byte for byte.
	marker := "/* EXORYN Product Design System V1 */"
	index := strings.Index(copied, marker)
	if index < 0 {
		t.Fatalf("the console's tokens.css does not contain the design system's header; "+
			"it is no longer recognisably a copy of %s",
			filepath.Join("design", "exoryn", "02-tokens", "tokens.css"))
	}

	if body := copied[index:]; body != source {
		t.Errorf("apps/console-web/app/tokens.css has drifted from "+
			"design/exoryn/02-tokens/tokens.css. Regenerate the copy rather than editing "+
			"it: a product palette that differs from the design system is a design "+
			"system nobody is following.\n\ndesign %d bytes, console copy %d bytes",
			len(source), len(body))
	}
}

// The Console's stylesheet uses tokens, not colour literals.
//
// One hard-coded hex is how a parallel palette starts. The rule is easier to keep than to
// recover: if no literal is reachable by convenience, none appears.
func TestTheConsoleStylesheetUsesOnlyTokens(t *testing.T) {
	path := filepath.Join(repoRoot, "apps", "console-web", "app", "globals.css")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read globals.css: %v", err)
	}

	for number, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "*") || strings.HasPrefix(trimmed, "/*") {
			continue
		}
		if strings.Contains(trimmed, "#") && !strings.Contains(trimmed, "var(--x-") {
			// A hex colour is the only thing "#" appears in here; a selector would be
			// an id, which this stylesheet does not use either.
			if hasHexColour(trimmed) {
				t.Errorf("globals.css:%d uses a colour literal: %q. Every colour comes "+
					"from a design token, or the product grows a palette the design "+
					"system does not know about.", number+1, trimmed)
			}
		}
	}
}

func hasHexColour(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		digits := 0
		for j := i + 1; j < len(line) && digits < 8; j++ {
			c := line[j]
			isHex := (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
			if !isHex {
				break
			}
			digits++
		}
		if digits == 3 || digits == 4 || digits == 6 || digits == 8 {
			return true
		}
	}
	return false
}

// The Console ships the brand's logo, not a copy of it that has drifted.
//
// The same property verified when the design system was imported, kept true afterwards:
// a subtly different logo in a public/ directory is how a brand drifts in the direction
// nobody approved.
func TestTheConsoleLogoIsTheBrandMaster(t *testing.T) {
	master := readSource(t, filepath.Join(repoRoot, "brand", "exoryn", "logos", "svg",
		"exoryn-primary-horizontal.svg"))
	served := readSource(t, filepath.Join(repoRoot, "apps", "console-web", "public",
		"exoryn-primary-horizontal.svg"))

	if master != served {
		t.Error("apps/console-web/public/exoryn-primary-horizontal.svg differs from the " +
			"brand master. SVG masters are not regenerated, optimized or redrawn by " +
			"implementation (brand authority, rules 3 and 9).")
	}
}
