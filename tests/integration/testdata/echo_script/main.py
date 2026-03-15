"""Echo script — echoes params back as output with progress."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "..", "..", "..", "scripts", "_lib"))

from runner import complete, connect, fail, output, progress

if __name__ == "__main__":
    try:
        params = connect()
        message = params.get("message", "hello")
        output(f"echo: {message}")
        for i in range(3):
            progress(i + 1, 3, "echoing")
        complete()
    except Exception as e:
        fail(str(e))
