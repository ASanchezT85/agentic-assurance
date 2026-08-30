//go:build process

// Process-level acceptance, against the deployable rather than a Pipeline built in a
// test.
//
// The integration suite starts an httptest.Server around a Pipeline in the test's own
// process. That proves the composition and the database coordination, and it cannot
// prove two things the audit asked for: that two operating-system processes sharing one
// database cannot overspend a grant, and that a gateway killed between a venue's
// acceptance and the outcome write recovers without submitting twice. A test that calls
// its in-process server "a replica" is a documentation defect regardless of what it
// proves.
//
//	go test -tags=process -count=1 -v ./tests/process/
//
// It needs PostgreSQL and a venue whose orders outlive a killed process, which means
// Alpaca Paper. It skips loudly rather than passing when either is missing.
package process

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/policy"
)

const livePolicy = `
version: 1
policy: pol_process
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 1000000
`

// deployment is one tenant provisioned in the database plus the files and credentials a
// gateway process needs to serve it.
type deployment struct {
	dir        string
	tenant     string
	grantID    string
	agentID    string
	keyID      string
	token      string
	issuer     string
	signingKey ed25519.PrivateKey
	policyPub  ed25519.PublicKey
	pool       *pgxpool.Pool
	binary     string
}

func dsn(t *testing.T) string {
	t.Helper()
	v := os.Getenv("POSTGRES_APP_DSN")
	if v == "" {
		t.Skip("POSTGRES_APP_DSN is not set; process-level acceptance needs a real database")
	}
	return v
}

func newDeployment(t *testing.T, limits authority.Limits) *deployment {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()

	pool, err := pgxpool.New(ctx, dsn(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)

	d := &deployment{
		dir:     t.TempDir(),
		tenant:  fmt.Sprintf("tenant_proc_%d", now.UnixNano()),
		grantID: "grant_proc",
		agentID: "agent_proc",
		keyID:   "key_proc",
		token:   randomToken(t),
		issuer:  randomToken(t),
		pool:    pool,
	}

	if err := authority.NewStore(pool).Save(ctx, &authority.Grant{
		GrantID:             d.grantID,
		TenantID:            d.tenant,
		PrincipalID:         "prin_proc",
		AccountID:           "acct_proc",
		AgentID:             d.agentID,
		IssuedAt:            now.Add(-time.Hour),
		ValidFrom:           now.Add(-time.Hour),
		ValidUntil:          now.Add(24 * time.Hour),
		AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
		AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
		AllowedInstruments:  []string{"instr_us_equity_00206R102"},
		Limits:              limits,
		Status:              authority.StatusActive,
	}); err != nil {
		t.Fatalf("save grant: %v", err)
	}

	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("agent keygen: %v", err)
	}
	d.signingKey = agentPriv
	if _, err := identity.NewKeyStore(pool).Register(ctx, identity.AgentKey{
		TenantID: d.tenant, AgentID: d.agentID, KeyID: d.keyID,
		Algorithm: identity.AlgorithmEd25519, PublicKey: agentPub,
		Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register agent key: %v", err)
	}

	d.stagePolicy(t, now)
	d.binary = buildGateway(t)
	return d
}

// stagePolicy writes the signed bundle and the customer's signed authorization to
// activate it, and registers the activation key.
func (d *deployment) stagePolicy(t *testing.T, now time.Time) {
	t.Helper()
	ctx := context.Background()

	src, err := policy.ParseSource([]byte(livePolicy))
	if err != nil {
		t.Fatalf("parse policy: %v", err)
	}
	bundle, err := policy.Compile(src, d.tenant, "bundle_proc", now)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	policyPub, policyPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("policy keygen: %v", err)
	}
	d.policyPub = policyPub
	if err := bundle.Sign(policyPriv, "process-test", now); err != nil {
		t.Fatalf("sign bundle: %v", err)
	}
	for _, stage := range []policy.Status{
		policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
	} {
		if err := bundle.Transition(stage, now, "process-test"); err != nil {
			t.Fatalf("stage %s: %v", stage, err)
		}
	}

	policyDir := filepath.Join(d.dir, "policy")
	if err := os.MkdirAll(policyDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	write(t, filepath.Join(policyDir, d.tenant+".json"), bundle)

	activationPub, activationPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("activation keygen: %v", err)
	}
	activations := policy.NewActivationStore(d.pool)
	if _, err := activations.RegisterKey(ctx, policy.ActivationKey{
		TenantID: d.tenant, KeyID: "act_proc", PublicKey: activationPub,
		Holder: "process-test", Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("register activation key: %v", err)
	}

	authorization := policy.Authorization{
		SchemaVersion:     policy.AuthorizationSchemaVersion,
		TenantID:          d.tenant,
		BundleID:          bundle.BundleID,
		BundleContentHash: bundle.ContentHash,
		Action:            policy.ActionActivate,
		Actor:             "process-test",
		AuthorizedAt:      now,
		Nonce:             fmt.Sprintf("proc_%s", d.tenant),
	}
	if err := authorization.Sign(activationPriv, "act_proc"); err != nil {
		t.Fatalf("sign authorization: %v", err)
	}
	write(t, filepath.Join(policyDir, d.tenant+".activation.json"), authorization)

	write(t, filepath.Join(d.dir, "instruments.json"),
		map[string]string{"instr_us_equity_00206R102": "AAPL"})
}

// buildGateway compiles the deployable once per run.
//
// Built into the repository rather than the module cache: this workstation's application
// control policy blocks executables staged under the user cache directory, which is
// where `go run` and `go test` place them.
func buildGateway(t *testing.T) string {
	t.Helper()
	out := filepath.Join(repoRoot(t), ".live", "gateway-process-test.exe")
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cmd := exec.Command("go", "build", "-o", out, "./cmd/assurance-gateway")
	cmd.Dir = repoRoot(t)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build the gateway: %v\n%s", err, output)
	}
	return out
}

