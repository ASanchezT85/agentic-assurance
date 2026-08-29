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

echo "==> ClickHouse"
# ClickHouse has no psql, and its HTTP interface takes one statement per request.
# The migrations are therefore one statement per file, applied in filename order.
CH_URL=${CLICKHOUSE_HTTP_URL:-http://localhost:8123}
CH_USER=${CLICKHOUSE_USER:-assurance}
CH_PASS=${CLICKHOUSE_PASSWORD:-assurance_dev_only}

for file in $(ls migrations/clickhouse/*.sql 2>/dev/null | sort); do
  echo "==> $file"
  curl -sS -f --data-binary "@$file" "$CH_URL/?user=$CH_USER&password=$CH_PASS" >/dev/null
done

# The evidence archive bucket.
#
# Created here rather than by the exporter: where a customer's evidence is archived, and
# under what lifecycle and object-lock settings, is their decision, and a process that
# makes its own bucket makes one with defaults. This is the local one the retention
# tests archive into, and it is skipped when MinIO is not running.
BUCKET=${OBJECT_STORE_BUCKET:-assurance-evidence}
if docker compose ps --status running --services 2>/dev/null | grep -qx minio; then
  echo "==> object store bucket $BUCKET"
  docker compose exec -T minio sh -c "
    mc alias set local http://127.0.0.1:9000 \"\$MINIO_ROOT_USER\" \"\$MINIO_ROOT_PASSWORD\" >/dev/null &&
    mc mb --ignore-existing local/$BUCKET >/dev/null" ||     echo "    (bucket not created; the retention archive tests will skip)"
fi

echo "Migrations applied."
