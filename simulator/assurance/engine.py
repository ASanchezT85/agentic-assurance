"""Assurance Engine.

The twin's stand-in for the enforcement plane.

# What this is not

It is **not** the production policy engine. That lives in Go, in internal/policy and
internal/authority, and it is the only thing that decides anything real. This module
applies a deliberately small set of deterministic limits so that a scenario can
observe what changes when enforcement is present.

Keeping the two separate is a choice with a cost, and the cost is stated here rather
than discovered later: **these rules can drift from the Go ones**, and a scenario that
passes here proves nothing about production policy. What the twin is for, per
ADR-013, is fleet behaviour and market-risk scenarios. Testing that the real policy
engine reaches the right verdicts is what internal/policy's own tests do.

Wiring the Go engine into the Python twin would close the gap and needs a
cross-language boundary nobody has specified. It is not in Phase 11's scope, and
pretending this module is equivalent would be worse than saying so.
"""

from __future__ import annotations

from dataclasses import dataclass, field

from simulator.execution.engine import Intent


@dataclass(frozen=True)
class Limits:
    """The subset of authority limits the twin enforces.

    Per-order notional only. It is unambiguous, it needs no state, and it is enough
    to make the difference between "enforcement on" and "enforcement off" visible in
    a scenario. Rolling and daily limits need consumed usage, which would mean
    reimplementing the authority store here too.
    """

    per_order_notional: float = 0.0

    # Instruments the population may not touch. Enough to express a blanket
    # prohibition without a rule language.
    denied_instruments: tuple[str, ...] = ()


@dataclass
class Decision:
    intent_id: str
    allowed: bool
    code: str
    reason: str


@dataclass
class AssuranceEngine:
    """Deterministic gate in front of the execution engine."""

    limits: Limits
    reference_price: float

    # When enabled is False every intent passes. Scenario S10 needs to compare a run
    # with enforcement against the same run without it, and the comparison is only
    # meaningful if the two differ in exactly this flag.
    enabled: bool = True

    decisions: list[Decision] = field(default_factory=list)

    def evaluate(self, intent: Intent) -> Decision:
        decision = self._evaluate(intent)
        self.decisions.append(decision)
        return decision

    def _evaluate(self, intent: Intent) -> Decision:
        if not self.enabled:
            return Decision(intent.intent_id, True, "ENFORCEMENT_DISABLED",
                            "the assurance engine is switched off for this run")

        if intent.malformed:
            return Decision(intent.intent_id, False, "INVALID_INTENT",
                            "the intent is malformed")

        if intent.instrument_id in self.limits.denied_instruments:
            return Decision(intent.intent_id, False, "INSTRUMENT_DENIED",
                            "the instrument is on the denied list")

        if self.limits.per_order_notional > 0:
            notional = intent.quantity * self.reference_price
            if notional > self.limits.per_order_notional:
                return Decision(
                    intent.intent_id, False, "PER_ORDER_LIMIT_EXCEEDED",
                    f"notional {notional:.2f} exceeds the per-order limit "
                    f"{self.limits.per_order_notional:.2f}",
                )

        return Decision(intent.intent_id, True, "ALLOWED", "within the configured limits")

    def denied_count(self) -> int:
        return sum(1 for d in self.decisions if not d.allowed)

    def denial_codes(self) -> dict[str, int]:
        counts: dict[str, int] = {}
        for d in self.decisions:
            if not d.allowed:
                counts[d.code] = counts.get(d.code, 0) + 1
        return counts
