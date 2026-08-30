// Command live-setup prepares one tenant so the deployables can be started and driven.
//
// It exists for the acceptance item that source wiring and integration tests cannot
// cover: starting the actual binary. A gateway is only as correct as what main.go does
// with its environment, and nothing that constructs a Pipeline in a test ever runs that.
//
// Everything here goes through the same packages the platform uses — the grant store,
// the key store, the policy compiler and signer — so what the gateway later reads is
// what the platform can write, rather than rows somebody hand-crafted to match.
//
// It writes an environment file to stdout and its artifacts to -dir. Local development
// only: the credentials it prints are generated per run and the broker is the fake one.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
	"agentic-assurance/internal/pg"
	"agentic-assurance/internal/policy"
)

const livePolicy = `
version: 1
policy: pol_live
rules:
  - id: cap_notional
    action: DENY
    when:
      notional_gt: 25000
`

func main() {
	dir := flag.String("dir", ".live", "where to write the bundle, instruments and env")
	fleet := flag.Int("agents", 0, "also register signing keys for agent_load_0..N-1")
	tenants := flag.Int("tenants", 1, "how many tenants to provision; more than one is for the isolation-under-load run")
	flag.Parse()

	if err := run(*dir, *fleet, *tenants); err != nil {
		fmt.Fprintln(os.Stderr, "live-setup:", err)
		os.Exit(1)
	}
}

