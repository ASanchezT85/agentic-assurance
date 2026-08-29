# Evidence retention: a decision brief

What the regulation actually requires, what it costs, what changes in the platform, and
the four questions someone has to answer. Researched 2026-08-29.

**This is regulatory information, not legal advice.** Which rules bind you depends on
the entity and the jurisdiction, and that determination belongs to your compliance
function. What is engineering — the tiers, the costs, the code — is stated separately
from what is legal.

---

## 1. The finding that changes the design

The SEC amended Rule 17a-4 in 2022, effective January 2023, and added an **audit-trail
alternative to WORM storage**. A broker-dealer's electronic recordkeeping system may now
satisfy the rule either the old way — storage that physically prevents alteration — or
by maintaining

> "a complete time-stamped audit trail that includes: all modifications to and deletions
> of a record or any part thereof; the date and time of operator entries and actions
> that create, modify, or delete the record; the individual(s) creating, modifying, or
> deleting the record"

with enough information to **recreate the original record and its interim iterations**.

That is a description of what this platform already builds. Evidence is append-only, a
correction is a new event referencing the one it supersedes (ADR-009, INV-006), every
event carries `occurred_at`, `produced_at`, a producer and a sequence, and the chain is
returned exactly as stored with nothing merged away. The amendment was written for
"dynamic systems updated constantly" rather than firms bolting on separate WORM
infrastructure — which is the shape we have.

**Consequence for the decision: immutable storage is no longer the gating requirement.**
The archive still benefits from object-lock immutability, and it is free to switch on,
but the platform does not have to be rebuilt around WORM to be compliant-shaped.

**Consequence for the design, and this one is a real change:** the audit trail must
cover *deletions*. Today evidence is never deleted, so the requirement is satisfied
trivially. The moment retention deletes anything, **the deletion itself must be
evidenced** — what was removed, when, on whose authority, and how to recreate what the
record said. A retention job that quietly drops a partition would take a compliant
system and make it non-compliant. That is now a design constraint on the work, not a
nicety.

---

## 2. What the periods actually are

| Regime | Period | Accessibility |
|---|---|---|
| FINRA Rule 4511(b) | **6 years** default, for records with no other specified period | Per 17a-4 format requirements |
| SEC Rule 17a-4 | 6 years for blotters, ledgers, customer account records; **3 years** for several categories including communications | **First 2 years in an easily accessible place** |
| MiFID II Art. 16(7) | **5 years**, extendable to **7** at a competent authority's request | Durable medium, unalterable, searchable, available to client or authority on request |
| MiFID II RTS 6 (algorithmic trading) | **5 years** | Per-order records including internal timestamps, sequences, unique internal identifiers |

Two observations that matter more than the numbers:

**"Easily accessible" is a tier, not a synonym for online.** The first two years must be
retrievable without an unreasonable delay. Glacier Deep Archive, at 12 to 48 hours to
restore, does not obviously qualify for that tier; standard or infrequent-access object
storage with an index does. This is why the naive "90 days hot, everything else in
cheap cold storage" plan is wrong — it puts year one into a tier that takes two days to
read.

**RTS 6 is the closest existing rule to what this platform records.** Per-order internal
timestamps, sequences and unique internal identifiers are exactly the envelope,
correlation and sequence fields the evidence chain already carries.

---

## 3. What FINRA now expects of autonomous agents

FINRA's **2026 Regulatory Oversight Report** added a section on generative AI covering
governance, recordkeeping and autonomous agents, and it names the risks in language that
maps onto this platform's reason for existing:

- **Scope and authority** — "agents may act beyond the user's actual or intended scope
  and authority." That is INV-002 and the authority grant.
- **Autonomy** — agents "acting autonomously without human validation and approval."
  That is the fleet control and `REQUIRE_APPROVAL`.
- **Auditability** — "complicated, multi-step agent reasoning tasks can make outcomes
  difficult to trace or explain." That is the evidence chain.

