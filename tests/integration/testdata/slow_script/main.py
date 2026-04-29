"""Slow script — runs for a long time, used for cancel testing."""

from __future__ import annotations

import time

from runner import complete, output, progress, run


def main(_params: dict[str, str]) -> None:
    output("starting slow work")
    for i in range(30):
        progress(i + 1, 30, "working")
        time.sleep(1)
    complete()


if __name__ == "__main__":
    run(main)
