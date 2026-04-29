"""Cache crash — stores data in cache then exits abruptly without cleanup."""

from __future__ import annotations

import os

from runner import cache_set, connect, output


def main() -> None:
    connect()
    cache_set("crash_data", {"will": "be orphaned"})
    output("cached:crash_data")
    # Exit without calling complete() or fail() — simulates a crash.
    # We don't use runner.run() here because that would invoke fail() on exit;
    # the whole point of this fixture is to skip clean shutdown.
    os._exit(1)


if __name__ == "__main__":
    main()
