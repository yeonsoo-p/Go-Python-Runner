"""S6 fixture: ignore SIGTERM and sleep, forcing Manager's force-kill grace
window to fire. After kill, exit code is non-zero → trust-order rule 1 →
Failed."""

from __future__ import annotations

import signal
import sys
import time

from runner import connect, output


def main(_params: dict[str, str]) -> None:
    # Ignore SIGTERM. Cancel grace window will then force-kill via
    # cmd.Process.Kill (SIGKILL on Unix, TerminateProcess on Windows),
    # which can't be caught.
    if hasattr(signal, "SIGTERM"):
        signal.signal(signal.SIGTERM, signal.SIG_IGN)

    output("ignoring SIGTERM, sleeping")
    sys.stdout.flush()
    # Sleep longer than cancelGracePeriod (3s) so force-kill must fire.
    time.sleep(15)
    # If we somehow get here, fail loudly.
    output("UNEXPECTED: SIGTERM ignore did not work")


if __name__ == "__main__":
    # Bypass runner.run() — we want NOT to convert the kill into fail();
    # we want the process to actually die from the OS-level kill so
    # waitForExit sees exit != 0.
    connect()
    main({})