// gateway is one running gateway process.
type gateway struct {
	cmd     *exec.Cmd
	addr    string
	log     string
	logFile *os.File
}

// start launches a gateway process and waits until it is serving.
func (d *deployment) start(t *testing.T, extra map[string]string) *gateway {
	t.Helper()

	addr := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	logPath := filepath.Join(d.dir, strings.ReplaceAll(addr, ":", "_")+".log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("log: %v", err)
	}

	env := map[string]string{
		"GATEWAY_ADDR":        addr,
		"POSTGRES_APP_DSN":    os.Getenv("POSTGRES_APP_DSN"),
		"POSTGRES_OUTBOX_DSN": os.Getenv("POSTGRES_OUTBOX_DSN"),
		"POLICY_BUNDLE_DIR":   filepath.Join(d.dir, "policy"),
		"POLICY_PUBLIC_KEY":   hex.EncodeToString(d.policyPub),
		"INSTRUMENT_SYMBOLS":  filepath.Join(d.dir, "instruments.json"),
		"ASSURANCE_ENV":       "development",
		"GATEWAY_API_CREDENTIALS": fmt.Sprintf("svc_proc@%s=%s,svc_issuer@%s=%s",
			d.tenant, d.token, d.tenant, d.issuer),
		"GATEWAY_GRANT_ISSUERS": "svc_issuer",
		"BROKER":                brokerFor(t),
		"ALPACA_BASE_URL":       os.Getenv("ALPACA_BASE_URL"),
		"ALPACA_KEY_ID":         os.Getenv("ALPACA_KEY_ID"),
		"ALPACA_SECRET_KEY":     os.Getenv("ALPACA_SECRET_KEY"),
	}
	for k, v := range extra {
		env[k] = v
	}

	cmd := exec.Command(d.binary)
	cmd.Dir = repoRoot(t)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), flatten(env)...)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the gateway: %v", err)
	}

	g := &gateway{cmd: cmd, addr: addr, log: logPath, logFile: logFile}
	t.Cleanup(func() { g.stop() })

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://" + addr + "/healthz")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return g
			}
		}
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			t.Fatalf("the gateway exited during startup:\n%s", tail(logPath))
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("the gateway on %s never became healthy:\n%s", addr, tail(logPath))
	return nil
}

