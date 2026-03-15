"""Crash script — raises an exception immediately for testing error handling."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import connect, fail

if __name__ == "__main__":
    try:
        connect()
        raise RuntimeError("intentional crash for testing")
    except Exception as e:
        fail(str(e))