func run(dir string, fleet, tenantCount int) error {
	ctx := context.Background()
	now := time.Now().UTC()

	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		return fmt.Errorf("POSTGRES_APP_DSN is required")
	}
	pool, err := pg.Open(ctx, dsn)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	const (
		grantID = "grant_live"
		agentID = "agent_live"
		keyID   = "key_live"
	)
	if tenantCount < 1 {
		tenantCount = 1
	}
	names := make([]string, tenantCount)
	for i := range names {
		names[i] = fmt.Sprintf("tenant_live_%d_%d", now.Unix(), i)
	}
	tenant := names[0]

	// One agent key pair and one policy key pair across every tenant provisioned here.
	// The gateway holds a single POLICY_PUBLIC_KEY, and what a multi-tenant load run
	// measures is isolation under load rather than key management.
	agentPub, agentPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	policyPub, policyPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	// The activation key is a third key, deliberately.
	//
	// It authorizes putting a policy into production, which is an operator's act. The
	// bundle key signs rules and the agent key signs orders; neither may promote a
	// policy, or an agent's compromised key could decide what constrains it.
	activationPub, activationPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	activations := policy.NewActivationStore(pool)

	if err := os.MkdirAll(filepath.Join(dir, "policy"), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "instruments.json"),
		[]byte(`{"instr_us_equity_00206R102":"AAPL"}`), 0o600); err != nil {
		return err
	}

	grants := authority.NewStore(pool)
	keys := identity.NewKeyStore(pool)
	tokens := make([]string, len(names))

	for i, name := range names {
		tokens[i] = randomToken()

		// The grant, written through the store the gateway reads it with. A fixture
		// inserted straight into the table would prove the table accepts rows.
		if err := grants.Save(ctx, &authority.Grant{
			GrantID:             grantID,
			TenantID:            name,
			PrincipalID:         "prin_live",
			AccountID:           "acct_live",
			AgentID:             agentID,
			IssuedAt:            now.Add(-time.Hour),
			ValidFrom:           now.Add(-time.Hour),
			ValidUntil:          now.Add(48 * time.Hour),
			AllowedOperations:   []intent.Side{intent.SideBuy, intent.SideSell},
			AllowedAssetClasses: []intent.AssetClass{intent.AssetEquity},
			AllowedInstruments:  []string{"instr_us_equity_00206R102"},
			Limits: authority.Limits{
				// Wide enough that a load run measures the platform rather than the
				// ceiling. A run that spends its authority in the first second
				// reports the latency of a refusal.
				PerOrderNotional:  money.MustParse("50000"),
				Rolling1hNotional: money.MustParse("500000000"),
				DailyNotional:     money.MustParse("500000000"),
				MaxOpenOrders:     2000000,
			},
			Status: authority.StatusActive,
		}); err != nil {
			return fmt.Errorf("save the grant for %s: %w", name, err)
		}

		// The signing keys. Envelopes are verified rather than trusted, so a load run
		// exercises signature verification the way a customer's agent does.
		//
		// Registered through the store rather than through POST /v1/agent-keys, because
		// this runs before the gateway is up: it is what prepares the tenant the gateway
		// will serve. A customer onboarding an agent uses the endpoint, and the
		// integration suite proves that path end to end.
		agentIDs := []string{agentID}
		for a := range fleet {
			agentIDs = append(agentIDs, fmt.Sprintf("agent_load_%d", a))
		}
		for _, id := range agentIDs {
			if _, err := keys.Register(ctx, identity.AgentKey{
				TenantID: name, AgentID: id, KeyID: keyID,
				Algorithm: identity.AlgorithmEd25519, PublicKey: agentPub,
				Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
			}); err != nil {
				return fmt.Errorf("register %s for %s: %w", id, name, err)
			}
		}

		// A signed, fully staged bundle on disk. The gateway verifies the signature
		// and refuses anything that is not ACTIVE; it does not activate policy itself.
		src, err := policy.ParseSource([]byte(livePolicy))
		if err != nil {
			return err
		}
		bundle, err := policy.Compile(src, name, "bundle_live", now)
		if err != nil {
			return err
		}
		if err := bundle.Sign(policyPriv, "live-setup", now); err != nil {
			return err
		}
		for _, to := range []policy.Status{
			policy.StatusSimulated, policy.StatusShadow, policy.StatusCanary, policy.StatusActive,
		} {
			if err := bundle.Transition(to, now, "live-setup"); err != nil {
				return err
			}
		}
		raw, err := json.MarshalIndent(bundle, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "policy", name+".json"), raw, 0o600); err != nil {
			return err
		}

		// The customer's authorization to activate it, and the key it is signed with.
		if _, err := activations.RegisterKey(ctx, policy.ActivationKey{
			TenantID: name, KeyID: "act_live", PublicKey: activationPub,
			Holder: "live-setup", Status: "ACTIVE", ValidFrom: now.Add(-time.Hour),
		}); err != nil {
			return fmt.Errorf("register the activation key for %s: %w", name, err)
		}

		authorization := policy.Authorization{
			SchemaVersion:     policy.AuthorizationSchemaVersion,
			TenantID:          name,
			BundleID:          bundle.BundleID,
			BundleContentHash: bundle.ContentHash,
			Action:            policy.ActionActivate,
			Actor:             "live-setup",
			Reason:            "local development bootstrap",
			AuthorizedAt:      now,
			Nonce:             fmt.Sprintf("live_%s_%d", name, now.UnixNano()),
		}
		if err := authorization.Sign(activationPriv, "act_live"); err != nil {
			return err
		}
		authRaw, err := json.MarshalIndent(authorization, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "policy", name+".activation.json"),
			authRaw, 0o600); err != nil {
			return err
		}
	}

	agentToken := tokens[0]
	issuerToken := randomToken()

	registrarToken := randomToken()
	// A fourth identity, for the strongest privilege: bootstrapping the key that
	// authorizes a policy into force. Separate from the registrar for the same reason
	// the registrar is separate from the issuer.
	policyToken := randomToken()
	credentials := fmt.Sprintf("svc_issuer@%s=%s,svc_registrar@%s=%s,svc_policy@%s=%s",
		tenant, issuerToken, tenant, registrarToken, tenant, policyToken)
	loadTenants := ""
	for i, name := range names {
		credentials += fmt.Sprintf(",svc_live_%d@%s=%s", i, name, tokens[i])
		if i > 0 {
			// Each tenant issues its own grants under its own credential, and each one
			// needs its own token.
			//
			// They shared one, and credentials are indexed by token: the second entry
			// replaced the first, so the issuer token resolved to the last tenant
			// provisioned and a whole load run issued its grants into the wrong
			// tenant. The submissions were then refused with GRANT_UNAVAILABLE, which
			// is the platform being right about a harness being wrong.
			credentials += fmt.Sprintf(",svc_issuer_%d@%s=%s", i, name, randomToken())
		}
		if loadTenants != "" {
			loadTenants += ","
		}
		loadTenants += name + "=" + tokens[i]
	}

	// Printed as an environment file rather than written into the repository. These are
	// credentials, generated per run, and spec section 35 keeps that kind of value out
	// of the source tree.
	fmt.Printf("export LIVE_TENANT=%s\n", tenant)
	fmt.Printf("export LIVE_GRANT_ID=%s\n", grantID)
	fmt.Printf("export LIVE_AGENT_ID=%s\n", agentID)
	fmt.Printf("export LIVE_KEY_ID=%s\n", keyID)
	fmt.Printf("export LIVE_SIGNING_KEY=%s\n", hex.EncodeToString(agentPriv))
	fmt.Printf("export POLICY_PUBLIC_KEY=%s\n", hex.EncodeToString(policyPub))
	fmt.Printf("export GATEWAY_API_TOKEN=%s\n", agentToken)
	fmt.Printf("export GATEWAY_ISSUER_TOKEN=%s\n", issuerToken)
	fmt.Printf("export GATEWAY_API_CREDENTIALS=%s\n", credentials)
	fmt.Printf("export LOAD_TENANTS=%s\n", loadTenants)

	issuers := "svc_issuer"
	for i := range names {
		if i > 0 {
			issuers += fmt.Sprintf(",svc_issuer_%d", i)
		}
	}
	fmt.Printf("export GATEWAY_GRANT_ISSUERS=%s\n", issuers)
	// The registrar, separate from the issuer. Onboarding an agent over the API needs it,
	// and nothing else should have it.
	fmt.Printf("export GATEWAY_KEY_REGISTRARS=svc_registrar\n")
	fmt.Printf("export GATEWAY_REGISTRAR_TOKEN=%s\n", registrarToken)
	return nil
}

func randomToken() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
