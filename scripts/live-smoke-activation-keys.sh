#!/usr/bin/env bash
# Smoke test for the two key-registration endpoints, against the running binary.
#
# What an integration test cannot say: that main.go wires these routes to these
# privileges, from these environment variables. A handler proved correct in a rig is
# still a handler nobody reached.
#
# It waits for the freshly built binaries to be runnable: this workstation's Application
# Control policy blocks a newly written executable until it has been evaluated, which can
# take a while and has nothing to do with the code.
set -eu
cd "$(dirname "$0")/.."

deadline=$(( $(date +%s) + ${SMOKE_WAIT_SECONDS:-5400} ))
until ./.live/live-setup.exe -h >/dev/null 2>&1 || ./.live/live-setup.exe -h 2>&1 | grep -qi "usage of"; do
  if [ "$(date +%s)" -gt "$deadline" ]; then
    echo "the built binaries are still blocked by Application Control; nothing was run" >&2
    exit 2
  fi
  sleep 60
done

echo "==> booting"
bash scripts/live-boot.sh >/dev/null
. .live/env.sh

gw=${GATEWAY_URL:-http://127.0.0.1:8073}
pub() { python -c "import os,binascii;print(binascii.hexlify(os.urandom(32)).decode())"; }

post() { # path token body -> status
  curl -sS -o /tmp/smoke-body.json -w "%{http_code}" -X POST "$gw$1" \
    -H "Authorization: Bearer $2" -H "Content-Type: application/json" -d "$3"
}

fail=0
check() { # label expected actual
  if [ "$2" = "$3" ]; then
    echo "ok   $1: $3"
  else
    echo "FAIL $1: expected $2, got $3 -- $(head -c 200 /tmp/smoke-body.json)"
    fail=1
  fi
}

AK=$(pub)
check "agent key, registrar" 201 \
  "$(post /v1/agent-keys "$GATEWAY_REGISTRAR_TOKEN" \
     "{\"agent_id\":\"agent_smoke\",\"key_id\":\"key_smoke\",\"public_key\":\"$AK\",\"registered_by\":\"ops@example.test\"}")"
check "agent key, same id again" 409 \
  "$(post /v1/agent-keys "$GATEWAY_REGISTRAR_TOKEN" \
     "{\"agent_id\":\"agent_smoke\",\"key_id\":\"key_smoke\",\"public_key\":\"$AK\",\"registered_by\":\"ops@example.test\"}")"
check "agent key, agent token" 403 \
  "$(post /v1/agent-keys "$GATEWAY_API_TOKEN" \
     "{\"agent_id\":\"agent_x\",\"key_id\":\"k\",\"public_key\":\"$AK\",\"registered_by\":\"x\"}")"

# The activation key. live-setup registers one for the tenant it provisions, so this
# tenant already holds policy authority: the bootstrap must be refused, which is the
# property the whole design is about.
PK=$(pub)
check "activation key, bootstrap on a tenant that already has one" 400 \
  "$(post /v1/policy-activation-keys "$GATEWAY_POLICY_TOKEN" \
     "{\"key_id\":\"act_smoke\",\"public_key\":\"$PK\",\"holder\":\"risk@example.test\",\"actor\":\"ops@example.test\"}")"
check "activation key, agent-key registrar" 403 \
  "$(post /v1/policy-activation-keys "$GATEWAY_REGISTRAR_TOKEN" \
     "{\"key_id\":\"act_smoke\",\"public_key\":\"$PK\",\"holder\":\"h\",\"actor\":\"a\"}")"
check "activation key, agent token" 403 \
  "$(post /v1/policy-activation-keys "$GATEWAY_API_TOKEN" \
     "{\"key_id\":\"act_smoke\",\"public_key\":\"$PK\",\"holder\":\"h\",\"actor\":\"a\"}")"
check "activation key, unauthenticated" 401 \
  "$(post /v1/policy-activation-keys "" \
     "{\"key_id\":\"act_smoke\",\"public_key\":\"$PK\",\"holder\":\"h\",\"actor\":\"a\"}")"
check "activation key revoke, the tenant's only key" 409 \
  "$(post /v1/policy-activation-keys/revoke "$GATEWAY_POLICY_TOKEN" \
     "{\"key_id\":\"act_live\",\"revoked_by\":\"security@example.test\"}")"

exit $fail
