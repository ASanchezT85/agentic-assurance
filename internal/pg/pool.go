// Package pg opens PostgreSQL pools sized for a fleet rather than for a laptop.
//
// One function, in one place, because it encodes a decision rather than a convenience.
// pgxpool defaults to four connections per CPU, and with a thousand agents submitting
// concurrently that default is where every submission queues: the load run in
// docs/operations measured 232 intents/s on it against 422/s with fifty connections,
// on the same hardware, with the enforcement work unchanged.
//
// It lived in the gateway's main and the fleet engine kept the library default, which
// is the shape of bug this repository keeps finding — something true of a part read as
// true of the whole. A sizing policy that applies to one of two processes is not a
// policy, it is a patch.
package pg

import (
	"context"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DefaultMaxConns is what a process gets when nothing says otherwise.
const DefaultMaxConns = 50

// Open connects with a pool sized by POSTGRES_MAX_CONNS, or DefaultMaxConns.
//
// A DSN that sets pool_max_conns itself is left exactly as written: an operator who
// tuned the connection string meant it, and silently overriding them would make the
// setting they can see lose to the one they cannot.
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(dsn, "pool_max_conns") {
		cfg.MaxConns = int32(maxConns())
	}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func maxConns() int {
	if v, err := strconv.Atoi(os.Getenv("POSTGRES_MAX_CONNS")); err == nil && v > 0 {
		return v
	}
	return DefaultMaxConns
}
