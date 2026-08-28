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
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/adapters/alpaca"
	"agentic-assurance/adapters/fakebroker"
	"agentic-assurance/adapters/tradier"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
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

func newMux(reader evidenceReader, submit http.HandlerFunc, creds *identity.Credentials) *http.ServeMux {
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
	mux.HandleFunc("GET /v1/evidence", evidenceByCorrelation(reader, creds))
	mux.HandleFunc("GET /v1/intents/{id}/evidence", evidenceByIntent(reader, creds))

	// The write path. Absent rather than answering when the enforcement plane is not
	// fully configured: a 404 is honest, and a handler that accepted intents it
	// could not evaluate would be worse than no handler.
	if submit != nil {
		mux.HandleFunc("POST /v1/intents", submit)
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
func tenantOf(r *http.Request, creds *identity.Credentials) (string, int, string) {
	var certs []*x509.Certificate
	if r.TLS != nil {
		certs = r.TLS.PeerCertificates
	}
	attested := (&identity.Verifier{}).Resolve(
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

func evidenceByCorrelation(reader evidenceReader, creds *identity.Credentials) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := tenantOf(r, creds)
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

func evidenceByIntent(reader evidenceReader, creds *identity.Credentials) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant, status, message := tenantOf(r, creds)
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
	pool, err := pgxpool.New(ctx, dsn)
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
	pool, err := pgxpool.New(ctx, dsn)
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
	return &identity.Verifier{Bundle: roots, TrustDomain: os.Getenv("SPIFFE_TRUST_DOMAIN")}
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
	pipeline, _ := buildPipeline(ctx, log)
	var submit http.HandlerFunc
	if pipeline != nil {
		submit = gateway.SubmitHandler(pipeline, creds)
		log.Info("submission path served", "route", "POST /v1/intents")
	}

	srv := &http.Server{
		Addr:              addr(),
		Handler:           newMux(openEvidence(ctx, log), submit, creds),
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
