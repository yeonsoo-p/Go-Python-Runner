"""Crash script — raises an exception immediately for testing error handling."""

from __future__ import annotations

from runner import run


def main(_params: dict[str, str]) -> None:
    raise RuntimeError("intentional crash for testing")


if __name__ == "__main__":
    run(main)
