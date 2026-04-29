"""S4 fixture: send N progress messages back-to-back. N comes from params."""

from __future__ import annotations

from runner import complete, progress, run


def main(params: dict[str, str]) -> None:
    n = int(params.get("count", "1000"))
    for i in range(n):
        progress(i + 1, n, "burst")
    complete()


if __name__ == "__main__":
    run(main)
