package tests

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scanned source extensions. Documentation is deliberately excluded: the ADRs and
// the threat model discuss forbidden concepts by name, and must be free to do so.
var sourceExts = map[string]bool{".go": true, ".ts": true, ".tsx": true, ".py": true}

var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".next": true, "docs": true,
	".pytest_cache": true, ".ruff_cache": true, "__pycache__": true, ".venv": true,
}

// walkSource visits every source file in the repository, skipping vendored trees,
// documentation, and this guard file itself.
func walkSource(t *testing.T, visit func(relPath, body string)) {
	t.Helper()
	root := abs(t, ".")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !sourceExts[filepath.Ext(path)] {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "tests/") && strings.HasSuffix(rel, "_guard_test.go") {
			return nil // this file names the forbidden strings on purpose
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		visit(rel, string(raw))
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

// TestNoRealMoneyCredentialsAreRequired — handoff §14. Phase 0 must not require, or
// even name, a live broker credential. .env.example is the only committed env file.
func TestNoRealMoneyCredentialsAreRequired(t *testing.T) {
	raw, err := os.ReadFile(abs(t, ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}
	body := strings.ToUpper(string(raw))
	for _, banned := range []string{
		"ALPACA_API_KEY", "ALPACA_SECRET", "ALPACA_LIVE", "BROKER_API_KEY",
		"BROKER_SECRET", "TRADING_API_KEY",
	} {
		if strings.Contains(body, banned) {
			t.Errorf(".env.example names a broker credential: %s", banned)
		}
	}

	entries, err := os.ReadDir(abs(t, "."))
	if err != nil {
		t.Fatalf("read root: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".env") && e.Name() != ".env.example" {
			t.Errorf("committed env file other than .env.example: %s", e.Name())
		}
	}
}

// TestNoTradingRecommendationModule — handoff §14, ADR-001. The product boundary
// forbids generating trade ideas, and the crudest way that shows up is a symbol
// named for it.
func TestNoTradingRecommendationModule(t *testing.T) {
	banned := []string{
		"buy_stock", "sell_stock", "trade_crypto", "buyStock", "sellStock",
		"recommendTrade", "recommend_trade", "pickStock", "pick_stock",
		"portfolioRecommendation", "portfolio_recommendation", "generateTradeIdea",
	}
	walkSource(t, func(rel, body string) {
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s contains %q: trading recommendation scope is prohibited (ADR-001)", rel, b)
			}
		}
	})
}

// TestNoHRIImplementation — handoff §14, ADR-014. V0 exposes a Fleet Risk Vector.
// A composite score requires empirical calibration and its own ADR.
//
// The check is on declared identifiers, not on raw text. An earlier version matched
// the substring anywhere in a file, which failed on a comment explaining why no such
// score exists. A guard that punishes the explanation teaches authors to delete the
// explanation, so it now looks at what the code declares and lets the prose say
// whatever it needs to.
func TestNoHRIImplementation(t *testing.T) {
	banned := []string{"hri", "hriscore", "riskscore", "compositerisk", "compositescore"}

	fset := token.NewFileSet()
	root := abs(t, ".")

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_guard_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return nil // unparseable files are the compiler's problem, not this guard's
		}
		rel, _ := filepath.Rel(root, path)

		ast.Inspect(file, func(n ast.Node) bool {
			var name string
			switch node := n.(type) {
			case *ast.Ident:
				name = node.Name
			case *ast.Field:
				for _, id := range node.Names {
					checkIdentifier(t, filepath.ToSlash(rel), id.Name, banned)
				}
				return true
			default:
				return true
			}
			checkIdentifier(t, filepath.ToSlash(rel), name, banned)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}

func checkIdentifier(t *testing.T, file, name string, banned []string) {
	t.Helper()
	lower := strings.ToLower(name)
	for _, b := range banned {
		if lower == b || strings.HasSuffix(lower, b) {
			t.Errorf("%s declares or uses identifier %q: no composite risk score in V0 (ADR-014)",
				file, name)
		}
	}
}

// TestGatewayRequiresNoLLMPackage — handoff §14, ADR-004 and ADR-022. Checked at the
// dependency level, which is where it actually matters, not just at import sites.
func TestGatewayRequiresNoLLMPackage(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}
	gomod := strings.ToLower(string(raw))
	for _, b := range []string{"openai", "anthropic", "langchain", "cohere", "huggingface", "ollama", "genai"} {
		if strings.Contains(gomod, b) {
			t.Errorf("go.mod depends on %q: no LLM in the decision path (ADR-004, ADR-022)", b)
		}
	}

	walkSource(t, func(rel, body string) {
		if !strings.HasPrefix(rel, "cmd/") && !strings.HasPrefix(rel, "internal/") {
			return
		}
		lower := strings.ToLower(body)
		for _, b := range []string{"openai", "anthropic", "langchain"} {
			if strings.Contains(lower, b) {
				t.Errorf("%s references %q (ADR-004, ADR-022)", rel, b)
			}
		}
	})
}

// TestConsoleHasExactlySixSurfaces — ADR-017 and spec section 48.
//
// This replaces the Phase 0 scaffold guard, which failed if any page performed I/O.
// ADR-017 said that guard comes off in the same commit that adds the first real
// surface, and Phase 14 is that commit.
//
// What replaces it is the constraint that actually matters now. Section 48 fixes six
// principal surfaces and ends by saying not to add dashboards without a defined
// acceptance requirement. Consoles do not grow a seventh screen by decision; they
// grow one because somebody needed a place to put something.
func TestConsoleHasExactlySixSurfaces(t *testing.T) {
	required := map[string]bool{
		"fleet": false, "flow": false, "dependencies": false,
		"incidents": false, "lab": false, "controls": false,
	}

	appDir := abs(t, "apps/console-web/app")
	entries, err := os.ReadDir(appDir)
	if err != nil {
		t.Fatalf("read console app: %v", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Next.js private and grouping directories are not routes.
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, "(") || strings.HasPrefix(name, "@") {
			continue
		}
		if _, isSurface := required[name]; !isSurface {
			t.Errorf("apps/console-web/app/%s is a seventh surface; spec section 48 fixes "+
				"six and requires a defined acceptance requirement for any more", name)
			continue
		}
		required[name] = true
	}

	for name, present := range required {
		if !present {
			t.Errorf("surface %q from spec section 48 is missing", name)
		}
	}
}

// TestConsoleHasNoWritePath — spec sections 17 and 59.
//
// Production must be unaffected when the console is down, which is only true while
// the console cannot cause anything. A form that posts, or a fetch with a method,
// makes it operationally load-bearing: the fastest way to stop trading would then run
// through a service the architecture treats as optional.
func TestConsoleHasNoWritePath(t *testing.T) {
	roots := []string{"apps/console-web/app", "apps/console-web/lib", "apps/console-web/components"}

	for _, root := range roots {
		dir := abs(t, root)
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if !sourceExts[filepath.Ext(path)] {
				return nil
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			body := string(raw)
			rel, _ := filepath.Rel(abs(t, "."), path)
			rel = filepath.ToSlash(rel)

			for _, write := range []string{
				`method: "POST"`, `method: "PUT"`, `method: "PATCH"`, `method: "DELETE"`,
				`method: 'POST'`, `method: 'PUT'`, `method: 'PATCH'`, `method: 'DELETE'`,
				`method="post"`, `method="POST"`,
			} {
				if strings.Contains(body, write) {
					t.Errorf("%s contains %s; the console reads and production must be "+
						"unaffected when it is down (spec sections 17 and 59)", rel, write)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
}

// TestNoLLMDependencyInTheConsole mirrors the Go check on the TypeScript side.
func TestNoLLMDependencyInTheConsole(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "apps/console-web/package.json"))
	if err != nil {
		t.Fatalf("read console package.json: %v", err)
	}
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal(raw, &pkg); err != nil {
		t.Fatalf("parse console package.json: %v", err)
	}
	for _, set := range []map[string]string{pkg.Dependencies, pkg.DevDependencies} {
		for name := range set {
			lower := strings.ToLower(name)
			for _, b := range []string{"openai", "anthropic", "langchain", "ai-sdk"} {
				if strings.Contains(lower, b) {
					t.Errorf("console depends on %q (ADR-022)", name)
				}
			}
		}
	}
}

// TestTemporalStaysOutOfCompose — ADR-018, approved 2026-08-27. Temporal is deferred
// out of V0 and was deleted from docker-compose.yml outright: an unused service
// definition is an invitation. Reintroducing it requires a new ADR, and that ADR is
// where this guard gets updated.
func TestTemporalStaysOutOfCompose(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	compose := string(raw)

	if strings.Contains(compose, "\n  temporal:") {
		t.Error("docker-compose.yml defines a temporal service; Temporal is deferred out of V0 (ADR-018)")
	}
	if strings.Contains(compose, "temporalio/") {
		t.Error("docker-compose.yml references a temporalio image; Temporal is deferred out of V0 (ADR-018)")
	}
	for _, line := range strings.Split(compose, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "- temporal") {
			t.Errorf("docker-compose.yml: a service depends on temporal (%q) (ADR-018)", strings.TrimSpace(line))
		}
	}

	// No developer command or CI step may start it either.
	for _, f := range []string{"Makefile", "scripts/verify.sh", ".github/workflows/ci.yml"} {
		body, err := os.ReadFile(abs(t, f))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), "profile temporal") {
			t.Errorf("%s references a temporal profile; Temporal is deferred out of V0 (ADR-018)", f)
		}
	}
}

