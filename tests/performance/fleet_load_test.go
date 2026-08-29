//go:build load

// Spec section 56 item 1: 1,000+ synthetic agents sending concurrent intents, and
// section 66 step 2, which asks a reviewer to launch them.
//
// This is a separate build tag because it talks to a running gateway over HTTP and
// takes minutes rather than seconds. `go test -tags=load ./tests/performance/ -run
// TestAThousandAgents -v`, with GATEWAY_URL, LOAD_AGENT_TOKEN and LOAD_ISSUER_TOKEN
// set; it skips loudly rather than passing when they are not.
//
// What it proves is narrow and worth stating: a thousand agents each authorized
// separately, submitting concurrently, every one of them getting a decision. It does
// not prove the platform is fast on someone else's hardware, and the latencies it
// prints are of a laptop talking to containers on the same laptop.

package performance

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"agentic-assurance/internal/identity"
)

const (
	agents          = 1000
	intentsPerAgent = 3

	// Concurrency is the whole point, so every agent is in flight at once. The
	// transport is the thing being measured together with the pipeline: a client
	// that queued its own requests would report a calm platform and a busy client.
	loadTimeout = 60 * time.Second

	// loadSockets bounds concurrent TCP connections, not concurrent agents.
	loadSockets = 256
)

type loadEnv struct {
	base        string
	agentToken  string
	issuerToken string
	tenant      string
	client      *http.Client
}

func loadEnvironment(t *testing.T) loadEnv {
	t.Helper()

	base := os.Getenv("GATEWAY_URL")
	agent := os.Getenv("LOAD_AGENT_TOKEN")
	issuer := os.Getenv("LOAD_ISSUER_TOKEN")
	if base == "" || agent == "" || issuer == "" {
		t.Skip("set GATEWAY_URL, LOAD_AGENT_TOKEN and LOAD_ISSUER_TOKEN against a running gateway")
	}

	return loadEnv{
		base:        base,
		agentToken:  agent,
		issuerToken: issuer,
		tenant:      envOrDefault("LOAD_TENANT", "tenant_live"),
		client: &http.Client{
			Timeout: loadTimeout,
			Transport: &http.Transport{
				// The default of 2 idle connections per host would serialise a
				// thousand agents onto two sockets and measure the pool.
				//
				// Capped below the agent count on purpose. A thousand simultaneous
				// TCP connections overflow the listen backlog on a Windows
				// workstation and the kernel answers with RST, which arrives here as
				// "connection refused" and looks exactly like a platform that
				// stopped deciding. A thousand agents each with an intent in flight
				// is the requirement; a thousand sockets is a property of this
				// laptop. Sockets are the constrained resource, so they queue.
				MaxIdleConns:        loadSockets,
				MaxIdleConnsPerHost: loadSockets,
				MaxConnsPerHost:     loadSockets,
			},
		},
	}
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (e loadEnv) post(ctx context.Context, path, token string, body []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.base+path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, nil
}

// grantFor issues one authority grant per synthetic agent.
//
// One each rather than one shared, because a shared grant would put a thousand agents
// under one rolling limit and the run would measure how quickly a limit is reached
// rather than whether a thousand agents can be enforced concurrently. Section 56 item
// 4 already covers grants being enforceable; this is item 1.
func (e loadEnv) grantFor(ctx context.Context, run string, i int) (string, error) {
	id := fmt.Sprintf("grant_load_%s_%d", run, i)
	body, _ := json.Marshal(map[string]any{
		"grant_id":     id,
		"principal_id": "prin_load",
		"account_id":   "acct_load",
		// The ids live-setup registered signing keys for. Stable across runs: the
		// same fleet running again is what a customer's deployment looks like.
		"agent_id":              fmt.Sprintf("agent_load_%d", i),
		"issued_by":             "load-harness",
		"valid_until":           time.Now().UTC().Add(2 * time.Hour).Format(time.RFC3339),
		"allowed_operations":    []string{"BUY", "SELL"},
		"allowed_asset_classes": []string{"EQUITY"},
		"allowed_instruments":   []string{"instr_us_equity_00206R102"},
		"per_order_notional":    25000,
		"rolling_1h_notional":   1000000,
		"daily_notional":        5000000,
		"max_open_orders":       1000,
	})

	status, raw, err := e.post(ctx, "/v1/authority-grants", e.issuerToken, body)
	if err != nil {
		return "", err
	}
	if status != http.StatusCreated {
		return "", fmt.Errorf("issue grant %s: HTTP %d: %s", id, status, raw)
	}
	return id, nil
}

// loadSigningKey is the fleet's signing key, registered by live-setup.
//
// Envelopes are signed rather than merely well-formed, because signature verification is
// now on the hot path: a load run against unsigned envelopes would measure a pipeline
// that refuses at the second stage and report the latency of a rejection.
//
// One key across the synthetic fleet. They are separate agent identities with separate
// grants; what a run of a thousand measures is concurrent authorization, not concurrent
// key generation.
func loadSigningKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	raw := os.Getenv("LIVE_SIGNING_KEY")
	if raw == "" {
		t.Skip("set LIVE_SIGNING_KEY (see scripts/live-boot.sh); an unsigned envelope is " +
			"refused before anything worth measuring happens")
	}
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != ed25519.PrivateKeySize {
		t.Fatalf("LIVE_SIGNING_KEY is not a hex ed25519 private key")
	}
	return ed25519.PrivateKey(decoded)
}

