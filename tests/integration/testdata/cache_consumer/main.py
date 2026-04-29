"""Cache consumer — retrieves a dict from shared memory cache and outputs it."""

from __future__ import annotations

import json

from runner import cache_get, complete, output, run


def main(_params: dict[str, str]) -> None:
    data = cache_get("shared_data")
    output(f"retrieved:{json.dumps(data, sort_keys=True)}")
    complete()


if __name__ == "__main__":
    run(main)
