# EXORYN — demo script

A runnable walkthrough of the platform, about two and a half minutes if narrated. Every
command below is a real command in this repository and was executed end to end while this
document was written.

**No video exists yet.** This is the script for one, not a transcript of one.

Everything runs against a local stack with a synthetic tenant. No real credential is
needed and no real money path exists.

---

## Before you start

```bash
docker compose up -d --wait
sh scripts/migrate.sh
```

Then bring up the two deployables with their own synthetic tenant, grant, agent signing
key, signed policy bundle and instrument map:

```bash
sh scripts/live-boot.sh
```

It prints the environment to use — a gateway URL, a tenant id, an agent token, an issuer
token and a signing key. **Those values are generated per boot and live under `.live/`,
which is git-ignored.** Export them as printed; they are referenced below as
`$GATEWAY_URL`, `$LOAD_AGENT_TOKEN` and so on.

Do not read those tokens aloud on camera.

---

## 0:00 — What EXORYN is

> EXORYN sits between an AI-generated financial intent and execution infrastructure. It
> answers who produced the action, whether that actor holds authority, whether
> deterministic policy permits it, whether it already ran, and what evidence exists —
> before anything reaches a venue.
>
> It is not a trading bot. There is no real-money path in this repository.

Show the repository root and `MASTER_BUILD_SPEC.md`.

## 0:20 — Architecture

Show the diagram in [`../README.md`](../README.md#how-one-intent-moves-through-exoryn) and
name the boundary:

> Everything above the line is PostgreSQL truth and runs locally. Below it, NATS, the
> fleet engine, ClickHouse and the Console are the intelligence plane. A hard financial
> decision never waits on any of them.

The gateway's own startup log states what it decided to serve:

```bash
grep -E "submission path|event backbone|outbox|policy" .live/gateway.log
```

## 0:45 — Submit signed intents

There is no toy submitter in this repository, and inventing one for a demo would be
demonstrating something the product does not have. The real path is the load harness,
which builds and signs genuine envelopes with the agent key and posts them to the running
gateway:

```bash
export GOTMPDIR=$PWD/.gotmp
go test -tags=load -count=1 -timeout 10m \
  -run TestAThousandAgentsSubmitConcurrently ./tests/performance/
```

A thousand agents, each with its own identity, submitting concurrently against one
tenant's authority.

## 1:05 — Show what authority and policy decided

```bash
curl -s -H "Authorization: Bearer $LOAD_AGENT_TOKEN" \
  "$GATEWAY_URL/v1/intents?limit=3" | python -m json.tool
```

Point at one entry and read the `events` array aloud. Each intent carries the events it
produced — `agent.identity.verified.v1`, `authority.evaluated.v1`,
`policy.evaluated.v1`, `authority.reserved.v1`, `broker.order.accepted.v1` — rather than
this endpoint's opinion of them.

Worth saying: the tenant is never taken from the request. Sending a header that names
another tenant is refused, not served.

## 1:25 — Show the evidence chain

Take a `correlation_id` from the previous response and ask for its chain:

```bash
curl -s -H "Authorization: Bearer $LOAD_AGENT_TOKEN" \
  "$GATEWAY_URL/v1/evidence?correlation_id=<paste one>" | python -m json.tool
```

> This is append-only and it is written in the same transaction as the decision it
> records. It is not a log that was reconstructed afterwards.

## 1:45 — Show the fleet and an incident

Start the Console against the running stack. Create `apps/console-web/.env.local` — it is
git-ignored — with the gateway URL, the fleet engine URL and the agent token as
`CONSOLE_API_TOKEN`, then:

```bash
pnpm --filter console-web dev --port 3100
```

Open <http://localhost:3100> and walk three surfaces:

- **Fleet** — measurements per cohort per window, each with its own coverage beside it.
  There is no composite risk score, and the page says why.
- **Dependencies** — a thousand agents resting on one market-data feed, with verified,
  declared and unknown counted separately.
- **Incidents** — the concentration was detected and the badge reads `RECOMMENDED ONLY`:
  *would recommend THROTTLE for this cohort, pending customer policy.*

That last badge is the sentence to land:

> The platform recommends. Only a customer authorization turns a recommendation into a
> control that binds. Intelligence never silently becomes authority.

Then open **Controls**, which is empty, and read the panel titled *"Why there are no
buttons."*

## 2:10 — The failure boundary, and paper-only

Kill the analytics database in front of the camera:

```bash
docker compose stop clickhouse
```

Submit again. It still works: enforcement does not depend on the analytical store. The
Console shows the surface as `UNAVAILABLE` — not as zero.

```bash
docker compose start clickhouse
```

The whole claim is under test rather than under discussion:

```bash
go test -tags=chaos -count=1 ./tests/chaos/...
```

Nine scenarios that stop the real containers, including `TestPostgresOutageFailsClosed`.

Then say the boundary plainly:

> Both broker adapters refuse any endpoint that is not a paper endpoint. There is no
> real-money path in this repository, and the platform never cancels an order already
> working at a venue.

## 2:30 — Close on the engineering

> Fourteen audit passes, each with a different method. They found a release path that had
> never worked, invariants whose tests passed while the code lied, an adapter that
> recorded a filled order as having filled nothing, and a database password riding in a
> URL into three log files.
>
> The lesson that repeated: a method that starts from a document finds only what the
> document mentions. The ones that started from the running system found what the
> documents did not know.

Show [`AUDITS.md`](AUDITS.md) and stop.

---

## Afterwards

```bash
sh scripts/live-boot.sh stop
docker compose down
```

`.live/` and `apps/console-web/.env.local` hold per-run secrets and are git-ignored.
Delete them if the machine is shared.