// signed attaches a signature over the canonical form, the way an agent would.
func signed(raw []byte, priv ed25519.PrivateKey) []byte {
	value, err := identity.SignEnvelope(raw, priv)
	if err != nil {
		panic(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		panic(err)
	}
	m["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519,
		"key_id":    envOrDefault("LIVE_KEY_ID", "key_live"),
		"value":     value,
	}
	out, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}
	return out
}

func envelopeFor(tenant, grantID, agentID, key string) []byte {
	now := time.Now().UTC().Format(time.RFC3339)
	env := map[string]any{
		"schema_version":     "0.1",
		"envelope_id":        "env_" + key,
		"idempotency_key":    key,
		"correlation_id":     "corr_" + key,
		"received_at":        now,
		"tenant_id":          tenant,
		"authority_grant_id": grantID,
		"principal":          map[string]any{"principal_id": "prin_load", "account_id": "acct_load"},
		"agent": map[string]any{
			"agent_id":          agentID,
			"workload_identity": map[string]any{"spiffe_id": "spiffe://acme.example/ns/load/sa/" + agentID},
			"attestation":       map[string]any{"level": "A1", "method": "AUTHENTICATED_API_IDENTITY"},
		},
		"runtime_claims": map[string]any{
			"model_provider": map[string]any{"value": "provider-x", "verification": "DECLARED"},
			"model_family":   map[string]any{"value": "model-y", "verification": "DECLARED"},
			"model_version":  map[string]any{"value": "2026-08", "verification": "DECLARED"},
		},
		"dependencies": []any{map[string]any{
			"type": "MARKET_DATA", "id": "feed-a", "verification": "DECLARED", "observed_at": now,
		}},
		"intent": map[string]any{
			"asset_class": "EQUITY", "instrument_id": "instr_us_equity_00206R102",
			"side": "BUY", "order_type": "MARKET", "notional": 1200,
			"time_in_force": "DAY", "extended_hours": false,
		},
		"lineage": map[string]any{"strategy_id": "strategy_load"},
		"context": map[string]any{"portfolio_snapshot_id": "ps_1", "market_snapshot_id": "ms_1"},
	}
	raw, _ := json.Marshal(env)
	return raw
}

