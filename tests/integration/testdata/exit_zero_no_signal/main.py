"""S6 fixture: connect via runner module load, then exit 0 without ever
calling complete() or fail(). Trust-order rule 5 should derive Failed."""

from __future__ import annotations

import os
import sys

# Importing connect makes Python attach the gRPC stream — exercising the path
# AFTER connect, before any complete/fail signal.
from runner import connect

connect()
# Skip the runner.run() wrapper so no try/except converts our exit into fail().
sys.stdout.flush()
os._exit(0)
