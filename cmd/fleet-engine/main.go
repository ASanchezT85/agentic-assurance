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
	"syscall"
	"time"
)

const component = "fleet-engine"

func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	health := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":    "ok",
			"component": component,
			"phase":     "6",
		})
	}
	mux.HandleFunc("/healthz", health)
	mux.HandleFunc("/readyz", health)
	return mux
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
		Handler:           newMux(),
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
