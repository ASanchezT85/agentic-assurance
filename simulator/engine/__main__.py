"""Entry point for the simulation-engine deployable (ADR-016).

    python -m simulator.engine --scenario demo --seed 42
    python -m simulator.engine --scenario scenarios/flash_crash.json --seed 42 -o run.json

ADR-011 counts four V0 deployables and ADR-016 places this one: a Python process
rooted at simulator/, not a Go binary and not a directory under cmd/.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import UTC, datetime
from pathlib import Path

from simulator.agents.population import Archetype
from simulator.assurance.engine import Limits
from simulator.engine.experiment import Scenario, run
from simulator.engine.library import ScenarioError, load

DEMO = Scenario(
    scenario_id="demo",
    scenario_version=1,
    description="A small mixed population under a per-order limit, for smoke runs.",
    steps=30,
    archetypes=(
        Archetype(
            name="momentum_conservative",
            population=50,
            declared_model="model_A",
            declared_feed="feed_B",
            strategy_id="strategy_momentum",
            panic_probability=0.03,
        ),
        Archetype(
            name="mean_reversion",
            population=30,
            declared_model="model_B",
            declared_feed="feed_B",
            strategy_id="strategy_reversion",
        ),
    ),
    limits=Limits(per_order_notional=25_000.0),
    response_loss_probability=0.02,
)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(prog="simulator.engine")
    parser.add_argument("--scenario", default="demo", help="'demo', or the path to a scenario file")
    parser.add_argument(
        "-o", "--output", default="", help="write the experiment record here as well as to stdout"
    )
    parser.add_argument(
        "--seed",
        type=int,
        required=True,
        help="random seed; required, because an unseeded run is not reproducible",
    )
    parser.add_argument("--code-commit", default="unknown")
    args = parser.parse_args(argv)

    if args.scenario == "demo":
        scenario, scenario_hash = DEMO, "builtin"
    else:
        path = Path(args.scenario)
        if not path.is_file():
            print(
                f"no scenario file at {args.scenario!r}. Pass 'demo' for the built-in "
                f"one, or the path to a scenario file.",
                file=sys.stderr,
            )
            return 2
        try:
            scenario, scenario_hash = load(path)
        except ScenarioError as err:
            # Refused rather than repaired. A scenario the engine had to guess at
            # produces a reproducible run of something nobody asked for.
            print(f"{args.scenario}: {err}", file=sys.stderr)
            return 2

    started = datetime.now(UTC).isoformat()
    record = run(
        scenario,
        args.seed,
        code_commit=args.code_commit,
        started_at=started,
        completed_at=datetime.now(UTC).isoformat(),
    )

    output = record.to_dict()
    output["result_fingerprint"] = record.result_fingerprint()

    # The exact bytes of the scenario file, so a record says what was run and not only
    # what it was called. Two files with the same scenario_id and different contents
    # are different experiments, and a record that named only the id could not tell
    # them apart.
    output["scenario_source_hash"] = scenario_hash

    text = json.dumps(output, indent=2, default=str)
    if args.output:
        Path(args.output).write_text(text + "\n", encoding="utf-8")
    sys.stdout.write(text + "\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
