//go:build integration

package integration

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/policy"
)

// An order submitted over mutual TLS by a workload SPIRE issued.
//
// A2 has been built since Phase 2 and unreachable in practice: the verifier accepted a
// SPIRE certificate, nothing said which customer the workload acted for, and the
// tenant check refused. Every test of it stopped at the verifier.
//
// This is the difference between the pieces working and the path working, which is the
// distinction the whole audit turned on. An SVID goes onto a TLS connection, the
// gateway reads it off the transport, the workload registry supplies the tenant, and an
// order reaches the venue — or does not, when the workload is not mapped.
//
// Run with:  make up && make test-integration

const mtlsWorkload = "spiffe://acme.example/ns/agents/sa/momentum"

// mintSVIDWithKey returns a usable client certificate, not just a parsed one.
//
// The existing helper keeps the certificate blocks and drops the key, which is enough
// to verify an SVID and not enough to present one. Presenting it is the half that was
// never tested.
func mintSVIDWithKey(t *testing.T, spiffeID string) (tls.Certificate, []*x509.Certificate) {
	t.Helper()

	cmd := exec.Command("docker", "compose", "exec", "-T", "spire-server",
		"/opt/spire/bin/spire-server", "x509", "mint", "-spiffeID", spiffeID, "-ttl", "1h")
	cmd.Dir = "../.."

	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("spire-server x509 mint failed (is `make up` running?): %v\n%s", err, out)
	}

	var (
		leafDER [][]byte
		keyPEM  []byte
		roots   []*x509.Certificate
		seen    int
	)

	rest := out
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			cert, parseErr := x509.ParseCertificate(block.Bytes)
			if parseErr != nil {
				t.Fatalf("parse certificate: %v", parseErr)
			}
			if seen == 0 {
				leafDER = append(leafDER, block.Bytes)
			} else {
				roots = append(roots, cert)
			}
			seen++
		case strings.Contains(block.Type, "PRIVATE KEY"):
			keyPEM = pem.EncodeToMemory(block)
		}
	}

	if len(leafDER) == 0 || keyPEM == nil {
		t.Fatalf("mint produced %d certificates and %d keys; a client certificate needs "+
			"both", len(leafDER), len(keyPEM))
	}

	leafPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER[0]})
	pair, err := tls.X509KeyPair(leafPEM, keyPEM)
	if err != nil {
		t.Fatalf("build client certificate: %v", err)
	}
	return pair, roots
}

type mtlsRig struct {
	server *httptest.Server
	broker *fakebroker.Broker
	client *http.Client
}

func newMTLSRig(t *testing.T, workloads string) *mtlsRig {
	t.Helper()

	clientCert, roots := mintSVIDWithKey(t, mtlsWorkload)

	pool := x509.NewCertPool()
	for _, root := range roots {
		pool.AddCert(root)
	}

	registry, err := identity.ParseWorkloads(workloads)
	if err != nil {
		t.Fatalf("workloads: %v", err)
	}

	at := time.Now().UTC()

	if mtlsActiveBundle == nil {
		mtlsActiveBundle = buildMTLSBundle(t, at)
	}

	venue := fakebroker.New()
	venue.SetClock(func() time.Time { return at })

	usage := authority.NewMemoryUsage()
	pipeline := &gateway.Pipeline{
		Identity: &identity.Verifier{
			TrustDomain: "acme.example",
			Bundle:      pool,
			Workloads:   registry,
		},
		Grants:   mtlsGrants{"grant_mtls": mtlsGrant(at)},
		Policies: mtlsBundles{},
		Usage:    usage,
		Reserve:  usage,
		Execution: &execution.Service{
			Broker: venue,
			Store:  execution.NewMemoryStore(),
			Now:    func() time.Time { return at },
		},
		Symbols: gateway.StaticSymbols{"instr_us_equity_00206R102": "AAPL"},
		Now:     func() time.Time { return at },
	}

	// No API credentials at all. The only way in is the certificate, which is what
	// makes this a test of A2 rather than of the bearer path with a certificate
	// attached.
	srv := httptest.NewUnstartedServer(gateway.SubmitHandler(pipeline, nil))
	srv.TLS = &tls.Config{
		// Requested, not verified at the TLS layer. The platform verifies the SVID
		// against the SPIRE bundle itself, and letting the TLS stack decide would
		// move an identity decision out of the code that owns it.
		ClientAuth: tls.RequestClientCert,
		MinVersion: tls.VersionTLS12,
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)

	client := srv.Client()
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("the test server's client is not using an *http.Transport")
	}
	transport.TLSClientConfig.Certificates = []tls.Certificate{clientCert}

	return &mtlsRig{server: srv, broker: venue, client: client}
}

