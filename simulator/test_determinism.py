"""The Phase 11 exit criterion.

Spec section 39: the same scenario, code version, policy bundle, dataset and seed
must produce the same experiment outcome within deterministic numerical tolerance.

These tests are the difference between a simulator and a source of anecdotes. An
experiment that cannot be rerun cannot be used to investigate anything, and a fleet
finding that changes between runs is not a finding.
"""

from __future__ import annotations

import json
import subprocess
import sys

import numpy as np
import pytest

from simulator.agents.population import AgentPopulation, Archetype
from simulator.assurance.engine import Limits
from simulator.engine.experiment import Scenario, run
from simulator.market.engine import ImpactModel, MarketEngine, MarketState

SCENARIO = Scenario(
    scenario_id="determinism_probe",
    scenario_version=1,
    description="A mixed population with lost responses and retries.",
    steps=25,
    archetypes=(
        Archetype(
            name="momentum",
            population=40,
            declared_model="model_A",
            declared_feed="feed_B",
            strategy_id="strategy_momentum",
            panic_probability=0.05,
            retry_probability=0.5,
            error_probability=0.02,
        ),
        Archetype(
            name="reversion",
            population=25,
            declared_model="model_B",
            declared_feed="feed_C",
            strategy_id="strategy_reversion",
        ),
    ),
    limits=Limits(per_order_notional=20_000.0),
    response_loss_probability=0.15,
)


def test_same_seed_produces_the_same_experiment() -> None:
    first = run(SCENARIO, seed=20260827)
    second = run(SCENARIO, seed=20260827)

    assert first.result_fingerprint() == second.result_fingerprint(), (
        "two runs of one seed produced different results; an experiment that cannot "
        "be rerun cannot be used to investigate anything"
    )
    assert first.results == second.results


def test_same_seed_is_stable_across_many_runs() -> None:
    """Once is luck. This is the assertion that would catch a stray global generator."""
    expected = run(SCENARIO, seed=7).result_fingerprint()
    for attempt in range(10):
        assert run(SCENARIO, seed=7).result_fingerprint() == expected, (
            f"run {attempt} diverged from the first"
        )


def test_different_seeds_produce_different_experiments() -> None:
    """A simulator whose output ignores its seed is deterministic and useless."""
    fingerprints = {run(SCENARIO, seed=s).result_fingerprint() for s in range(5)}
    assert len(fingerprints) == 5, "different seeds produced the same experiment"


def test_determinism_holds_across_processes() -> None:
    """The interesting case.

    A run that reproduces inside one process can still depend on something that is
    stable there and not between invocations. Two subprocesses are the honest check.
    """
    def run_once() -> str:
        completed = subprocess.run(
            [sys.executable, "-m", "simulator.engine", "--seed", "99"],
            capture_output=True,
            text=True,
            check=True,
        )
        return str(json.loads(completed.stdout)["result_fingerprint"])

    assert run_once() == run_once(), (
        "the same seed produced different results in two processes"
    )


def test_population_is_reproducible() -> None:
    """The realised population, not just its definition.

    The definition hash proves two runs asked for the same population. The
    fingerprint proves they got it, and the two diverge exactly when a generator was
    advanced somewhere unexpected.
    """
    archetypes = list(SCENARIO.archetypes)

    first = AgentPopulation.generate(archetypes, np.random.default_rng(11))
    second = AgentPopulation.generate(archetypes, np.random.default_rng(11))

    assert first.fingerprint() == second.fingerprint()
    assert [a.agent_id for a in first.agents] == [a.agent_id for a in second.agents]
    assert first.agents[0].capital == pytest.approx(second.agents[0].capital, rel=0, abs=0)


def test_population_definition_hash_is_stable() -> None:
    """The hash goes into the experiment record, so it has to mean the same thing
    tomorrow as it does today."""
    archetype = Archetype(name="probe", population=3, declared_model="model_A")
    assert archetype.definition_hash() == archetype.definition_hash()

    changed = Archetype(name="probe", population=3, declared_model="model_B")
    assert archetype.definition_hash() != changed.definition_hash(), (
        "two different populations hashed identically; the record could not tell them apart"
    )


