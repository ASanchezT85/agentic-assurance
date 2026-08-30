package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
)

// What this binary does with its environment.
//
// Every handler here is proved correct elsewhere, in a rig that hands it a credential
// registry built by the test. None of that says this binary reads the right variable
// names, grants the right privilege to the right identity, or hangs the handler on the
// route the privilege belongs to. A handler proved correct and reached by the wrong
// credential is a hole with a passing test suite.
//
// It was going to be a smoke test against the running process. This workstation's Smart
// App Control refuses to execute freshly built unsigned binaries — `go test` binaries run,
// standalone builds do not — so the same wiring is asserted through the same functions
// main calls, in a test binary. The process boundary is what is missing, and it is named
// here rather than left implied.

const (
	wiringPolicyToken    = "wiring-policy-token-of-thirty-two-plus-chars"
	wiringRegistrarToken = "wiring-registrar-token-of-thirty-two-plus-x"
	wiringIssuerToken    = "wiring-issuer-token-of-thirty-two-plus-chars"
	wiringAgentToken     = "wiring-agent-token-of-thirty-two-plus-charss"
)

// wiringEnv sets the four identities and the three privilege lists, exactly as a
// deployment does.
func wiringEnv(t *testing.T) *identity.Credentials {
	t.Helper()
	t.Setenv("GATEWAY_API_CREDENTIALS", strings.Join([]string{
		"svc_policy@tenant_w=" + wiringPolicyToken,
		"svc_registrar@tenant_w=" + wiringRegistrarToken,
		"svc_issuer@tenant_w=" + wiringIssuerToken,
		"svc_agent@tenant_w=" + wiringAgentToken,
	}, ","))
	t.Setenv("GATEWAY_GRANT_ISSUERS", "svc_issuer")
	t.Setenv("GATEWAY_KEY_REGISTRARS", "svc_registrar")
	t.Setenv("GATEWAY_ACTIVATION_KEY_REGISTRARS", "svc_policy")

	creds := credentialsFromEnv(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	if creds == nil {
		t.Fatal("no credentials were parsed from the environment")
	}
	return creds
}

// The three privileges are three lists, and holding one grants nothing else.
func TestTheThreePrivilegeListsAreSeparate(t *testing.T) {
	creds := wiringEnv(t)

	for _, c := range []struct {
		token                          string
		issue, register, activationKey bool
	}{
		{wiringIssuerToken, true, false, false},
		{wiringRegistrarToken, false, true, false},
		{wiringPolicyToken, false, false, true},
		{wiringAgentToken, false, false, false},
	} {
		caller, ok := creds.Identify(c.token)
		if !ok {
			t.Fatalf("a credential in GATEWAY_API_CREDENTIALS did not authenticate")
		}
		if caller.MayIssueAuthority != c.issue ||
			caller.MayRegisterKeys != c.register ||
			caller.MayRegisterActivationKeys != c.activationKey {
			t.Errorf("%s holds issue=%v register=%v activationKey=%v; expected %v/%v/%v.\n\n"+
				"A grant says what an agent may do, an agent key says which key IS that "+
				"agent, and an activation key says which policy constrains every agent. "+
				"Holding one of these must never confer another.",
				caller.Identity, caller.MayIssueAuthority, caller.MayRegisterKeys,
				caller.MayRegisterActivationKeys, c.issue, c.register, c.activationKey)
		}
	}
}

// And the routes hang the handlers where those privileges belong.
//
// The stores are nil, so a caller who passes the privilege gate reaches the store check
// and gets 503. That is the assertion: 503 means "authorized, and this deployment has no
// store", while 403 means the gate refused. The two are what distinguishes a route wired
// to the right privilege from one wired to the wrong one.
func TestTheKeyRoutesAreWiredToTheirOwnPrivileges(t *testing.T) {
	creds := wiringEnv(t)

	mux := newMux(nil, routes{
		registerKey: gateway.RegisterAgentKeyHandler(nil, nil, creds, nil, nil),
		revokeKey:   gateway.RevokeAgentKeyHandler(nil, nil, creds, nil, nil),
		registerActivationKey: gateway.RegisterActivationKeyHandler(
			nil, nil, creds, nil, nil),
		revokeActivationKey: gateway.RevokeActivationKeyHandler(nil, nil, creds, nil, nil),
	}, creds, nil)

	const authorized = http.StatusServiceUnavailable

	for _, c := range []struct {
		path   string
		token  string
		expect int
	}{
		{"/v1/agent-keys", wiringRegistrarToken, authorized},
		{"/v1/agent-keys", wiringPolicyToken, http.StatusForbidden},
		{"/v1/agent-keys", wiringIssuerToken, http.StatusForbidden},
		{"/v1/agent-keys", wiringAgentToken, http.StatusForbidden},
		{"/v1/agent-keys", "", http.StatusUnauthorized},

		{"/v1/agent-keys/revoke", wiringRegistrarToken, authorized},
		{"/v1/agent-keys/revoke", wiringAgentToken, http.StatusForbidden},

		{"/v1/policy-activation-keys", wiringPolicyToken, authorized},
		{"/v1/policy-activation-keys", wiringRegistrarToken, http.StatusForbidden},
		{"/v1/policy-activation-keys", wiringIssuerToken, http.StatusForbidden},
		{"/v1/policy-activation-keys", wiringAgentToken, http.StatusForbidden},
		{"/v1/policy-activation-keys", "", http.StatusUnauthorized},

		{"/v1/policy-activation-keys/revoke", wiringPolicyToken, authorized},
		{"/v1/policy-activation-keys/revoke", wiringRegistrarToken, http.StatusForbidden},
	} {
		req := httptest.NewRequest(http.MethodPost, c.path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != c.expect {
			who := c.token
			if who == "" {
				who = "(no credential)"
			}
			t.Errorf("POST %s with %s answered %d, expected %d: %s",
				c.path, who, rec.Code, c.expect, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// A route the binary does not serve answers 404 rather than 200 with nothing.
func TestTheKeyRoutesAreAbsentWhenNoStoreIsConfigured(t *testing.T) {
	creds := wiringEnv(t)
	mux := newMux(nil, routes{}, creds, nil)

	for _, path := range []string{
		"/v1/agent-keys", "/v1/agent-keys/revoke",
		"/v1/policy-activation-keys", "/v1/policy-activation-keys/revoke",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+wiringPolicyToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Errorf("POST %s answered %d with no handler configured; absent is honest "+
				"and a handler that accepts what it cannot record is not", path, rec.Code)
		}
	}
}
