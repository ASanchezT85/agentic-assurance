# V0 KEY REGISTRATION: THE TWO ENDPOINTS THAT CLOSED THE LAST DATABASE-ACCESS GAP

**Build:** `5d4ae7b` · **Date:** 2026-08-30 · **Environment:** Windows 11 workstation,
Docker (PostgreSQL 16, ClickHouse 25.3, NATS 2, MinIO), Go 1.25, fake broker.

## STATUS

Open risk 1 of the third remediation report — *"no endpoint registers an agent signing key
or a policy activation key; onboarding either requires database access"* — is closed.
Both endpoints exist, each behind a privilege of its own, and both were exercised against
a real database and against the running binary.

This is new surface, not a fix to audited code. It is submitted for audit rather than
declared accepted. Three defects were found by running rather than by reading, and they
are in §5 with what they cost.

---

## 1. WHY THIS WAS NOT ONE ENDPOINT WITH TWO TABLES

Three powers exist in this platform and the design spends most of its effort keeping them
apart:

| Power | What it asserts | Privilege |
|---|---|---|
| Issue an authority grant | *this agent may trade up to X* | `GATEWAY_GRANT_ISSUERS` |
| Register an agent signing key | *this public key **is** that agent* | `GATEWAY_KEY_REGISTRARS` |
| Register a policy activation key | *this key decides which policy constrains **every** agent* | `GATEWAY_ACTIVATION_KEY_REGISTRARS` |

They are three lists, and holding one confers none of the others. The reasons are
concrete rather than tidy:

- An **issuer** who could also register agent keys would reach every ceiling in the tenant
  through the back door: mint a key for any agent, including one whose grant they never
  issued and could not have widened. INV-002 would be enforced against a limit the party
  under it can move (P-002).
- An **agent-key registrar** who could also register activation keys could decide what
  constrains the agents it onboards. The two are not neighbouring privileges; one is
  bounded by a grant and the other bounds every grant.

A workload-attested caller (A2) carries none of them. An SVID says which workload is
running, not which person authorized it.

---

## 2. AGENT SIGNING KEYS — `POST /v1/agent-keys`

Envelope signatures became mandatory in the second remediation and nothing registered a
key, so every onboarding needed a `psql` session — which is not a product, and which
pushed operators toward a credential far stronger than the task.

**What it refuses, and why each refusal exists:**

| Refusal | Reason |
|---|---|
| A tenant in the request body | The tenant comes from the credential. Letting the body name whose agent this is is the cross-tenant hole in its most direct form (INV-007) |
| Replacing an existing key (409) | Overwriting takes over an agent that already holds authority, and every envelope signed by the previous key stops verifying at the same moment — a takeover and an outage sharing one code path. Rotation is a new key id, then a revocation |
| A 64-byte "public" key | It is an ed25519 *private* key. The refusal names the disclosure rather than reporting a length error: the caller must know they have to generate another pair |
| Any algorithm but ed25519 | A negotiated set is a downgrade attack with extra steps |
| An unknown JSON field | A misspelled validity window would otherwise be dropped and the key registered without the expiry its author wrote down |
| An empty validity window | A key that verifies nothing would look registered |

`KeyStore.Register` now returns `(registered bool, err error)`. It returned only an error,
so a caller whose key id was taken was told "no error" while somebody else's key stayed in
force.

Both acts are evidence events: `agent.signing_key.registered.v1` and
`agent.signing_key.revoked.v1`, carrying who registered it, which API identity authorized
it, and the public key's fingerprint.

---

## 3. POLICY ACTIVATION KEYS — `POST /v1/policy-activation-keys` (ADR-028)

The strongest act the API exposes. An activation key authorizes a bundle into force, and a
bundle says what every agent in the tenant may not do.

The obvious design is wrong. If an operator credential could register activation keys the
way it registers agent keys, the platform's own operator could add a signer to any
customer, sign an activation with it, and enforce a policy the customer never approved —
while every signature in the evidence chain verified perfectly. The signature scheme would
be intact and the authority behind it would be the platform's.

**The rule:** a tenant's **first** activation key is a bootstrap, gated by
`GATEWAY_ACTIVATION_KEY_REGISTRARS`. Every **later** key requires a `KeyAuthorization`
signed by a key the tenant already holds. Which of the two a request is, is not the
caller's choice — the number of active keys decides it. The operator's power is bounded in
time rather than in scope: it exists for a tenant until that tenant has one key, and never
again.

Also enforced:

- **A key may not authorize its own registration.** Self-signing is the bootstrap path
  without the credential that gates the bootstrap.
- **The nonce is a primary key** of `policy_activation_key_grants` (migration 0028). A
  captured authorization presented twice is refused by the database on every replica,
  rather than by whichever process remembered it.
- **One transaction** carries the key row, the grant record and the evidence event. A key
  able to decide which policy enforces cannot become usable through a commit that did not
  also record who granted it.
- **The last active key cannot be revoked.** A tenant with none can never authorize another
  policy change — including the rollback an incident needs — and recovering from that
  needs database access, which is the state this endpoint exists to remove.
