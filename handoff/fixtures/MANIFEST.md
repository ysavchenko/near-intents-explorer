# Fixture manifest

Raw `execute_intents` transactions (base64 args intact) + final status,
fetched 2026-07-18 via RPC. `expected_legs.json` holds the oracle output
of the Python parser for each tx (join key: tx_hash) — a Go port must
reproduce these legs exactly (solver, assets, raw amounts, class, order).

- `tx_CqEGVbMajEHonbzb9ai2yvDeKCUuXzF2tY6DVLccBXby.json` — most legs in one settlement; encodings=['nep413'], 5 signed msgs, 6 token_diffs, 8 expected legs
- `tx_AJjgNtgv3P16WUHjmNdk8JRWhqwsgmD2SriC1QEA7GEC.json` — multiple solvers in one settlement; encodings=['nep413'], 4 signed msgs, 5 token_diffs, 3 expected legs
- `tx_7j9CRcpi23A2Yc3efSATWvvwhRaqYkVfA96njwbrHwWk.json` — solver solver-priv-liq.near; encodings=['nep413'], 3 signed msgs, 3 token_diffs, 2 expected legs
- `tx_4u23tHV6mDjGetDxyHCt6PT9RGuR4HsSXe3DZZNrz7Yb.json` — solver solver-multichain-asset.near; encodings=['nep413'], 4 signed msgs, 4 token_diffs, 2 expected legs
- `tx_G5SwitQnPHEJCJ7WKKj1S2Ln6WHeE7r6mTshY1prD5Dq.json` — solver 0x2e30c32738a28833e3d85189c04895c25ee54cb5; encodings=['erc191', 'nep413'], 6 signed msgs, 7 token_diffs, 5 expected legs

## Coverage notes

- A 25-tx random sample of the same window showed only `nep413` and `erc191`
  payload standards. **`eip712`/`webauthn` payloads exist on this contract**
  (rare, usually user-side) but none landed in this window — the parser must
  still tolerate them: any `payload` that is a JSON *string* parses like the
  EIP-712 shape, and unknown standards must be skipped, never fatal
  (see `reference/lib/settle.py::parse_signed`).
- No `multi_asset` token_diff (>2 assets in one diff) occurred in this window
  (0 of 14,292 legs), so no fixture covers the dominant-pair selection path.
  Port it from `settle.py::_leg_from_diff` and unit-test it synthetically.
- All fixtures are *successful* settlements (legs only exist for successes).
  The follower must still count failed `execute_intents` txs (`succeeded=false`,
  no legs).
