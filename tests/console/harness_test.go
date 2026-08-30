//go:build console

// What the Console actually renders.
//
// The Console had four guards over its source — tokens, colour literals, logo, collection
// keys — and none of them could catch what shipped: it read `/v1/evidence` expecting a
// field the endpoint does not return, so a chain of ten events rendered as "the source
// answered; it had nothing to report". A structural test cannot see that. It was found by
// driving a browser, once, by hand.
//
// So these tests drive the built Console against a gateway this file controls, and assert
// on the HTML it produces. Controlled rather than live, because the properties worth
// testing are the ones a live environment will not reliably produce on demand: a source
// that cannot be read, a source that answers with nothing, a correction that points at an
// earlier event, a control that binds versus one that is only recommended.
//
//	go test -tags=console -count=1 -v ./tests/console/
//
// It needs Node and a built Console; it builds one itself. No database, no gateway, no
// containers — the point is the rendering, and everything under it is a fixture.
package console

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fixture is what the fake platform answers for one endpoint.
type fixture struct {
	status int
	body   any
}

// platform is a stand-in for the gateway and the fleet engine.
//
// Every surface reads one of these, and what a surface must do with an unreadable source,
// an empty one and a full one is precisely what is under test — so the source is a map of
// canned answers rather than a running system.
type platform struct {
	mu      sync.Mutex
	answers map[string]fixture
	gateway *httptest.Server
	fleet   *httptest.Server
}

func newPlatform(t *testing.T) *platform {
	t.Helper()
	p := &platform{answers: map[string]fixture{}}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		answer, ok := p.answers[r.URL.Path]
		p.mu.Unlock()

		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(answer.status)
		if answer.body != nil {
			_ = json.NewEncoder(w).Encode(answer.body)
		}
	})

	p.gateway = httptest.NewServer(handler)
	p.fleet = httptest.NewServer(handler)
	t.Cleanup(p.gateway.Close)
	t.Cleanup(p.fleet.Close)
	return p
}

func (p *platform) answer(path string, status int, body any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.answers[path] = fixture{status: status, body: body}
}

// console is the built Console, running.
type console struct {
	base string
}

// start builds the Console once and runs it against the fake platform.
//
// `next start` rather than the dev server: the dev server recompiles per request and
// tolerates things a production build refuses, and what a customer runs is the build.
func (p *platform) start(t *testing.T) *console {
	t.Helper()
	root := repoRoot(t)
	app := filepath.Join(root, "apps", "console-web")

	env := append(os.Environ(),
		"GATEWAY_URL="+p.gateway.URL,
		"FLEET_ENGINE_URL="+p.fleet.URL,
		// Present, because an absent token is its own refusal path and every surface
		// would report that instead of what is being tested.
		"CONSOLE_API_TOKEN=console-behaviour-test-token",
	)

	build := exec.Command("pnpm", "--filter", "console-web", "build")
	build.Dir = root
	build.Env = env
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the console: %v\n%s", err, lastLines(string(out), 25))
	}

	port := freePort(t)
	run := exec.Command("pnpm", "exec", "next", "start", "--port", fmt.Sprint(port))
	run.Dir = app
	run.Env = env

	// Outside t.TempDir(). `next start` spawns a child that inherits this handle and
	// outlives the kill by a moment, and Windows refuses to remove a directory holding
	// an open one — so a passing test failed on its own cleanup. A stray log in the
	// system temp directory is the cheaper problem.
	logs, err := os.CreateTemp("", "exoryn-console-*.log")
	if err != nil {
		t.Fatalf("log: %v", err)
	}
	run.Stdout = logs
	run.Stderr = logs

	if err := run.Start(); err != nil {
		t.Fatalf("start the console: %v", err)
	}
	t.Cleanup(func() {
		if run.Process != nil {
			_ = run.Process.Kill()
			_, _ = run.Process.Wait()
		}
		_ = logs.Close()
		_ = os.Remove(logs.Name())
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(base + "/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return &console{base: base}
			}
		}
		if run.ProcessState != nil && run.ProcessState.Exited() {
			t.Fatalf("the console exited during startup:\n%s", readAll(logs.Name()))
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("the console never served:\n%s", readAll(logs.Name()))
	return nil
}

// page fetches one surface and returns its rendered HTML.
func (c *console) page(t *testing.T, path string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		t.Fatalf("request %s: %v", path, err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("fetch %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s answered %d:\n%s", path, resp.StatusCode, lastLines(string(body), 10))
	}
	return string(body)
}

// --- assertions -----------------------------------------------------------------

// mustContain fails with the reason a reader would care about, not with a diff.
//
// Against the page as a reader sees it. React separates adjacent text nodes with an empty
// HTML comment, so `sequence {n}` reaches the browser as `sequence <!-- -->2` and renders
// as "sequence 2"; asserting on the raw markup would fail on a page that is correct.
func mustContain(t *testing.T, html, needle, why string) {
	t.Helper()
	if !strings.Contains(asRead(html), needle) {
		t.Errorf("the page does not contain %q.\n%s", needle, why)
	}
}

// asRead removes React's text-node separators so an assertion can be written the way the
// sentence appears on screen.
func asRead(html string) string {
	return strings.ReplaceAll(html, "<!-- -->", "")
}

func mustNotContain(t *testing.T, html, needle, why string) {
	t.Helper()
	if strings.Contains(asRead(html), needle) {
		t.Errorf("the page contains %q.\n%s", needle, why)
	}
}

// --- helpers --------------------------------------------------------------------

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("cwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", ".."))
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()
	return listener.Addr().(*net.TCPAddr).Port
}

func readAll(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(no log)"
	}
	return lastLines(string(raw), 25)
}

func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
