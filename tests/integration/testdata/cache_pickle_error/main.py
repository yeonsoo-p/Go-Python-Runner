"""S5 fixture: try to cache_set a non-picklable object. fail() should
propagate a clear error."""

from __future__ import annotations

import threading

from runner import cache_set, complete, run


def main(_params: dict[str, str]) -> None:
    # threading.Lock is not picklable.
    not_picklable = threading.Lock()
    # cache_set will raise during pickle.dumps. runner.run() converts that
    # into a structured fail() with the exception message.
    cache_set("bad", not_picklable)
    complete()  # unreachable


if __name__ == "__main__":
    run(main)
