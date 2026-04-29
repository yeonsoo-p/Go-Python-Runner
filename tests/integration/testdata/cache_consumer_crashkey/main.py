"""Cache consumer that asks for the key left behind by cache_crash.

Used by TestCacheCleanupOnCrash to assert that after the owning run terminates
abnormally, the registry has been cleaned and a follow-up cache_get raises
KeyError (rather than reporting "found" for shm that's already been reclaimed).
"""

from __future__ import annotations

from runner import cache_get, complete, output, run


def main(_params: dict[str, str]) -> None:
    data = cache_get("crash_data")  # expected to raise KeyError after cleanup
    output(f"unexpected:retrieved {data!r}")
    complete()


if __name__ == "__main__":
    run(main)
