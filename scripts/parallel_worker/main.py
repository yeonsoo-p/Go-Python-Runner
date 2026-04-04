"""Parallel Worker — single worker process that caches and shares data.

Each invocation is one worker with its own Go goroutines and gRPC stream.
Launch multiple instances to see parallel processes sharing data via the
shared memory cache. Example:
  1. Start "Alpha" (defaults) — computes, caches, holds handle
  2. Start "Beta" with read_from=Alpha — computes, caches, reads Alpha's data
"""

from __future__ import annotations

import os
import sys
import time

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "_lib"))

import numpy as np

from runner import (
    cache_get,
    cache_release,
    cache_set,
    complete,
    connect,
    fail,
    is_cancelled,
    output,
    progress,
)

_TASKS = [
    "Collecting samples",
    "Crunching numbers",
    "Validating results",
    "Indexing records",
    "Compressing data",
]


def main(params: dict[str, str]) -> None:
    name = params.get("worker_name", "Alpha")

    try:
        steps = int(params.get("steps", "5"))
    except ValueError:
        fail(f"[{name}] 'steps' must be an integer, got: {params.get('steps')}")
        return
    if steps < 1:
        fail(f"[{name}] 'steps' must be >= 1")
        return

    try:
        delay = float(params.get("delay", "0.8"))
    except ValueError:
        fail(f"[{name}] 'delay' must be a number, got: {params.get('delay')}")
        return
    if delay < 0:
        fail(f"[{name}] 'delay' must be >= 0")
        return

    try:
        array_size = int(params.get("array_size", "1000"))
    except ValueError:
        fail(f"[{name}] 'array_size' must be an integer, got: {params.get('array_size')}")
        return
    if array_size < 1:
        fail(f"[{name}] 'array_size' must be >= 1")
        return

    read_from = params.get("read_from", "").strip()

    try:
        hold_seconds = float(params.get("hold_seconds", "30"))
    except ValueError:
        fail(f"[{name}] 'hold_seconds' must be a number, got: {params.get('hold_seconds')}")
        return
    if hold_seconds < 0:
        fail(f"[{name}] 'hold_seconds' must be >= 0")
        return

    total = steps + 1 + (1 if read_from else 0) + 1  # steps + produce + consume? + hold
    done = 0

    output(f"[{name}] Starting — {steps} steps, then cache phase")

    # Phase 1: simulated work steps
    for step in range(1, steps + 1):
        if is_cancelled():
            output(f"[{name}] Cancelled at step {step}/{steps}")
            fail(f"[{name}] Cancelled by user")
            return

        task_label = _TASKS[(step - 1) % len(_TASKS)]
        done += 1
        progress(done, total, f"[{name}] {task_label}")
        output(f"[{name}] Step {step}/{steps}: {task_label}")

        remaining = delay
        while remaining > 0:
            chunk = min(remaining, 0.25)
            time.sleep(chunk)
            remaining -= chunk
            if is_cancelled():
                output(f"[{name}] Cancelled during step {step}/{steps}")
                fail(f"[{name}] Cancelled by user")
                return

    # Phase 2: produce — generate data and cache it
    arr = np.random.default_rng().random(array_size)
    cache_key = f"pw_{name}"
    cache_set(cache_key, arr)
    done += 1
    progress(done, total, f"[{name}] Cached data")
    output(f"[{name}] Cached {array_size} values as '{cache_key}' (sum={arr.sum():.1f})")

    # Phase 3: consume — read another worker's cached data (if requested)
    if read_from:
        read_key = f"pw_{read_from}"
        output(f"[{name}] Reading '{read_key}' from shared cache...")
        try:
            other = cache_get(read_key)
        except KeyError:
            output(f"[{name}] Key '{read_key}' not found — is {read_from} still running?")
            done += 1
            progress(done, total, f"[{name}] Cache miss")
        except RuntimeError as e:
            output(f"[{name}] Failed to read cache: {e}")
            done += 1
            progress(done, total, f"[{name}] Cache error")
        else:
            other_arr = np.asarray(other)
            output(f"[{name}] Got {read_from}'s data: {len(other_arr)} values, sum={other_arr.sum():.1f}")
            combined = arr.sum() + other_arr.sum()
            output(f"[{name}] Combined ({name} + {read_from}) = {combined:.1f}")
            done += 1
            progress(done, total, f"[{name}] Shared result")

    # Phase 4: hold cache handle alive so other workers can read it
    output(f"[{name}] Holding cache for {hold_seconds:.0f}s (other workers can read '{cache_key}')")
    remaining = hold_seconds
    while remaining > 0:
        chunk = min(remaining, 0.5)
        time.sleep(chunk)
        remaining -= chunk
        if is_cancelled():
            output(f"[{name}] Cancelled during hold phase")
            cache_release(cache_key)
            fail(f"[{name}] Cancelled by user")
            return

    done += 1
    progress(done, total, f"[{name}] Done")
    cache_release(cache_key)
    output(f"[{name}] Released cache and finished")
    complete()


if __name__ == "__main__":
    try:
        main(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))
