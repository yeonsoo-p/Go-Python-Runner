"""Key-Value Store demo — read/write operations on the built-in kv table."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "_lib"))

from runner import complete, connect, db_execute, db_query, fail, output, progress


def list_all() -> None:
    rows = db_query("SELECT key, value, updated_at FROM kv ORDER BY key")
    if not rows:
        output("Key-value store is empty.")
        return
    output(f"{'Key':<20}  {'Value':<30}  {'Updated'}")
    output("-" * 75)
    for r in rows:
        output(f"{r['key']:<20}  {r['value']:<30}  {r['updated_at']}")


def get_key(key: str) -> None:
    rows = db_query("SELECT value, updated_at FROM kv WHERE key = ?", [key])
    if not rows:
        output(f"Key not found: {key}")
        return
    output(f"{key} = {rows[0]['value']}  (updated: {rows[0]['updated_at']})")


def set_key(key: str, value: str) -> None:
    db_execute(
        "INSERT INTO kv (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP) "
        "ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP",
        [key, value],
    )
    output(f"Set {key} = {value}")


def demo() -> None:
    sample_data = {
        "app.theme": "dark",
        "app.language": "en",
        "app.font_size": "14",
        "user.name": "Alice",
        "user.role": "admin",
    }

    # Insert sample key-value pairs
    output("=== Writing key-value pairs ===")
    total = len(sample_data)
    for i, (key, value) in enumerate(sample_data.items()):
        set_key(key, value)
        progress(i + 1, total, f"Set: {key}")

    # List all
    output("\n=== All entries ===")
    list_all()

    # Update one
    output("\n=== Updating app.theme to 'light' ===")
    set_key("app.theme", "light")

    # Read it back
    output("\n=== Reading back ===")
    get_key("app.theme")

    # Delete one
    output("\n=== Deleting user.role ===")
    result = db_execute("DELETE FROM kv WHERE key = ?", ["user.role"])
    output(f"Deleted {result['rows_affected']} entry.")

    # Final state
    output("\n=== Final state ===")
    list_all()


def main(params: dict[str, str]) -> None:
    action = params.get("action", "demo").strip().lower()

    if action == "demo":
        demo()
    elif action == "list":
        list_all()
    elif action == "get":
        key = params.get("key", "").strip()
        if not key:
            fail("'key' parameter is required for get action.")
            return
        get_key(key)
    elif action == "set":
        key = params.get("key", "").strip()
        value = params.get("value", "").strip()
        if not key:
            fail("'key' parameter is required for set action.")
            return
        set_key(key, value)
    else:
        fail(f"Unknown action: {action}. Use demo, list, get, or set.")
        return

    complete()


if __name__ == "__main__":
    try:
        main(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))
