"""Fastest possible script for stress tests — connects, calls complete()."""

from __future__ import annotations

from runner import complete, run


def main(_params: dict[str, str]) -> None:
    complete()


if __name__ == "__main__":
    run(main)
