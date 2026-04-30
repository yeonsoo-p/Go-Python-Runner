"""Unit tests for the runner helper library.

Tests that helper functions produce correct protobuf messages
by inspecting the message queue directly.
"""

from __future__ import annotations

import os
import sys
from typing import Any

import pytest

# Add _lib to path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import runner


def setup_function() -> None:
    """Reset module state before each test."""
    runner._msg_iter = runner._MessageIterator()
    runner._stream = None
    runner._stub = None
    runner._cache_refs = {}
    runner._cache_owned = set()
    runner._cancel_event = runner.threading.Event()
    runner._finished = False
    runner._reader_thread = None
    runner._reader_started = False
    runner._reader_error = None
    runner._response_queue = runner.queue.Queue()


def _prime_cache_create_response(*, error: str = "") -> None:
    """Set up runner module so cache_set's _recv_response returns a CacheInfo.

    Tests don't have a real gRPC stream, so we mark _stream as truthy and
    stub _start_reader_thread by flipping _reader_started to True. The fake
    response is preloaded onto _response_queue.
    """
    from gen import runner_pb2 as pb

    runner._stream = object()  # truthy; _recv_response only checks for None
    runner._reader_started = True  # short-circuit _start_reader_thread
    info = pb.CacheInfo(found=True, error=error)
    runner._response_queue.put(pb.ServerMessage(cache_info=info))


def _drain_messages(msg_iter: runner._MessageIterator, count: int) -> list[Any]:
    """Drain `count` messages from the iterator (thread-safe)."""
    messages = []
    with msg_iter._condition:
        for _ in range(count):
            if not msg_iter._queue:
                break
            messages.append(msg_iter._queue.pop(0))
    return messages


def test_output_sends_correct_message() -> None:
    runner.output("Hello, World!")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("output")
    assert msgs[0].output.text == "Hello, World!"


def test_progress_sends_correct_message() -> None:
    runner.progress(3, 10, "Processing")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("progress")
    assert msgs[0].progress.current == 3
    assert msgs[0].progress.total == 10
    assert msgs[0].progress.label == "Processing"


def test_complete_sends_status_completed() -> None:
    runner.complete()
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("status")
    assert msgs[0].status.state == "completed"


def test_fail_sends_error_and_status() -> None:
    runner.fail("something broke", "traceback here")
    msgs = _drain_messages(runner._msg_iter, 2)
    assert len(msgs) == 2
    assert msgs[0].HasField("error")
    assert msgs[0].error.message == "something broke"
    assert msgs[0].error.traceback == "traceback here"
    assert msgs[1].HasField("status")
    assert msgs[1].status.state == "failed"


def test_data_result_sends_bytes() -> None:
    runner.data_result("key1", b"\x00\x01\x02")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("data")
    assert msgs[0].data.key == "key1"
    assert msgs[0].data.value == b"\x00\x01\x02"


def test_output_converts_to_string() -> None:
    runner.output(42)
    msgs = _drain_messages(runner._msg_iter, 1)
    assert msgs[0].output.text == "42"


def test_multiple_outputs() -> None:
    runner.output("line 1")
    runner.output("line 2")
    runner.output("line 3")
    msgs = _drain_messages(runner._msg_iter, 3)
    assert len(msgs) == 3
    assert [m.output.text for m in msgs] == ["line 1", "line 2", "line 3"]


def test_cache_set_sends_cache_create_message() -> None:
    _prime_cache_create_response()
    runner.cache_set("test_key", {"hello": "world"})
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("cache_create")
    assert msgs[0].cache_create.key == "test_key"
    assert msgs[0].cache_create.size > 0
    assert msgs[0].cache_create.shm_name != ""
    # Verify the ref was stored for cleanup
    assert "test_key" in runner._cache_refs
    # Clean up shared memory
    runner._cache_refs["test_key"].close()
    runner._cache_refs["test_key"].unlink()
    del runner._cache_refs["test_key"]


def test_cache_set_raises_on_rejection() -> None:
    """When Go rejects a duplicate key, cache_set must release its just-created
    shm and raise — the script can't be allowed to keep working with the
    assumption that its data was shared."""
    _prime_cache_create_response(error='cache key "dup" already registered')
    with pytest.raises(RuntimeError, match="cache_set rejected"):
        runner.cache_set("dup", {"payload": 1})
    # Ref must NOT be stored — registration failed.
    assert "dup" not in runner._cache_refs
    assert "dup" not in runner._cache_owned


def test_cache_set_raises_without_connection() -> None:
    """cache_set must raise RuntimeError if connect() was never called."""
    with pytest.raises(RuntimeError, match="not connected"):
        runner.cache_set("k", {"x": 1})


def test_cache_release_sends_message() -> None:
    runner.cache_release("some_key")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("cache_release")
    assert msgs[0].cache_release.key == "some_key"


def test_cache_get_raises_without_connection() -> None:
    """cache_get must raise RuntimeError if connect() was never called."""

    with pytest.raises(RuntimeError, match="not connected"):
        runner.cache_get("any_key")


def test_dialog_open_raises_without_connection() -> None:
    """dialog_open must raise RuntimeError if connect() was never called."""

    with pytest.raises(RuntimeError, match="not connected"):
        runner.dialog_open()


def test_dialog_save_raises_without_connection() -> None:
    """dialog_save must raise RuntimeError if connect() was never called."""

    with pytest.raises(RuntimeError, match="not connected"):
        runner.dialog_save()


def test_progress_converts_types() -> None:
    """progress() should accept float-like values and convert them."""
    runner.progress(1.5, 10.0, "test")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert msgs[0].HasField("progress")
    assert msgs[0].progress.current == 1
    assert msgs[0].progress.total == 10


def test_is_cancelled_default_false() -> None:
    """is_cancelled() returns False when no cancel has been received."""
    assert runner.is_cancelled() is False


def test_is_cancelled_after_event_set() -> None:
    """is_cancelled() returns True after the cancel event is set."""
    runner._cancel_event.set()
    assert runner.is_cancelled() is True


def test_send_raises_on_cancel() -> None:
    """_send() raises CancelledError when the cancel event is set."""

    runner._cancel_event.set()
    with pytest.raises(runner.CancelledError):
        runner.output("should fail")


def test_finish_is_idempotent() -> None:
    """Calling _finish() twice does not raise."""
    runner._finish()
    runner._finish()  # second call is a no-op
    assert runner._finished is True


def test_send_noop_after_finish() -> None:
    """_send() silently drops messages after _finish()."""
    runner._finish()
    runner.output("should be dropped")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 0


def test_fail_works_after_cancel() -> None:
    """fail() bypasses cancel check so error+status are always delivered."""
    runner._cancel_event.set()
    runner.fail("cancelled by user", "no traceback")
    msgs = _drain_messages(runner._msg_iter, 2)
    assert len(msgs) == 2
    assert msgs[0].HasField("error")
    assert msgs[0].error.message == "cancelled by user"
    assert msgs[1].HasField("status")
    assert msgs[1].status.state == "failed"
