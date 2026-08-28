"""Market Engine.

Spec section 38 is explicit that V0 does not reproduce an exchange matching engine,
and this module does not pretend to. It maintains the minimum state a fleet-risk
scenario needs and applies a documented impact approximation.

The impact model is the part most likely to be quoted out of context, so it says so
in its own docstring: it is a stress-testing approximation, not market truth, and
ADR-013 forbids presenting simulated impact as evidence about real markets.
"""

from __future__ import annotations

from dataclasses import dataclass, field, replace

import numpy as np


@dataclass(frozen=True)
class MarketState:
    """The observable state of one instrument at one instant (spec section 38)."""

    instrument_id: str
    mid: float
    spread: float
    volume: float
    depth: float
    volatility: float

    # Cumulative permanent impact this simulation has applied, kept separately from
    # the mid so a reader can tell how much of the price move the simulated fleet
    # caused rather than inferring it.
    permanent_impact: float = 0.0

    @property
    def bid(self) -> float:
        return self.mid - self.spread / 2

    @property
    def ask(self) -> float:
        return self.mid + self.spread / 2

    def stale(self, other: MarketState) -> bool:
        """Whether this state differs from a later one.

        Scenario S03 turns on agents acting from stale prices, so staleness has to be
        a property the twin can express rather than something the scenario mimics by
        not updating a variable.
        """
        return self.mid != other.mid


@dataclass
class ImpactModel:
    """Square-root market impact (spec section 38).

    ``impact ≈ gamma * sigma * sqrt(Q / V)``

    THIS IS AN APPROXIMATION, NOT MARKET TRUTH. Spec section 38 requires it to be
    documented as one, section 59 forbids claiming paper trading simulates market
    impact, and ADR-013 keeps the twin separate from broker paper trading precisely
    so nobody reads a simulated fill as evidence about a real venue.

    The coefficients are conventional starting points, not calibrated values. Their
    provenance is the published square-root literature, and calibrating them against
    a real venue is out of V0 scope (spec section 65).
    """

    permanent_gamma: float = 0.1
    temporary_gamma: float = 0.3

    def permanent(self, state: MarketState, quantity: float) -> float:
        """Impact that persists after the order finishes."""
        return self._sqrt_impact(state, quantity, self.permanent_gamma)

    def temporary(self, state: MarketState, quantity: float) -> float:
        """Impact felt during execution that decays afterwards."""
        return self._sqrt_impact(state, quantity, self.temporary_gamma)

    def _sqrt_impact(self, state: MarketState, quantity: float, gamma: float) -> float:
        if state.volume <= 0 or quantity <= 0:
            return 0.0
        participation = quantity / state.volume
        return gamma * state.volatility * state.mid * float(np.sqrt(participation))


@dataclass
class MarketEngine:
    """A deterministic market simulator.

    Every source of randomness comes from the injected generator, so two engines
    built from the same seed produce the same path. That is the Phase 11 exit
    criterion, and it is a property of construction rather than of care.
    """

    state: MarketState
    rng: np.random.Generator
    impact: ImpactModel = field(default_factory=ImpactModel)

    # Liquidity decay per step, used by the liquidity-shock scenario. 1.0 leaves
    # depth unchanged.
    liquidity_decay: float = 1.0

    history: list[MarketState] = field(default_factory=list)

    def __post_init__(self) -> None:
        self.history.append(self.state)

    def step(self, drift: float = 0.0) -> MarketState:
        """Advance one tick.

        The price follows a simple random walk with optional drift. It is not a model
        of anything: it exists so that a scenario has a moving price to react to, and
        a scenario that depends on the shape of this walk is testing the walk rather
        than the platform.
        """
        shock = float(self.rng.normal(0.0, self.state.volatility))
        new_mid = max(0.01, self.state.mid * (1.0 + drift + shock))
        new_depth = max(0.0, self.state.depth * self.liquidity_decay)

        # A thinner book is a wider spread. Crude and monotonic, which is all the
        # scenarios need.
        if self.state.depth <= 0:
            widening = 1.0
        else:
            widening = max(1.0, self.state.depth / max(new_depth, 1e-9))
        new_spread = self.state.spread * min(widening, 10.0)

        self.state = replace(
            self.state, mid=new_mid, depth=new_depth, spread=new_spread
        )
        self.history.append(self.state)
        return self.state

    def execute(self, quantity: float, side: str) -> tuple[float, MarketState]:
        """Fill an order and apply its impact.

        Returns the fill price and the resulting state. The fill crosses the spread
        and pays temporary impact; the permanent component moves the mid and stays.
        """
        if quantity <= 0:
            return self.state.mid, self.state

        temporary = self.impact.temporary(self.state, quantity)
        permanent = self.impact.permanent(self.state, quantity)

        direction = 1.0 if side == "BUY" else -1.0
        fill_price = (self.state.ask if direction > 0 else self.state.bid) + direction * temporary

        self.state = replace(
            self.state,
            mid=max(0.01, self.state.mid + direction * permanent),
            permanent_impact=self.state.permanent_impact + direction * permanent,
        )
        self.history.append(self.state)
        return fill_price, self.state

    def realised_volatility(self) -> float:
        """Volatility of the path actually taken, not the parameter it was given."""
        if len(self.history) < 3:
            return 0.0
        mids = np.array([s.mid for s in self.history], dtype=float)
        returns = np.diff(mids) / mids[:-1]
        return float(np.std(returns))
