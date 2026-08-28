//go:build integration

package performance

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"agentic-assurance/internal/execution"
)

// A minimal wrapper so the latency measurement talks to the real store rather than
// to a reimplementation of it. Measuring a copy would measure the copy.

type benchStore struct {
	inner *execution.PostgresStore
}

func newBenchStore(pool *pgxpool.Pool) *benchStore {
	return &benchStore{inner: execution.NewPostgresStore(pool)}
}

func (b *benchStore) claim(ctx context.Context, key string) error {
	_, _, err := b.inner.Claim(ctx, execution.Record{
		TenantID:       "tenant_bench",
		IdempotencyKey: key,
		EnvelopeID:     "env_" + key,
		ClientOrderID:  "coid_" + key,
		CreatedAt:      time.Now().UTC(),
	})
	return err
}

// idemPoolForBench returns a pool, or nil when PostgreSQL is not available.
//
// It returns nil rather than failing so a benchmark run without infrastructure skips
// the database measurement and still reports the enforcement one. A performance suite
// that refuses to run at all when one dependency is missing gets run less often.
func idemPoolForBench(t *testing.T) *pgxpool.Pool {
	t.Helper()

	dsn := os.Getenv("POSTGRES_APP_DSN")
	if dsn == "" {
		dsn = "postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil
	}
	t.Cleanup(pool.Close)
	return pool
}
