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

	// Phase 0 binaries must be stdlib-only. A dependency arriving here should be a
	// deliberate, reviewed act, not a drive-by.
	if strings.Contains(gomod, "require") {
		t.Error("go.mod has a require block; Phase 0 Go code is stdlib-only")
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

// TestTemporalStaysOptional — ADR-018, approved 2026-08-27. Temporal is deferred out
// of V0 and survives only as an unused Compose profile. The failure mode a deferral
// invites is quiet promotion back to a dependency, so it is checked rather than
// trusted.
func TestTemporalStaysOptional(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "docker-compose.yml"))
	if err != nil {
		t.Fatalf("read docker-compose.yml: %v", err)
	}
	compose := string(raw)

	// The temporal service must be gated behind a profile.
	idx := strings.Index(compose, "\n  temporal:")
	if idx < 0 {
		return // removing it entirely is also compliant with ADR-018
	}
	block := compose[idx:]
	if end := strings.Index(block[1:], "\n  "); end > 0 {
		// crude block scan: stop at the next service key at the same indent
		if next := strings.Index(block[1:], "\n\n  "); next > 0 {
			block = block[:next]
		}
	}
	if !strings.Contains(block, "profiles:") {
		t.Error("docker-compose.yml: the temporal service has no profiles: key; it must stay opt-in (ADR-018)")
	}

	// No service may depend on it.
	for _, line := range strings.Split(compose, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- temporal") || trimmed == "temporal:" && strings.Contains(line, "      ") {
			t.Errorf("docker-compose.yml: a service depends on temporal (%q); nothing may depend on it (ADR-018)", trimmed)
		}
	}

	// No developer command or CI step may start it.
	for _, f := range []string{"Makefile", "scripts/verify.sh", ".github/workflows/ci.yml"} {
		body, err := os.ReadFile(abs(t, f))
		if err != nil {
			continue
		}
		if strings.Contains(string(body), "--profile temporal") {
			t.Errorf("%s starts the temporal profile; Phase 0 boot must not require it (ADR-018)", f)
		}
	}
}
