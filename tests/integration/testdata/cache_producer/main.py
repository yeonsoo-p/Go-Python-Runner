"""Cache producer — stores a dict in shared memory and holds the handle.

Holds the SharedMemory handle alive until cancelled / a hold timeout elapses,
so a consumer process can attach to the same OS shm block. Required on Windows,
where the OS reclaims the block when the last handle closes.
"""

from __future__ import annotations

import time

from runner import cache_set, complete, is_cancelled, output, run


def main(params: dict[str, str]) -> None:
    data = {"key": "value", "numbers": [1, 2, 3], "nested": {"a": True}}
    cache_set("shared_data", data)
    output("cached:shared_data")

    # Hold the handle for `hold_seconds` (default 10s) so a consumer running
    # in parallel can open the same block before the OS reclaims it.
    hold = float(params.get("hold_seconds", "10"))
    deadline = time.monotonic() + hold
    while time.monotonic() < deadline:
        if is_cancelled():
            break
        time.sleep(0.1)
    complete()


if __name__ == "__main__":
    run(main)
