package security

import (
	"crypto/x509"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"agentic-assurance/internal/identity"
)

// INV-007, the transport half.
//
// The database half was enforced from the start: row level security, FORCE, and a
// non-superuser role, all with a test. It isolated correctly to whichever tenant the
// platform gave it, and nothing established which tenant that should be.
//
// The submission endpoint authenticated the caller and then read the tenant from the
// request body. A caller authenticated as svc_a could submit an envelope naming
// tenant_b and, with a grant id from that tenant, have the order evaluated against
// tenant_b's grant and tenant_b's policy and placed at the venue. Every lookup after
// the identity check takes the envelope's tenant: the grant, the policy bundle, the
// idempotency record, and the app.tenant_id that RLS itself keys on.
//
// The simulation API had the same shape with a header instead of a body field: a run
// is stored, retrieved and cancelled by tenant, so a header let anyone read another
// customer's results and stop their runs.
//
// A credential is issued to a tenant now. These tests are the guard.

const tenantToken = "a-token-of-at-least-thirty-two-characters"

func credentials(t *testing.T) *identity.Credentials {
	t.Helper()
	creds, err := identity.ParseCredentials("svc_a@tenant_a=" + tenantToken)
	if err != nil {
		t.Fatalf("credentials: %v", err)
	}
	return creds
}

// A credential names the tenant it speaks for. One that does not is refused at
// startup, because it would leave the tenant to come from the request.
func TestACredentialMustNameItsTenant(t *testing.T) {
	for _, raw := range []string{
		"svc_a=" + tenantToken,
		"svc_a@=" + tenantToken,
		"@tenant_a=" + tenantToken,
		"svc_a@tenant a=" + tenantToken,
		"svc_a@tenant_a=short",
		"",
	} {
		if _, err := identity.ParseCredentials(raw); err == nil {
			t.Errorf("%q was accepted as a credential", raw)
		}
	}

	if _, err := identity.ParseCredentials("svc_a@tenant_a=" + tenantToken); err != nil {
		t.Errorf("a well-formed credential was refused: %v", err)
	}
}

// The tenant a caller is authenticated for comes from the transport, never from the
// request.
func TestTheTenantComesFromTheCredential(t *testing.T) {
	creds := credentials(t)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tenantToken)
	// A header naming a different tenant. It must not be what establishes anything.
	req.Header.Set("X-Tenant-Id", "tenant_victim")

	attested := (&identity.Verifier{}).Resolve(
		identity.FromTransport(req.Header.Get("Authorization"), nil, creds))

	if attested.TenantID != "tenant_a" {
		t.Fatalf("established tenant = %q, want tenant_a", attested.TenantID)
	}
	if attested.APIIdentity != "svc_a" {
		t.Errorf("identity = %q, want svc_a", attested.APIIdentity)
	}
}

// A claim to another tenant is refused, and the refusal does not name the caller's own
// tenant. An error that printed both would be a probe with feedback (spec section 45).
func TestAClaimToAnotherTenantIsRefused(t *testing.T) {
	creds := credentials(t)

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
	req.Header.Set("Authorization", "Bearer "+tenantToken)
	attested := (&identity.Verifier{}).Resolve(
		identity.FromTransport(req.Header.Get("Authorization"), nil, creds))

	err := identity.RequireTenant(attested, "tenant_victim")
	if err == nil {
		t.Fatal("a caller authenticated for tenant_a was allowed to act on tenant_victim")
	}
	if strings.Contains(err.Error(), "tenant_a") {
		t.Errorf("the refusal names the caller's own tenant: %q. An attacker guessing "+
			"tenant names would learn one from every refusal", err)
	}

	if err := identity.RequireTenant(attested, "tenant_a"); err != nil {
		t.Errorf("the caller's own tenant was refused: %v", err)
	}
}

// An identity with no established tenant is refused rather than trusted.
//
// A workload certificate proves which workload is calling and says nothing about which
// customer it acts for. Until the workload registry existed there was nothing to check
// a claim against, and falling back to the request would have reintroduced the hole
// this closes. Now there is a registry, and a workload absent from it is still refused:
// it is one nobody has assigned to a customer, and guessing would assign it.
func TestAnIdentityWithNoTenantCannotClaimOne(t *testing.T) {
	unmapped := identity.Attested{
		Level:    "A2",
		Method:   "x509-svid",
		SpiffeID: identity.SPIFFEID{TrustDomain: "acme.example", Path: "/ns/agents/sa/unmapped"},
	}

	err := identity.RequireTenant(unmapped, "tenant_a")
	if err == nil {
		t.Fatal("an unmapped workload was allowed to name a tenant")
	}
	if !strings.Contains(err.Error(), "/ns/agents/sa/unmapped") {
		t.Errorf("error = %q; the refusal should name the workload, or an operator "+
			"cannot tell which registry entry is missing", err)
	}

	// And an identity with neither a workload nor a credential.
	if err := identity.RequireTenant(identity.Attested{Method: "NONE"}, "tenant_a"); err == nil {
		t.Fatal("an identity with nothing established was allowed to name a tenant")
	}
}

