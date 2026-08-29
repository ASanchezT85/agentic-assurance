package security

import (
	"regexp"
	"strings"
	"testing"
)

// The API reference and the routes the binaries actually serve, checked against each
// other in both directions.
//
// Three separate audit passes found documentation describing a system nobody was
// running: the evidence endpoints documented as unauthenticated after they were not,
// the console page saying controls were not persisted after they were, the route table
// listing POST /v1/authority-grants as a future phase while it served requests. Each
// was fixed by hand, which fixes the instance and not the class.
//
// A route table nobody checks is a comment. This checks it.

var (
	registration = regexp.MustCompile(`(?m)HandleFunc\(\s*"((?:GET|POST|PUT|DELETE) )?(/v1/[^"]*)"`)
	documented   = regexp.MustCompile(`(?m)^(GET|POST)\s+(/v1/\S*)\s+(.*)$`)
)

// servedRoutes returns every /v1/ path the binaries register.
func servedRoutes(t *testing.T) map[string]bool {
	t.Helper()

	out := map[string]bool{}
	for _, path := range goSources(t) {
		source := readSource(t, path)
		if !strings.Contains(source, "HandleFunc(") {
			continue
		}
		for _, m := range registration.FindAllStringSubmatch(source, -1) {
			// Method and path together: GET and POST on one path are two endpoints,
			// and a table that documented only one of them would pass a guard that
			// compared paths alone.
			out[strings.TrimSpace(m[1])+" "+m[2]] = true
		}
	}
	if len(out) < 10 {
		t.Fatalf("found %d served routes; the walk is not finding registrations and "+
			"this guard would pass over nothing", len(out))
	}
	return out
}

// documentedRoutes returns the API reference's route table: path to whether it claims
// to be built.
func documentedRoutes(t *testing.T) map[string]bool {
	t.Helper()

	// The route table only, which is the first fenced block. Later sections show
	// example requests, and a POST in an example is a curl for a reader rather than a
	// claim about what is built.
	doc := readSource(t, repoRoot+"/docs/api/README.md")
	_, after, found := strings.Cut(doc, "```text")
	if !found {
		t.Fatal("docs/api/README.md has no route table")
	}
	table, _, _ := strings.Cut(after, "```")
	out := map[string]bool{}
	for _, m := range documented.FindAllStringSubmatch(table, -1) {
		// The query string is documentation for the caller and not part of the route
		// the mux registers.
		path, _, _ := strings.Cut(m[2], "?")
		out[m[1]+" "+path] = strings.Contains(m[3], "DONE")
	}
	if len(out) < 10 {
		t.Fatalf("found %d documented routes; the table is not being parsed", len(out))
	}
	return out
}

// A served route that nobody wrote down is a surface a customer cannot find and a
// reviewer cannot audit.
func TestEveryServedRouteIsDocumented(t *testing.T) {
	documented := documentedRoutes(t)

	for route := range servedRoutes(t) {
		if _, listed := documented[route]; !listed {
			t.Errorf("%s is served and does not appear in docs/api/README.md. A route "+
				"table nobody checks is a comment.", route)
		}
	}
}

// And the reverse, which is the failure that keeps happening: prose that was true when
// it was written and is not any more.
func TestEveryRouteDocumentedAsBuiltIsServed(t *testing.T) {
	served := servedRoutes(t)

	for route, done := range documentedRoutes(t) {
		if done && !served[route] {
			t.Errorf("docs/api/README.md marks %s DONE and no binary registers it. "+
				"A reader who believes the table would build against an endpoint that "+
				"answers 404.", route)
		}
	}
}

// A route the table still calls a future phase, that is already serving requests, is
// the same failure pointed the other way: it was true when it was written.
func TestNoServedRouteIsListedAsUnbuilt(t *testing.T) {
	served := servedRoutes(t)

	for route, done := range documentedRoutes(t) {
		if !done && served[route] {
			t.Errorf("docs/api/README.md lists %s as not built and a binary serves it. "+
				"The table is describing a system nobody is running.", route)
		}
	}
}
