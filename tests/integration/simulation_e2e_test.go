//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/simulation"
)

// The simulation surface end to end: an HTTP request, a real Python engine, a run
// durable in PostgreSQL, and the record retrievable afterwards.
//
// Spec section 46 lists these two endpoints and the engine wrote its record to stdout,
// so a run existed for as long as a terminal did. The unit tests cover what is refused;
// this covers what happens when a request is honoured.
//
// Run with:  make up && make migrate && make test-integration

func simInterpreter(t *testing.T) string {
	t.Helper()
	root := simRepoRoot(t)
	for _, c := range []string{
		filepath.Join(root, ".venv", "Scripts", "python.exe"),
		filepath.Join(root, ".venv", "bin", "python"),
	} {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	if runtime.GOOS != "windows" {
		if _, err := os.Stat("/usr/bin/python3"); err == nil {
			return "/usr/bin/python3"
		}
	}
	t.Skip("no project interpreter; run make bootstrap")
	return ""
}

func simRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return dir
}

type simRig struct {
	server *httptest.Server
	tenant string
	store  *simulation.Store
}

func newSimRig(t *testing.T) *simRig {
	t.Helper()
	python := simInterpreter(t)
	pool := usagePool(t)
	root := simRepoRoot(t)

	tenant := fmt.Sprintf("tenant_sim_%d", time.Now().UnixNano())
	t.Cleanup(func() { purge(t, pool, tenant, "simulation_runs", "evidence_events") })

	store := simulation.NewStore(pool)
	runner := &simulation.Runner{
		Python:      python,
		Repo:        root,
		ScenarioDir: filepath.Join(root, "simulator", "scenarios"),
		Store:       store,
		Evidence:    simulation.NewEvents(evidence.NewStore(pool), nil),
		Timeout:     2 * time.Minute,
		Concurrency: 2,
	}
	if err := runner.Prepare(); err != nil {
		t.Fatalf("runner: %v", err)
	}

	mux := http.NewServeMux()
	(&simulation.API{Runner: runner, Store: store}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &simRig{server: srv, tenant: tenant, store: store}
}

func (r *simRig) do(t *testing.T, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, r.server.URL+path, reader)
	req.Header.Set("X-Tenant-Id", r.tenant)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("%s %s returned non-JSON (%d): %s", method, path, resp.StatusCode, raw)
	}
	return resp.StatusCode, decoded
}

// awaitTerminal polls until the run finishes, because POST returns before it has.
func (r *simRig) awaitTerminal(t *testing.T, runID string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		status, body := r.do(t, http.MethodGet, "/v1/simulations/"+runID, "")
		if status != http.StatusOK {
			t.Fatalf("GET returned %d: %v", status, body)
		}
		switch body["status"] {
		case "COMPLETED", "FAILED":
			return body
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("the run never reached a terminal state")
	return nil
}

func TestSimulationEndToEnd(t *testing.T) {
	rig := newSimRig(t)

	status, accepted := rig.do(t, http.MethodPost, "/v1/simulations",
		`{"scenario":"correlated_panic","seed":7,"requested_by":"ana@example"}`)

	// 202: accepted and durable, and it has not happened yet. A caller that read 200
	// as "here is your result" would be reading an empty record.
	if status != http.StatusAccepted {
		t.Fatalf("status = %d, want 202: %v", status, accepted)
	}
	runID, _ := accepted["run_id"].(string)
	if runID == "" {
		t.Fatalf("no run id: %v", accepted)
	}
	if accepted["status"] != "QUEUED" {
		t.Errorf("status = %v, want QUEUED", accepted["status"])
	}

	// The run is retrievable immediately, before it has finished. That is the whole
	// reason the id comes back first.
	if code, body := rig.do(t, http.MethodGet, "/v1/simulations/"+runID, ""); code != http.StatusOK {
		t.Fatalf("a queued run was not retrievable: %d %v", code, body)
	}

	final := rig.awaitTerminal(t, runID)
	if final["status"] != "COMPLETED" {
		t.Fatalf("run %s: %v (%v)", runID, final["status"], final["error"])
	}

	fingerprint, _ := final["result_fingerprint"].(string)
	if len(fingerprint) != 64 {
		t.Errorf("result_fingerprint = %q; without it a run cannot be compared to "+
			"another, which is the only thing a simulation result is for", fingerprint)
	}
	if hash, _ := final["scenario_source_hash"].(string); len(hash) != 64 {
		t.Errorf("scenario_source_hash = %q; a record must say which file was run",
			final["scenario_source_hash"])
	}

	record, _ := final["record"].(map[string]any)
	if record == nil {
		t.Fatal("the completed run carries no record")
	}
	results, _ := record["results"].(map[string]any)
	if results == nil || results["intents_submitted"] == nil {
		t.Errorf("the record has no results: %v", record)
	}

	// The lifecycle events of the section 32 catalog. They have existed since Phase 0
	// and nothing had ever emitted one: a simulation ran against a customer's own
	// configuration and left no trace an auditor could find.
	chain, err := evidence.NewStore(usagePool(t)).Chain(context.Background(), rig.tenant, runID)
	if err != nil {
		t.Fatalf("evidence: %v", err)
	}
	seen := map[evidence.EventName]bool{}
	for _, e := range chain {
		seen[e.EventName] = true
	}
	for _, want := range []evidence.EventName{evidence.SimulationStarted, evidence.SimulationCompleted} {
		if !seen[want] {
			t.Errorf("the evidence chain is missing %s; got %d events", want, len(chain))
		}
	}
}

// The same scenario and seed produce the same fingerprint, through the API, across two
// separate engine processes. This is the property the whole surface rests on.
func TestTwoRunsOfOneSeedAgree(t *testing.T) {
	rig := newSimRig(t)

	fingerprints := make([]string, 2)
	for i := range fingerprints {
		_, accepted := rig.do(t, http.MethodPost, "/v1/simulations",
			`{"scenario":"demo","seed":42,"requested_by":"ana@example"}`)
		runID, _ := accepted["run_id"].(string)
		final := rig.awaitTerminal(t, runID)
		if final["status"] != "COMPLETED" {
			t.Fatalf("run %d failed: %v", i, final["error"])
		}
		fingerprints[i], _ = final["result_fingerprint"].(string)
	}

	if fingerprints[0] != fingerprints[1] {
		t.Errorf("the same scenario and seed produced two fingerprints, %q and %q. "+
			"A simulation that does not reproduce cannot be used to argue about "+
			"anything", fingerprints[0], fingerprints[1])
	}
}

// A run belongs to the tenant that asked for it, and looks identical to absent from
// anyone else. Spec section 45 lists cross-tenant leakage as a threat, and an error
// that distinguishes "no such run" from "not yours" is itself the disclosure.
func TestARunIsInvisibleToAnotherTenant(t *testing.T) {
	rig := newSimRig(t)

	_, accepted := rig.do(t, http.MethodPost, "/v1/simulations",
		`{"scenario":"demo","seed":1,"requested_by":"ana@example"}`)
	runID, _ := accepted["run_id"].(string)

	req, _ := http.NewRequest(http.MethodGet, rig.server.URL+"/v1/simulations/"+runID, nil)
	req.Header.Set("X-Tenant-Id", "tenant_someone_else")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404; another tenant's run was visible", resp.StatusCode)
	}

	rig.awaitTerminal(t, runID)
}