func (g *gateway) stop() {
	if g == nil || g.cmd == nil {
		return
	}
	if g.cmd.Process != nil {
		_ = g.cmd.Process.Kill()
		_, _ = g.cmd.Process.Wait()
	}
	// Closed explicitly. Windows refuses to remove a directory holding an open handle,
	// so an unclosed log turns a passing test into a cleanup failure.
	if g.logFile != nil {
		_ = g.logFile.Close()
	}
}

// alive reports whether the process is still running.
func (g *gateway) alive() bool {
	if g.cmd.ProcessState != nil {
		return false
	}
	resp, err := http.Get("http://" + g.addr + "/healthz")
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return true
}

// submit posts one signed envelope and returns the status and decoded body.
func (d *deployment) submit(t *testing.T, g *gateway, key string,
	mutate func(map[string]any)) (int, map[string]any) {

	t.Helper()
	body := d.envelope(t, key, mutate)

	client := &http.Client{Timeout: 30 * time.Second}
	req, err := http.NewRequest(http.MethodPost, "http://"+g.addr+"/v1/intents",
		strings.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+d.token)

	resp, err := client.Do(req)
	if err != nil {
		// A killed gateway drops the connection mid-request, which is the expected
		// shape of the crash test rather than a failure of it.
		return 0, map[string]any{"transport_error": err.Error()}
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(resp.Body)
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded)
	return resp.StatusCode, decoded
}

func (d *deployment) envelope(t *testing.T, key string, mutate func(map[string]any)) string {
	t.Helper()
	now := time.Now().UTC()

	body := map[string]any{
		"schema_version":     "0.1",
		"envelope_id":        "env_" + key,
		"idempotency_key":    key,
		"correlation_id":     "corr_" + key,
		"received_at":        now.Format(time.RFC3339),
		"tenant_id":          d.tenant,
		"authority_grant_id": d.grantID,
		"principal": map[string]any{
			"principal_id": "prin_proc", "account_id": "acct_proc",
			"principal_type": "INDIVIDUAL",
		},
		"agent": map[string]any{
			"agent_id": d.agentID, "agent_type": "EXECUTION", "operator_id": "op_proc",
			"attestation": map[string]any{"level": "A1", "method": "api_key"},
		},
		"intent": map[string]any{
			"instrument_id": "instr_us_equity_00206R102",
			"asset_class":   "EQUITY",
			"side":          "BUY",
			"order_type":    "MARKET",
			"notional":      money.MustParse("4000"),
			"time_in_force": "DAY",
		},
	}
	if mutate != nil {
		mutate(body)
	}

	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	value, err := identity.SignEnvelope(raw, d.signingKey)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// json.Number, so the financial literal survives being re-encoded with the
	// signature attached. Decoding into map[string]any would put it through a float.
	var document map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatalf("decode: %v", err)
	}
	document["signature"] = map[string]any{
		"algorithm": identity.AlgorithmEd25519, "key_id": d.keyID, "value": value,
	}
	signed, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal signed: %v", err)
	}
	return string(signed)
}

// --- helpers -------------------------------------------------------------------

func brokerFor(t *testing.T) string {
	t.Helper()
	if os.Getenv("ALPACA_KEY_ID") != "" {
		return "alpaca"
	}
	return "fake"
}

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

func randomToken(t *testing.T) string {
	t.Helper()
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("random: %v", err)
	}
	return hex.EncodeToString(b)
}

func write(t *testing.T, path string, value any) {
	t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("marshal %s: %v", path, err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func flatten(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

func tail(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "(no log)"
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) > 20 {
		lines = lines[len(lines)-20:]
	}
	return strings.Join(lines, "\n")
}
