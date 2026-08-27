// Command assurance-gateway runs in the customer's infrastructure (ADR-003).
//
// Phase 6 adds the two read-only evidence endpoints of ADR-023. Everything else is
// still health only: the submission path is wired in a later phase, and shipping a
// half-wired POST /v1/intents would be worse than shipping none.
package main

import (
	"context"
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

	"agentic-assurance/internal/evidence"
)

const component = "assurance-gateway"

// evidenceReader is the slice of the store the HTTP surface may use. It is read-only
// by construction: there is no Append here, so no request can write evidence
// (ADR-009, INV-006).
type evidenceReader interface {
	Chain(ctx context.Context, tenantID, correlationID string) ([]evidence.Event, error)
	ByAggregate(ctx context.Context, tenantID, aggregateID string) ([]evidence.Event, error)
}

func newMux(reader evidenceReader) *http.ServeMux {
	mux := http.NewServeMux()

	health := func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":    "ok",
			"component": component,
			"phase":     "6",
		})
	}
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", health)

	// ADR-023. Spec section 66 step 19 requires the chain
	// agent -> intent -> authority -> policy -> broker order -> result to be
	// inspectable, and section 59 forbids the console from being required for it.
	mux.HandleFunc("GET /v1/evidence", evidenceByCorrelation(reader))
	mux.HandleFunc("GET /v1/intents/{id}/evidence", evidenceByIntent(reader))

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

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", component)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{
		Addr:              addr(),
		Handler:           newMux(openEvidence(ctx, log)),
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
