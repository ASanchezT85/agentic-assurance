// Command assurance-gateway runs in the customer's infrastructure (ADR-003).
//
// The submission path is wired: POST /v1/intents runs an envelope through every check
// in docs/architecture/hot-path.md, in that order, and an order reaches a venue only
// if all of them allow it.
//
// It refuses to serve that endpoint unless it is fully configured. A gateway that
// booted with no policy bundle, no venue and no credentials, and answered anyway,
// would be a gateway that enforces nothing while reporting healthy.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"agentic-assurance/adapters/alpaca"
	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/adapters/tradier"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/control"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
	"agentic-assurance/internal/fleet"
	"agentic-assurance/internal/gateway"
	"agentic-assurance/internal/identity"
	"agentic-assurance/internal/intent"
	"agentic-assurance/internal/pg"
	"agentic-assurance/internal/policy"
)

const component = "assurance-gateway"

// evidenceReader is the slice of the store the HTTP surface may use. It is read-only
// by construction: there is no Append here, so no request can write evidence
// (ADR-009, INV-006).
type evidenceReader interface {
	Chain(ctx context.Context, tenantID, correlationID string) ([]evidence.Event, error)
	ByAggregate(ctx context.Context, tenantID, aggregateID string) ([]evidence.Event, error)
}

// routes are the handlers the mux serves when they are configured.
//
// A struct rather than a positional list. It was eleven bare http.HandlerFunc parameters
// and every caller passed a run of nils; adding the activation-key endpoints would have
// made it thirteen, and a mis-ordered pair in that run would wire the wrong endpoint to
// the wrong privilege check without failing to compile.
type routes struct {
	submit        http.HandlerFunc
	status        http.HandlerFunc
	list          http.HandlerFunc
	revokeGrant   http.HandlerFunc
	issueGrant    http.HandlerFunc
	applyControl  http.HandlerFunc
	revokeControl http.HandlerFunc
	listControls  http.HandlerFunc

	registerKey http.HandlerFunc
	revokeKey   http.HandlerFunc

	registerActivationKey http.HandlerFunc
	revokeActivationKey   http.HandlerFunc
}

func newMux(reader evidenceReader, h routes,
	creds *identity.Credentials, verifier *identity.Verifier) *http.ServeMux {
	mux := http.NewServeMux()

	health := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":    "ok",
			"component": component,
			"phase":     "17",
		})
	}
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", health)

	// ADR-023. Spec section 66 step 19 requires the chain
	// agent -> intent -> authority -> policy -> broker order -> result to be
	// inspectable, and section 59 forbids the console from being required for it.
	mux.HandleFunc("GET /v1/evidence", evidenceByCorrelation(reader, creds, verifier))
	mux.HandleFunc("GET /v1/intents/{id}/evidence", evidenceByIntent(reader, creds, verifier))

	// The write path. Absent rather than answering when the enforcement plane is not
	// fully configured: a 404 is honest, and a handler that accepted intents it
	// could not evaluate would be worse than no handler.
	if h.submit != nil {
		mux.HandleFunc("POST /v1/intents", h.submit)
	}
	if h.status != nil {
		mux.HandleFunc("GET /v1/intents/{id}", h.status)
	}
	if h.list != nil {
		mux.HandleFunc("GET /v1/intents", h.list)
	}
	if h.revokeGrant != nil {
		mux.HandleFunc("POST /v1/authority-grants/{id}/revoke", h.revokeGrant)
	}
	if h.issueGrant != nil {
		mux.HandleFunc("POST /v1/authority-grants", h.issueGrant)
	}
	if h.applyControl != nil {
		mux.HandleFunc("POST /v1/controls", h.applyControl)
	}
	// Revoke is registered before register, so the more specific pattern is the one a
	// path with a suffix matches.
	if h.revokeKey != nil {
		mux.HandleFunc("POST /v1/agent-keys/revoke", h.revokeKey)
	}
	if h.registerKey != nil {
		mux.HandleFunc("POST /v1/agent-keys", h.registerKey)
	}
	if h.revokeActivationKey != nil {
		mux.HandleFunc("POST /v1/policy-activation-keys/revoke", h.revokeActivationKey)
	}
	if h.registerActivationKey != nil {
		mux.HandleFunc("POST /v1/policy-activation-keys", h.registerActivationKey)
	}
	if h.revokeControl != nil {
		mux.HandleFunc("POST /v1/controls/{id}/revoke", h.revokeControl)
	}
	if h.listControls != nil {
		mux.HandleFunc("GET /v1/controls", h.listControls)
	}

	return mux
}