func (r *mtlsRig) submit(t *testing.T, body string) (int, map[string]any) {
	t.Helper()

	resp, err := r.client.Post(r.server.URL+"/v1/intents", "application/json",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("response is not JSON (%d): %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, decoded
}

// mtlsBundles serves an ACTIVE bundle. The lifecycle is proven in internal/policy;
// what this file is about is whether a certificate can carry an order through.
type mtlsBundles struct{}

func (mtlsBundles) Active(context.Context, string) (*policy.Bundle, error) {
	return mtlsActiveBundle, nil
}

var mtlsActiveBundle *policy.Bundle

type mtlsGrants map[string]*authority.Grant

func (g mtlsGrants) Load(_ context.Context, _, grantID string) (*authority.Grant, error) {
	grant, ok := g[grantID]
	if !ok {
		return nil, fmt.Errorf("no such grant")
	}
	return grant, nil
}

func mtlsGrant(at time.Time) *authority.Grant {
	return &authority.Grant{
		GrantID:             "grant_mtls",
		TenantID:            "tenant_momentum",
		PrincipalID:         "prin_mtls",
		AccountID:           "acct_mtls",
		AgentID:             "agent_momentum",
		IssuedAt:            at.Add(-time.Hour),
		ValidFrom:           at.Add(-time.Hour),
		ValidUntil:          at.Add(time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits:              authority.Limits{PerOrderNotional: 50000},
		Status:              authority.StatusActive,
	}
}

func mtlsEnvelope(at time.Time, key, tenant, level string) string {
	m := map[string]any{
		"schema_version":  "0.1",
		"envelope_id":     "env_" + key,
		"idempotency_key": key,
		"correlation_id":  "corr_" + key,
		"received_at":     at.Format(time.RFC3339),
		"tenant_id":       tenant,
		"principal":       map[string]any{"principal_id": "prin_mtls", "account_id": "acct_mtls"},
		"agent": map[string]any{
			"agent_id":          "agent_momentum",
			"workload_identity": map[string]any{"spiffe_id": mtlsWorkload},
			"attestation":       map[string]any{"level": level, "method": "spiffe"},
		},
		"authority_grant_id": "grant_mtls",
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102",
			"asset_class":   "EQUITY",
			"side":          "BUY",
			"order_type":    "LIMIT",
			"quantity":      10,
			"limit_price":   100,
			"time_in_force": "DAY",
		},
	}
	raw, _ := json.Marshal(m)
	return string(raw)
}

// The whole point: a mapped workload places an order over mTLS, with no API credential
// anywhere in the configuration.
func TestAMappedWorkloadPlacesAnOrderOverMTLS(t *testing.T) {
	rig := newMTLSRig(t, "spiffe://acme.example/ns/agents/sa/momentum=tenant_momentum")
	at := time.Now().UTC()
	key := fmt.Sprintf("idem-mtls-%d", at.UnixNano())

	status, body := rig.submit(t, mtlsEnvelope(at, key, "tenant_momentum", "A2"))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %v", status, body)
	}
	if body["attestation_level"] != "A2" {
		t.Errorf("attestation level = %v, want A2; the certificate should have "+
			"established it", body["attestation_level"])
	}
	if body["accepted"] != true {
		t.Fatalf("the order was not accepted: %v", body)
	}
	if n := rig.broker.Submissions("coid-" + key); n != 1 {
		t.Errorf("the venue received %d submissions, want 1", n)
	}
}

// The same certificate, and a tenant it does not act for. The registry decides, and the
// envelope does not get to disagree.
func TestAWorkloadCannotActOnAnotherTenantOverMTLS(t *testing.T) {
	rig := newMTLSRig(t, "spiffe://acme.example/ns/agents/sa/momentum=tenant_momentum")
	at := time.Now().UTC()
	key := fmt.Sprintf("idem-mtls-x-%d", at.UnixNano())

	status, body := rig.submit(t, mtlsEnvelope(at, key, "tenant_victim", "A2"))

	if status == http.StatusOK {
		t.Fatalf("a workload mapped to tenant_momentum placed an order for "+
			"tenant_victim: %v", body)
	}
	if body["code"] != "TENANT_NOT_AUTHENTICATED" {
		t.Errorf("code = %v, want TENANT_NOT_AUTHENTICATED", body["code"])
	}
	if n := rig.broker.Submissions("coid-" + key); n != 0 {
		t.Errorf("the order reached the venue %d times", n)
	}
}

// An unmapped workload is refused, over a real connection with a real certificate. The
// registry is what makes A2 usable, and its absence is what makes it safe.
func TestAnUnmappedWorkloadIsRefusedOverMTLS(t *testing.T) {
	rig := newMTLSRig(t, "spiffe://acme.example/ns/other/=tenant_acme")
	at := time.Now().UTC()
	key := fmt.Sprintf("idem-mtls-u-%d", at.UnixNano())

	status, body := rig.submit(t, mtlsEnvelope(at, key, "tenant_momentum", "A2"))

	if status == http.StatusOK {
		t.Fatalf("an unmapped workload placed an order: %v", body)
	}
	reason, _ := body["reason"].(string)
	if !strings.Contains(reason, mtlsWorkload) {
		t.Errorf("reason = %q; the refusal should name the workload that has no "+
			"registry entry", reason)
	}
	if n := rig.broker.Submissions("coid-" + key); n != 0 {
		t.Errorf("the order reached the venue %d times", n)
	}
}

const mtlsPolicySource = `
version: 1
policy: pol_mtls
rules:
  - id: no_extended_hours
    action: DENY
    when:
      extended_hours: true
`

func buildMTLSBundle(t *testing.T, at time.Time) *policy.Bundle {
	t.Helper()

	src, err := policy.ParseSource([]byte(mtlsPolicySource))
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	bundle, err := policy.Compile(src, "tenant_momentum", "bundle_mtls", at)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	_, priv, _ := ed25519.GenerateKey(nil)
	if err := bundle.Sign(priv, "mtls", at); err != nil {
		t.Fatalf("sign: %v", err)
	}
	for _, to := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(to, at, "mtls"); err != nil {
			t.Fatalf("transition %s: %v", to, err)
		}
	}
	return bundle
}
