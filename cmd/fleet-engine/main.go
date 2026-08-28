// Command fleet-engine is a Phase 0 boot stub.
//
// It exposes health/readiness endpoints only. Identity, authority, policy and
// broker logic arrive in later phases (MASTER_BUILD_SPEC.md section 57).
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
			"phase":     "14",
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

func addr() string {
	if a := os.Getenv("FLEET_ENGINE_ADDR"); a != "" {
		return a
	}
	return ":8081"
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("component", component)

	srv := &http.Server{
		Addr:              addr(),
		Handler:           newMux(openStore(log)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
