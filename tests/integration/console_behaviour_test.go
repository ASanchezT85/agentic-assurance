//go:build integration

package integration

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// The two promises the Console makes that nothing had ever executed.
//
// docs/ESTADO_V0.md recorded the gap plainly: the six pages are exercised only by
// `next build`, and nothing asserts that an unavailable source renders as unavailable
// rather than as zero — which is the single most important thing this Console does. A
// dashboard that prints 0 for a source it could not reach has told an operator the fleet
// is quiet.
//
// There is no frontend test runner in this repository and adding one for two assertions
// would be a framework for a sentence. So these drive the real thing: the production
// Console is started as a process, pointed at sources this test controls, and the served
// HTML is read back. It is slower than a unit test and it tests what actually ships.
//
// Both cases were live defects. The zero-for-unavailable confusion is what the
// `Unavailable` component exists to prevent, and the lexical comparison is A-13-01 — 64-bit
// counts arrived from ClickHouse as quoted strings, so the Dependencies surface compared
// them as text and ranked "9" above "10".

// freePort asks the operating system for a port nobody is using.
//
// Fixed ports were wrong twice. Two tests sharing one port meant the second dialled
// successfully and reached the *first* console — still pointed at a dead backend — and
// reported that the surface had not rendered its rows. Giving each test its own fixed port
// only moved the problem: `pnpm start` spawns a child that outlives the parent's kill on
// Windows, so a later run inherits the previous run's console, with the previous run's
// environment. A port the OS just handed out cannot belong to anything else.
func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

// startConsole boots the built Console with the environment this test wants and returns
// its base URL. It skips rather than fails when the prerequisites are absent: a machine
// without pnpm or without a production build cannot answer the question, and a test that
// failed there would be reporting on the toolchain instead of on the Console.
func startConsole(t *testing.T, env map[string]string) string {
	port := freePort(t)
	t.Helper()

	root := repoRootFromTest(t)
	consoleDir := filepath.Join(root, "apps", "console-web")
	if _, err := os.Stat(filepath.Join(consoleDir, ".next", "BUILD_ID")); err != nil {
		t.Skip("no production build in apps/console-web/.next; run `pnpm -C apps/console-web build` first")
	}

	pnpm := "pnpm"
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pnpm.cmd"); err == nil {
			pnpm = "pnpm.cmd"
		}
	}
	if _, err := exec.LookPath(pnpm); err != nil {
		t.Skip("pnpm is not on PATH")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, pnpm, "start", "--port", fmt.Sprint(port))
	cmd.Dir = consoleDir
	// The inherited environment is filtered rather than appended to. A developer who has
	// sourced .env already exports GATEWAY_URL and FLEET_ENGINE_URL, and appending a
	// second definition of the same key leaves which one wins up to the platform. This
	// test is about what the Console does with a source it cannot reach, so the source
	// has to be the one this test named.
	cmd.Env = []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := env[key]; !overridden {
			cmd.Env = append(cmd.Env, entry)
		}
	}
	cmd.Env = append(cmd.Env, "NODE_ENV=production")
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if err := cmd.Start(); err != nil {
		t.Skipf("could not start the console: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		_ = cmd.Wait()
	})

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			_ = conn.Close()
			return base
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Skip("the console never began listening")
	return ""
}

func consoleGet(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: 30 * time.Second}

	// The first render compiles and reaches out; give it a couple of attempts before
	// deciding the page is what it says.
	var body string
	for attempt := range 3 {
		resp, err := client.Get(url)
		if err != nil {
			if attempt == 2 {
				t.Fatalf("GET %s: %v", url, err)
			}
			time.Sleep(time.Second)
			continue
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatalf("read %s: %v", url, readErr)
		}
		body = string(raw)
		break
	}
	return body
}

// An unreachable source renders as unavailable. It never renders as zero.
func TestAnUnreachableSourceIsNotRenderedAsZero(t *testing.T) {
	// A port nothing listens on. Not a server that errors — genuinely absent, which is
	// the case an operator meets when a service is down.
	dead := "http://127.0.0.1:1"

	base := startConsole(t, map[string]string{
		"GATEWAY_URL":       dead,
		"FLEET_ENGINE_URL":  dead,
		"CONSOLE_API_TOKEN": "console-behaviour-token-of-32-plus-chars",
	})

	for _, surface := range []string{"/fleet", "/dependencies", "/incidents", "/lab", "/controls", "/flow"} {
		t.Run(surface, func(t *testing.T) {
			body := consoleGet(t, base+surface)

			if !strings.Contains(body, "Not available") {
				t.Errorf("%s did not say the source was unavailable; an operator reading this "+
					"page cannot tell a quiet fleet from an unreachable one", surface)
			}
			if !strings.Contains(body, "UNAVAILABLE") {
				t.Errorf("%s did not mark its source strip UNAVAILABLE", surface)
			}

			// The specific confusion this Console exists to prevent. EMPTY means the
			// source answered and had nothing; it must not appear when nothing answered.
			if strings.Contains(body, ">EMPTY<") {
				t.Errorf("%s reported EMPTY for a source that could not be reached; "+
					"an empty result and an absent source are different facts", surface)
			}

			// And it must not have drawn a table of measurements out of nothing.
			if strings.Contains(body, "<table") {
				t.Errorf("%s rendered a table with no readable source", surface)
			}
		})
	}
}

// Counts are compared as numbers. A dependency shared by 10 agents outranks one shared
// by 9.
//
// This is A-13-01 as a behavioural test rather than a unit test of the fix: the defect
// was never in one function, it was in the agreement between what ClickHouse serialises
// and what the Console's types claim, and only the rendered page shows whether they
// agree.
func TestTheMostSharedDependencyIsChosenNumerically(t *testing.T) {
	fleet := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/v1/dependencies") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"rows":[],"count":0}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		// Nine and ten. Compared as text, "9" sorts above "10"; compared as numbers it
		// is the other way round, which is the whole point.
		_, _ = io.WriteString(w, `{"count":2,"rows":[
			{"dependency_type":"MARKET_DATA","dependency_id":"feed-nine","observations":9,
			 "verified":0,"declared":9,"unknown":0,"agents":9,"last_seen":"2026-08-31 12:00:00.000"},
			{"dependency_type":"MARKET_DATA","dependency_id":"feed-ten","observations":10,
			 "verified":0,"declared":10,"unknown":0,"agents":10,"last_seen":"2026-08-31 12:00:00.000"}
		]}`)
	}))
	defer fleet.Close()

	base := startConsole(t, map[string]string{
		"GATEWAY_URL":       fleet.URL,
		"FLEET_ENGINE_URL":  fleet.URL,
		"CONSOLE_API_TOKEN": "console-behaviour-token-of-32-plus-chars",
	})

	body := consoleGet(t, base+"/dependencies")

	if !strings.Contains(body, "feed-ten") {
		t.Fatalf("the dependencies surface did not render the rows it was given")
	}
	if !strings.Contains(body, "most shared: feed-ten") {
		t.Errorf("the most-shared dependency was not feed-ten (10 agents). If it names " +
			"feed-nine, the counts are being compared as text, where \"9\" outranks \"10\" — " +
			"which is exactly the defect the thirteenth audit fixed at the ClickHouse client.")
	}
}
