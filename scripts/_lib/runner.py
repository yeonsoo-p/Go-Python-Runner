"""Python helper library for go-python-runner scripts.

Wraps the generated gRPC client, providing a simple API for scripts
to send output, progress, status, errors, and cache data back to the Go backend.
"""

from __future__ import annotations

import atexit
import contextlib
import importlib
import os
import pickle
import queue
import sys
import threading
import traceback
from collections.abc import Callable
from multiprocessing import shared_memory
from typing import Any

import grpc

# Ensure _lib dir is importable so `gen` package resolves
_lib_dir = os.path.dirname(os.path.abspath(__file__))
if _lib_dir not in sys.path:
    sys.path.insert(0, _lib_dir)

# Late-import generated code (path must be set first)
runner_pb2 = importlib.import_module("gen.runner_pb2")
runner_pb2_grpc = importlib.import_module("gen.runner_pb2_grpc")

# Status constants — must match Go RunStatus values.
# STATUS_RUNNING / STATUS_CANCELLED are included for symmetry with Go/TS even
# though Python never emits them. Manager is the authoritative status source
# (see CLAUDE.md "Frontend shows, Go manages, Python does"). Cancellation is
# observed by Python via is_cancelled() / CancelledError, but the terminal
# status itself is set on the Go side when CancelRun was invoked.
STATUS_RUNNING = "running"
STATUS_COMPLETED = "completed"
STATUS_FAILED = "failed"
STATUS_CANCELLED = "cancelled"

# Severity constants — must match proto runner.Severity enum values and Go
# notify.Severity ordering. Used by fail()/error()/warn()/info() to classify
# Error-message notifications for the central reservoir.
SEVERITY_UNSPECIFIED = 0
SEVERITY_INFO = 1
SEVERITY_WARN = 2
SEVERITY_ERROR = 3
SEVERITY_CRITICAL = 4

# Module-level state
_stub: Any = None
_stream: Any = None
_cache_refs: dict[str, shared_memory.SharedMemory] = {}
_cache_owned: set[str] = set()  # keys created by this process (need unlink on cleanup)
_run_id: str | None = None
_cancel_event = threading.Event()
_finished = False
_reader_thread: threading.Thread | None = None
_request_lock = threading.Lock()  # serializes send+recv pairs for thread safety


class CancelledError(Exception):
    """Raised when the script has been cancelled by the backend."""


class _MessageIterator:
    """Thread-safe iterator that yields ClientMessage objects.

    Used as the request iterator for the bidirectional gRPC stream.
    """

    def __init__(self) -> None:
        self._queue: list[Any] = []
        self._condition = threading.Condition()
        self._done = False

    def __iter__(self) -> _MessageIterator:
        return self

    def __next__(self) -> Any:
        with self._condition:
            while not self._queue and not self._done:
                self._condition.wait()
            if self._queue:
                return self._queue.pop(0)
            raise StopIteration

    def put(self, msg: Any) -> None:
        with self._condition:
            self._queue.append(msg)
            self._condition.notify()

    def close(self) -> None:
        with self._condition:
            self._done = True
            self._condition.notify()


# Global message iterator for sending messages
_msg_iter = _MessageIterator()


def _send(msg: Any, *, _force: bool = False) -> None:
    """Send a ClientMessage through the gRPC stream."""
    if _finished and not _force:
        return
    if not _force and _cancel_event.is_set():
        raise CancelledError("Script cancelled by backend")
    _msg_iter.put(msg)


# Timeout for blocking server responses (cache_get, dialogs, db queries).
_RESPONSE_TIMEOUT = 30  # seconds

# Persistent reader thread and queue for server responses.
# This avoids the bug where a ThreadPoolExecutor timeout leaves an orphaned
# thread consuming the next message from the stream, desynchronizing the protocol.
_response_queue: queue.Queue[Any] = queue.Queue()
_reader_error: Any = None  # set if the reader thread encounters an error
_reader_started = False


def _start_reader_thread() -> None:
    """Start a daemon thread that reads server messages into a queue."""
    global _reader_started, _reader_thread
    if _reader_started:
        return
    _reader_started = True

    def _reader() -> None:
        global _reader_error
        try:
            for msg in _stream:
                if msg.HasField("cancel"):
                    _cancel_event.set()
                else:
                    _response_queue.put(msg)
        except Exception as e:
            _reader_error = e
        finally:
            # Sentinel to unblock any waiting get()
            _response_queue.put(None)

    _reader_thread = threading.Thread(target=_reader, daemon=True)
    _reader_thread.start()