// A listing shows the tenant's runs and omits the records, which are by far the
// largest thing in them.
func TestListingOmitsTheRecords(t *testing.T) {
	rig := newSimRig(t)

	_, accepted := rig.do(t, http.MethodPost, "/v1/simulations",
		`{"scenario":"demo","seed":3,"requested_by":"ana@example"}`)
	runID, _ := accepted["run_id"].(string)
	rig.awaitTerminal(t, runID)

	status, body := rig.do(t, http.MethodGet, "/v1/simulations", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d: %v", status, body)
	}

	runs, _ := body["runs"].([]any)
	if len(runs) == 0 {
		t.Fatal("the listing is empty")
	}
	for _, entry := range runs {
		run, _ := entry.(map[string]any)
		if run["record"] != nil {
			t.Error("a listing carried a full record; fifty of those is megabytes of " +
				"detail nobody asked for")
		}
		if run["run_id"] == "" {
			t.Error("a listed run has no id")
		}
	}
}

// A scenario the engine refuses produces a FAILED run that says why, rather than a run
// stuck in RUNNING forever.
func TestAnEngineRefusalBecomesAFailedRun(t *testing.T) {
	python := simInterpreter(t)
	pool := usagePool(t)
	root := simRepoRoot(t)

	tenant := fmt.Sprintf("tenant_simfail_%d", time.Now().UnixNano())
	t.Cleanup(func() { purge(t, pool, tenant, "simulation_runs", "evidence_events") })

	// A scenario directory holding a file the engine will reject: valid JSON, and a
	// misspelled field. The API accepts the name because the file exists; the engine
	// refuses the contents, which is exactly the split this test is about.
	dir := t.TempDir()
	broken := `{"scenario_id":"broken","scenario_version":1,"description":"x",` +
		`"archetypes":[{"name":"a","population":5,"panic_probabilty":0.9}]}`
	if err := os.WriteFile(filepath.Join(dir, "broken.json"), []byte(broken), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	store := simulation.NewStore(pool)
	runner := &simulation.Runner{
		Python: python, Repo: root, ScenarioDir: dir, Store: store,
		Timeout: time.Minute, Concurrency: 1,
	}
	if err := runner.Prepare(); err != nil {
		t.Fatalf("runner: %v", err)
	}

	mux := http.NewServeMux()
	(&simulation.API{Runner: runner, Store: store}).Routes(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rig := &simRig{server: srv, tenant: tenant, store: store}
	status, accepted := rig.do(t, http.MethodPost, "/v1/simulations",
		`{"scenario":"broken","seed":1,"requested_by":"ana@example"}`)
	if status != http.StatusAccepted {
		t.Fatalf("status = %d: %v", status, accepted)
	}

	runID, _ := accepted["run_id"].(string)
	final := rig.awaitTerminal(t, runID)

	if final["status"] != "FAILED" {
		t.Fatalf("status = %v, want FAILED", final["status"])
	}
	reason, _ := final["error"].(string)
	if !strings.Contains(reason, "panic_probabilty") {
		t.Errorf("error = %q; the failure must carry the engine's own reason, or an "+
			"operator has to reproduce it by hand to find out what was wrong", reason)
	}
}