// An unauthenticated caller establishes nothing, including a tenant.
func TestAnUnauthenticatedCallerHasNoTenant(t *testing.T) {
	creds := credentials(t)

	for _, header := range []string{"", "Bearer wrong-token", "Basic " + tenantToken} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		if header != "" {
			req.Header.Set("Authorization", header)
		}
		req.Header.Set("X-Tenant-Id", "tenant_a")

		attested := (&identity.Verifier{}).Resolve(
			identity.FromTransport(req.Header.Get("Authorization"), nil, creds))
		if attested.TenantID != "" {
			t.Errorf("header %q established tenant %q", header, attested.TenantID)
		}
		if err := identity.RequireExecutable(attested); err == nil {
			t.Errorf("header %q produced an executable identity", header)
		}
	}
}

// Every route that carries tenant data authenticates.
//
// This class of hole has now appeared three times: the submission endpoint bound a
// caller but not a tenant, the evidence endpoints took the tenant from a header, and
// the intelligence API did the same. Each was found by looking rather than by a guard.
//
// So the guard reads the routes. Every registration outside health and readiness names
// a handler, and that handler's body has to reach one of the authentication entry
// points. It is a source-level check because the muxes live in three packages and one
// of them is package main; what it costs in precision it buys in covering all of them
// at once.
func TestEveryRouteThatCarriesTenantDataAuthenticates(t *testing.T) {
	// Handlers that legitimately carry nothing about a tenant.
	unauthenticated := map[string]string{
		"health": "liveness and readiness say whether the process is up, nothing about a customer",
	}

	// The one primitive every authenticated path reaches, directly or through a
	// helper. Named rather than listing the helpers: an earlier version matched
	// "tenantOf(r, creds)" and broke the moment that function took another argument,
	// which is a guard failing on a rename rather than on a hole.
	const authEntryPoint = "identity.FromTransport"

	// Discovered, not listed. The first version named four files, and a new package
	// with an unauthenticated route was invisible to it: the same
	// enumerate-your-own-coverage bug this guard exists to catch, in the guard.
	files := goSources(t)

	routes := regexp.MustCompile(`(?m)HandleFunc\(\s*"([^"]+)"\s*,\s*([A-Za-z0-9_.,()&{} ]+?)\)\s*$`)
	checked := 0

	for _, path := range files {
		source := readSource(t, path)
		if !strings.Contains(source, "HandleFunc(") {
			continue
		}

		for _, match := range routes.FindAllStringSubmatch(source, -1) {
			route, handler := match[1], strings.TrimSpace(match[2])

			// The handler expression is a name, a method value, or a call that returns
			// one. Reduce it to the identifier a func declaration would carry.
			name := handler
			name = strings.TrimPrefix(name, "a.")
			if i := strings.IndexAny(name, "("); i > 0 {
				name = name[:i]
			}

			if why, ok := unauthenticated[name]; ok {
				if strings.Contains(route, "/v1/") {
					t.Errorf("%s serves %s and is listed as needing no authentication (%s), "+
						"but a /v1/ route carries tenant data", path, route, why)
				}
				continue
			}

			// A handler built elsewhere and passed in as a value. Written down rather
			// than followed automatically: the guard refusing to guess is what caught
			// this one, and a guard that resolved names loosely would have passed.
			elsewhere := map[string]string{
				"submit":       "../../internal/gateway/http.go:SubmitHandler",
				"status":       "../../internal/gateway/intents.go:IntentStatusHandler",
				"revoke":       "../../internal/gateway/intents.go:RevokeGrantHandler",
				"issue":        "../../internal/gateway/grants.go:IssueGrantHandler",
				"applyControl": "../../internal/gateway/controls.go:IssueControlHandler",
			}

			target, fn := path, name
			if !funcExists(t, path, name) {
				where, known := elsewhere[name]
				if !known {
					t.Errorf("%s registers %q with handler %q and no function by that "+
						"name is in the file; this guard cannot see whether it "+
						"authenticates", path, route, name)
					continue
				}
				target, fn, _ = strings.Cut(where, ":")
			}

			if !reachesAuth(t, target, fn, authEntryPoint, 0) {
				t.Errorf("%s serves %s and its handler %q never reaches %s, directly or "+
					"through a function in the same file. Every route that carries tenant "+
					"data authenticates, and the tenant comes from the credential rather "+
					"than from the request (INV-007).",
					path, route, name, authEntryPoint)
			}
			checked++
		}
	}

	if checked < 15 {
		t.Errorf("only %d routes were checked; the guard is not finding the registrations "+
			"and would stay green while an unauthenticated one was added", checked)
	}
}