def _recv_response(operation: str = "server response", timeout: float = _RESPONSE_TIMEOUT) -> Any:
    """Receive next server message with timeout to prevent deadlocks."""
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")
    _start_reader_thread()
    try:
        msg = _response_queue.get(timeout=timeout)
    except queue.Empty:
        raise RuntimeError(
            f"Timed out waiting for {operation} after {timeout}s (possible deadlock in bidirectional stream)"
        ) from None
    if msg is None:
        if _reader_error is not None:
            raise RuntimeError(f"Server stream error while waiting for {operation}: {_reader_error}") from None
        raise RuntimeError(f"Server closed stream unexpectedly while waiting for {operation}")
    return msg


def connect() -> dict[str, str]:
    """Connect to the Go gRPC server and establish a bidirectional stream.

    Reads GRPC_ADDRESS and RUN_ID from environment variables.
    Returns the params dict from the StartRequest.
    """
    global _stub, _stream, _run_id

    addr = os.environ.get("GRPC_ADDRESS")
    if not addr:
        raise RuntimeError("GRPC_ADDRESS environment variable not set")

    _run_id = os.environ.get("RUN_ID", "unknown")

    channel = grpc.insecure_channel(addr)
    _stub = runner_pb2_grpc.PythonRunnerStub(channel)

    # Create bidirectional stream with run-id metadata
    metadata = [("run-id", _run_id)]
    _stream = _stub.Execute(_msg_iter, metadata=metadata)

    # Wait for StartRequest from Go
    try:
        start_msg = next(_stream)
    except (StopIteration, grpc.RpcError) as e:
        raise RuntimeError(f"Lost connection while waiting for StartRequest: {e}") from e
    if not start_msg.HasField("start"):
        raise RuntimeError(f"Expected StartRequest, got {start_msg}")

    # Start reader thread so CancelRequest is intercepted for all scripts,
    # not just those that call _recv_response() (cache_get, db_query, etc.).
    _start_reader_thread()

    return dict(start_msg.start.params)


def output(text: object) -> None:
    """Send an output message to Go."""
    _send(runner_pb2.ClientMessage(output=runner_pb2.Output(text=str(text))))


def progress(current: int, total: int, label: str = "") -> None:
    """Send a progress update to Go."""
    _send(
        runner_pb2.ClientMessage(
            progress=runner_pb2.Progress(
                current=int(current),
                total=int(total),
                label=str(label),
            )
        )
    )


def complete() -> None:
    """Send a completed status to Go and close the stream."""
    _send(runner_pb2.ClientMessage(status=runner_pb2.Status(state=STATUS_COMPLETED)))
    _finish()


def fail(message: str, tb: str | None = None, *, severity: int = SEVERITY_ERROR) -> None:
    """Send an error and failed status to Go, terminating the run.

    When called without an explicit ``tb`` outside an ``except`` block,
    ``traceback.format_exc()`` returns the literal string ``"NoneType: None\\n"``
    — meaningless metadata that misleadingly suggests an unhandled exception
    occurred. Treat that as "no traceback available" so Go logs and the
    LogViewer don't display the noise. Validation-style ``fail("bad input")``
    calls now produce an empty traceback, which is the truth.

    The ``severity`` defaults to SEVERITY_ERROR. Pass SEVERITY_CRITICAL for
    failures that should surface as a full-screen catastrophic pane on the
    frontend (e.g. unrecoverable resource exhaustion).
    """
    if tb is None:
        captured = traceback.format_exc()
        tb = "" if captured.strip() == "NoneType: None" else captured
    _send(
        runner_pb2.ClientMessage(
            error=runner_pb2.Error(
                message=str(message),
                traceback=str(tb),
                severity=severity,
            )
        ),
        _force=True,
    )
    _send(runner_pb2.ClientMessage(status=runner_pb2.Status(state=STATUS_FAILED)), _force=True)
    _finish()


