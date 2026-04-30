"""Numpy Statistics — computes stats on numeric data using a pre-installed package.

Exercises: numpy (pre-installed), output, progress, complete.
"""

from __future__ import annotations

import numpy as np
from runner import complete, fail, output, progress, run


def main(params: dict[str, str]) -> None:
    raw = params.get("data", "")
    if not raw:
        fail("'data' parameter is required (comma-separated numbers)")
        return

    try:
        values = [float(x.strip()) for x in raw.split(",")]
    except ValueError as e:
        fail(f"Invalid number in 'data': {e}")
        return
    arr = np.array(values)
    output(f"Loaded {len(arr)} values")

    stats = [
        ("mean", float(np.mean(arr))),
        ("std", float(np.std(arr))),
        ("min", float(np.min(arr))),
        ("max", float(np.max(arr))),
    ]

    for i, (name, value) in enumerate(stats):
        progress(i + 1, len(stats), f"Computing {name}")
        output(f"{name}: {value:.4f}")

    complete()


if __name__ == "__main__":
    run(main)
