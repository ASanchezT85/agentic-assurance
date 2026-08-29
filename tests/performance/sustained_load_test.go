//go:build load

// Sustained load, and load across tenants.
//
// The thousand-agent run answers "can a fleet arrive at once". These answer the two
// questions after it: does the platform still decide correctly after minutes of steady
// traffic, and does one tenant's traffic — or one tenant's fleet control — reach
// another's orders when both are busy.
//
// Isolation is the one property here that is not about speed. INV-007 has been proved
// against a quiet database many times; a pooled connection carrying a stale
// app.tenant_id is a thing that happens under concurrency, and this is where it would
// show.
//
//	GATEWAY_URL=http://localhost:8073 \
//	LOAD_AGENT_TOKEN=... LOAD_ISSUER_TOKEN=... \
//	LOAD_TENANTS="tenant_a=agent-token-a...,tenant_b=agent-token-b..." \
//	  go test -tags=load -count=1 -v ./tests/performance/ -run TestSustained
//
// LOAD_MINUTES defaults to 2.

package performance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSustainedLoadKeepsDeciding runs steady traffic for a few minutes.
//
// What it asserts is narrow on purpose: every request gets a decision and none is a
// 5xx. Latency is reported per bucket rather than asserted, because a threshold that
// passes on this laptop says nothing about a deployment, and a test that fails on a
// background process stealing a core teaches people to ignore it.
func TestSustainedLoadKeepsDeciding(t *testing.T) {
	e := loadEnvironment(t)
	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)

	minutes := 2
	if raw := os.Getenv("LOAD_MINUTES"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			minutes = parsed
		}
	}
	const workers = 50

	grants := make([]string, workers)
	for i := 0; i < workers; i++ {
		id, err := e.grantFor(ctx, "sust"+run, i)
		if err != nil {
			t.Fatalf("could not authorize worker %d: %v", i, err)
		}
		grants[i] = id
	}

	type bucket struct {
		mu        sync.Mutex
		latencies []time.Duration
		statuses  map[int]int

		// The decision codes, not only the statuses. A run long enough to matter will
		// spend its grants, and a wall of 403s that nobody can attribute reads as a
		// platform falling over rather than as one enforcing a rolling limit exactly
		// as it was asked to.
		codes map[string]int
	}
	buckets := map[int]*bucket{}
	var bucketsMu sync.Mutex
	record := func(minute int, latency time.Duration, status int, code string) {
		bucketsMu.Lock()
		b, ok := buckets[minute]
		if !ok {
			b = &bucket{statuses: map[int]int{}, codes: map[string]int{}}
			buckets[minute] = b
		}
		bucketsMu.Unlock()

		b.mu.Lock()
		b.latencies = append(b.latencies, latency)
		b.statuses[status]++
		b.codes[code]++
		b.mu.Unlock()
	}

	var unanswered atomic.Int64
	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)
	start := time.Now()

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			agentID := fmt.Sprintf("agent_load_sust%s_%d", run, i)
			for n := 0; time.Now().Before(deadline); n++ {
				key := fmt.Sprintf("sust_%s_%d_%d", run, i, n)
				body := envelopeFor(e.tenant, grants[i], agentID, key)

				began := time.Now()
				status, raw, err := e.post(ctx, "/v1/intents", e.agentToken, body)
				latency := time.Since(began)
				if err != nil {
					unanswered.Add(1)
					continue
				}
				var decoded struct {
					Code string `json:"code"`
				}
				_ = json.Unmarshal(raw, &decoded)
				record(int(time.Since(start).Minutes()), latency, status, decoded.Code)
			}
		}(i)
	}
	wg.Wait()

	if n := unanswered.Load(); n > 0 {
		t.Errorf("%d submissions got no response at all", n)
	}

	minutesSeen := make([]int, 0, len(buckets))
	for minute := range buckets {
		minutesSeen = append(minutesSeen, minute)
	}
	sortInts(minutesSeen)

	total := 0
	for _, minute := range minutesSeen {
		b := buckets[minute]
		p50, p95, p99 := loadPercentiles(b.latencies)
		total += len(b.latencies)
		t.Logf("minute %d: %d decided, p50 %s p95 %s p99 %s, statuses %v, codes %v",
			minute, len(b.latencies), p50.Round(time.Millisecond),
			p95.Round(time.Millisecond), p99.Round(time.Millisecond), b.statuses, b.codes)

		for status, count := range b.statuses {
			if status >= 500 {
				t.Errorf("minute %d: %d submissions got HTTP %d; the platform stopped deciding",
					minute, count, status)
			}
		}
	}
	if total == 0 {
		t.Fatal("no submission completed; the run measured nothing")
	}
	t.Logf("%d decisions over %d minutes (%.0f/s sustained)",
		total, minutes, float64(total)/time.Since(start).Seconds())
}