def warn(message: str) -> None:
    """Emit a structured warning notification to Go without terminating the run.

    The Go reservoir routes this to slog.Warn + the LogViewer. Use for
    recoverable issues the user/operator should know about but that don't
    invalidate the run's results (e.g. "row 7 had a malformed value, skipped").
    """
    _send(runner_pb2.ClientMessage(error=runner_pb2.Error(message=str(message), severity=SEVERITY_WARN)))


def info(message: str) -> None:
    """Emit a structured informational notification to Go.

    Distinct from ``output()``: ``output()`` is part of the script's user-facing
    text stream rendered in the run pane; ``info()`` is metadata for the
    LogViewer / log file (e.g. "loaded 12 rows from cache"). Use ``output()``
    for results, ``info()`` for trace.
    """
    _send(runner_pb2.ClientMessage(error=runner_pb2.Error(message=str(message), severity=SEVERITY_INFO)))


def _finish() -> None:
    """Close send stream and block until Go confirms receipt (stream EOF)."""
    global _finished
    if _finished:
        return
    _finished = True
    _msg_iter.close()
    # Wait for the reader thread to finish draining the server stream.
    # Closing the iterator signals gRPC to close the client→server half;
    # the server then closes its side, and the reader thread exits.
    if _reader_thread is not None:
        _reader_thread.join(timeout=5)


def run(main_func: Callable[[dict[str, str]], None]) -> None:
    """Standard entrypoint wrapper.

    Connects to the Go backend, calls ``main_func`` with the params dict,
    and translates KeyboardInterrupt / SystemExit / Exception into a
    structured ``fail()``. Scripts should use this instead of inlining their
    own try/except in ``__main__``.

    Example:
        from runner import run, output, complete

        def main(params):
            output("hello")
            complete()

        if __name__ == "__main__":
            run(main)
    """
    try:
        main_func(connect())
    except (KeyboardInterrupt, SystemExit):
        fail("cancelled")
    except Exception as e:
        fail(str(e))


def is_cancelled() -> bool:
    """Check if the Go backend has requested cancellation.

    Scripts should poll this periodically in long-running loops
    and exit gracefully when True.
    """
    return _cancel_event.is_set()


# --- Shared Cache API ---


def cache_set(key: str, obj: object) -> None:
    """Pickle any Python object into a shared memory block and register with Go.

    ``track=False`` opts out of ``multiprocessing.resource_tracker``. Go's
    ``CacheManager.CleanupRun`` is the canonical lifecycle authority (see
    `_cleanup_cache`); leaving the tracker on causes any process that *opens*
    this block to unlink it from ``/dev/shm`` on exit, killing the segment
    while the producer is still alive. ``_cleanup_cache`` and Go provide the
    backstop unlink for owned keys.

    Synchronous: blocks on Go's CacheCreateResponse ack. Go rejects when the
    key is already registered (overwriting would orphan the prior block and
    silently drop every other run's ref). On rejection we close+unlink our
    just-created shm and raise — the script must learn that its data isn't
    actually shared.
    """
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    data = pickle.dumps(obj, protocol=pickle.HIGHEST_PROTOCOL)
    shm = shared_memory.SharedMemory(create=True, size=len(data), track=False)
    registered = False
    try:
        if shm.buf is None:
            raise RuntimeError("SharedMemory buffer is None")
        shm.buf[: len(data)] = data

        with _request_lock:
            _send(
                runner_pb2.ClientMessage(
                    cache_create=runner_pb2.CacheCreateRequest(
                        key=str(key),
                        size=len(data),
                        shm_name=shm.name,
                    )
                )
            )
            try:
                resp = _recv_response("cache create")
            except grpc.RpcError as e:
                raise RuntimeError(f"Lost connection while waiting for cache create ack: {e}") from e
            if not resp.HasField("cache_create_response"):
                raise RuntimeError(f"Expected CacheCreateResponse response to cache_create, got {resp}")

        ack = resp.cache_create_response
        if ack.error_code != runner_pb2.CACHE_CREATE_OK:
            detail = ack.error_message or runner_pb2.CacheCreateError.Name(ack.error_code)
            raise RuntimeError(f"cache_set rejected: {detail}")
        registered = True
    finally:
        if not registered:
            with contextlib.suppress(OSError):
                shm.close()
            with contextlib.suppress(OSError):
                shm.unlink()

    _cache_refs[key] = shm
    _cache_owned.add(key)


