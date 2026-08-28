"""The twin's assurance rules must agree with production's.

simulator/assurance/engine.py states that it is not the production engine and that its
rules can drift from the Go ones. Stating a risk is not detecting one. Both engines now
read tests/fixtures/per_order_limit_cases.json, and the same file is checked from Go in
tests/security/twin_agreement_test.go.

If the two ever disagree, one of these two tests fails and names the case. Without them
the first divergence would surface as a scenario that passed in the twin and a fleet
that behaved differently in production, which is the most expensive possible place to
find it.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any

import pytest

from simulator.assurance.engine import AssuranceEngine, Limits
from simulator.execution.engine import Intent

FIXTURE = Path(__file__).resolve().parents[1] / "tests" / "fixtures" / "per_order_limit_cases.json"


def _cases() -> list[dict[str, Any]]:
    document = json.loads(FIXTURE.read_text(encoding="utf-8"))
    cases: list[dict[str, Any]] = document["cases"]
    assert cases, "the shared fixture has no cases; an empty contract agrees with anything"
    return cases


@pytest.mark.parametrize("case", _cases(), ids=lambda c: str(c["name"]))
def test_twin_matches_the_shared_limit_contract(case: dict[str, Any]) -> None:
    engine = AssuranceEngine(
        limits=Limits(per_order_notional=case["per_order_notional"]),
        reference_price=case["reference_price"],
    )

    decision = engine.evaluate(
        Intent(
            intent_id="agreement",
            agent_id="agent_agreement",
            instrument_id="instr_us_equity_00206R102",
            side="BUY",
            quantity=case["quantity"],
            at_step=0,
        )
    )

    assert decision.allowed == case["allowed"], (
        f"{case['name']}: the twin {'allowed' if decision.allowed else 'refused'} an "
        f"intent the shared contract says it should "
        f"{'allow' if case['allowed'] else 'refuse'} "
        f"({case['quantity']} x {case['reference_price']} against a limit of "
        f"{case['per_order_notional']}). Either the twin has drifted from the "
        f"production authority engine, or the contract is wrong."
    )

    if "code" in case:
        assert decision.code == case["code"], (
            f"{case['name']}: code {decision.code!r}, contract says {case['code']!r}. "
            f"The two engines must refuse for the same stated reason, or an operator "
            f"reading a scenario cannot map it onto production behaviour."
        )
