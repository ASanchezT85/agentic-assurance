"""Phase 0 boot test: the simulator package and all sub-packages import cleanly.

This is the Python half of the Phase 0 acceptance criteria (handoff §14).
"""

import importlib

import simulator

SUBPACKAGES = [
    "simulator.engine",
    "simulator.market",
    "simulator.agents",
    "simulator.execution",
    "simulator.assurance",
]


def test_package_imports() -> None:
    assert simulator.__version__ == "0.0.0"
    assert simulator.PHASE == 0


def test_subpackages_import() -> None:
    for name in SUBPACKAGES:
        assert importlib.import_module(name) is not None


def test_no_simulation_implementation_yet() -> None:
    """Phase 0 must not carry Phase 11 logic. Guard against premature scope."""
    forbidden = {"MarketEngine", "AgentPopulation", "ExecutionEngine", "AssuranceEngine"}
    for name in SUBPACKAGES:
        module = importlib.import_module(name)
        leaked = forbidden & set(vars(module))
        assert not leaked, f"{name} implements Phase 11 scope: {sorted(leaked)}"
