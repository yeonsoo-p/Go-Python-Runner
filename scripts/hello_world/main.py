"""Hello World script — simple greeting with progress reporting."""

from __future__ import annotations

from runner import complete, output, progress, run


def main(params: dict[str, str]) -> None:
    name = params.get("name", "World")
    output(f"Hello, {name}!")
    for i in range(5):
        progress(i + 1, 5, "Working")
    complete()


if __name__ == "__main__":
    run(main)
