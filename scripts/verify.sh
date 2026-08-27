#!/usr/bin/env sh
# Phase 0 quality gate without make, for hosts that do not have it (Windows).
# Equivalent to `make verify`. Keep the two in step.
set -eu

PY="${PY:-python}"

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

echo "Phase 0 quality gate passed."
