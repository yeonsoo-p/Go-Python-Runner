"""Partial fail — sends output and progress, then fails with explicit error."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import connect, fail, output, progress

if __name__ == "__main__":
    try:
        connect()
        output("step 1 ok")
        progress(1, 3, "step 1")
        output("step 2 ok")
        progress(2, 3, "step 2")
        fail("intentional failure at step 3", "partial_fail.main: step=3, total=3")
    except Exception as e:
        fail(str(e))
