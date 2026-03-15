"""Numpy Statistics — computes stats on numeric data using a pre-installed package.

Exercises: numpy (pre-installed), output, progress, complete, data_result.
"""

from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "_lib"))

import numpy as np
from runner import complete, connect, data_result, fail, output, progress


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

    result = {name: value for name, value in stats}
    output(f"Result: {json.dumps(result)}")

    # Send raw array bytes via data_result
    data_result("array", arr.tobytes())

    complete()


if __name__ == "__main__":
    try:
        main(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))