- **Revocation is not signed for.** The case that matters is a key believed compromised,
  and requiring that key's cooperation to retire it would be requiring the attacker's.

**Accepted consequence.** A tenant that loses every private key it registered is stuck: no
new key can be signed for, and the bootstrap is spent. This is deliberate — the
alternative is an operator path back in, which is the escalation itself. Registering a
second key held separately is the documented answer, and the last-key rule keeps one
alive.

---

## 4. WHAT WAS RUN

| Suite | Result |
|---|---|
| Quality gate (`scripts/verify.sh`) | passed |
| `tests/security` — 26 new tests across the two endpoints | passed |
| `tests/integration` — full suite, **97 tests + 19 subtests** | passed, 42.7 s |
| Race detector over unit + security + integration | passed |
| Chaos, 8 tests, run alone | passed, 28.4 s |
| Live smoke against the running binary, 8 checks | passed |

Three skips, each stated: two live Alpha Vantage tests that need a key §35 keeps out of
the repository, and the outbox claim test when another publisher has already drained the
queue.

**The central property was verified red.** Allowing an unsigned second activation-key
registration makes `TestAnOperatorCannotMintASecondActivationKey` fail with a 201, and
pointing `AllowActivationKeyRegistrars` at `GATEWAY_KEY_REGISTRARS` fails both wiring
tests naming the crossed privilege.

**Live smoke, across the process boundary** (`scripts/live-smoke-activation-keys.sh`):

```
ok   agent key, registrar: 201
ok   agent key, same id again: 409
ok   agent key, agent token: 403
ok   activation key, bootstrap on a tenant that already has one: 400
ok   activation key, agent-key registrar: 403
ok   activation key, agent token: 403
ok   activation key, unauthenticated: 401
ok   activation key revoke, the tenant's only key: 409
```

The 400 is the one that matters: the tenant `live-setup` provisions already holds an
activation key, so over the API the bootstrap is refused — the property the whole design
is about, checked in the binary and not only in a rig.

---

## 5. DEFECTS FOUND BY RUNNING

**5.1 `ActivationStore.RegisterKey` upserted.** A registration under a key id already taken
silently replaced the public key — substituting the authority that decides which policy
enforces — and reported success. It now refuses and reports. This also exposed a fixture
bug that the upsert had been hiding: two reload authorities in one tenant shared the key id
`reload_key`, the second replaced the first, and whichever was built last was the one that
verified.

**5.2 The outbox capacity test depended on a gateway nobody started.** It drained only
after arrivals stopped, so the measurement said more about what else was running than about
the outbox: with a gateway up the queue stayed shallow, and with none up the same code
reached 100% of arrivals in flight and the test failed. It now runs its own concurrent
publisher — 2.5% peak depth, 178 ms catch-up.

**5.3 A stale listener answered for the gateway.** A gateway from an earlier boot kept
`127.0.0.1:8073` through a run of `live-boot.sh`: the new process logged
`server failed: bind` while the old one answered every request — `/healthz` included, so
the readiness check passed — with the credentials of the tenant *it* had been started for.
Every call came back 401 and the script reported the gateway live. It was live; it was the
wrong one. `stop()` now frees both ports and the start path refuses to continue while one
is held. Comparing pids does not work here: under Git Bash the pid the shell records is not
the pid `netstat` reports, which is why the pid file was blind to it.

---

## 6. WHAT AN AUDIT SHOULD LOOK AT

1. **The bootstrap window.** It is the one moment an operator credential can create policy
   authority. Is "until the tenant has one active key" the right boundary, and is it
   closed in every path — including a tenant whose only key is revoked? (The last-key rule
   is what keeps that from happening; it is one rule holding one invariant.)
2. **`KeyAuthorization` canonicalization.** It reuses the generic canonical form and
   deletes `signature` before signing, the same shape as `Authorization`. Worth checking
   that no field can be added later that changes meaning without changing the signed bytes.
3. **The privilege lists are read once, at startup, from environment variables.** A
   deployment that misspells `GATEWAY_ACTIVATION_KEY_REGISTRARS` grants nothing rather than
   everything, which is the safe direction — but nothing warns.
4. **The evidence for a revocation is best effort**, as on every administrative path: the
   key is revoked whether or not the record commits. Registration is not — it shares the
   transaction. The asymmetry is deliberate and worth a second opinion.
5. **No endpoint lists registered keys.** An operator cannot see what is registered without
   the database. Deliberate for this pass — a read surface for keys is its own decision —
   and it means rotation is done blind.

---

## 7. OPEN RISKS, UNCHANGED FROM THE THIRD REPORT

1. Retention has no scheduler; the archive path exists and running it is a manual act.
2. The `authority_usage` tombstone grows without bound (ADR-027 accepts this for V0).
3. `/v1/simulations` has no field-contract coverage; it needs `SIMULATOR_PYTHON`.
4. Sustained synthetic peak still exceeds one publisher; the lease makes running several
   safe and nothing runs more than one today.
5. No human reviewer has driven the console for semantic UI review.
