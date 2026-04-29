"""Cache Produce — creates a numpy array and caches it via shared memory.

Keeps the shared memory handle alive for `hold_seconds` so that Cache Consume
can retrieve it from a parallel run. On Windows, shared memory is reclaimed
when the last handle closes, so the consumer must connect while this is running.
"""

from __future__ import annotations

import time

import numpy as np
from runner import cache_set, complete, fail, output, progress, run


def main(params: dict[str, str]) -> None:
    key = params.get("key", "shared_array")
    try:
        size = int(params.get("size", "100"))
        hold = int(params.get("hold_seconds", "30"))
    except ValueError:
        fail("'size' and 'hold_seconds' must be integers")
        return

    progress(1, 3, "Creating array")
    arr = np.arange(size, dtype=np.float64)
    output(f"Created array: len={len(arr)}, sum={arr.sum():.1f}")

    progress(2, 3, "Caching via shared memory")
    cache_set(key, arr)
    output(f"Cached under key '{key}' — run Cache Consume now")

    progress(3, 3, f"Holding handle for {hold}s")
    try:
        for remaining in range(hold, 0, -1):
            output(f"Holding... {remaining}s remaining")
            time.sleep(1)
    except (KeyboardInterrupt, SystemExit):
        output("Cancelled — releasing handle")
        fail("cancelled")
        return

    output("Handle released")
    complete()


if __name__ == "__main__":
    run(main)
