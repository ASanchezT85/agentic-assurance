#!/usr/bin/env sh
# Print the interpreter the Python toolchain should use.
#
# Order: an explicit $PY, then the project virtualenv, then whatever is on PATH.
# The venv comes first because ambient installs are not reproducible: a global
# `pip install ruff` lands in the user site-packages, which some environments
# (GUI git clients running hooks, for one) do not put on sys.path.
set -eu

if [ -n "${PY:-}" ]; then
  echo "$PY"
  exit 0
fi

root=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
for candidate in "$root/.venv/Scripts/python.exe" "$root/.venv/bin/python"; do
  if [ -x "$candidate" ]; then
    echo "$candidate"
    exit 0
  fi
done

if command -v python3 >/dev/null 2>&1; then
  echo python3
else
  echo python
fi
