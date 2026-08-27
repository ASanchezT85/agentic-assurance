#!/usr/bin/env sh
# Install every toolchain dependency. Equivalent to `make bootstrap`.
set -eu

ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$ROOT"

echo "==> go modules"
go mod download

echo "==> node modules"
pnpm install --frozen-lockfile

# A project-local venv, so the gate does not depend on which python happens to be
# on PATH or on whether the caller's environment exposes the user site-packages.
if [ ! -d .venv ]; then
  echo "==> creating .venv"
  BASE=${PY:-python}
  command -v python3 >/dev/null 2>&1 && [ -z "${PY:-}" ] && BASE=python3
  "$BASE" -m venv .venv
fi

PY=$(sh "$ROOT/scripts/python-bin.sh")
echo "==> python dev tools into $PY"
"$PY" -m pip install --quiet --upgrade pip
"$PY" -m pip install --quiet pytest ruff mypy

echo "Bootstrap complete. Enable the pre-push gate with:"
echo "  git config core.hooksPath scripts/githooks"
