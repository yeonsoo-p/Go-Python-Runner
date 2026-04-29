"""Partial fail — sends output and progress, then fails with explicit error."""

from __future__ import annotations

from runner import fail, output, progress, run


def main(_params: dict[str, str]) -> None:
    output("step 1 ok")
    progress(1, 3, "step 1")
    output("step 2 ok")
    progress(2, 3, "step 2")
    fail("intentional failure at step 3", "partial_fail.main: step=3, total=3")


if __name__ == "__main__":
    run(main)
