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
	"agentic-assurance/adapters/tradier"
	"agentic-assurance/internal/authority"
	"agentic-assurance/internal/broker"
	"agentic-assurance/internal/evidence"
	"agentic-assurance/internal/execution"
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

func newMux(reader evidenceReader, submit http.HandlerFunc) *http.ServeMux {
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
	mux.HandleFunc("GET /v1/evidence", evidenceByCorrelation(reader))
	mux.HandleFunc("GET /v1/intents/{id}/evidence", evidenceByIntent(reader))

	// The write path. Absent rather than answering when the enforcement plane is not
	// fully configured: a 404 is honest, and a handler that accepted intents it
	// could not evaluate would be worse than no handler.
	if submit != nil {
		mux.HandleFunc("POST /v1/intents", submit)
	}

	return mux
}

// tenantOf reads the tenant from the request.
//
// Phase 6 takes it from a header. That is not authentication and does not pretend to
// be: the authenticated-tenant requirement of spec section 46 arrives with the API
// surface that carries authentication. Until then these endpoints are reachable only
// inside the customer's own network, and the handler says so rather than implying a
// check it does not perform.
func tenantOf(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get("X-Tenant-Id"))
}

func evidenceByCorrelation(reader evidenceReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantOf(r)
		correlationID := strings.TrimSpace(r.URL.Query().Get("correlation_id"))

		if tenant == "" {
			writeError(w, http.StatusBadRequest, "X-Tenant-Id is required")
			return
		}
		if correlationID == "" {
			writeError(w, http.StatusBadRequest, "correlation_id is required")
			return
		}
		serveChain(w, r, reader, tenant, func(ctx context.Context) ([]evidence.Event, error) {
			return reader.Chain(ctx, tenant, correlationID)
		})
	}
}

func evidenceByIntent(reader evidenceReader) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenant := tenantOf(r)
		intentID := strings.TrimSpace(r.PathValue("id"))

		if tenant == "" {
			writeError(w, http.StatusBadRequest, "X-Tenant-Id is required")
			return
		}
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
func buildPipeline(ctx context.Context, log *slog.Logger) (*gateway.Pipeline, *gateway.Credentials) {
	missing := func(what, why string) (*gateway.Pipeline, *gateway.Credentials) {
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

	creds, err := gateway.ParseCredentials(os.Getenv("GATEWAY_API_CREDENTIALS"))
	if err != nil {
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
		Symbols:  symbols,
		Evidence: evidence.NewStore(pool),
		Parent:   gateway.NewParentTracker(intent.DefaultClusterConfig),
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

	pipeline, creds := buildPipeline(ctx, log)
	var submit http.HandlerFunc
	if pipeline != nil {
		submit = gateway.SubmitHandler(pipeline, creds)
		log.Info("submission path served", "route", "POST /v1/intents")
	}

	srv := &http.Server{
		Addr:              addr(),
		Handler:           newMux(openEvidence(ctx, log), submit),
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
