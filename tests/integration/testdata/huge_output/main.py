"""S4 fixture: send a single very large output message. Size from params."""

from __future__ import annotations

from runner import complete, output, run


def main(params: dict[str, str]) -> None:
    size_mb = int(params.get("size_mb", "10"))
    payload = "x" * (size_mb * 1024 * 1024)
    output(payload)
    complete()


if __name__ == "__main__":
    run(main)
