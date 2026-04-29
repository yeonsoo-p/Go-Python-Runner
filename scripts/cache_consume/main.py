"""Cache Consume — retrieves a numpy array from shared memory.

Must be run while Cache Produce is still holding its shared memory handle.
On Windows, shared memory blocks are reclaimed when all handles close.
"""

from __future__ import annotations

import numpy as np
from runner import cache_get, complete, output, progress, run


def main(params: dict[str, str]) -> None:
    key = params.get("key", "shared_array")

    progress(1, 2, "Retrieving from cache")
    obj = cache_get(key)
    arr = np.asarray(obj)
    output(f"Retrieved array: len={len(arr)}, sum={arr.sum():.1f}")

    progress(2, 2, "Done")
    output("Cache sharing verified across parallel scripts")
    complete()


if __name__ == "__main__":
    run(main)
