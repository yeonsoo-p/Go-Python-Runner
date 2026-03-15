"""Cache producer — stores a dict in shared memory cache."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import cache_set, complete, connect, fail, output

if __name__ == "__main__":
    try:
        connect()
        data = {"key": "value", "numbers": [1, 2, 3], "nested": {"a": True}}
        cache_set("shared_data", data)
        output("cached:shared_data")
        complete()
    except Exception as e:
        fail(str(e))