def cache_get(key: str) -> object:
    """Retrieve any Python object from shared memory via Go lookup."""
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    # Lock ensures no other thread can interleave its own send+recv on the shared stream.
    with _request_lock:
        _send(runner_pb2.ClientMessage(cache_lookup=runner_pb2.CacheLookupRequest(key=str(key))))

        try:
            resp = _recv_response("cache lookup")
        except grpc.RpcError as e:
            raise RuntimeError(f"Lost connection while waiting for cache lookup: {e}") from e
        if not resp.HasField("cache_lookup_response"):
            raise RuntimeError(f"Expected CacheLookupResponse, got {resp}")

        info = resp.cache_lookup_response
        if not info.found:
            raise KeyError(f"Cache key not found: {key}")

    try:
        shm = shared_memory.SharedMemory(name=info.shm_name, track=False)
    except FileNotFoundError:
        raise KeyError(
            f"Cache key '{key}' found in registry but shared memory '{info.shm_name}' "
            f"is no longer available (reclaimed by OS after owning process exited)"
        ) from None
    if shm.buf is None:
        raise RuntimeError("SharedMemory buffer is None")
    obj = pickle.loads(bytes(shm.buf[: info.size]))
    _cache_refs[key] = shm
    return obj


def cache_release(key: str) -> None:
    """Release local reference to a shared memory block."""
    if key in _cache_refs:
        shm = _cache_refs.pop(key)
        with contextlib.suppress(OSError):
            shm.close()
        if key in _cache_owned:
            with contextlib.suppress(OSError):
                shm.unlink()
            _cache_owned.discard(key)

    _send(runner_pb2.ClientMessage(cache_release=runner_pb2.CacheRelease(key=str(key))))


# --- File Dialog API ---


def _unwrap_dialog_response(resp: Any) -> str | None:
    """Map a FileDialogResponse oneof outcome to the dialog API's return shape.

    Selected → first path. Cancelled → None. Error → raise RuntimeError (Go has
    already reported it via reservoir.Report; the raise is local control flow
    so callers can branch on success vs. OS failure).
    """
    outcome = resp.WhichOneof("outcome")
    if outcome == "selected":
        paths = resp.selected.paths
        return str(paths[0]) if paths else None
    if outcome == "cancelled":
        return None
    if outcome == "error":
        raise RuntimeError(f"File dialog error: {resp.error}")
    # No outcome set is treated as cancelled — defensive against schema drift.
    return None


def dialog_open(
    title: str = "",
    directory: str = "",
    filters: list[tuple[str, str]] | None = None,
) -> str | None:
    """Open a native file picker dialog. Blocks until the user selects or cancels.

    Args:
        title: Dialog window title.
        directory: Initial directory to show.
        filters: List of (display_name, pattern) tuples, e.g. [("Text Files", "*.txt")].

    Returns:
        Absolute file path, or None if the user cancelled.
    """
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    proto_filters = [runner_pb2.FileFilter(display_name=dn, pattern=pat) for dn, pat in (filters or [])]
    with _request_lock:
        _send(
            runner_pb2.ClientMessage(
                file_dialog=runner_pb2.FileDialogRequest(
                    kind=runner_pb2.DIALOG_KIND_OPEN,
                    title=title,
                    directory=directory,
                    filters=proto_filters,
                )
            )
        )
        try:
            resp = _recv_response("file dialog")
        except grpc.RpcError as e:
            raise RuntimeError(f"Lost connection while waiting for file dialog response: {e}") from e
        if not resp.HasField("file_dialog_response"):
            raise RuntimeError(f"Expected FileDialogResponse, got {resp}")
    return _unwrap_dialog_response(resp.file_dialog_response)