// tenantOf establishes which tenant a caller speaks for.
//
// It used to read a header, with a comment saying authentication would arrive with the
// surface that carried it. It never did, and what these endpoints return is the whole
// audit chain: every intent, identity, authority decision, policy decision and broker
// order. Naming a tenant in a header was enough to read all of it.
//
// The tenant comes from the credential now (INV-007, ADR-025). A header, if sent, must
// agree: ignoring one that disagrees would let a caller believe they were reading
// someone else's evidence.
func tenantOf(r *http.Request, creds *identity.Credentials, verifier *identity.Verifier) (string, int, string) {
	var certs []*x509.Certificate
	if r.TLS != nil {
		certs = r.TLS.PeerCertificates
	}
	if verifier == nil {
		verifier = &identity.Verifier{}
	}
	attested := verifier.Resolve(
		identity.FromTransport(r.Header.Get("Authorization"), certs, creds))

	if err := identity.RequireExecutable(attested); err != nil {
		return "", http.StatusUnauthorized, "the caller is not authenticated"
	}
	if attested.TenantID == "" {
		return "", http.StatusUnauthorized,
			"the caller is authenticated but no tenant is established for it"
	}
	if claimed := strings.TrimSpace(r.Header.Get("X-Tenant-Id")); claimed != "" &&
		claimed != attested.TenantID {
		return "", http.StatusForbidden,
			"the request names a tenant this caller is not authenticated for"
	}
	return attested.TenantID, 0, ""
}

func evidenceByCorrelation(reader evidenceReader, creds *identity.Credentials, verifier *identity.Verifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := tenantOf(r, creds, verifier)
		if tenant == "" {
			writeError(w, status, message)
			return
		}
		correlationID := strings.TrimSpace(r.URL.Query().Get("correlation_id"))

		if correlationID == "" {
			writeError(w, http.StatusBadRequest, "correlation_id is required")
			return
		}
		serveChain(w, r, reader, tenant, func(ctx context.Context) ([]evidence.Event, error) {
			return reader.Chain(ctx, tenant, correlationID)
		})
	}
}

func evidenceByIntent(reader evidenceReader, creds *identity.Credentials, verifier *identity.Verifier) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := tenantOf(r, creds, verifier)
		if tenant == "" {
			writeError(w, status, message)
			return
		}
		intentID := strings.TrimSpace(r.PathValue("id"))

		if intentID == "" {
			writeError(w, http.StatusBadRequest, "intent id is required")
			return
		}
		serveChain(w, r, reader, tenant, func(ctx context.Context) ([]evidence.Event, error) {
			return reader.ByAggregate(ctx, tenant, intentID)
		})
	}
}

func serveChain(w http.ResponseWriter, r *http.Request, reader evidenceReader, tenant string,
	load func(context.Context) ([]evidence.Event, error)) {

	if reader == nil {
		writeError(w, http.StatusServiceUnavailable, "no evidence store is configured")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	events, err := load(ctx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "evidence could not be read")
		return
	}
	if events == nil {
		events = []evidence.Event{}
	}

	// The chain is returned exactly as stored. Corrections appear as later events
	// referencing earlier ones and are never merged away: a reader needs to see
	// that a correction happened, not a tidied result (ADR-023).
	writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id": tenant,
		"count":     len(events),
		"events":    events,
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func writeError(w http.ResponseWriter, status int, message string) {
	// The message is deliberately generic. Spec section 45 lists cross-tenant
	// leakage as a threat, and an error that distinguishes "no such correlation" in
	// this tenant from "exists in another" is itself a disclosure.
	writeJSON(w, status, map[string]string{"error": message})
}

func addr() string {
	if a := os.Getenv("GATEWAY_ADDR"); a != "" {
		return a
	}
	return ":8080"
}

// openEvidence connects to PostgreSQL if a DSN is configured.
//
// A missing DSN is not fatal. The gateway must boot and report health without a
// database so that an operator can see the process is alive, and the evidence
// endpoints answer 503 rather than the whole binary refusing to start.
func openEvidence(ctx context.Context, log *slog.Logger) evidenceReader {
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		log.Warn("no POSTGRES_APP_DSN; evidence endpoints will report unavailable")
		return nil
	}
	pool, err := pg.Open(ctx, dsn)
	if err != nil {
		log.Error("evidence store unavailable", "err", err)
		return nil
	}
	return evidence.NewStore(pool)
}

