"""Todo List demo — manages tasks in SQLite via gRPC database access."""

from __future__ import annotations

from runner import complete, db_execute, db_query, fail, output, progress, run

SAMPLE_TASKS = [
    "Buy groceries",
    "Write unit tests",
    "Review pull request",
    "Update documentation",
    "Deploy to staging",
]


def ensure_table() -> None:
    db_execute(
        "CREATE TABLE IF NOT EXISTS todos ("
        "  id INTEGER PRIMARY KEY AUTOINCREMENT,"
        "  title TEXT NOT NULL,"
        "  done INTEGER NOT NULL DEFAULT 0,"
        "  created_at DATETIME DEFAULT CURRENT_TIMESTAMP"
        ")"
    )


def list_todos() -> None:
    rows = db_query("SELECT id, title, done, created_at FROM todos ORDER BY id")
    if not rows:
        output("No todos found.")
        return
    output(f"{'ID':>4}  {'Status':<8}  {'Title':<30}  {'Created'}")
    output("-" * 70)
    for r in rows:
        status = "done" if r["done"] == "1" else "pending"
        output(f"{r['id']:>4}  {status:<8}  {r['title']:<30}  {r['created_at']}")


def demo() -> None:
    ensure_table()

    # Insert sample tasks
    output("=== Inserting tasks ===")
    for i, title in enumerate(SAMPLE_TASKS):
        db_execute("INSERT INTO todos (title) VALUES (?)", [title])
        progress(i + 1, len(SAMPLE_TASKS), f"Added: {title}")
        output(f"  + {title}")

    # List all
    output("\n=== All todos ===")
    list_todos()

    # Mark first two as done
    output("\n=== Completing first two tasks ===")
    db_execute("UPDATE todos SET done = 1 WHERE title = ?", [SAMPLE_TASKS[0]])
    output(f"  Completed: {SAMPLE_TASKS[0]}")
    db_execute("UPDATE todos SET done = 1 WHERE title = ?", [SAMPLE_TASKS[1]])
    output(f"  Completed: {SAMPLE_TASKS[1]}")

    # Show updated state
    output("\n=== Updated todos ===")
    list_todos()

    # Delete completed
    result = db_execute("DELETE FROM todos WHERE done = 1")
    output(f"\n=== Deleted {result['rows_affected']} completed task(s) ===")

    # Final state
    output("\n=== Remaining todos ===")
    list_todos()


def cleanup() -> None:
    db_execute("DROP TABLE IF EXISTS todos")
    output("Dropped todos table.")


def main(params: dict[str, str]) -> None:
    action = params.get("action", "demo").strip().lower()

    if action == "demo":
        demo()
    elif action == "list":
        ensure_table()
        list_todos()
    elif action == "cleanup":
        cleanup()
    else:
        fail(f"Unknown action: {action}. Use demo, list, or cleanup.")
        return

    complete()


if __name__ == "__main__":
    run(main)
