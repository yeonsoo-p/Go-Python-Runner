"""S6 fixture: call complete(), then crash with non-zero exit code.
Trust-order rule 1 (exit code) should dominate rule 4 (gotCompletedStatus)
and derive Failed."""

from __future__ import annotations

import os

from runner import complete, connect

connect()
complete()
# Process exits non-zero AFTER successfully sending Status(completed).
# Manager must trust the exit code, not Python's intent.
os._exit(1)
