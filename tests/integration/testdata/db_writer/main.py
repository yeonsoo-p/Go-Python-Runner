"""S7 fixture: each invocation does a small INSERT into a stress table.
Used by TestConcurrentDbWrites to verify SQLite serialization under
parallel runs."""

from __future__ import annotations

from runner import complete, db_execute, run


def main(params: dict[str, str]) -> None:
    db_execute(
        "CREATE TABLE IF NOT EXISTS stress_writes (id INTEGER PRIMARY KEY AUTOINCREMENT, runner_label TEXT)",
    )
    label = params.get("label", "unlabeled")
    db_execute(
        "INSERT INTO stress_writes (runner_label) VALUES (?)",
        [label],
    )
    complete()


if __name__ == "__main__":
    run(main)
