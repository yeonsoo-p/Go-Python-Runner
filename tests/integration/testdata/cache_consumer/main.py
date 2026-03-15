"""Cache consumer — retrieves a dict from shared memory cache and outputs it."""

from __future__ import annotations

import json
import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import cache_get, complete, connect, fail, output

if __name__ == "__main__":
    try:
        connect()
        data = cache_get("shared_data")
        output(f"retrieved:{json.dumps(data, sort_keys=True)}")
        complete()
    except Exception as e:
        fail(str(e))
