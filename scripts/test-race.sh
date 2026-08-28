#!/usr/bin/env sh
# The race detector.
#
# -race needs cgo, and a Windows development environment without a C compiler cannot
# run it at all. That is why it had never run on this repository: not a decision, an
# absence nobody had priced. This project already requires Docker, so the detector runs
# where a compiler exists.
#
# Nothing is excluded. An earlier version skipped internal/simulation, because its tests
# execute the project's Python interpreter and a .venv built on Windows cannot run in a
# Linux container. That removed from the detector the one package with two goroutines, a
# mutex and an atomic — the cancellation path, the watchdog and the in-flight map — for
# a reason that had nothing to do with concurrency. The image carries a usable
# interpreter instead.
#
# The module and build caches live in named volumes, so a second run does not download
# thirteen modules and recompile the world. The first run pays for both.
#
# With INTEGRATION=1 it also runs the integration suite against services on the host,
# which is where the concurrent idempotency claims, the cross-replica cancellation and
# the watchdog actually run. Those need `make up && make migrate` first.
set -eu

IMAGE="agentic-assurance-race"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
# Git Bash reports a POSIX path that the Docker daemon cannot resolve. -W gives the
# Windows one when the shell has it, and the POSIX path is correct everywhere else.
if command -v cygpath >/dev/null 2>&1; then
  ROOT="$(cygpath -w "$ROOT")"
fi
HOST="host.docker.internal"

echo "==> building the race image"
MSYS_NO_PATHCONV=1 docker build -q -t "$IMAGE" -f "$ROOT/scripts/race.Dockerfile" "$ROOT" >/dev/null

run() {
  # MSYS_NO_PATHCONV keeps Git Bash from rewriting the container paths into Windows ones.
  MSYS_NO_PATHCONV=1 docker run --rm \
    --add-host="$HOST:host-gateway" \
    -v "$ROOT:/src" -w /src     -v agentic-assurance-gomod:/tmp/gomod     -v agentic-assurance-gocache:/tmp/gocache \
    -e SIMULATOR_REPO=/src \
    -e POSTGRES_APP_DSN="postgres://assurance_app:assurance_app_dev_only@$HOST:5432/assurance?sslmode=disable" \
    -e CLICKHOUSE_HTTP_URL="http://$HOST:8123" \
    -e CLICKHOUSE_USER=assurance \
    -e CLICKHOUSE_PASSWORD=assurance_dev_only \
    -e POSTGRES_HOST="$HOST" \
    -e CLICKHOUSE_HOST="$HOST" \
    -e REDIS_HOST="$HOST" \
    -e NATS_HOST="$HOST" \
    -e NATS_URL="nats://$HOST:4222" \
    "$IMAGE" "$@"
}

echo "==> race detector"
run go test -race ./...

if [ "${INTEGRATION:-0}" = "1" ]; then
  echo "==> race detector, integration suite (needs make up && make migrate)"
  run go test -race -tags=integration -count=1 ./tests/integration/...
fi

echo "Race detector passed."
