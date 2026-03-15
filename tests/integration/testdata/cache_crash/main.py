"""Cache crash — stores data in cache then exits abruptly without cleanup."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import cache_set, connect, output

if __name__ == "__main__":
    connect()
    cache_set("crash_data", {"will": "be orphaned"})
    output("cached:crash_data")
    # Exit without calling complete() or fail() — simulates a crash
    os._exit(1)
