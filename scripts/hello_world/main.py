"""Hello World script — simple greeting with progress reporting."""

from __future__ import annotations

import os
import sys

# Add _lib to path so runner module is importable
sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "_lib"))

from runner import complete, connect, fail, output, progress


def main(params: dict[str, str]) -> None:
    name = params.get("name", "World")
    output(f"Hello, {name}!")
    for i in range(5):
        progress(i + 1, 5, "Working")
    complete()


if __name__ == "__main__":
    try:
        main(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))
