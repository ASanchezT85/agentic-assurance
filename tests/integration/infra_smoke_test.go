//go:build integration

// Package integration holds the Phase 0 infrastructure smoke test.
//
// Run with:  make up && make test-integration
//
// It proves the host can actually reach all four services, which the container
// healthchecks alone do not prove. Stdlib only: Phase 0 has no database drivers,
// and adding four of them to run a connectivity check would be the tail wagging
// the dog. Real drivers arrive with the code that needs them (Phase 5 and 8).
package integration

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

const dialTimeout = 5 * time.Second

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func hostPort(hostKey, hostDefault, portKey, portDefault string) string {
	return net.JoinHostPort(env(hostKey, hostDefault), env(portKey, portDefault))
}

func TestPostgresAcceptsConnections(t *testing.T) {
	addr := hostPort("POSTGRES_HOST", "localhost", "POSTGRES_PORT", "5432")
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		t.Fatalf("postgres unreachable at %s: %v", addr, err)
	}
	defer conn.Close()

	// Ceiling: this is a TCP reachability check, not a protocol handshake. A real
	// authenticated round trip arrives in Phase 5 with the pgx driver, which is the
	// first time we have a reason to depend on one.
}

func TestRedisRespondsToPing(t *testing.T) {
	addr := hostPort("REDIS_HOST", "localhost", "REDIS_PORT", "6379")
	conn, err := net.DialTimeout("tcp", addr, dialTimeout)
	if err != nil {
		t.Fatalf("redis unreachable at %s: %v", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(dialTimeout))

	if _, err := io.WriteString(conn, "PING\r\n"); err != nil {
		t.Fatalf("write PING: %v", err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read PONG: %v", err)
	}
	if !strings.HasPrefix(line, "+PONG") {
		t.Fatalf("got %q, want +PONG", strings.TrimSpace(line))
	}
}

func TestClickHouseAnswersPing(t *testing.T) {
	url := fmt.Sprintf("http://%s/ping", hostPort("CLICKHOUSE_HOST", "localhost", "CLICKHOUSE_HTTP_PORT", "8123"))
	body := httpGet(t, url)
	if !strings.HasPrefix(body, "Ok.") {
		t.Fatalf("clickhouse /ping returned %q, want Ok.", strings.TrimSpace(body))
	}
}

func TestNATSReportsHealthy(t *testing.T) {
	url := fmt.Sprintf("http://%s/healthz", hostPort("NATS_HOST", "localhost", "NATS_MONITOR_PORT", "8222"))
	if body := httpGet(t, url); !strings.Contains(body, "ok") {
		t.Fatalf("nats /healthz returned %q", strings.TrimSpace(body))
	}
}

// TestNATSJetStreamIsEnabled — JetStream is required by ADR-008 and spec §9.7. A
// NATS server running without it would pass the health check and fail Phase 6.
func TestNATSJetStreamIsEnabled(t *testing.T) {
	url := fmt.Sprintf("http://%s/varz", hostPort("NATS_HOST", "localhost", "NATS_MONITOR_PORT", "8222"))
	if body := httpGet(t, url); !strings.Contains(body, "jetstream") {
		t.Fatal("nats /varz does not report jetstream; start it with -js")
	}
}

func httpGet(t *testing.T, url string) string {
	t.Helper()
	client := &http.Client{Timeout: dialTimeout}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", url, resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		t.Fatalf("read %s: %v", url, err)
	}
	return string(raw)
}
