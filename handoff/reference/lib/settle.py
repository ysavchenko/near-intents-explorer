"""Parse an `execute_intents` settlement into individual solver swap legs.

The unit of analysis is **one `token_diff` intent = one swap leg, from the
solver's side**. We do NOT net by account: a solver that routes WNEAR->USDT then
USDT->USDC signs two token_diffs, and those are two distinct swaps (one is a
real NEAR/USDT trade, the other is stable->stable plumbing). Netting them would
hide the price-bearing leg and mangle the amounts. (In the reference corpus, 21%
of settlements have a signer in multiple payloads and ~2% pack two token_diffs
into one signed message — so splitting at the token_diff level is required.)

For each leg, from the solver's perspective:
  * received asset = the positive entry in the diff (flows into the solver)
  * given asset    = the negative entry (flows out)
  * amounts are the solver's own on-chain amounts (the user pays the fee, so the
    user-side amounts differ slightly — we use the solver side for hedge math).

We keep only *solver* legs (the user's net token_diff is the combined intent,
not a tradeable leg), identified via the solver set (frequency) with a
withdrawal-intent fallback. Pair classification (same-asset / stable<->stable /
interesting) is applied later with the asset registry.
"""
from __future__ import annotations

import base64
import json
from dataclasses import dataclass, field
from typing import Any, Optional

from .solvers import SolverSet

SETTLEMENT_METHOD = "execute_intents"
WITHDRAWAL_KINDS = {"ft_withdraw", "native_withdraw", "mt_withdraw"}


@dataclass
class SignedMsg:
    """One signer's signed message within a settlement."""
    signer: str
    kinds: list[str] = field(default_factory=list)
    token_diffs: list[dict[str, int]] = field(default_factory=list)  # one per intent

    @property
    def has_withdrawal(self) -> bool:
        return any(k in WITHDRAWAL_KINDS for k in self.kinds)

    @property
    def has_token_diff(self) -> bool:
        return bool(self.token_diffs)


@dataclass
class SwapLeg:
    """One token_diff, from the solver's perspective."""
    signer: str
    recv_asset: str
    recv_amount: int
    give_asset: str
    give_amount: int
    multi_asset: bool = False           # diff had >2 assets (dominant pair kept)
    leg_class: str = "unclassified"     # interesting | stable_stable | same_asset | unknown
    raw_diff: dict[str, int] = field(default_factory=dict)


# --------------------------------------------------------------------------
# Decode raw actions -> signed messages (token_diffs split out individually)
# --------------------------------------------------------------------------
def _message_to_signed(msg: dict[str, Any]) -> SignedMsg:
    kinds: list[str] = []
    tds: list[dict[str, int]] = []
    for it in msg.get("intents", []):
        kinds.append(it.get("intent", "?"))
        if it.get("intent") == "token_diff":
            d = {a: int(v) for a, v in it.get("diff", {}).items() if int(v) != 0}
            if d:
                tds.append(d)
    return SignedMsg(signer=msg.get("signer_id", "?"), kinds=kinds, token_diffs=tds)


def parse_signed(args_obj: dict[str, Any]) -> list[SignedMsg]:
    """Decode an `execute_intents` args object into signed messages, tolerating
    NEP-413 (`payload.message` is a JSON string) and EIP-712 (`payload` is the
    message JSON string) encodings."""
    out: list[SignedMsg] = []
    for s in args_obj.get("signed", []):
        if isinstance(s, str):
            s = json.loads(s)
        payload = s.get("payload")
        if isinstance(payload, str):
            msg = json.loads(payload)               # EIP-712
        elif isinstance(payload, dict):
            raw = payload.get("message")
            if not raw:
                continue
            msg = json.loads(raw)                    # NEP-413
        else:
            continue
        out.append(_message_to_signed(msg))
    return out


def signed_from_actions(actions: list[Any]) -> list[SignedMsg]:
    """Extract signed messages from every `execute_intents` FunctionCall (args
    are base64 JSON in both `tx`/`EXPERIMENTAL_tx_status` and `chunk` shapes)."""
    out: list[SignedMsg] = []
    for a in actions:
        if not isinstance(a, dict):
            continue
        fc = a.get("FunctionCall")
        if not fc or fc.get("method_name") != SETTLEMENT_METHOD:
            continue
        args_obj = json.loads(base64.b64decode(fc["args"]))
        out.extend(parse_signed(args_obj))
    return out


