"""Stress fixture: sleeps a bit then completes — used to test cancel-mid-run."""

from __future__ import annotations

import time

from runner import complete, is_cancelled, run


def main(_params: dict[str, str]) -> None:
    for _ in range(50):  # ~5s total at 100ms granularity
        if is_cancelled():
            return
        time.sleep(0.1)
    complete()


if __name__ == "__main__":
    run(main)