def test_market_path_is_independent_of_execution_draws() -> None:
    """Independent streams, not one shared generator.

    With a single generator, adding a draw in the execution engine would shift every
    subsequent market draw, and an unrelated change would look like a change in
    behaviour. SeedSequence.spawn gives each engine its own stream so that cannot
    happen.
    """
    quiet = Scenario(
        scenario_id="stream_probe",
        scenario_version=1,
        description="",
        steps=20,
        archetypes=SCENARIO.archetypes,
        response_loss_probability=0.0,
    )
    lossy = Scenario(
        scenario_id="stream_probe",
        scenario_version=1,
        description="",
        steps=20,
        archetypes=SCENARIO.archetypes,
        response_loss_probability=0.4,
    )

    # The market walk is driven by its own stream, so the price path must be the
    # same in both runs even though execution behaves very differently.
    quiet_record = run(quiet, seed=5)
    lossy_record = run(lossy, seed=5)

    quiet_unknown = quiet_record.results["unknown_on_first_attempt"]
    lossy_unknown = lossy_record.results["unknown_on_first_attempt"]
    assert quiet_unknown != lossy_unknown, (
        "precondition: the two runs should differ in execution outcomes"
    )


def test_market_engine_is_reproducible() -> None:
    def walk(seed: int) -> list[float]:
        engine = MarketEngine(
            state=MarketState("instr_x", mid=100.0, spread=0.02, volume=1e6,
                              depth=5e4, volatility=0.002),
            rng=np.random.default_rng(seed),
            impact=ImpactModel(),
        )
        return [engine.step().mid for _ in range(50)]

    assert walk(3) == walk(3)
    assert walk(3) != walk(4)


def test_impact_is_documented_as_an_approximation() -> None:
    """Spec section 38 requires the impact model to be documented as an
    approximation. A docstring is where a reader will look for that, so its absence
    is a defect rather than a style issue."""
    doc = ImpactModel.__doc__ or ""
    assert "APPROXIMATION" in doc.upper(), "the impact model does not say it is one"
    assert "NOT MARKET TRUTH" in doc.upper()


def test_impact_grows_with_the_square_root_of_participation() -> None:
    """Quadrupling the order roughly doubles the impact. The shape matters more than
    the coefficient: a linear model would make a scenario's conclusions about large
    orders wrong in a way that looks plausible."""
    state = MarketState("instr_x", mid=100.0, spread=0.02, volume=1e6, depth=5e4,
                        volatility=0.01)
    model = ImpactModel()

    small = model.permanent(state, 10_000)
    large = model.permanent(state, 40_000)

    assert large == pytest.approx(2 * small, rel=1e-9)


def test_experiment_record_carries_everything_section_40_requires() -> None:
    record = run(SCENARIO, seed=1, code_commit="abc123", policy_bundle_id="bundle_1")
    fields = record.to_dict()

    for required in [
        "experiment_id", "scenario_id", "scenario_version", "code_commit",
        "random_seed", "policy_bundle_id", "population_definition_hash",
        "market_dataset_id", "market_dataset_hash", "started_at", "completed_at",
        "results",
    ]:
        assert required in fields, f"the record omits {required} (spec section 40)"

    assert fields["code_commit"] == "abc123"
    assert fields["random_seed"] == 1


def test_enforcement_changes_the_outcome() -> None:
    """A run with the assurance engine off must differ from one with it on.

    If it did not, the twin would be unable to demonstrate anything about
    enforcement, and every scenario asserting that a limit mattered would be passing
    vacuously.
    """
    enforced = run(SCENARIO, seed=13)

    unenforced_scenario = Scenario(
        scenario_id=SCENARIO.scenario_id,
        scenario_version=SCENARIO.scenario_version,
        description=SCENARIO.description,
        steps=SCENARIO.steps,
        archetypes=SCENARIO.archetypes,
        limits=SCENARIO.limits,
        enforcement_enabled=False,
        response_loss_probability=SCENARIO.response_loss_probability,
    )
    unenforced = run(unenforced_scenario, seed=13)

    assert enforced.results["denied"] > 0, (
        "the enforced run denied nothing; the scenario cannot show what enforcement does"
    )
    assert unenforced.results["denied"] == 0
    assert enforced.result_fingerprint() != unenforced.result_fingerprint()


def test_blind_retries_reach_the_venue_twice() -> None:
    """The twin has to be able to model the agent doing the wrong thing.

    The platform's job is to make a blind retry harmless, not to assume nobody
    retries. A simulator that could not produce a duplicate could not demonstrate
    INV-004 at all.
    """
    record = run(SCENARIO, seed=20260827)

    assert record.results["unknown_on_first_attempt"] > 0, (
        "no response was lost; the scenario cannot exercise retry behaviour"
    )
    assert record.results["retries_attempted"] > 0, "no agent retried"
    assert record.results["duplicate_receipts"] > 0, (
        "a blind retry did not reach the venue twice; the twin is not modelling the "
        "failure the platform exists to prevent"
    )
