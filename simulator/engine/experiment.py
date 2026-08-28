"""Experiment orchestration and the record spec section 40 requires.

The Phase 11 exit criterion is that a repeated same-seed experiment produces the same
result within tolerance. This module is where that holds or fails, so the ordering of
every random draw is deliberate and documented.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import asdict, dataclass, field
from typing import Any

import numpy as np

from simulator.agents.population import AgentPopulation, Archetype
from simulator.assurance.engine import AssuranceEngine, Limits
from simulator.execution.engine import ExecutionEngine, Intent, Outcome
from simulator.market.engine import ImpactModel, MarketEngine, MarketState


@dataclass(frozen=True)
class Scenario:
    """A scenario definition (spec section 41)."""

    scenario_id: str
    scenario_version: int
    description: str

    instrument_id: str = "instr_us_equity_00206R102"
    steps: int = 60

    archetypes: tuple[Archetype, ...] = ()
    limits: Limits = field(default_factory=Limits)
    enforcement_enabled: bool = True

    # Market setup.
    initial_price: float = 100.0
    initial_spread: float = 0.02
    daily_volume: float = 1_000_000.0
    initial_depth: float = 50_000.0
    volatility: float = 0.001
    drift: float = 0.0
    liquidity_decay: float = 1.0

    # Execution behaviour.
    response_loss_probability: float = 0.0
    rejection_probability: float = 0.0

    # Fraction of the population that acts on any given step.
    participation_rate: float = 0.1

    def dataset_hash(self) -> str:
        """A hash of the scenario, standing in for the market dataset hash.

        Spec section 40 wants market_dataset_hash. V0 generates its market rather than
        replaying one, so the generating parameters are the dataset: two runs with the
        same hash saw the same market by construction. Replaying a recorded dataset
        would hash the file instead, and this field is where that would go.
        """
        payload = json.dumps(
            {k: v for k, v in asdict(self).items() if k != "archetypes"},
            sort_keys=True,
            separators=(",", ":"),
            default=str,
        )
        return hashlib.sha256(payload.encode("utf-8")).hexdigest()[:32]


@dataclass
class ExperimentRecord:
    """Everything spec section 40 requires a simulation to store.

    Its purpose is a reproducible investigation: given this record, someone should be
    able to rerun the experiment and get the same answer, or find out exactly which
    input changed.
    """

    experiment_id: str
    scenario_id: str
    scenario_version: int
    code_commit: str
    random_seed: int
    policy_bundle_id: str
    population_definition_hash: str
    market_dataset_id: str
    market_dataset_hash: str
    started_at: str
    completed_at: str
    results: dict[str, Any]

    def to_dict(self) -> dict[str, Any]:
        return asdict(self)

    def result_fingerprint(self) -> str:
        """A hash of the results, for comparing two runs in one line.

        Floats are rounded before hashing. Two runs of one seed produce bit-identical
        floats here because the operation order is fixed, but the rounding means a
        future change that introduces a last-digit difference reports as a difference
        in behaviour rather than as noise.
        """
        rounded = _round_floats(self.results, places=9)
        payload = json.dumps(rounded, sort_keys=True, separators=(",", ":"), default=str)
        return hashlib.sha256(payload.encode("utf-8")).hexdigest()


# ANN401 is right almost everywhere and wrong here: this walks an arbitrary
# JSON-shaped structure, and narrowing the type would mean either lying about
# what it accepts or writing an overload per JSON type.
def _round_floats(value: Any, places: int) -> Any:  # noqa: ANN401
    if isinstance(value, float):
        return round(value, places)
    if isinstance(value, dict):
        return {k: _round_floats(v, places) for k, v in sorted(value.items())}
    if isinstance(value, (list, tuple)):
        return [_round_floats(v, places) for v in value]
    return value


def run(
    scenario: Scenario,
    seed: int,
    *,
    code_commit: str = "unknown",
    policy_bundle_id: str = "none",
    started_at: str = "",
    completed_at: str = "",
) -> ExperimentRecord:
    """Run one experiment.

    Determinism rests on three things, all of them structural rather than careful:

    1. Every draw comes from generators seeded from ``seed``, and there is no other
       source of randomness anywhere in the call.
    2. The three generators are independent streams from one SeedSequence, so adding
       a draw to one engine cannot shift another engine's stream. That is the failure
       mode a single shared generator has, and it makes an unrelated change look like
       a behavioural one.
    3. Iteration order is fixed everywhere: agents in population order, steps in
       sequence. Nothing iterates a set, and nothing depends on dict ordering beyond
       insertion.
    """
    market_seed, population_seed, execution_seed = np.random.SeedSequence(seed).spawn(3)

    population = AgentPopulation.generate(
        list(scenario.archetypes), np.random.default_rng(population_seed)
    )

    market = MarketEngine(
        state=MarketState(
            instrument_id=scenario.instrument_id,
            mid=scenario.initial_price,
            spread=scenario.initial_spread,
            volume=scenario.daily_volume,
            depth=scenario.initial_depth,
            volatility=scenario.volatility,
        ),
        rng=np.random.default_rng(market_seed),
        impact=ImpactModel(),
        liquidity_decay=scenario.liquidity_decay,
    )

    execution_rng = np.random.default_rng(execution_seed)
    execution = ExecutionEngine(
        market=market,
        rng=execution_rng,
        response_loss_probability=scenario.response_loss_probability,
        rejection_probability=scenario.rejection_probability,
    )
    assurance = AssuranceEngine(
        limits=scenario.limits,
        reference_price=scenario.initial_price,
        enabled=scenario.enforcement_enabled,
    )

    intents_submitted = 0
    retries_attempted = 0
    unknown_on_first_attempt = 0
    retries_also_unknown = 0

    for step in range(scenario.steps):
        market.step(drift=scenario.drift)

        # Who acts this step. Drawn once for the whole population so the number of
        # draws does not depend on how many agents happen to act.
        acting = execution_rng.random(len(population)) < scenario.participation_rate

        for index, agent in enumerate(population.agents):
            if not acting[index]:
                continue

            side = "SELL" if agent.will_panic else "BUY"
            quantity = max(1.0, (agent.capital * agent.max_position_pct) / market.state.mid)

            intent = Intent(
                intent_id=f"{agent.agent_id}_s{step}",
                agent_id=agent.agent_id,
                instrument_id=scenario.instrument_id,
                side=side,
                quantity=quantity,
                at_step=step,
                declared_model=agent.declared_model,
                declared_feed=agent.declared_feed,
                strategy_id=agent.strategy_id,
                malformed=agent.will_error,
            )
            intents_submitted += 1

            decision = assurance.evaluate(intent)
            if not decision.allowed:
                continue

            fill = execution.submit(intent)

            if fill.outcome is Outcome.UNKNOWN:
                unknown_on_first_attempt += 1
                # A blind retry. The twin models the agent doing the wrong thing so
                # that a scenario can measure what it costs: the platform's job is to
                # make the retry harmless, not to assume nobody retries.
                if agent.will_retry:
                    retries_attempted += 1
                    retry = execution.submit(intent)
                    if retry.outcome is Outcome.UNKNOWN:
                        # The compounding case, and the reason the retry's own outcome
                        # is no longer discarded: an agent that retried into a second
                        # ambiguous answer now has two orders it cannot account for,
                        # and nothing in the record said so.
                        retries_also_unknown += 1

    results: dict[str, Any] = {
        "intents_submitted": intents_submitted,
        "denied": assurance.denied_count(),
        "denial_codes": assurance.denial_codes(),
        # outcomes counts every submission the venue answered, retries included.
        # unknown_on_first_attempt counts only what made an agent decide to retry.
        # They are deliberately different numbers, and they used to have the same
        # name: a run reported unknown_outcomes 70 beside outcomes.UNKNOWN 71, which
        # reads as an arithmetic error rather than as two questions.
        "outcomes": execution.outcome_counts(),
        "unknown_on_first_attempt": unknown_on_first_attempt,
        "retries_attempted": retries_attempted,
        "retries_also_unknown": retries_also_unknown,
        "duplicate_receipts": execution.duplicate_receipts(),
        "final_mid": market.state.mid,
        "permanent_impact": market.state.permanent_impact,
        "realised_volatility": market.realised_volatility(),
        "population": population.summary(),
    }

    return ExperimentRecord(
        experiment_id=f"exp_{scenario.scenario_id}_{seed}",
        scenario_id=scenario.scenario_id,
        scenario_version=scenario.scenario_version,
        code_commit=code_commit,
        random_seed=seed,
        policy_bundle_id=policy_bundle_id,
        population_definition_hash=population.fingerprint(),
        market_dataset_id=f"generated_{scenario.instrument_id}",
        market_dataset_hash=scenario.dataset_hash(),
        started_at=started_at,
        completed_at=completed_at,
        results=results,
    )