def dialog_save(
    title: str = "",
    directory: str = "",
    filename: str = "",
    filters: list[tuple[str, str]] | None = None,
) -> str | None:
    """Open a native save-file dialog. Blocks until the user selects or cancels.

    Args:
        title: Dialog message text.
        directory: Initial directory to show.
        filename: Suggested file name.
        filters: List of (display_name, pattern) tuples.

    Returns:
        Absolute file path chosen by the user, or None if cancelled.
    """
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    proto_filters = [runner_pb2.FileFilter(display_name=dn, pattern=pat) for dn, pat in (filters or [])]
    with _request_lock:
        _send(
            runner_pb2.ClientMessage(
                file_dialog=runner_pb2.FileDialogRequest(
                    kind=runner_pb2.DIALOG_KIND_SAVE,
                    title=title,
                    directory=directory,
                    filename=filename,
                    filters=proto_filters,
                )
            )
        )
        try:
            resp = _recv_response("save dialog")
        except grpc.RpcError as e:
            raise RuntimeError(f"Lost connection while waiting for file dialog response: {e}") from e
        if not resp.HasField("file_dialog_response"):
            raise RuntimeError(f"Expected FileDialogResponse, got {resp}")
    return _unwrap_dialog_response(resp.file_dialog_response)


# --- Database API ---


def db_execute(sql: str, params: list[str] | None = None) -> dict[str, int]:
    """Execute a write SQL statement (INSERT, UPDATE, DELETE, CREATE TABLE, etc.).

    Args:
        sql: SQL statement with ? placeholders for parameters.
        params: List of string values for positional ? placeholders.

    Returns:
        Dict with 'rows_affected' (int) and 'last_insert_id' (int).

    Raises:
        RuntimeError: If not connected or if the SQL execution fails.
    """
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    with _request_lock:
        _send(
            runner_pb2.ClientMessage(
                db_execute=runner_pb2.DbExecuteRequest(
                    sql=sql,
                    params=params or [],
                )
            )
        )

        try:
            resp = _recv_response("db execute")
        except grpc.RpcError as e:
            raise RuntimeError(f"Lost connection while waiting for db execute response: {e}") from e
        if not resp.HasField("db_execute_response"):
            raise RuntimeError(f"Expected DbExecuteResponse, got {resp}")

    if resp.db_execute_response.error:
        raise RuntimeError(f"SQL error: {resp.db_execute_response.error}")

    return {
        "rows_affected": resp.db_execute_response.rows_affected,
        "last_insert_id": resp.db_execute_response.last_insert_id,
    }


def db_query(sql: str, params: list[str] | None = None) -> list[dict[str, str]]:
    """Execute a read SQL query (SELECT).

    Args:
        sql: SQL query with ? placeholders for parameters.
        params: List of string values for positional ? placeholders.

    Returns:
        List of dicts, one per row, with column names as keys and string values.

    Raises:
        RuntimeError: If not connected or if the SQL query fails.
    """
    if _stream is None:
        raise RuntimeError("not connected — call connect() first")

    with _request_lock:
        _send(
            runner_pb2.ClientMessage(
                db_query=runner_pb2.DbQueryRequest(
                    sql=sql,
                    params=params or [],
                )
            )
        )

        try:
            resp = _recv_response("db query")
        except grpc.RpcError as e:
            raise RuntimeError(f"Lost connection while waiting for db query response: {e}") from e
        if not resp.HasField("db_query_response"):
            raise RuntimeError(f"Expected DbQueryResponse, got {resp}")

    if resp.db_query_response.error:
        raise RuntimeError(f"SQL error: {resp.db_query_response.error}")

    columns = list(resp.db_query_response.columns)
    return [dict(zip(columns, row.values, strict=True)) for row in resp.db_query_response.rows]


def _cleanup_cache() -> None:
    """Close shared memory references on graceful exit.

    BEST-EFFORT cleanup. ``atexit`` only fires on normal interpreter shutdown —
    not on ``os._exit()``, ``SIGKILL``, or ``CancelRun()`` from Go (which kills
    the process via ``cmd.Process.Kill()``).

    The CANONICAL authority for cache lifecycle is Go's ``CacheManager.CleanupRun``,
    which runs in ``Manager.waitForExit`` for every terminal status (graceful and
    forced). On Linux it also calls ``shm_unlink`` so dead-process blocks don't
    leak in ``/dev/shm/``.

    Both paths are idempotent: Go's CleanupRun finding zero refs is a no-op;
    Python's atexit running after Go already unlinked is also a no-op.
    """
    for key, shm in _cache_refs.items():
        with contextlib.suppress(OSError):
            shm.close()
        if key in _cache_owned:
            with contextlib.suppress(OSError):
                shm.unlink()
    _cache_refs.clear()
    _cache_owned.clear()


atexit.register(_cleanup_cache)