// allowedModules is every third-party module the core is permitted to depend on,
// with the reason it earned a place. Phase 0 asserted go.mod had no require block at
// all, which was the right guard while the answer was "none" and the wrong one the
// moment a real dependency was needed.
//
// An allowlist keeps what mattered about the original rule: a dependency arriving
// here has to be a deliberate act with a stated reason, not a drive-by from
// `go get`. Adding a line to this map is the review.
var allowedModules = map[string]string{
	"github.com/jackc/pgx/v5": "PostgreSQL driver. Phase 3: authority grants are the first " +
		"persisted tenant-scoped records, and row level security needs a real connection.",
	"github.com/jackc/pgpassfile":    "pgx transitive dependency.",
	"github.com/jackc/pgservicefile": "pgx transitive dependency.",
	"golang.org/x/text":              "pgx transitive dependency.",
	"github.com/jackc/puddle/v2":     "pgx connection pool, transitive.",
	"golang.org/x/crypto":            "pgx SCRAM authentication, transitive.",
	"golang.org/x/sync":              "pgx transitive dependency.",
	"gopkg.in/yaml.v3": "Policy authoring format (spec section 15.1). Compile-time only: " +
		"section 15.2 forbids interpreting YAML per order, and TestEvaluationNeverParsesSource " +
		"asserts the evaluator never reaches it.",
	"github.com/kr/text": "Test-only transitive of yaml.v3, through gopkg.in/check.v1. " +
		"Reachable from `go test all`, never linked into a binary.",
	"github.com/rogpeppe/go-internal": "Test-only transitive of yaml.v3. `go mod why` " +
		"reports the main module does not need it; go mod tidy retains it for the test graph.",
	"github.com/nats-io/nats.go": "NATS JetStream client (spec section 9.7). Phase 6: the " +
		"event backbone. Asynchronous only, and forbidden on the hot path by INV-005.",
	// The market data adapter is stdlib-only on purpose: it speaks HTTP and JSON, and
	// a provider SDK would put a vendor's types one import from the fleet engine.
	"github.com/nats-io/nkeys":      "nats.go transitive dependency (authentication).",
	"github.com/nats-io/nuid":       "nats.go transitive dependency (identifier generation).",
	"github.com/klauspost/compress": "nats.go transitive dependency.",
	"golang.org/x/sys":              "nats.go transitive dependency.",
}

