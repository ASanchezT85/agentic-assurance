package security

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/money"
)

// P-002: issuing authority is not submitting intents, and withdrawing it is neither.
//
// The gateway has always checked MayIssueAuthority before creating a grant, and §3 of the
// project status lists that separation first among the three it calls deliberate. Nothing
// tested it. A mutation sweep removed the check and every suite stayed green — unit,
// security, scenarios, contract — because no test anywhere asked whether a submission
// credential could widen its own ceiling.
//
// Revocation had no privilege check at all, and nothing said that was intended. It is the
// emergency lever, and a lever any agent in the tenant can pull against its peers is a
// denial of service with a credential that was only supposed to be able to trade.

type grantStore struct {
	saved   map[string]*authority.Grant
	revoked map[string]string
}

func newGrantStore() *grantStore {
	return &grantStore{saved: map[string]*authority.Grant{}, revoked: map[string]string{}}
}

func (s *grantStore) Save(_ context.Context, g *authority.Grant) error {
	s.saved[g.TenantID+"|"+g.GrantID] = g
	return nil
}

func (s *grantStore) Load(_ context.Context, tenantID, grantID string) (*authority.Grant, error) {
	g, ok := s.saved[tenantID+"|"+grantID]
	if !ok {
		return nil, authority.ErrGrantNotFound
	}
	return g, nil
}

// Revoke takes the reason, not the author: the store records why the grant was cut and the
// evidence event records who cut it.
func (s *grantStore) Revoke(_ context.Context, tenantID, grantID string, _ time.Time,
	reason string) error {

	if _, ok := s.saved[tenantID+"|"+grantID]; !ok {
		return authority.ErrGrantNotFound
	}
	s.revoked[tenantID+"|"+grantID] = reason
	return nil
}

const (
	issuerToken   = "issuer-token-of-at-least-thirty-two-characters"
	traderToken   = "trader-token-of-at-least-thirty-two-characters"
	authorityTest = "tenant_authority"
)

// authorityCredentials is one tenant with an issuer and an agent that may only submit.
func authorityCredentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials(fmt.Sprintf(
		"svc_issuer@%s=%s,svc_agent@%s=%s",
		authorityTest, issuerToken, authorityTest, traderToken))
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	creds.AllowIssuers("svc_issuer")
	return creds
}

func grantBody(t *testing.T, grantID string) string {
	t.Helper()
	now := time.Now().UTC()
	raw, err := json.Marshal(map[string]any{
		"grant_id": grantID, "principal_id": "prin_1", "account_id": "acct_1",
		"agent_id": "agent_1", "issued_by": "ops@example.test",
		"valid_from": now.Add(-time.Hour), "valid_until": now.Add(time.Hour),
		"allowed_operations":    []string{string(intent.SideBuy)},
		"allowed_asset_classes": []string{string(intent.AssetEquity)},
		"allowed_instruments":   []string{"instr_us_equity_00206R102"},
		"per_order_notional":    money.MustParse("1000").String(),
		"max_open_orders":       5,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(raw)
}

func authorityRequest(t *testing.T, path, body, token string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestOnlyAnIssuerMayCreateAGrant(t *testing.T) {
	store := newGrantStore()
	handler := gateway.IssueGrantHandler(store, nil, authorityCredentials(t), nil, nil)

	// The issuer may.
	rec := httptest.NewRecorder()
	handler(rec, authorityRequest(t, "/v1/authority-grants", grantBody(t, "grant_ok"), issuerToken))
	if rec.Code != http.StatusCreated {
		t.Fatalf("the issuer was refused: %d %s", rec.Code, rec.Body.String())
	}

	// The submission credential may not.
	rec = httptest.NewRecorder()
	handler(rec, authorityRequest(t, "/v1/authority-grants", grantBody(t, "grant_self"), traderToken))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a submission credential issued authority: %d %s\n\n"+
			"An agent that can issue its own grant can raise its own ceiling, and INV-002 "+
			"would then be enforced against a limit the party under it decides (P-002).",
			rec.Code, rec.Body.String())
	}
	if _, exists := store.saved[authorityTest+"|grant_self"]; exists {
		t.Error("the grant was stored anyway")
	}

	// And nothing unauthenticated does.
	rec = httptest.NewRecorder()
	handler(rec, authorityRequest(t, "/v1/authority-grants", grantBody(t, "grant_anon"), ""))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("an unauthenticated caller answered %d", rec.Code)
	}
}

func TestOnlyAnIssuerMayRevokeAGrant(t *testing.T) {
	store := newGrantStore()
	if err := store.Save(context.Background(), &authority.Grant{
		GrantID: "grant_live", TenantID: authorityTest, Status: authority.StatusActive,
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	handler := gateway.RevokeGrantHandler(store, nil, authorityCredentials(t), nil, nil)
	body := `{"revoked_by":"ops@example.test","reason":"incident"}`

	// The agent may not, which is the finding: revocation had no privilege check, so any
	// credential in the tenant could cut any agent's authority — including a peer's.
	rec := httptest.NewRecorder()
	req := authorityRequest(t, "/v1/authority-grants/grant_live/revoke", body, traderToken)
	req.SetPathValue("id", "grant_live")
	handler(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("a submission credential revoked a grant: %d %s\n\n"+
			"The emergency lever must be fast, which is why it needs no signature and no "+
			"second party. Fast is not the same as unprivileged: an agent that can revoke "+
			"can stop every other agent in the tenant with a credential issued to trade.",
			rec.Code, rec.Body.String())
	}
	if reason, revoked := store.revoked[authorityTest+"|grant_live"]; revoked {
		t.Errorf("the grant was revoked anyway (%q)", reason)
	}

	// The issuer may, and it still needs no signature and no second party.
	rec = httptest.NewRecorder()
	req = authorityRequest(t, "/v1/authority-grants/grant_live/revoke", body, issuerToken)
	req.SetPathValue("id", "grant_live")
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("the issuer could not pull the lever: %d %s", rec.Code, rec.Body.String())
	}
	if store.revoked[authorityTest+"|grant_live"] != "incident" {
		t.Errorf("the revocation recorded %q as its reason",
			store.revoked[authorityTest+"|grant_live"])
	}
}