// buildPipeline assembles the enforcement plane from configuration.
//
// It returns nil when anything required is missing, and says which. Every one of
// these is load-bearing: without a venue nothing executes, without a signed policy
// bundle nothing is constrained, without credentials nothing is authenticated, and
// without a usage ledger every grant with a rolling limit denies.
// openCredentials parses the API credential registry.
//
// Separate from buildPipeline because the read endpoints need it too, and they are
// useful without a venue or a policy bundle. Tying their authentication to the write
// path's configuration would make an operator who only wants to read evidence
// configure a broker to do it.
// openPool connects to PostgreSQL, or reports why not.
//
// Separate from buildPipeline because the endpoints that only read and revoke need a
// database and nothing else. Tying them to the submission path's configuration would
// mean an operator who cannot submit orders also cannot revoke the authority to.
func openPool(ctx context.Context, log *slog.Logger) *pgxpool.Pool {
	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		return nil
	}
	pool, err := pg.Open(ctx, dsn)
	if err != nil {
		log.Error("database unavailable", "err", err)
		return nil
	}
	return pool
}

// openOutboxPool connects as the publisher role, or returns nil and says why.
func openOutboxPool(ctx context.Context, log *slog.Logger) *pgxpool.Pool {
	dsn := os.Getenv("POSTGRES_OUTBOX_DSN")
	if dsn == "" {
		if os.Getenv("POSTGRES_APP_DSN") != "" {
			log.Warn("no POSTGRES_OUTBOX_DSN; the evidence outbox is not published",
				"consequence", "committed evidence stays in the outbox and the event "+
					"stream is empty",
				"fix", "set POSTGRES_OUTBOX_DSN for the assurance_outbox role")
		}
		return nil
	}
	pool, err := pg.Open(ctx, dsn)
	if err != nil {
		log.Error("outbox database unavailable", "err", err)
		return nil
	}
	return pool
}

// crashPoint arms deterministic fault injection for the deployable recovery test.
//
// It exists because an in-process reconstruction of the crash window can model the
// failure and cannot prove the deployable survives it: only a real process killed
// between a venue's acceptance and the outcome write does that.
//
// Guarded twice. The variable must name the point exactly, and the process must be a
// development one pointed at a fake venue — a production configuration cannot arm it by
// accident, and a configuration that tries is refused loudly rather than ignored.
func crashPoint(log *slog.Logger) func(string) {
	point := os.Getenv("ASSURANCE_TEST_CRASH_POINT")
	if point == "" {
		return nil
	}
	if point != "after_broker_accept_before_outcome_commit" {
		log.Error("unknown ASSURANCE_TEST_CRASH_POINT", "value", point)
		os.Exit(2)
	}
	if os.Getenv("ASSURANCE_ENV") != "development" {
		log.Error("ASSURANCE_TEST_CRASH_POINT requires ASSURANCE_ENV=development",
			"consequence", "refusing to start rather than arming fault injection in a "+
				"configuration that could be production")
		os.Exit(2)
	}

	log.Warn("fault injection armed",
		"point", point,
		"consequence", "this process will exit without recording the outcome of the "+
			"first order a venue accepts")

	return func(clientOrderID string) {
		log.Warn("crashing after venue acceptance", "client_order_id", clientOrderID)
		// Not a panic and not a graceful shutdown: a kill, which is what the test is
		// about. Deferred writes, flushes and shutdown hooks must not run.
		os.Exit(9)
	}
}

func openCredentials(log *slog.Logger) *identity.Credentials {
	creds, err := identity.ParseCredentials(os.Getenv("GATEWAY_API_CREDENTIALS"))
	if err != nil {
		log.Warn("no usable GATEWAY_API_CREDENTIALS",
			"err", err.Error(),
			"consequence", "every endpoint that carries tenant data refuses")
		return nil
	}
	return creds
}