// TestDependenciesAreOnTheAllowlist fails when go.mod grows a module nobody wrote a
// reason for.
func TestDependenciesAreOnTheAllowlist(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "go.mod"))
	if err != nil {
		t.Fatalf("read go.mod: %v", err)
	}

	inRequire := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)

		switch {
		case strings.HasPrefix(trimmed, "require ("):
			inRequire = true
			continue
		case inRequire && trimmed == ")":
			inRequire = false
			continue
		case strings.HasPrefix(trimmed, "require "):
			// single-line require
			trimmed = strings.TrimPrefix(trimmed, "require ")
		case !inRequire:
			continue
		}

		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		module := strings.Fields(trimmed)[0]
		if _, ok := allowedModules[module]; !ok {
			t.Errorf("go.mod requires %q, which is not on the allowlist in %s.\n"+
				"Add it there with the reason it is needed, or remove the dependency.",
				module, "tests/scope_guard_test.go")
		}
	}
}

// TestNoAPIKeyValuesAreCommitted looks for a credential that was written down.
//
// The existing env-file guard checks for named broker variables. This one is about
// values: an assignment whose right-hand side looks like an actual key, anywhere in
// the repository. Keys arrive by chat, by copy-paste and by "just for a minute", and
// a repository is the worst place for one to land quietly.
func TestNoAPIKeyValuesAreCommitted(t *testing.T) {
	// <NAME>_KEY / _SECRET / _TOKEN / _PASSWORD followed by a value that is not
	// obviously a placeholder.
	assignment := regexp.MustCompile(
		`(?i)(api[_-]?key|apikey|secret[_-]?key|access[_-]?token|auth[_-]?token|password)\s*[:=]\s*["']?([A-Za-z0-9_\-]{16,})["']?`)

	// Placeholders and test doubles are fine and necessary; a guard that banned them
	// would ban the tests that prove the real thing is handled correctly.
	//
	// The `_dev_only` suffix is the convention for a value that is deliberately
	// written down because it is worthless outside a developer's laptop. Making it a
	// naming rule rather than a per-file exception means the intent is visible in the
	// value itself, and anyone adding one has to say so in the name.
	allowed := regexp.MustCompile(`(?i)(^(test|fake|dummy|example|placeholder|changeme|your|xxx)|_dev_only$|^super-secret-key-value$)`)

	root := abs(t, ".")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipDirs[d.Name()] || d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		switch filepath.Ext(path) {
		case ".go", ".ts", ".tsx", ".py", ".yml", ".yaml", ".json", ".sh", ".md", ".sql", "":
		default:
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if strings.HasSuffix(rel, "_guard_test.go") {
			return nil // this file describes the pattern it is looking for
		}

		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, match := range assignment.FindAllStringSubmatch(string(raw), -1) {
			value := match[2]
			if allowed.MatchString(value) {
				continue
			}
			t.Errorf("%s assigns %s a concrete-looking value; credentials belong in the "+
				"environment or a secret manager, never in the repository (spec section 35)",
				rel, match[1])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
}
