"""S6 fixture: send Error msg, then send Status(completed). Trust-order
rule 2 (gotError) should dominate rule 4 (gotCompletedStatus) — gotError
wins regardless of any later "completed" signal."""

from __future__ import annotations

import importlib
import sys

import runner as _runner

# Reach below runner.run() to hand-craft the message order. _send/_finish
# are internal — used here intentionally to exercise the trust-order ladder.
from runner import connect

runner_pb2 = importlib.import_module("gen.runner_pb2")

connect()
_runner._send(  # type: ignore[attr-defined]
    runner_pb2.ClientMessage(error=runner_pb2.Error(message="explicit error", traceback="")),
    _force=True,
)
_runner._send(  # type: ignore[attr-defined]
    runner_pb2.ClientMessage(status=runner_pb2.Status(state="completed")),
    _force=True,
)
_runner._finish()  # type: ignore[attr-defined]
sys.exit(0)
