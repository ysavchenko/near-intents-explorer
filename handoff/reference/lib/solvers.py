"""Solver identification.

In a settlement, *solvers* are the liquidity providers and *users* are the
parties whose intent is being filled. The reliable discriminator is frequency:
solver accounts recur across thousands of settlements, while a user account
typically appears once. Account *naming* is NOT reliable — some solvers are EVM
(`0x..`) or implicit (64-hex) accounts, not `*-solver.near`.

This module holds a seed set of known solvers (mined from a prior backfill) and
a `SolverSet` that can additionally learn solvers by frequency as we ingest. The
seed set lets the first parses classify correctly before any learning has run.
"""
from __future__ import annotations

from collections import Counter

# Accounts observed as recurring liquidity providers (token_diff signers across
# many distinct settlements). Includes named, EVM, and implicit accounts.
SEED_SOLVERS: set[str] = {
    "crux-solver.near",
    "solver-priv-liq.near",
    "solver-priv-liq-2.near",
    "solver-multichain-asset.near",
    "solver-multichain-asset-escrow.near",
    "solver-multichain-asset-3.near",
    "solver-ref.near",
    "l2solver.near",
    "nocturnallabs.near",
    "kipseli.near",
    "sodax-solver.near",
    "defuse-relay.near",
    "pablow-stables.near",
    # EVM / implicit accounts that recur as solvers:
    "0x2e30c32738a28833e3d85189c04895c25ee54cb5",
    "6247d9c671ca6a80cf2c75e26edb8cd75ebf00fcd3c56c39107d6ca20c9cfb29",
    "433d1f6e5452c9f6087af460d1c6b799587132caabeb2477a0ad37e3e0b88d0c",
    "4fdbc608fd712338c7680c224f249360c855987cc25ec4501a9a47ef4c789264",
}


class SolverSet:
    """Known solvers plus a frequency tally that promotes recurring signers.

    An account is treated as a solver if it is in the seed set or has appeared as
    a token_diff signer in at least `min_settlements` distinct settlements.
    """

    def __init__(self, *, min_settlements: int = 5):
        self.min_settlements = min_settlements
        self._counts: Counter[str] = Counter()

    def observe(self, signer_ids: set[str]) -> None:
        """Record the distinct token_diff signers seen in one settlement."""
        for s in signer_ids:
            self._counts[s] += 1

    def is_seed(self, account: str) -> bool:
        """A curated, known professional maker."""
        return account in SEED_SOLVERS

    def is_solver(self, account: str) -> bool:
        return account in SEED_SOLVERS or self._counts[account] >= self.min_settlements

    def learned(self) -> dict[str, int]:
        """Accounts promoted to solver purely by frequency (excludes seeds)."""
        return {a: c for a, c in self._counts.items()
                if a not in SEED_SOLVERS and c >= self.min_settlements}
