package tests

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
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
func TestNoHRIImplementation(t *testing.T) {
	banned := []string{"HRI", "hri_score", "riskScore", "risk_score", "compositeRisk", "composite_risk"}
	walkSource(t, func(rel, body string) {
		for _, b := range banned {
			if strings.Contains(body, b) {
				t.Errorf("%s contains %q: no composite risk score in V0 (ADR-014)", rel, b)
			}
		}
	})
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

// TestConsoleIsStillAScaffold — ADR-017. The Phase 0 console proves the toolchain
// compiles. It does not fetch, and it does not render financial data. Delete this
// guard in the same commit that opens Phase 14.
func TestConsoleIsStillAScaffold(t *testing.T) {
	appDir := abs(t, "apps/console-web/app")
	err := filepath.WalkDir(appDir, func(path string, d fs.DirEntry, err error) error {
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
		for _, b := range []string{"fetch(", "axios", "new WebSocket", "EventSource("} {
			if strings.Contains(body, b) {
				rel, _ := filepath.Rel(appDir, path)
				t.Errorf("apps/console-web/app/%s performs I/O (%q); the Phase 0 console is a build target, not a UI (ADR-017)",
					filepath.ToSlash(rel), b)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk console: %v", err)
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
