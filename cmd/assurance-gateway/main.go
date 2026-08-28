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
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
)

const component = "assurance-gateway"

// evidenceReader is the slice of the store the HTTP surface may use. It is read-only
// by construction: there is no Append here, so no request can write evidence
// (ADR-009, INV-006).
type evidenceReader interface {
	Chain(ctx context.Context, tenantID, correlationID string) ([]evidence.Event, error)
	ByAggregate(ctx context.Context, tenantID, aggregateID string) ([]evidence.Event, error)
}

func newMux(reader evidenceReader, submit, status, revoke, issue, applyControl,
	revokeControl, listControls http.HandlerFunc,
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
	if submit != nil {
		mux.HandleFunc("POST /v1/intents", submit)
	}
	if status != nil {
		mux.HandleFunc("GET /v1/intents/{id}", status)
	}
	if revoke != nil {
		mux.HandleFunc("POST /v1/authority-grants/{id}/revoke", revoke)
	}
	if issue != nil {
		mux.HandleFunc("POST /v1/authority-grants", issue)
	}
	if applyControl != nil {
		mux.HandleFunc("POST /v1/controls", applyControl)
	}
	if revokeControl != nil {
		mux.HandleFunc("POST /v1/controls/{id}/revoke", revokeControl)
	}
	if listControls != nil {
		mux.HandleFunc("GET /v1/controls", listControls)
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
	pool, err := openPoolFrom(ctx, dsn)
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
	pool, err := openPoolFrom(ctx, dsn)
	if err != nil {
		log.Error("database unavailable", "err", err)
		return nil
	}
	return pool
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
	pool, err := openPoolFrom(ctx, dsn)
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
		Identity:      identityVerifier(),
		Grants:        gateway.StoreGrants{Store: authority.NewStore(pool)},
		Policies:      bundles,
		Usage:         usage,
		UsageRecorder: usage,
		Execution: &execution.Service{
			Broker: venue,
			Store:  execution.NewPostgresStore(pool),
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
	symbolPassthrough := func(s string) (string, bool) { return s, s != "" }

	switch os.Getenv("BROKER") {
	case "alpaca":
		return alpaca.New(alpaca.Config{
			BaseURL:   os.Getenv("ALPACA_BASE_URL"),
			KeyID:     os.Getenv("ALPACA_KEY_ID"),
			SecretKey: os.Getenv("ALPACA_SECRET_KEY"),
			SymbolFor: symbolPassthrough,
		})
	case "tradier":
		return tradier.New(tradier.Config{
			BaseURL:   os.Getenv("TRADIER_BASE_URL"),
			Token:     os.Getenv("TRADIER_TOKEN"),
			AccountID: os.Getenv("TRADIER_ACCOUNT_ID"),
			SymbolFor: symbolPassthrough,
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

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", component)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	creds := openCredentials(log)
	if creds != nil {
		// Which identities may issue authority. Named separately from the credential
		// itself so the privilege is something an operator granted rather than
		// something every credential happened to have (P-002).
		creds.AllowIssuers(os.Getenv("GATEWAY_GRANT_ISSUERS"))
	}
	pipeline, _ := buildPipeline(ctx, log)
	verifier := identityVerifier()

	// The two section 46 endpoints that need only the database, so they are served
	// whether or not a venue and a policy bundle are configured. Revocation in
	// particular must not depend on the submission path being healthy: it is the lever
	// an operator reaches for when it is not.
	var status, revoke, issue, applyControl, revokeControl, listControls http.HandlerFunc
	if pool := openPool(ctx, log); pool != nil {
		status = gateway.IntentStatusHandler(execution.NewPostgresStore(pool), creds, verifier)
		revoke = gateway.RevokeGrantHandler(authority.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		issue = gateway.IssueGrantHandler(authority.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		applyControl = gateway.IssueControlHandler(control.NewStore(pool), evidence.NewStore(pool),
			evidence.NewStore(pool), creds, verifier, nil)
		revokeControl = gateway.RevokeControlHandler(control.NewStore(pool), evidence.NewStore(pool),
			creds, verifier, nil)
		listControls = gateway.ListControlsHandler(control.NewStore(pool), creds, verifier, nil)
		log.Info("intent status and grant lifecycle served",
			"routes", "GET /v1/intents/{id}, POST /v1/authority-grants, "+
				"POST /v1/authority-grants/{id}/revoke, GET|POST /v1/controls, "+
				"POST /v1/controls/{id}/revoke")
	}
	var submit http.HandlerFunc
	if pipeline != nil {
		submit = gateway.SubmitHandler(pipeline, creds)
		log.Info("submission path served", "route", "POST /v1/intents")
	}

	srv := &http.Server{
		Addr: addr(),
		Handler: newMux(openEvidence(ctx, log), submit, status, revoke, issue, applyControl,
			revokeControl, listControls, creds, verifier),
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

// openPoolFrom connects with a pool sized for a fleet rather than for a laptop.
//
// pgxpool defaults to four connections per CPU. With a thousand agents submitting
// concurrently that is where every submission queues: the same load run measured
// 232 intents/s on the default and 422/s with fifty connections, on identical
// hardware, with the enforcement work unchanged. A DSN that sets pool_max_conns
// itself is left alone — an operator who tuned it means it.
func openPoolFrom(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = int32(envInt("POSTGRES_MAX_CONNS", 50))
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func envInt(key string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(key)); err == nil && v > 0 {
		return v
	}
	return fallback
}
