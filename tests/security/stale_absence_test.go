package security

import (
	"regexp"
	"strings"
	"testing"
)

// Prose that says an endpoint does not exist, next to an endpoint that does.
//
// This is the failure that keeps coming back, and the route-table guard only catches
// it in the table. It has also appeared in a package comment, in three console pages
// and in the API reference's own paragraphs: a sentence that was true when it was
// written, kept being read after it stopped being true, and was believed precisely
// because it sounded careful.
//
// So every file that documents the running system is checked for an absence claim
// sitting near a route that is served.

// absenceClaims are the phrasings used when something genuinely was not built.
var absenceClaims = regexp.MustCompile(
	`(?i)(no phase has built|has not been built|have not been built|not built|nothing (stores|serves|exposes|persists) them|there is nothing to read|no endpoint (exists|behind))`)

// routeMention finds a /v1/ path in prose, with or without a code fence around it.
var routeMention = regexp.MustCompile(`/v1/[a-z0-9{}/_-]*`)

// TestNoDocumentClaimsAServedRouteIsMissing checks documentation and console prose.
//
// The window is deliberately generous: a claim and the route it is about are usually
// in the same sentence, and never further apart than a paragraph.
func TestNoDocumentClaimsAServedRouteIsMissing(t *testing.T) {
	served := servedRoutes(t)
	paths := map[string]bool{}
	for route := range served {
		_, path, _ := strings.Cut(route, " ")
		paths[path] = true
	}

	// Where a sentence explains a fixed defect it names it in the past tense, which
	// this guard cannot tell from a live claim. Those sentences say so.
	pastTense := regexp.MustCompile(`(?i)(used to|stopped being true|kept being read|no longer|this comment used|was no endpoint|there was no)`)

	// "Not built here" is a statement about this surface, not about the platform. The
	// console says it on purpose: the endpoints exist and the console will not call
	// them, because a console that could act would become required for execution.
	elsewhere := regexp.MustCompile(`(?i)not built (here|in the console|there)`)

	checked := 0
	for _, path := range append(docFiles(t), consoleSources(t)...) {
		source := readSource(t, path)
		for _, claim := range absenceClaims.FindAllStringIndex(source, -1) {
			checked++

			from := max(0, claim[0]-400)
			to := min(len(source), claim[1]+400)
			window := source[from:to]
			if pastTense.MatchString(window) || elsewhere.MatchString(window) {
				continue
			}

			for _, mention := range routeMention.FindAllString(window, -1) {
				if paths[strings.TrimRight(mention, "/")] {
					t.Errorf("%s says %q near %s, which is served. A stale caveat "+
						"describes a system nobody is running, and is believed because "+
						"it sounds careful.",
						short(path), strings.TrimSpace(source[claim[0]:claim[1]]), mention)
				}
			}
		}
	}

	if checked == 0 {
		t.Error("no absence claim was found anywhere; the guard is not reading the " +
			"documentation and would stay green over anything")
	}
}

func short(path string) string {
	if i := strings.Index(path, "agentic-assurance"); i >= 0 {
		return path[i+len("agentic-assurance")+1:]
	}
	return path
}