func buildPipeline(ctx context.Context, log *slog.Logger) (*gateway.Pipeline, *identity.Credentials) {
	missing := func(what, why string) (*gateway.Pipeline, *identity.Credentials) {
		log.Warn("submission path not served", "missing", what, "consequence", why)
		return nil, nil
	}

	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		return missing("POSTGRES_APP_DSN", "idempotency and usage have nowhere authoritative to live")
	}
	pool, err := pg.Open(ctx, dsn)
	if err != nil {
		log.Error("database unavailable", "err", err)
		return nil, nil
	}

	creds := openCredentials(log)
	if creds == nil {
		return missing("GATEWAY_API_CREDENTIALS", "an unauthenticated caller must never produce an executable order (INV-001)")
	}

	bundles, err := gateway.NewFileBundles(envOr("POLICY_BUNDLE_DIR", "/etc/assurance/policy"), os.Getenv("POLICY_PUBLIC_KEY"))
	if err != nil {
		return missing("POLICY_PUBLIC_KEY", "an unverified policy bundle is not policy")
	}
	// A bundle enforces only when the customer has authorized the activation itself,
	// and the transition is recorded in the same transaction that accepts it. Without
	// this store no policy change can be attributed, so none is applied and whatever is
	// already in force stays in force.
	bundles.Activations = policy.NewActivationStore(pool)
	bundles.Report = func(tenantID string, err error) {
		// A refused activation leaves the previous policy enforcing, which is the safe
		// outcome and an invisible one. Said out loud, or an operator finds out days
		// later that what they staged never took effect.
		log.Warn("policy activation refused",
			"tenant", tenantID, "err", err.Error(),
			"consequence", "the previously authorized bundle stays in force")
	}

	symbols, err := gateway.LoadSymbols(envOr("INSTRUMENT_SYMBOLS", "/etc/assurance/instruments.json"))
	if err != nil {
		return missing("INSTRUMENT_SYMBOLS", "no instrument can be mapped to a venue symbol")
	}

	venue, err := openBroker()
	if err != nil {
		return missing("broker configuration", err.Error())
	}

	usage := authority.NewPostgresUsage(pool)

	// The analytical feed. Optional, and its absence is stated: without it the fleet
	// engine measures a fleet it never observes, and every answer is an empty list
	// that looks exactly like a calm fleet.
	var telemetry *gateway.Telemetry
	if base := os.Getenv("CLICKHOUSE_HTTP_URL"); base != "" {
		user := os.Getenv("CLICKHOUSE_USER")
		if user == "" {
			user = "assurance"
		}
		telemetry = gateway.NewTelemetry(
			fleet.NewSink(strings.TrimRight(base, "/"), user, os.Getenv("CLICKHOUSE_PASSWORD")), log)
		go telemetry.Run(ctx)
	} else {
		log.Warn("no CLICKHOUSE_HTTP_URL; intents will not reach the analytical plane",
			"consequence", "the fleet engine will measure an empty fleet")
	}

	return &gateway.Pipeline{
		Identity: identityVerifier(),
		Grants:   gateway.StoreGrants{Store: authority.NewStore(pool)},
		Policies: bundles,
		Usage:    usage,
		Reserve:  usage,
		Keys:     identity.NewKeyStore(pool),
		Execution: &execution.Service{
			Broker:          venue,
			Store:           execution.NewPostgresStore(pool),
			CrashAfterVenue: crashPoint(log),
		},
		Controls:  control.NewStore(pool),
		Symbols:   symbols,
		Evidence:  evidence.NewStore(pool),
		Telemetry: telemetry,
		Parent:    gateway.NewParentTracker(intent.DefaultClusterConfig),
	}, creds
}

