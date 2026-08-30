#!/usr/bin/env sh
# Phase 0 quality gate without make, for hosts that do not have it (Windows).
# Equivalent to `make verify`. Keep the two in step.
set -eu

ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
PY=$(sh "$ROOT/scripts/python-bin.sh")

# Build test binaries inside the repository rather than the user cache.
#
# This workstation's Smart App Control evaluates every freshly written executable and
# blocks some of them, and `go test` stages its binaries under the user cache by default —
# so the gate failed with "An Application Control policy has blocked this file" on a
# different package each run. Building here makes it rarer rather than impossible — the
# check is per file and probabilistic, and a repository-local binary is blocked sometimes
# too — so a run that fails this way is worth repeating before it is believed. The process
# harness and scripts/live-boot.sh already build into the repository for the same reason.
#
# Set explicitly in the environment to override.
GOTMPDIR=${GOTMPDIR:-$ROOT/.gotmp}
export GOTMPDIR
mkdir -p "$GOTMPDIR"


# Preflight. A missing tool must say what to run, not raise ImportError three
# steps into the gate.
for module in ruff mypy pytest; do
  if ! "$PY" -c "import $module" >/dev/null 2>&1; then
    echo "verify: $PY cannot import '$module'."
    echo "verify: run 'make bootstrap' (or scripts/bootstrap.sh) to build the project venv."
    exit 1
  fi
done
echo "==> python: $PY"

echo "==> gofmt"
if [ -n "$(gofmt -l .)" ]; then
  echo "gofmt needed:"; gofmt -l .; exit 1
fi

echo "==> go vet"
go vet ./...

echo "==> eslint"
pnpm -C apps/console-web lint

echo "==> ruff"
"$PY" -m ruff check .

echo "==> tsc"
pnpm -C apps/console-web typecheck

echo "==> mypy"
"$PY" -m mypy

echo "==> go test"
go test ./...

echo "==> pytest"
"$PY" -m pytest -q

echo "==> go build"
go build -o bin/ ./cmd/...

echo "==> next build"
pnpm -C apps/console-web build

echo "Quality gate passed."
echo ""
echo "NOT run by this gate, and each needs something it cannot assume:"
echo "  make test-integration   real PostgreSQL, ClickHouse, NATS, Redis and SPIRE"
echo "  make test-chaos         stops those containers, so it runs alone"
echo "  make test-race          a C compiler, so it runs in a container"
echo ""
echo "A gate that says \"passed\" without saying what it skipped is read as covering"
echo "everything. These are where the tenant isolation, the ambiguous-outcome handling"
echo "and every shared mutex are actually checked."
