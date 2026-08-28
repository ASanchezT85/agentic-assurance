"""Entry point for the simulation-engine deployable (ADR-016).

    python -m simulator.engine --scenario demo --seed 42

ADR-011 counts four V0 deployables and ADR-016 places this one: a Python process
rooted at simulator/, not a Go binary and not a directory under cmd/.
"""

from __future__ import annotations

import argparse
import json
import sys
from datetime import UTC, datetime

from simulator.agents.population import Archetype
from simulator.assurance.engine import Limits
from simulator.engine.experiment import Scenario, run

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
    parser.add_argument("--scenario", default="demo", help="scenario to run")
    parser.add_argument("--seed", type=int, required=True,
                        help="random seed; required, because an unseeded run is not reproducible")
    parser.add_argument("--code-commit", default="unknown")
    args = parser.parse_args(argv)

    if args.scenario != "demo":
        print(f"unknown scenario {args.scenario!r}; the stress library arrives in Phase 12",
              file=sys.stderr)
        return 2

    started = datetime.now(UTC).isoformat()
    record = run(DEMO, args.seed, code_commit=args.code_commit, started_at=started,
                 completed_at=datetime.now(UTC).isoformat())

    output = record.to_dict()
    output["result_fingerprint"] = record.result_fingerprint()
    json.dump(output, sys.stdout, indent=2, default=str)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
