"""File Export — writes data to a user-chosen file via native save dialog.

Demonstrates the file dialog API: Python requests a native OS save dialog
through gRPC → Go → Wails, gets back the full file path, and writes directly.
No browser sandbox, no temporary copies.
"""

from __future__ import annotations

import csv
import io

from runner import complete, dialog_save, fail, output, progress, run


def main(params: dict[str, str]) -> None:
    raw = params.get("data", "hello,world,foo,bar,42")
    values = [v.strip() for v in raw.split(",")]

    progress(1, 3, "Preparing data")
    output(f"Data to export: {values}")

    progress(2, 3, "Choosing save location")
    path = dialog_save(
        title="Export Data",
        filename="export.csv",
        filters=[("CSV Files", "*.csv"), ("Text Files", "*.txt")],
    )

    if path is None:
        output("Export cancelled by user")
        complete()
        return

    progress(3, 3, "Writing file")
    buf = io.StringIO()
    writer = csv.writer(buf)
    writer.writerow(["index", "value"])
    for i, v in enumerate(values):
        writer.writerow([i, v])

    try:
        with open(path, "w", newline="", encoding="utf-8") as f:
            f.write(buf.getvalue())
    except OSError as e:
        fail(f"Failed to write file '{path}': {e}")
        return

    output(f"Exported {len(values)} values to {path}")
    complete()


if __name__ == "__main__":
    run(main)
