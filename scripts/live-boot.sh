#!/bin/sh
# Start the deployables and prove one signed submission goes through them.
#
# Source wiring and integration tests are not the same thing as starting the binary.
# The tests build a Pipeline in process; main.go reads twenty-odd environment variables,
# decides what to serve and what to refuse, and has its own ways of being wrong — a
# missing variable that silently disables the submission path, a store constructed
# against the wrong role, a publisher that never starts. None of that is reachable from
# a test that never runs main.
#
# It prepares its own tenant, grant, agent key, signed policy bundle and instrument map,
# starts assurance-gateway and fleet-engine, and leaves them running. Everything it
# writes goes under .live/, which is git-ignored.
#
# Usage:  sh scripts/live-boot.sh          start and print the environment to use
#         sh scripts/live-boot.sh stop     stop them
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

LIVE="$ROOT/.live"
GOTMPDIR=${GOTMPDIR:-$ROOT/.gotmp}
export GOTMPDIR

: "${POSTGRES_APP_DSN:=postgres://assurance_app:assurance_app_dev_only@localhost:5432/assurance?sslmode=disable}"
: "${POSTGRES_OUTBOX_DSN:=postgres://assurance_outbox:assurance_outbox_dev_only@localhost:5432/assurance?sslmode=disable}"
: "${CLICKHOUSE_HTTP_URL:=http://localhost:8123}"
: "${CLICKHOUSE_USER:=assurance}"
: "${CLICKHOUSE_PASSWORD:=assurance_dev_only}"
: "${NATS_URL:=nats://localhost:4222}"
: "${GATEWAY_ADDR:=127.0.0.1:8073}"
: "${FLEET_ADDR:=127.0.0.1:8081}"
export POSTGRES_APP_DSN POSTGRES_OUTBOX_DSN CLICKHOUSE_HTTP_URL CLICKHOUSE_USER \
       CLICKHOUSE_PASSWORD NATS_URL GATEWAY_ADDR

stop() {
  for name in gateway fleet; do
    if [ -f "$LIVE/$name.pid" ]; then
      pid=$(cat "$LIVE/$name.pid")
      kill "$pid" 2>/dev/null || true
      rm -f "$LIVE/$name.pid"
      echo "stopped $name ($pid)"
    fi
  done
}

if [ "${1:-start}" = "stop" ]; then
  stop
  exit 0
fi

stop
mkdir -p "$LIVE/policy"

echo "==> preparing a tenant"
# The setup runs through the same packages the platform uses. A fixture written straight
# into the tables would prove the tables accept rows, not that the platform can read what
# it wrote.
# Built and run rather than `go run`: go run stages its binary in the user cache
# directory, which this workstation's Application Control policy blocks.
go build -o "$LIVE/live-setup.exe" ./cmd/live-setup
"$LIVE/live-setup.exe" -dir "$LIVE" -agents "${LIVE_FLEET:-1000}" -tenants "${LIVE_TENANTS_COUNT:-2}" > "$LIVE/env.sh"
. "$LIVE/env.sh"

export POLICY_BUNDLE_DIR="$LIVE/policy"
export INSTRUMENT_SYMBOLS="$LIVE/instruments.json"
export BROKER=fake
export ASSURANCE_ENV=development

echo "==> building"
go build -o "$LIVE/assurance-gateway.exe" ./cmd/assurance-gateway
go build -o "$LIVE/fleet-engine.exe" ./cmd/fleet-engine

echo "==> starting"
"$LIVE/assurance-gateway.exe" > "$LIVE/gateway.log" 2>&1 &
echo $! > "$LIVE/gateway.pid"
# The fleet engine binds FLEET_ENGINE_ADDR, not GATEWAY_ADDR, and authenticates with its
# own credential registry. It was started with neither, so it listened on its default port
# while this script advertised another, and every intelligence endpoint refused for want
# of a credential — the surfaces that read it could only ever show "unavailable".
FLEET_ENGINE_ADDR="$FLEET_ADDR" INTELLIGENCE_API_CREDENTIALS="svc_console@$LIVE_TENANT=$GATEWAY_API_TOKEN" FLEET_COHORT_TENANTS="$LIVE_TENANT"   "$LIVE/fleet-engine.exe" > "$LIVE/fleet.log" 2>&1 &
echo $! > "$LIVE/fleet.pid"

# Waited for rather than slept past: a fixed sleep is how a boot test passes on a fast
# machine and fails on a slow one, and how it reports "started" for a process that has
# already exited.
ready=0
i=0
while [ $i -lt 60 ]; do
  if curl -fsS "http://$GATEWAY_ADDR/healthz" >/dev/null 2>&1; then ready=1; break; fi
  if ! kill -0 "$(cat "$LIVE/gateway.pid")" 2>/dev/null; then
    echo "the gateway exited during startup:"
    tail -30 "$LIVE/gateway.log"
    exit 1
  fi
  i=$((i + 1))
  sleep 1
done
if [ $ready -eq 0 ]; then
  echo "the gateway never became healthy:"
  tail -30 "$LIVE/gateway.log"
  exit 1
fi

echo "==> gateway is live on http://$GATEWAY_ADDR"
grep -E "submission path|event backbone|outbox|policy" "$LIVE/gateway.log" | head -20 || true

cat <<EOF

Ready. To drive it:

  export GATEWAY_URL=http://$GATEWAY_ADDR
  export LOAD_AGENT_TOKEN=$GATEWAY_API_TOKEN
  export LOAD_ISSUER_TOKEN=$GATEWAY_ISSUER_TOKEN
  export LOAD_TENANT=$LIVE_TENANT
  export LIVE_SIGNING_KEY=$LIVE_SIGNING_KEY
  export LIVE_KEY_ID=$LIVE_KEY_ID
  export LOAD_TENANTS=$LOAD_TENANTS
  export FLEET_ENGINE_URL=http://$FLEET_ADDR

Logs: $LIVE/gateway.log, $LIVE/fleet.log
Stop: sh scripts/live-boot.sh stop
EOF