// openBroker builds the venue adapter. V0 has no real-money path, and both adapters
// refuse a non-paper endpoint themselves (spec section 59).
func openBroker() (broker.Adapter, error) {
	// The platform resolves instrument identity to a venue symbol and puts it on the
	// order (spec section 13), so an adapter never needs this. It used to be a
	// passthrough that returned the canonical instrument id as if it were a ticker,
	// and adapters preferred it over the resolved symbol: every order this gateway
	// sent to Alpaca carried "instr_us_equity_..." where AAPL belonged. Refusing is
	// the honest fallback — an unresolved instrument is an order to stop, not one to
	// send under a guessed name.
	symbolMustComeFromThePlatform := func(string) (string, bool) { return "", false }

	switch os.Getenv("BROKER") {
	case "alpaca":
		return alpaca.New(alpaca.Config{
			BaseURL:   os.Getenv("ALPACA_BASE_URL"),
			KeyID:     os.Getenv("ALPACA_KEY_ID"),
			SecretKey: os.Getenv("ALPACA_SECRET_KEY"),
			SymbolFor: symbolMustComeFromThePlatform,
		})
	case "tradier":
		return tradier.New(tradier.Config{
			BaseURL:   os.Getenv("TRADIER_BASE_URL"),
			Token:     os.Getenv("TRADIER_TOKEN"),
			AccountID: os.Getenv("TRADIER_ACCOUNT_ID"),
			SymbolFor: symbolMustComeFromThePlatform,
		})
	case "fake":
		// A deterministic venue for development and end-to-end runs. It is gated on
		// an explicit environment rather than merely on BROKER, because a production
		// gateway pointed at a fake venue accepts every order and sends none: the
		// enforcement plane would look healthy while nothing it authorized ever
		// reached a market.
		if os.Getenv("ASSURANCE_ENV") != "development" {
			return nil, errors.New(
				"BROKER=fake requires ASSURANCE_ENV=development; a fake venue accepts " +
					"every order and sends none")
		}
		return fakebroker.New(), nil

	default:
		return nil, errors.New("BROKER must name a configured venue adapter")
	}
}

