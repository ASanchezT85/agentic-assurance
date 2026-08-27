// Package tests holds the Phase 0 repository guards.
//
// Two jobs: prove the mandatory structure exists, and prove later-phase scope has
// not been implemented early. Both are cheap greps on purpose. They are the crudest
// mechanism that actually fails when someone drifts.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repoRoot is the parent of tests/.
const repoRoot = ".."

func abs(t *testing.T, rel string) string {
	t.Helper()
	return filepath.Join(repoRoot, filepath.FromSlash(rel))
}

func TestMandatoryRootFilesExist(t *testing.T) {
	files := []string{
		"README.md",
		"MASTER_BUILD_SPEC.md",
		"Makefile",
		"docker-compose.yml",
		"go.work",
		"go.mod",
		"package.json",
		"pnpm-workspace.yaml",
		"pyproject.toml",
		".editorconfig",
		".gitignore",
		".env.example",
		"pnpm-lock.yaml",
	}
	for _, f := range files {
		if _, err := os.Stat(abs(t, f)); err != nil {
			t.Errorf("missing mandatory root file %s", f)
		}
	}
}

func TestMandatoryDirectoriesExist(t *testing.T) {
	dirs := []string{
		"apps/console-web",
		"cmd/assurance-gateway", "cmd/fleet-engine",
		"internal/gateway", "internal/authority", "internal/identity", "internal/intent",
		"internal/policy", "internal/execution", "internal/fleet", "internal/incident",
		"internal/evidence", "internal/broker",
		"packages/envelope-schema", "packages/event-schema", "packages/policy-schema",
		"packages/telemetry-sdk",
		"adapters/alpaca", "adapters/fakebroker", "adapters/rest", "adapters/mcp",
		"simulator/engine", "simulator/market", "simulator/agents", "simulator/execution",
		"simulator/assurance",
		"migrations/postgres", "migrations/clickhouse",
		"infra/docker", "infra/terraform", "infra/kubernetes",
		"tests/integration", "tests/security", "tests/performance", "tests/chaos",
		"tests/fixtures",
		"docs/adr", "docs/architecture", "docs/api", "docs/threat-model",
		"docs/operations", "docs/runbooks",
	}
	for _, d := range dirs {
		info, err := os.Stat(abs(t, d))
		if err != nil || !info.IsDir() {
			t.Errorf("missing mandatory directory %s", d)
		}
	}
}

func TestAllTwelveScenarioDirectoriesExist(t *testing.T) {
	names := []string{
		"S01_correlated_stop_loss", "S02_poisoned_news", "S03_stale_market_feed",
		"S04_model_regression", "S05_retry_storm", "S06_order_fragmentation",
		"S07_cross_agent_accumulation", "S08_liquidity_shock", "S09_policy_regression",
		"S10_intelligence_outage", "S11_agent_credential_compromise", "S12_normal_consensus",
	}
	for _, n := range names {
		if info, err := os.Stat(abs(t, "scenarios/"+n)); err != nil || !info.IsDir() {
			t.Errorf("missing scenario directory %s", n)
		}
	}
}

func TestAllLockedAndAcceptedADRsExist(t *testing.T) {
	entries, err := os.ReadDir(abs(t, "docs/adr"))
	if err != nil {
		t.Fatalf("read docs/adr: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, "ADR-") && len(name) > 7 {
			seen[name[4:7]] = true
		}
	}
	// 001-014 are locked by spec §6; 015-024 resolve audited contradictions.
	for _, n := range []string{
		"001", "002", "003", "004", "005", "006", "007", "008", "009", "010",
		"011", "012", "013", "014", "015", "016", "017", "018", "019", "020",
		"021", "022", "023", "024",
	} {
		if !seen[n] {
			t.Errorf("missing ADR-%s", n)
		}
	}
}

func TestArchitectureDocsExist(t *testing.T) {
	for _, f := range []string{
		"docs/architecture/system-context.md",
		"docs/architecture/container-view.md",
		"docs/architecture/hot-path.md",
		"docs/architecture/data-ownership.md",
		"docs/threat-model/README.md",
	} {
		if _, err := os.Stat(abs(t, f)); err != nil {
			t.Errorf("missing %s", f)
		}
	}
}

func TestEverySecurityInvariantIsDocumented(t *testing.T) {
	raw, err := os.ReadFile(abs(t, "docs/threat-model/README.md"))
	if err != nil {
		t.Fatalf("read threat model: %v", err)
	}
	body := string(raw)
	for _, id := range []string{
		"INV-001", "INV-002", "INV-003", "INV-004", "INV-005", "INV-006", "INV-007",
		"INV-008", "INV-009", "INV-010", "INV-011", "INV-012", "INV-013", "INV-014",
		"INV-015",
	} {
		if !strings.Contains(body, id) {
			t.Errorf("threat model does not document %s", id)
		}
	}
}
