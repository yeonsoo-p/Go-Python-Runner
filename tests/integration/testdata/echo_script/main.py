"""Echo script — echoes params back as output with progress."""

from __future__ import annotations

from runner import complete, output, progress, run


def main(params: dict[str, str]) -> None:
    message = params.get("message", "hello")
    output(f"echo: {message}")
    for i in range(3):
        progress(i + 1, 3, "echoing")
    complete()


if __name__ == "__main__":
    run(main)