// TestTenantsUnderLoadStayIsolated runs several tenants at once and checks that each
// one's list contains only its own intents.
//
// LOAD_TENANTS carries "tenant=token" pairs. Without it the test skips: inventing a
// second tenant here would mean inventing a credential, and a credential this test
// made up would prove isolation against a caller nobody authenticated.
func TestTenantsUnderLoadStayIsolated(t *testing.T) {
	e := loadEnvironment(t)

	raw := os.Getenv("LOAD_TENANTS")
	if raw == "" {
		t.Skip(`set LOAD_TENANTS="tenant_a=token_a,tenant_b=token_b" with credentials the gateway knows`)
	}

	type tenant struct {
		id       string
		token    string
		envelope []string
	}
	var tenants []tenant
	for _, pair := range strings.Split(raw, ",") {
		id, token, ok := strings.Cut(strings.TrimSpace(pair), "=")
		if !ok {
			t.Fatalf("LOAD_TENANTS entry %q is not tenant=token", pair)
		}
		tenants = append(tenants, tenant{id: id, token: token})
	}
	if len(tenants) < 2 {
		t.Skip("LOAD_TENANTS needs at least two tenants to prove anything")
	}

	ctx := context.Background()
	run := strconv.FormatInt(time.Now().UnixNano(), 36)
	const perTenant = 40

	// Each tenant's grants are issued with its own credential, so a tenant that could
	// not issue for itself fails here rather than silently sharing another's grant.
	var wg sync.WaitGroup
	for i := range tenants {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tn := &tenants[i]
			scoped := loadEnv{base: e.base, agentToken: tn.token, issuerToken: e.issuerToken,
				tenant: tn.id, client: e.client}

			grant, err := scoped.grantFor(ctx, fmt.Sprintf("iso%s_%d", run, i), 0)
			if err != nil {
				t.Errorf("tenant %s: %v", tn.id, err)
				return
			}
			agentID := fmt.Sprintf("agent_load_iso%s_%d_0", run, i)

			for n := 0; n < perTenant; n++ {
				key := fmt.Sprintf("iso_%s_%d_%d", run, i, n)
				body := envelopeFor(tn.id, grant, agentID, key)
				status, _, err := scoped.post(ctx, "/v1/intents", tn.token, body)
				if err != nil {
					t.Errorf("tenant %s: %v", tn.id, err)
					return
				}
				if status >= 500 {
					t.Errorf("tenant %s: HTTP %d", tn.id, status)
					return
				}
				tn.envelope = append(tn.envelope, "env_"+key)
			}
		}(i)
	}
	wg.Wait()
	if t.Failed() {
		return
	}

	// The listing is the check. Every envelope it returns must belong to the tenant
	// that asked: a pooled connection carrying a stale app.tenant_id is exactly the
	// failure that concurrency produces and a quiet test never sees.
	for _, tn := range tenants {
		mine := map[string]bool{}
		for _, id := range tn.envelope {
			mine[id] = true
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.base+"/v1/intents?limit=200", nil)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+tn.token)
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatalf("tenant %s list: %v", tn.id, err)
		}
		var body struct {
			TenantID string `json:"tenant_id"`
			Intents  []struct {
				EnvelopeID string `json:"envelope_id"`
			} `json:"intents"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			t.Fatalf("tenant %s list: %v", tn.id, err)
		}

		if body.TenantID != tn.id {
			t.Errorf("the listing answered for tenant %q to a credential of %q",
				body.TenantID, tn.id)
		}
		foreign := 0
		for _, i := range body.Intents {
			if strings.HasPrefix(i.EnvelopeID, "env_iso_"+run) && !mine[i.EnvelopeID] {
				foreign++
			}
		}
		if foreign > 0 {
			t.Errorf("tenant %s saw %d of another tenant's intents from this run (INV-007)",
				tn.id, foreign)
		}
		t.Logf("tenant %s: %d intents listed, none belonging to another tenant", tn.id, len(body.Intents))
	}
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j] < v[j-1]; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