// identityVerifier trusts the SPIRE bundle if one is configured.
//
// Without it an SVID cannot be verified, so callers reach A1 at best. That is a
// degradation the taxonomy already models, and it is reported rather than silently
// treated as attestation.
func identityVerifier() *identity.Verifier {
	path := os.Getenv("SPIFFE_TRUST_BUNDLE")
	if path == "" {
		return &identity.Verifier{}
	}
	pem, err := os.ReadFile(path)
	if err != nil {
		return &identity.Verifier{}
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(pem) {
		return &identity.Verifier{}
	}

	// The workload registry. Without it a verified SVID establishes a workload and no
	// customer, and the submission is refused naming the missing entry: a workload
	// certificate says which workload is calling and nothing about which tenant it
	// acts for.
	workloads, err := identity.ParseWorkloads(os.Getenv("SPIFFE_WORKLOADS"))
	if err != nil {
		workloads = nil
	}

	return &identity.Verifier{
		Bundle:      roots,
		TrustDomain: os.Getenv("SPIFFE_TRUST_DOMAIN"),
		Workloads:   workloads,
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// credentialsFromEnv reads the credential registry and the three privilege lists.
//
// One function rather than a block inside main, so a test can exercise the mapping from
// environment variable to privilege. It is the only place the names are read, and a
// mis-typed name here would silently leave a privilege ungranted — or granted to
// everyone — with nothing failing to compile.
func credentialsFromEnv(log *slog.Logger) *identity.Credentials {
	creds := openCredentials(log)
	if creds == nil {
		return nil
	}
	// Which identities may issue authority. Named separately from the credential itself
	// so the privilege is something an operator granted rather than something every
	// credential happened to have (P-002).
	creds.AllowIssuers(os.Getenv("GATEWAY_GRANT_ISSUERS"))
	// And which may bind a public key to an agent. A separate list, because the two are
	// different powers: an issuer says what an agent may do, a registrar says which key
	// is that agent — and whoever can do the second can act as any agent in the tenant,
	// grants included.
	creds.AllowKeyRegistrars(os.Getenv("GATEWAY_KEY_REGISTRARS"))
	// And which may bootstrap the key that authorizes a policy into force. A third list,
	// because it is the strongest of the three: an activation key decides which bundle
	// enforces, and so what every agent in the tenant may not do. It gates the first key
	// only — a tenant that already holds one extends its own authority with a signed
	// authorization, and no operator credential can mint policy authority for it
	// (INV-009).
	creds.AllowActivationKeyRegistrars(os.Getenv("GATEWAY_ACTIVATION_KEY_REGISTRARS"))
	return creds
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", component)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	creds := credentialsFromEnv(log)
	pipeline, _ := buildPipeline(ctx, log)
	verifier := identityVerifier()

	// The two section 46 endpoints that need only the database, so they are served
	// whether or not a venue and a policy bundle are configured. Revocation in
	// particular must not depend on the submission path being healthy: it is the lever
	// an operator reaches for when it is not.
	var status, list, revoke, issue, applyControl, revokeControl, listControls http.HandlerFunc
	var registerKey, revokeKey http.HandlerFunc
	var registerActivationKey, revokeActivationKey http.HandlerFunc
	if pool := openPool(ctx, log); pool != nil {
		status = gateway.IntentStatusHandler(execution.NewPostgresStore(pool),
			evidence.NewStore(pool), creds, verifier)
		list = gateway.IntentListHandler(evidence.NewStore(pool), creds, verifier, nil)
		revoke = gateway.RevokeGrantHandler(authority.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		issue = gateway.IssueGrantHandler(authority.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		applyControl = gateway.IssueControlHandler(control.NewStore(pool), evidence.NewStore(pool),
			evidence.NewStore(pool), creds, verifier, nil)
		revokeControl = gateway.RevokeControlHandler(control.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		listControls = gateway.ListControlsHandler(control.NewStore(pool), creds, verifier, nil)
		registerKey = gateway.RegisterAgentKeyHandler(identity.NewKeyStore(pool),
			evidence.NewStore(pool), creds, verifier, nil)
		revokeKey = gateway.RevokeAgentKeyHandler(identity.NewKeyStore(pool),
			evidence.NewStore(pool), creds, verifier, nil)
		registerActivationKey = gateway.RegisterActivationKeyHandler(
			policy.NewActivationStore(pool), evidence.NewStore(pool), creds, verifier, nil)
		revokeActivationKey = gateway.RevokeActivationKeyHandler(
			policy.NewActivationStore(pool), evidence.NewStore(pool), creds, verifier, nil)
		log.Info("intent status and grant lifecycle served",
			"routes", "GET /v1/intents, GET /v1/intents/{id}, POST /v1/authority-grants, "+
				"POST /v1/authority-grants/{id}/revoke, GET|POST /v1/controls, "+
				"POST /v1/controls/{id}/revoke, POST /v1/agent-keys, "+
				"POST /v1/agent-keys/revoke, POST /v1/policy-activation-keys, "+
				"POST /v1/policy-activation-keys/revoke")
	}
	// Bounded retention for idempotency records (spec section 19), which the section
	// asks for beside a unique envelope id and deterministic duplicate handling and
	// which nothing implemented: the table grew with every intent ever decided.
	//
	// In this process rather than as a separate job, because this is the process that
	// owns the table and a retention job deployed on its own is one that can be
	// forgotten in an environment and quietly stop bounding anything.
	if pool := openPool(ctx, log); pool != nil && creds != nil {
		sweeper := &execution.Sweeper{
			Store:  execution.NewPostgresStore(pool),
			Every:  time.Duration(envInt("IDEMPOTENCY_SWEEP_MINUTES", 60)) * time.Minute,
			Keep:   time.Duration(envInt("IDEMPOTENCY_RETENTION_DAYS", 30)) * 24 * time.Hour,
			Log:    log,
			Tenant: creds.Tenants,
		}
		log.Info("idempotency retention",
			"keep", sweeper.Keep.String(), "every", sweeper.Every.String(),
			"tenants", len(creds.Tenants()),
			"note", "PENDING records are never pruned; evidence is untouched")
		go sweeper.Run(ctx)

		// And the outbox's own retention, which nothing had.
		//
		// A delivered row is a receipt: the evidence is in evidence_events, which is
		// partitioned by month and has an archive path, while the outbox kept the whole
		// event for ever after publishing it. On the reference workstation it had grown
		// to 4.3 GB of an 8.3 GB database — a second copy of the entire evidence stream,
		// with nothing in the system that would ever remove a row.
		//
		// Unpublished rows are never pruned at any age: those are work still owed.
		outbox := &evidence.OutboxSweeper{
			Store:   evidence.NewStore(pool),
			Every:   time.Duration(envInt("OUTBOX_SWEEP_MINUTES", 60)) * time.Minute,
			Keep:    time.Duration(envInt("OUTBOX_RETENTION_HOURS", 24)) * time.Hour,
			Tenants: creds.Tenants,
			Report: func(deleted int64, err error) {
				switch {
				case err != nil:
					log.Warn("outbox retention", "err", err,
						"consequence", "delivered rows are accumulating")
				case deleted > 0:
					log.Info("outbox retention", "deleted", deleted,
						"note", "delivered rows only; unpublished rows are never pruned")
				}
			},
		}
		log.Info("outbox retention",
			"keep", outbox.Keep.String(), "every", outbox.Every.String(),
			"note", "published rows are receipts; the evidence itself is untouched")
		go outbox.Run(ctx)
	}

	// The event backbone, wired into the running process at last.
	//
	// EnsureStream, Publisher and Consumer have existed since Phase 6 with passing
	// tests and no binary ever constructed them: evidence went straight to PostgreSQL
	// while the documentation called JetStream the backbone. An outside audit read the
	// source rather than the docs and named it — the project's own recurring defect,
	// a component whose tests pass while the producer never calls it.
	//
	// Off the critical path by construction: the publisher drains what is already
	// committed. A bus that is down delays the analytical plane and decides nothing
	// (INV-005).
	// The publisher connects as its own role.
	//
	// It reads across tenants, which is its job and nothing else's: granting that to
	// assurance_app would hand the exemption to every request handler that connects as
	// the same role (migration 0025). No POSTGRES_OUTBOX_DSN means no publisher — the
	// alternative is starting one on the application role, where RLS returns an empty
	// queue and a drained outbox is indistinguishable from a stalled one.
	if pool := openOutboxPool(ctx, log); pool != nil {
		if url := os.Getenv("NATS_URL"); url != "" {
			if conn, err := nats.Connect(url); err != nil {
				log.Warn("no event backbone", "err", err,
					"consequence", "evidence is committed and stays in the outbox until NATS returns")
			} else if js, err := evidence.EnsureStream(ctx, conn); err != nil {
				log.Warn("event stream unavailable", "err", err)
			} else {
				publisher := &evidence.OutboxPublisher{
					Store:     evidence.NewStore(pool),
					Publisher: evidence.NewPublisher(js),
					// The interval is now how long to wait when the queue is empty
					// rather than how often to publish: the publisher drains while a
					// backlog exists. A batch of 100 per one-second tick made the
					// service rate a constant of about 100/s regardless of depth, and
					// evidence arrives an order of magnitude faster than that under
					// load.
					Every: time.Duration(envInt("OUTBOX_INTERVAL_MS", 250)) * time.Millisecond,
					Batch: envInt("OUTBOX_BATCH", 500),
					Owner: envOr("HOSTNAME", "assurance-gateway"),
					Report: func(published, failed int, err error) {
						// The publisher reports and this logs, because INV-013 keeps a
						// logger out of the evidence package: a package that can write
						// to a log is one where somebody eventually writes evidence to
						// a log instead of recording it.
						switch {
						case err != nil:
							log.Warn("outbox could not be read", "err", err,
								"consequence", "committed evidence has not reached the bus yet")
						case failed > 0:
							log.Warn("evidence not published", "published", published,
								"failed", failed,
								"consequence", "the events stay in the outbox and are retried")
						}
					},
				}
				// How many publishers this process runs.
				//
				// One is the tested default and it keeps up with what this build
				// produces. The lease makes several safe — each claims its own rows with
				// SKIP LOCKED — so a deployment that outgrows one publisher raises this
				// rather than depending on there happening to be several gateway
				// replicas. A single supported gateway has a capacity envelope, and it is
				// measured in tests/performance.
				workers := envInt("OUTBOX_WORKERS", 1)
				if workers < 1 {
					workers = 1
				}
				log.Info("event backbone", "url", url, "every", publisher.Every.String(),
					"workers", workers,
					"note", "committed evidence is published from the outbox; nothing on the hot path waits for it")
				for i := range workers {
					worker := *publisher
					if workers > 1 {
						// A distinct owner per worker, so a lease names the publisher
						// that holds it rather than the process.
						worker.Owner = fmt.Sprintf("%s#%d", publisher.Owner, i)
					}
					go worker.Run(ctx)
				}
			}
		} else {
			log.Warn("no NATS_URL; evidence stays in the outbox",
				"consequence", "nothing consumes the event stream, and the outbox grows")
		}
	}

	var submit http.HandlerFunc
	if pipeline != nil {
		submit = gateway.SubmitHandler(pipeline, creds)
		log.Info("submission path served", "route", "POST /v1/intents")
	}

	srv := &http.Server{
		Addr: addr(),
		Handler: newMux(openEvidence(ctx, log), routes{
			submit: submit, status: status, list: list,
			revokeGrant: revoke, issueGrant: issue,
			applyControl: applyControl, revokeControl: revokeControl,
			listControls: listControls,
			registerKey:  registerKey, revokeKey: revokeKey,
			registerActivationKey: registerActivationKey,
			revokeActivationKey:   revokeActivationKey,
		}, creds, verifier),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown failed", "err", err)
	}
	log.Info("stopped")
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}
