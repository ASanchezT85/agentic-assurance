// Command fleet-engine is the intelligence plane.
//
// It measures closed windows of stored intents and serves the read-only intelligence
// API of spec section 46. It recommends and it never enforces: there is no code here
// that can submit an order or change a customer's policy, which is INV-009 expressed
// as an absence rather than as a check.
//
// The producer runs only when cohorts are configured. Without them the engine serves
// whatever is already stored and measures nothing new, and it says so at startup
// rather than looking like a healthy engine watching an empty fleet.
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

	"agentic-assurance/internal/fleet"
)

const component = "fleet-engine"

func newMux(store fleet.Reader) *http.ServeMux {
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"component": component,
			"phase":     "17",
		})
	}
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", health)

	// The intelligence API of spec section 46. Read-only: the fleet engine must not
	// submit orders or modify customer policy (section 29), and there is no handler
	// here that writes anything.
	(&fleet.API{Store: store}).Routes(mux)

	return mux
}

// openStore connects to ClickHouse if configured.
//
// A missing store is not fatal. The engine must boot and report health without it,
// and the API answers 503 rather than the binary refusing to start (ADR-021).
func openStore(log *slog.Logger) fleet.Reader {
	base := os.Getenv("CLICKHOUSE_HTTP_URL")
	if base == "" {
		log.Warn("no CLICKHOUSE_HTTP_URL; the intelligence API will report unavailable")
		return nil
	}
	user := os.Getenv("CLICKHOUSE_USER")
	if user == "" {
		user = "assurance"
	}
	return fleet.NewSink(strings.TrimRight(base, "/"), user, os.Getenv("CLICKHOUSE_PASSWORD"))
}

// openProducer builds the measurement producer, if cohorts are configured.
//
// FLEET_COHORT_TENANTS is a comma-separated list. Cohort predicates beyond "every
// intent of this tenant" arrive with the API that manages cohorts; a tenant-wide
// cohort is the one measurement that is always meaningful, and inventing predicates
// from an environment variable would be a configuration language nobody asked for.
func openProducer(store fleet.Reader, log *slog.Logger) *fleet.Producer {
	sink, ok := store.(*fleet.Sink)
	if !ok || sink == nil {
		return nil
	}

	var cohorts []fleet.Cohort
	for _, tenant := range strings.Split(os.Getenv("FLEET_COHORT_TENANTS"), ",") {
		if tenant = strings.TrimSpace(tenant); tenant != "" {
			cohorts = append(cohorts, fleet.Cohort{TenantID: tenant})
		}
	}
	if len(cohorts) == 0 {
		log.Warn("no FLEET_COHORT_TENANTS; nothing will be measured",
			"consequence", "the intelligence API serves only what is already stored")
		return nil
	}

	return &fleet.Producer{
		Store:    sink,
		Cohorts:  cohorts,
		Interval: durationOr("FLEET_WINDOW", time.Minute),
		Lag:      durationOr("FLEET_LAG", 15*time.Second),
		Log:      log,
	}
}

func durationOr(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return fallback
}

func addr() string {
	if a := os.Getenv("FLEET_ENGINE_ADDR"); a != "" {
		return a
	}
	return ":8081"
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", component)

	store := openStore(log)
	srv := &http.Server{
		Addr:              addr(),
		Handler:           newMux(store),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if producer := openProducer(store, log); producer != nil {
		log.Info("measuring", "cohorts", len(producer.Cohorts),
			"window", producer.Interval, "lag", producer.Lag)
		go producer.Run(ctx)
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