// TestAThousandAgentsSubmitConcurrently is section 56 item 1.
func TestAThousandAgentsSubmitConcurrently(t *testing.T) {
	e := loadEnvironment(t)
	signingKey := loadSigningKey(t)
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	// Authorization first, and sequentially enough not to be the thing under test.
	// A failure here is a setup failure and is reported as one: a load run that
	// silently submitted under fewer grants than it thinks would report a fleet it
	// never launched.
	t.Logf("issuing %d authority grants", agents)
	grants := make([]string, agents)
	issueStart := time.Now()
	var issueWG sync.WaitGroup
	slots := make(chan struct{}, 32)
	var issueErr atomic.Value
	for i := 0; i < agents; i++ {
		issueWG.Add(1)
		go func(i int) {
			defer issueWG.Done()
			slots <- struct{}{}
			defer func() { <-slots }()

			id, err := e.grantFor(ctx, run, i)
			if err != nil {
				issueErr.Store(err)
				return
			}
			grants[i] = id
		}(i)
	}
	issueWG.Wait()
	if err, ok := issueErr.Load().(error); ok && err != nil {
		t.Fatalf("could not authorize the fleet: %v", err)
	}
	t.Logf("%d grants issued in %s", agents, time.Since(issueStart).Round(time.Millisecond))

	type sample struct {
		latency time.Duration
		status  int
		code    string
	}

	results := make(chan sample, agents*intentsPerAgent)
	var transportErrors atomic.Int64
	var firstTransportError atomic.Value

	// Every agent released at once. A staggered start would measure a ramp.
	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup

	for i := 0; i < agents; i++ {
		done.Add(1)
		go func(i int) {
			defer done.Done()
			start.Wait()

			agentID := fmt.Sprintf("agent_load_%d", i)
			for n := 0; n < intentsPerAgent; n++ {
				key := fmt.Sprintf("load_%s_%d_%d", run, i, n)
				body := signed(envelopeFor(e.tenant, grants[i], agentID, key), signingKey)

				began := time.Now()
				status, raw, err := e.post(ctx, "/v1/intents", e.agentToken, body)
				latency := time.Since(began)
				if err != nil {
					transportErrors.Add(1)
					firstTransportError.CompareAndSwap(nil, err.Error())
					continue
				}

				var decoded struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(raw, &decoded)
				results <- sample{latency: latency, status: status, code: decoded.Code}
			}
		}(i)
	}

	wallStart := time.Now()
	start.Done()
	done.Wait()
	wall := time.Since(wallStart)
	close(results)

	latencies := make([]time.Duration, 0, agents*intentsPerAgent)
	byStatus := map[int]int{}
	byCode := map[string]int{}
	for s := range results {
		latencies = append(latencies, s.latency)
		byStatus[s.status]++
		byCode[s.code]++
	}

	if errs := transportErrors.Load(); errs > 0 {
		first, _ := firstTransportError.Load().(string)
		t.Errorf("%d intents got no response at all (first: %s); a submission that neither "+
			"succeeded nor was refused leaves the caller unable to know what happened", errs, first)
	}
	expected := agents * intentsPerAgent
	if len(latencies) != expected {
		t.Errorf("%d of %d intents produced a decision", len(latencies), expected)
	}

	// Every submission must produce a decision. A 5xx is the platform failing to
	// decide, which is the one outcome an assurance layer may not have under load.
	for status, count := range byStatus {
		if status >= 500 {
			t.Errorf("%d intents got HTTP %d; the platform failed to decide", count, status)
		}
	}

	p50, p95, p99 := loadPercentiles(latencies)
	t.Logf("%d agents x %d intents = %d submissions in %s (%.0f/s)",
		agents, intentsPerAgent, len(latencies), wall.Round(time.Millisecond),
		float64(len(latencies))/wall.Seconds())
	t.Logf("end-to-end latency over HTTP: p50 %s  p95 %s  p99 %s",
		p50.Round(time.Microsecond), p95.Round(time.Microsecond), p99.Round(time.Microsecond))
	t.Logf("status distribution: %v", byStatus)
	t.Logf("decision codes: %v", byCode)
}

// loadPercentiles is percentiles() again, because that one lives behind the
// integration tag and this file is behind the load tag. Duplicated rather than
// re-tagged: moving it would pull the whole integration bench into every load run.
func loadPercentiles(samples []time.Duration) (p50, p95, p99 time.Duration) {
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	if len(sorted) == 0 {
		return 0, 0, 0
	}
	at := func(q float64) time.Duration { return sorted[int(q*float64(len(sorted)-1))] }
	return at(0.50), at(0.95), at(0.99)
}