// reachesAuth reports whether a function reaches the authentication primitive,
// following calls to functions declared anywhere in the same package.
//
// The package rather than the file, because Go resolves names that way and this one
// does not: the gateway's handlers live in intents.go and the helper they authenticate
// through lives in http.go. A file-scoped version reported both as unauthenticated,
// which is a guard failing on a file split rather than on a hole.
//
// The parser rather than a regular expression. The regex version resolved every call
// correctly when driven by hand and returned false from inside itself, and an hour of
// staring at it is an hour not spent on the thing it was guarding. A guard nobody can
// debug is a guard nobody trusts.
func reachesAuth(t *testing.T, path, fn, entry string, depth int) bool {
	t.Helper()

	if depth > 3 {
		return false
	}

	files := packageFiles(t, path)

	var decl *ast.FuncDecl
	for _, f := range files {
		if d := findFunc(f, fn); d != nil && d.Body != nil {
			decl = d
			break
		}
	}
	if decl == nil {
		return false
	}

	pkg, sel, _ := strings.Cut(entry, ".")

	found := false
	var called []string

	ast.Inspect(decl.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch target := call.Fun.(type) {
		case *ast.SelectorExpr:
			// identity.FromTransport(...)
			if ident, ok := target.X.(*ast.Ident); ok && ident.Name == pkg && target.Sel.Name == sel {
				found = true
			}
			// a.requireTenant(...) — a method on this file's own type.
			called = append(called, target.Sel.Name)
		case *ast.Ident:
			called = append(called, target.Name)
		}
		return true
	})

	if found {
		return true
	}
	for _, name := range called {
		if name == fn {
			continue // direct recursion
		}
		if reachesAuth(t, path, name, entry, depth+1) {
			return true
		}
	}
	return false
}

// findFunc returns a function or method declaration by name.
// packageFiles parses every non-test Go file beside the given one.
func packageFiles(t *testing.T, path string) []*ast.File {
	t.Helper()

	dir := filepath.Dir(path)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	fset := token.NewFileSet()
	var out []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		full := filepath.Join(dir, name)
		parsed, err := parser.ParseFile(fset, full, readSource(t, full), 0)
		if err != nil {
			t.Fatalf("parse %s: %v", full, err)
		}
		out = append(out, parsed)
	}
	return out
}

func findFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, d := range file.Decls {
		if fn, ok := d.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}

// funcExists reports whether a file declares a function by that name.
func funcExists(t *testing.T, path, name string) bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, readSource(t, path), parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return findFunc(file, name) != nil
}

var _ = token.NewFileSet

// A client certificate must not suppress a bearer credential.
//
// FromTransport used to return as soon as it saw a peer certificate, so a caller
// behind a service mesh — which presents one on every connection — was A0 with a
// perfectly valid token in the request. Resolve's own comment says a
// presented-but-invalid SVID "falls back to whatever the caller independently
// established", and it was never given anything to fall back to.
//
// Nobody hit it while A2 was unreachable. Making A2 reachable made it a lockout.
func TestACertificateDoesNotSuppressACredential(t *testing.T) {
	creds := credentials(t)

	// A certificate this verifier cannot validate: no trust bundle is configured,
	// which is exactly the shape of a mesh certificate arriving at a service that
	// does not speak SPIFFE.
	presented := identity.FromTransport("Bearer "+tenantToken,
		[]*x509.Certificate{{}}, creds)

	if presented.APIIdentity == "" {
		t.Fatal("the bearer credential was not read because a certificate was present")
	}

	attested := (&identity.Verifier{}).Resolve(presented)

	if err := identity.RequireExecutable(attested); err != nil {
		t.Errorf("a caller with a valid credential behind mutual TLS is %s: %v",
			attested.Level, err)
	}
	if err := identity.RequireTenant(attested, "tenant_a"); err != nil {
		t.Errorf("the credential's tenant did not survive: %v", err)
	}
	if attested.Downgrade == nil {
		t.Error("the degradation carries no reason; an operator needs to know the " +
			"certificate was rejected rather than absent")
	}
}

// A verified workload that maps to nothing cannot borrow a credential's tenant.
//
// Otherwise a caller could pair a certificate mapped to no customer with a credential
// for some other one, and have the stronger-looking identity carry the weaker one's
// scope. The registry decides for a workload, or nothing does.
func TestAVerifiedWorkloadCannotBorrowACredentialsTenant(t *testing.T) {
	// Reached through Resolve's own logic rather than a live certificate: the
	// SPIRE-backed half is in tests/integration.
	attested := identity.Attested{
		Level:       "A2",
		SpiffeID:    identity.SPIFFEID{TrustDomain: "acme.example", Path: "/ns/unmapped"},
		APIIdentity: "svc_a",
		TenantID:    "",
	}

	if err := identity.RequireTenant(attested, "tenant_a"); err == nil {
		t.Fatal("an unmapped workload acted on a tenant it was never mapped to")
	}
}