And the recordkeeping expectation is explicit: **outputs alone are insufficient; firms
must preserve the underlying telemetry** — "intermediate tool calls, data fetches, and
decision pathways" — and those logs are treated as regulatory records subject to 17a-4
retention. Once a system can *take action* rather than generate content, it comes inside
the Rule 3110/3120 supervisory framework.

**Consequence for scope:** the retention question is not only "how long" but "how much".
The envelope already carries declared dependencies (the agent's data fetches) and
runtime claims (which model), with explicit provenance confidence (INV-008, P-003). Two
things worth deciding alongside the period: whether we retain the **full envelope body**
rather than the decision summary, and whether the customer is expected to feed us their
agent's intermediate steps or keep those themselves.

---

## 4. What it costs

Measured on this repository: **745 bytes per event, ~5 KB per intention**, uncompressed
in PostgreSQL.

| Daily volume | Per year in Postgres | Per year compressed in object storage |
|---|---|---|
| 100,000 intents | ~190 GB | ~20–40 GB |
| 1,000,000 intents | ~1.9 TB | ~200–400 GB |
| 10,000,000 intents | ~19 TB | ~2–4 TB |

At 2026 list prices, S3 Glacier Deep Archive is about **$1 per TB per month**, and
**S3 Object Lock costs nothing extra** — its compliance mode has been assessed by
Cohasset Associates against SEC 17a-4, CFTC and FINRA requirements, which is the
assessment regulators expect to see.

So seven years of a million intents a day is roughly **1.4–2.8 TB archived**, on the
order of **a few dollars a month**. Storage cost is not a reason to keep less.

The costs that are real:

- **Retrieval from deep archive**: $0.0025–0.02 per GB, and a 180-day minimum storage
  charge. Restoring a year to answer a regulator is a planned expense, not a surprise —
  but it is why the "easily accessible" tier must not be Deep Archive.
- **The hot table**: 683 MB and four indexes today. Every index makes the insert on the
  enforcement path slower, and that path already spends most of its time writing.

---

## 5. The recommendation

Three tiers, and the middle one exists because of the accessibility rule rather than
because of cost:

| Tier | Window | Where | Why |
|---|---|---|---|
| **Hot** | 90 days | PostgreSQL, partitioned by month | Disputes, reconciliation and investigations happen here. Keeps the table the hot path writes to small |
| **Accessible** | to 2 years | Object storage, standard/IA, indexed, retrievable in minutes | 17a-4's "easily accessible" tier for the first two years |
| **Archived** | to 7 years | Object storage, deep archive, Object Lock in compliance mode | Covers the maximum of FINRA 6, MiFID 7-on-request, without revisiting the decision if the jurisdiction changes |

**Deletion after seven years: allowed, but never automatic.** It requires a named
approver, and the deletion is itself an evidence event naming what was removed and on
whose authority — which is what the audit-trail alternative demands and what a silent
partition drop would break.

**Legal hold overrides everything.** Any tenant, correlation id or window can be pinned,
and a pinned record is not deleted by any tier transition. A retention system without a
hold is one that destroys the records of the dispute that made someone ask for them.

Why 90 days rather than 30 or a year: it is long enough that no ordinary investigation
touches the archive and short enough that the write path's table stays small. It is the
one number here that is engineering rather than law, and it is cheap to change — the
tier is a partition boundary.

---

## 6. What this means for the product, beyond storage

Two consequences worth deciding on deliberately, because they are commercial rather than
technical:

**We may be a "third party with access to records".** If a customer treats this
platform's evidence as its books and records, the amended rule requires third parties
holding those records to file an **undertaking to furnish them to the SEC, SROs and
state regulators** — or the firm designates a senior executive officer who can access
the records directly, which the 2022 amendments added specifically because cloud
providers would not sign the classic undertaking. Either the platform is deployed in the
customer's own infrastructure so they hold their own records, or we are prepared to sign
that undertaking. This is a deployment-model decision, not a storage one.

**The evidence chain is a feature, not overhead.** A regulator asking a firm to explain
what an autonomous agent did, under what authority, and why an order was or was not sent
is asking for exactly the artefact this platform produces by construction. The 2022
audit-trail alternative and the 2026 report on agent telemetry both moved toward what
was already built here.

---

## 7. What I need answered

1. **Regulated entity and jurisdiction?** US broker-dealer, EU investment firm, both, or
   not-yet-regulated product. This picks 6 vs 7 years and whether RTS 6's per-order
   fields are mandatory rather than nice to have.
2. **Deployment model:** does the customer hold their own evidence, or do we?
   That decides the undertaking question.
3. **Hot window:** 90 days unless someone has a reason.
4. **Deletion at the end:** allowed with a named approver, or never?

Until they are answered the working default is **90 days hot / 2 years accessible /
7 years archived / no automatic deletion**, recorded as an assumption rather than
applied silently.

---

## 8. What can be built before any of that is answered

None of this depends on the numbers:

1. **Partition `evidence_events` by month.** A tier transition becomes a `DETACH`
   instead of a mass delete, and the hot table's indexes stay small. This is the
   prerequisite for every other step and it is worth doing regardless.
2. **A verifiable export.** One month written out with a hash chain, so an archived
   month can prove it was not edited after the fact — the property that makes the
   audit-trail alternative hold outside the database.
3. **Deletion as an evidence event.** A new catalog entry so that when a tier transition
   eventually removes something, the record of the removal names what went, when, and on
   whose authority. Building this before deletion exists is the point: it is the thing
   that must never be added afterwards.
4. **Legal hold.** A pin that survives every transition.
5. **Per-tenant growth measurement**, so the conversation with compliance uses your
   traffic rather than the numbers in section 4.

---

## Sources

- [SEC adopts amendments to electronic recordkeeping requirements (Dechert)](https://www.dechert.com/knowledge/onpoint/2022/11/-sec-adopts-amendments-to-rule-17a-4-electronic-recordkeeping-re.html)
- [SEC.gov — Amendments to electronic recordkeeping requirements for broker-dealers](https://www.sec.gov/investment/amendments-electronic-recordkeeping-requirements-broker-dealers)
- [FINRA — Rule 17a-4 amendments, chart of significant changes](https://www.finra.org/sites/default/files/2022-12/rule-17a-4-amendments.pdf)
- [FINRA Rule 4511 — General requirements](https://www.finra.org/rules-guidance/rulebooks/finra-rules/4511)
- [FINRA — Books and records key topic](https://www.finra.org/rules-guidance/key-topics/books-records)
- [FINRA publishes 2026 Regulatory Oversight Report](https://www.finra.org/media-center/newsreleases/2025/finra-publishes-2026-regulatory-oversight-report-empower-member-firm)
- [FINRA's 2026 report signals a supervisory reckoning for autonomous AI (Snell & Wilmer)](https://www.swlaw.com/publication/finras-2026-oversight-report-signals-a-supervisory-reckoning-for-autonomous-ai/)
- [FINRA's 2026 annual report: new focus on AI and cybersecurity (McGuireWoods)](https://www.mcguirewoods.com/client-resources/alerts/2025/12/finras-2026-annual-regulatory-oversight-report-same-priorities-new-focus-on-ai-and-cybersecurity/)
- [MiFID II recordkeeping and Article 16(7) (Smarsh)](https://www.smarsh.com/regulations/markets-financial-instruments-directive-MIFIDII)
- [ESMA — Supervisory briefing on algorithmic trading in the EU](https://www.esma.europa.eu/sites/default/files/2026-02/ESMA74-1505669079-10311_Supervisory_Briefing_on_Algorithmic_Trading_in_the_EU.pdf)
- [Amazon S3 Object Lock — user guide](https://docs.aws.amazon.com/AmazonS3/latest/userguide/object-lock.html)
- [S3 Glacier and Deep Archive pricing](https://www.usage.ai/blogs/aws/storage-cost/glacier-deep-archive-pricing/)