def tx_succeeded(tx_result: dict[str, Any]) -> bool:
    status = tx_result.get("status")
    if isinstance(status, dict):
        return "Failure" not in status
    return True


# --------------------------------------------------------------------------
# Signer roles + leg extraction
# --------------------------------------------------------------------------
def classify_signers(msgs: list[SignedMsg], solvers: SolverSet) -> dict[str, str]:
    """Map each token_diff signer to a role: solver | user | unknown.

    Primary: the solver set (recurring accounts). Fallback when no known solver
    is present: the withdrawer is the user, everyone else with a token_diff is a
    solver. If neither signal fires, roles are `unknown` (surfaced, not guessed).
    """
    movers = [m for m in msgs if m.has_token_diff]
    signers = {m.signer for m in movers}
    roles: dict[str, str] = {}

    # Known professional maker(s) present -> they are the solvers, and every other
    # token_diff signer is a taker (user / fee-sweep / session account), even if it
    # recurs. This stops frequent takers from being mistaken for solvers.
    seed = {s for s in signers if solvers.is_seed(s)}
    if seed:
        for s in signers:
            roles[s] = "solver" if s in seed else "user"
        return roles

    # No known maker: best-effort. Frequency-promoted accounts, then withdrawal.
    freq = {s for s in signers if solvers.is_solver(s)}
    if freq:
        for s in signers:
            roles[s] = "solver" if s in freq else "user"
        return roles

    withdrawers = {m.signer for m in movers if m.has_withdrawal}
    if withdrawers:
        for s in signers:
            roles[s] = "user" if s in withdrawers else "solver"
        return roles

    for s in signers:
        roles[s] = "unknown"
    return roles


def _leg_from_diff(signer: str, diff: dict[str, int]) -> Optional[SwapLeg]:
    """Build a leg from one token_diff: dominant received vs dominant given."""
    pos = sorted([(a, v) for a, v in diff.items() if v > 0], key=lambda kv: -kv[1])
    neg = sorted([(a, -v) for a, v in diff.items() if v < 0], key=lambda kv: -kv[1])
    if not pos or not neg:
        return None  # one-sided accounting entry, not a swap
    recv_asset, recv_amt = pos[0]
    give_asset, give_amt = neg[0]
    return SwapLeg(
        signer=signer,
        recv_asset=recv_asset, recv_amount=recv_amt,
        give_asset=give_asset, give_amount=give_amt,
        multi_asset=(len(pos) > 1 or len(neg) > 1),
        raw_diff=dict(diff),
    )


def extract_solver_legs(msgs: list[SignedMsg], roles: dict[str, str]) -> list[SwapLeg]:
    legs: list[SwapLeg] = []
    for m in msgs:
        if roles.get(m.signer) != "solver":
            continue
        for d in m.token_diffs:
            leg = _leg_from_diff(m.signer, d)
            if leg is not None:
                legs.append(leg)
    return legs


@dataclass
class Settlement:
    tx_hash: str
    relayer: Optional[str]
    succeeded: bool
    msgs: list[SignedMsg]
    roles: dict[str, str]
    legs: list[SwapLeg]          # solver legs only (one per token_diff)

    @property
    def user_signers(self) -> list[str]:
        return [s for s, r in self.roles.items() if r == "user"]

    @property
    def solver_signers(self) -> list[str]:
        return sorted({s for s, r in self.roles.items() if r == "solver"})


def parse_settlement(
    tx_hash: str,
    actions: list[Any],
    *,
    relayer: Optional[str] = None,
    succeeded: bool = True,
    solvers: Optional[SolverSet] = None,
) -> Settlement:
    solvers = solvers or SolverSet()
    msgs = signed_from_actions(actions)
    solvers.observe({m.signer for m in msgs if m.has_token_diff})
    roles = classify_signers(msgs, solvers)
    legs = extract_solver_legs(msgs, roles)
    return Settlement(tx_hash=tx_hash, relayer=relayer, succeeded=succeeded,
                      msgs=msgs, roles=roles, legs=legs)
