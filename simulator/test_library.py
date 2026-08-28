"""Scenario files are refused rather than repaired.

The failure that matters here is the misspelled key. A scenario with
`panic_probabilty` and no validation runs a population that never panics, produces a
plausible result, and reproduces perfectly from its seed: the experiment is wrong and
everything about it looks right, including the fingerprint that is supposed to make it
trustworthy.
"""

from __future__ import annotations

import json
from collections.abc import Callable
from pathlib import Path
from typing import Any

import pytest

from simulator.engine.library import ScenarioError, load, parse

MINIMAL = {
    "scenario_id": "minimal",
    "scenario_version": 1,
    "description": "the smallest scenario that means anything",
    "steps": 5,
    "archetypes": [{"name": "only", "population": 3}],
}


def test_a_minimal_scenario_parses() -> None:
    scenario = parse(json.dumps(MINIMAL))
    assert scenario.scenario_id == "minimal"
    assert len(scenario.archetypes) == 1
    assert scenario.archetypes[0].population == 3


def test_a_misspelled_archetype_field_is_refused() -> None:
    document = json.loads(json.dumps(MINIMAL))
    document["archetypes"][0]["panic_probabilty"] = 0.9

    with pytest.raises(ScenarioError) as err:
        parse(json.dumps(document))

    message = str(err.value)
    assert "panic_probabilty" in message, "the refusal must name the field that was wrong"
    assert "panic_probability" in message, (
        "the refusal must list the known fields; a typo is easiest to fix beside the "
        "name it was nearly"
    )


def test_a_misspelled_scenario_field_is_refused() -> None:
    document = json.loads(json.dumps(MINIMAL))
    document["volatilty"] = 0.02
    with pytest.raises(ScenarioError, match="volatilty"):
        parse(json.dumps(document))


def test_a_misspelled_limit_is_refused() -> None:
    document = json.loads(json.dumps(MINIMAL))
    document["limits"] = {"per_order_notinal": 100.0}
    with pytest.raises(ScenarioError, match="per_order_notinal"):
        parse(json.dumps(document))


@pytest.mark.parametrize(
    ("mutate", "expect"),
    [
        (lambda d: d.pop("scenario_id"), "scenario_id"),
        (lambda d: d.update(archetypes=[]), "no archetypes"),
        (lambda d: d.update(steps=0), "no steps"),
        (lambda d: d["archetypes"][0].update(population=0), "no population"),
        (lambda d: d["archetypes"][0].update(name=""), "no name"),
    ],
)
def test_a_scenario_that_would_run_nothing_is_refused(
    mutate: Callable[[dict[str, Any]], object], expect: str
) -> None:
    document = json.loads(json.dumps(MINIMAL))
    mutate(document)
    with pytest.raises(ScenarioError, match=expect):
        parse(json.dumps(document))


def test_malformed_json_is_refused_as_such() -> None:
    with pytest.raises(ScenarioError, match="not valid JSON"):
        parse("{not json")


def test_the_shipped_scenario_loads(tmp_path: Path) -> None:
    """The scenario in the repository has to parse, or it is decoration."""
    path = Path(__file__).resolve().parent / "scenarios" / "correlated_panic.json"
    scenario, digest = load(path)

    assert scenario.scenario_id == "correlated_panic"
    assert sum(a.population for a in scenario.archetypes) == 135
    assert len(digest) == 64, "the source hash is a sha256 of the file's exact bytes"


def test_the_source_hash_follows_the_bytes(tmp_path: Path) -> None:
    """Two files with the same scenario and different bytes are different experiments.

    The hash is of the file as written, not of the parsed object, so a record says
    which file was run rather than only what it was called.
    """
    a = tmp_path / "a.json"
    b = tmp_path / "b.json"
    a.write_text(json.dumps(MINIMAL), encoding="utf-8")
    b.write_text(json.dumps(MINIMAL, indent=2), encoding="utf-8")

    scenario_a, hash_a = load(a)
    scenario_b, hash_b = load(b)

    assert scenario_a == scenario_b, "the two files describe the same scenario"
    assert hash_a != hash_b, "and their bytes differ, which a record should be able to show"
