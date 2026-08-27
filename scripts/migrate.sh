#!/usr/bin/env sh
# Apply PostgreSQL migrations, in order, into the running dev database.
#
# Migrations run as the superuser because they create roles and policies. The
# application connects as assurance_app, which is deliberately not a superuser:
# PostgreSQL exempts superusers from row level security, so an application running
# as one would make every policy inert.
set -eu

ROOT=$(git rev-parse --show-toplevel 2>/dev/null || pwd)
cd "$ROOT"

for file in $(ls migrations/postgres/*.sql | grep -v '\.down\.sql$' | sort); do
  echo "==> $file"
  docker compose exec -T postgres \
    psql -v ON_ERROR_STOP=1 -U assurance -d assurance -f - < "$file"
done

echo "Migrations applied."
