"""Scenarios as data.

Until now the engine could run exactly one scenario, hardcoded, and refused every
other name with a message saying the stress library arrived in Phase 12. Phase 12 did
arrive, and it put its scenarios in Go, against the real engines, which is where
scenarios that assert on production behaviour belong.

But that left the twin able to answer one question. A simulator whose question is
compiled in is a fixture, and asking a new one meant editing Python. Scenarios are
files now.

A scenario file is JSON rather than YAML: it is read by a process whose reproducibility
guarantee rests on the exact bytes of its inputs, and JSON has one way to write a
number.
"""

from __future__ import annotations

import hashlib
import json
from dataclasses import fields
from pathlib import Path
from typing import Any

from simulator.agents.population import Archetype
from simulator.assurance.engine import Limits
from simulator.engine.experiment import Scenario


class ScenarioError(ValueError):
    """A scenario file that cannot be trusted to mean what it says."""


def _known(cls: type) -> set[str]:
    return {f.name for f in fields(cls)}


def _reject_unknown(where: str, given: dict[str, Any], cls: type) -> None:
    """Refuse fields nobody will read.

    A misspelled key is the failure mode that matters here. Silently ignoring
    `panic_probabilty` produces a run with no panic, a plausible result, and a
    fingerprint that reproduces perfectly: the scenario is wrong and everything about
    it looks right.
    """
    unknown = sorted(set(given) - _known(cls))
    if unknown:
        raise ScenarioError(
            f"{where}: unknown field(s) {', '.join(unknown)}. "
            f"Known fields are {', '.join(sorted(_known(cls)))}"
        )


def _archetype(index: int, given: dict[str, Any]) -> Archetype:
    if not isinstance(given, dict):
        raise ScenarioError(f"archetypes[{index}] is not an object")
    _reject_unknown(f"archetypes[{index}]", given, Archetype)

    if not given.get("name"):
        raise ScenarioError(f"archetypes[{index}] has no name")
    if int(given.get("population", 0)) < 1:
        raise ScenarioError(f"archetypes[{index}] has no population")

    return Archetype(**given)


def _limits(given: dict[str, Any]) -> Limits:
    _reject_unknown("limits", given, Limits)
    if "denied_instruments" in given:
        given = dict(given, denied_instruments=tuple(given["denied_instruments"]))
    return Limits(**given)


def parse(raw: str) -> Scenario:
    """Build a Scenario from the text of a scenario file."""
    try:
        document = json.loads(raw)
    except json.JSONDecodeError as err:
        raise ScenarioError(f"the scenario file is not valid JSON: {err}") from err

    if not isinstance(document, dict):
        raise ScenarioError("a scenario file must contain a JSON object")

    document = dict(document)
    archetypes = document.pop("archetypes", [])
    limits = document.pop("limits", {})

    _reject_unknown("scenario", document, Scenario)

    for required in ("scenario_id", "scenario_version", "description"):
        if required not in document:
            raise ScenarioError(f"scenario is missing {required!r}")

    if not archetypes:
        raise ScenarioError(
            "a scenario with no archetypes has no population, and a run over an empty "
            "fleet measures nothing"
        )

    scenario = Scenario(
        **document,
        archetypes=tuple(_archetype(i, a) for i, a in enumerate(archetypes)),
        limits=_limits(limits),
    )

    if scenario.steps < 1:
        raise ScenarioError("a scenario with no steps runs nothing")
    return scenario


def load(path: Path) -> tuple[Scenario, str]:
    """Read a scenario file and return it with the hash of its exact bytes.

    The hash is of the file as written, not of the parsed object. Two files that
    differ only in whitespace produce the same run and different hashes, which is
    correct: the record says which file was run, and a reader comparing two records
    should see that the inputs were not byte-identical.
    """
    raw = path.read_bytes()
    return parse(raw.decode("utf-8")), hashlib.sha256(raw).hexdigest()
