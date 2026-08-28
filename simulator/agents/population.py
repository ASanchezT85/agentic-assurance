"""Agent Population Model (spec section 39).

Synthetic agents must be reproducible: the same archetype definition and the same
seed produce the same population, with the same capital, the same latencies and the
same panic decisions, on any machine. Everything drawn here comes from an injected
numpy Generator, and nothing reads the wall clock or a global random state.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import asdict, dataclass, field
from typing import Any

import numpy as np


@dataclass(frozen=True)
class Archetype:
    """A population definition (spec section 39).

    Declared dependencies are declarations, not facts. They flow into the intents the
    population emits as DECLARED provenance, which is what lets a scenario like S02
    exercise dependency concentration honestly: the simulator knows the truth, and
    the platform under test only ever sees the claim.
    """

    name: str
    population: int

    capital_median: float = 100_000.0
    capital_sigma: float = 0.5

    latency_median_ms: float = 1_800.0
    latency_sigma: float = 0.4

    declared_model: str = ""
    declared_feed: str = ""
    strategy_id: str = ""

    max_position_pct: float = 0.08
    panic_probability: float = 0.0

    # Fraction of agents that will retry a submission whose outcome they did not
    # learn. Drives scenario S05.
    retry_probability: float = 0.0

    # How strongly an agent reacts to a price it believes is current but is not.
    # Drives scenario S03.
    stale_data_sensitivity: float = 0.0

    # Fraction of intents that are malformed. A population where everything is
    # well-formed tests a world that does not exist.
    error_probability: float = 0.0

    def definition_hash(self) -> str:
        """A hash of the definition, for the experiment record (spec section 40).

        Sorted keys and a fixed separator, so the same definition hashes identically
        between processes and releases. A record whose population hash drifts cannot
        prove two runs used the same population.
        """
        payload = json.dumps(asdict(self), sort_keys=True, separators=(",", ":"))
        return hashlib.sha256(payload.encode("utf-8")).hexdigest()[:32]


@dataclass(frozen=True)
class Agent:
    """One synthetic agent."""

    agent_id: str
    archetype: str
    capital: float
    latency_ms: float
    declared_model: str
    declared_feed: str
    strategy_id: str
    max_position_pct: float
    will_panic: bool
    will_retry: bool
    will_error: bool
    stale_data_sensitivity: float


@dataclass
class AgentPopulation:
    """A reproducible population.

    Draws happen once, at construction, in a fixed order. Drawing lazily per agent
    would make the population depend on the order scenarios happen to ask for agents,
    which is the sort of thing that reproduces on one machine and not another.
    """

    agents: list[Agent] = field(default_factory=list)
    definition_hashes: dict[str, str] = field(default_factory=dict)

    @classmethod
    def generate(cls, archetypes: list[Archetype], rng: np.random.Generator) -> AgentPopulation:
        population = cls()

        # Archetypes are processed in the order given, and agents are numbered
        # within each. Sorting here would silently change every existing experiment's
        # results, so the caller's order is the order.
        for archetype in archetypes:
            population.definition_hashes[archetype.name] = archetype.definition_hash()

            capital = rng.lognormal(
                mean=float(np.log(archetype.capital_median)),
                sigma=archetype.capital_sigma,
                size=archetype.population,
            )
            latency = rng.lognormal(
                mean=float(np.log(archetype.latency_median_ms)),
                sigma=archetype.latency_sigma,
                size=archetype.population,
            )
            panic = rng.random(archetype.population) < archetype.panic_probability
            retry = rng.random(archetype.population) < archetype.retry_probability
            error = rng.random(archetype.population) < archetype.error_probability

            for i in range(archetype.population):
                population.agents.append(
                    Agent(
                        agent_id=f"agent_{archetype.name}_{i:05d}",
                        archetype=archetype.name,
                        capital=float(capital[i]),
                        latency_ms=float(latency[i]),
                        declared_model=archetype.declared_model,
                        declared_feed=archetype.declared_feed,
                        strategy_id=archetype.strategy_id,
                        max_position_pct=archetype.max_position_pct,
                        will_panic=bool(panic[i]),
                        will_retry=bool(retry[i]),
                        will_error=bool(error[i]),
                        stale_data_sensitivity=archetype.stale_data_sensitivity,
                    )
                )
        return population

    def __len__(self) -> int:
        return len(self.agents)

    def by_archetype(self, name: str) -> list[Agent]:
        return [a for a in self.agents if a.archetype == name]

    def fingerprint(self) -> str:
        """A hash of the realised population, not just its definition.

        The definition hash proves two runs asked for the same population. This
        proves they got it. The two differ when a generator is advanced somewhere
        unexpected, which is the failure mode a determinism guarantee actually has.
        """
        digest = hashlib.sha256()
        for agent in self.agents:
            digest.update(
                f"{agent.agent_id}|{agent.capital:.10f}|{agent.latency_ms:.10f}|"
                f"{agent.will_panic}|{agent.will_retry}|{agent.will_error}".encode()
            )
        return digest.hexdigest()[:32]

    def summary(self) -> dict[str, Any]:
        return {
            "total_agents": len(self.agents),
            "archetypes": {
                name: len(self.by_archetype(name)) for name in self.definition_hashes
            },
            "definition_hashes": dict(self.definition_hashes),
            "population_fingerprint": self.fingerprint(),
        }
