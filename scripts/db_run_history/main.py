"""Run History demo — queries the runs table for past execution data."""

from __future__ import annotations

import os
import sys

sys.path.insert(0, os.path.join(os.path.dirname(__file__), "..", "_lib"))

from runner import complete, connect, db_query, fail, output, progress


def show_runs(limit: str, script_filter: str) -> None:
    progress(1, 3, "Querying runs")

    if script_filter:
        rows = db_query(
            "SELECT id, script_id, status, params, started_at, finished_at, exit_code, error_message "
            "FROM runs ORDER BY started_at DESC LIMIT ?",
            [limit],
        )
        # Filter in Python since we have simple needs
        rows = [r for r in rows if r["script_id"] == script_filter]
        output(f"=== Recent runs for script '{script_filter}' (limit {limit}) ===")
    else:
        rows = db_query(
            "SELECT id, script_id, status, params, started_at, finished_at, exit_code, error_message "
            "FROM runs ORDER BY started_at DESC LIMIT ?",
            [limit],
        )
        output(f"=== Recent runs (limit {limit}) ===")

    progress(2, 3, "Formatting results")

    if not rows:
        output("No runs recorded yet.")
        output("\nTip: Run some other scripts first, then check back here!")
    else:
        output(f"\n{'Run ID':<38}  {'Script':<16}  {'Status':<10}  {'Started':<20}  {'Exit'}")
        output("-" * 110)
        for r in rows:
            run_id = r["id"][:36] if len(r["id"]) > 36 else r["id"]
            exit_code = r["exit_code"] if r["exit_code"] else "-"
            started = r["started_at"] if r["started_at"] else "-"
            output(f"{run_id:<38}  {r['script_id']:<16}  {r['status']:<10}  {started:<20}  {exit_code}")

            if r["error_message"]:
                output(f"  Error: {r['error_message']}")

    # Aggregate stats
    progress(3, 3, "Computing statistics")
    output("\n=== Statistics ===")

    stats = db_query("SELECT status, COUNT(*) as cnt FROM runs GROUP BY status ORDER BY cnt DESC")
    if stats:
        for s in stats:
            output(f"  {s['status']}: {s['cnt']}")

        total_rows = db_query("SELECT COUNT(*) as total FROM runs")
        if total_rows:
            output(f"  Total: {total_rows[0]['total']}")
    else:
        output("  No statistics available (no runs recorded).")


def main(params: dict[str, str]) -> None:
    limit = params.get("limit", "20").strip()
    script_filter = params.get("script_filter", "").strip()

    try:
        int(limit)
    except ValueError:
        fail(f"Invalid limit value: {limit}. Must be a number.")
        return

    show_runs(limit, script_filter)
    complete()


if __name__ == "__main__":
    try:
        main(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))
