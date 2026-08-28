"""Execution Engine.

Fills intents against the simulated market and records what happened. It models the
outcomes spec section 54 enumerates, including the one that matters: a submission
whose outcome the agent never learns.

ADR-013 keeps this separate from broker paper trading. Paper trading exercises a real
venue's order lifecycle; this exercises what a fleet does to a price, and neither is
evidence about the other.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from enum import StrEnum

import numpy as np

from simulator.market.engine import MarketEngine


class Outcome(StrEnum):
    """What became of a submitted intent."""

    FILLED = "FILLED"
    PARTIALLY_FILLED = "PARTIALLY_FILLED"
    REJECTED = "REJECTED"

    # The agent got no answer. Not a failure: an absence of information, which is the
    # state spec section 19 and INV-004 are built around.
    UNKNOWN = "UNKNOWN"

    # Refused before reaching the venue, by the assurance engine.
    DENIED = "DENIED"


@dataclass(frozen=True)
class Intent:
    """One agent's intent, in the shape the twin passes around.

    It is deliberately not the Go AgentExecutionEnvelope. The twin tests fleet
    behaviour, and coupling it to the wire contract would make every envelope change
    a simulator change. What it does carry is the provenance fields, because a
    scenario that cannot express a declared model cannot test concentration.
    """

    intent_id: str
    agent_id: str
    instrument_id: str
    side: str
    quantity: float
    at_step: int
    declared_model: str = ""
    declared_feed: str = ""
    strategy_id: str = ""
    malformed: bool = False


@dataclass(frozen=True)
class Fill:
    intent_id: str
    agent_id: str
    outcome: Outcome
    quantity: float
    price: float
    at_step: int
    reason: str = ""


@dataclass
class ExecutionEngine:
    """Deterministic execution against a simulated market."""

    market: MarketEngine
    rng: np.random.Generator

    # Probability that a submission's response is lost. The agent then does not know
    # whether it executed, which is what scenario S05 turns on.
    response_loss_probability: float = 0.0

    # Probability the venue refuses outright.
    rejection_probability: float = 0.0

    fills: list[Fill] = field(default_factory=list)

    # Intents the venue actually received, keyed by intent id. A retry that reaches
    # the venue twice appears here twice, which is what makes duplicate execution
    # measurable rather than assumed.
    received: list[str] = field(default_factory=list)

    def submit(self, intent: Intent) -> Fill:
        """Send one intent to the simulated venue."""
        if intent.malformed:
            fill = Fill(intent.intent_id, intent.agent_id, Outcome.REJECTED, 0.0,
                        self.market.state.mid, intent.at_step, "malformed intent")
            self.fills.append(fill)
            return fill

        # Counted before any failure: a request that reached the venue happened,
        # whatever the agent learns about it afterwards.
        self.received.append(intent.intent_id)

        if float(self.rng.random()) < self.rejection_probability:
            fill = Fill(intent.intent_id, intent.agent_id, Outcome.REJECTED, 0.0,
                        self.market.state.mid, intent.at_step, "venue rejected")
            self.fills.append(fill)
            return fill

        price, _ = self.market.execute(intent.quantity, intent.side)

        if float(self.rng.random()) < self.response_loss_probability:
            # The order executed. The agent does not know. This is the case that
            # turns one intent into two orders if anything retries blindly.
            fill = Fill(intent.intent_id, intent.agent_id, Outcome.UNKNOWN,
                        intent.quantity, price, intent.at_step,
                        "response lost; the order executed but the agent was not told")
            self.fills.append(fill)
            return fill

        fill = Fill(intent.intent_id, intent.agent_id, Outcome.FILLED,
                    intent.quantity, price, intent.at_step)
        self.fills.append(fill)
        return fill

    def duplicate_receipts(self) -> int:
        """How many intents the venue received more than once.

        A twin that could not answer this could not demonstrate INV-004 at all.
        """
        seen: dict[str, int] = {}
        for intent_id in self.received:
            seen[intent_id] = seen.get(intent_id, 0) + 1
        return sum(count - 1 for count in seen.values() if count > 1)

    def outcome_counts(self) -> dict[str, int]:
        counts: dict[str, int] = {}
        for fill in self.fills:
            counts[fill.outcome.value] = counts.get(fill.outcome.value, 0) + 1
        return counts
