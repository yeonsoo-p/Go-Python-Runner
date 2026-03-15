"""Slow script — runs for a long time, used for cancel testing."""

from __future__ import annotations

import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import complete, connect, fail, output, progress

if __name__ == "__main__":
    try:
        connect()
        output("starting slow work")
        for i in range(30):
            progress(i + 1, 30, "working")
            time.sleep(1)
        complete()
    except Exception as e:
        fail(str(e))
