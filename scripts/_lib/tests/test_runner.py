"""Unit tests for the runner helper library.

Tests that helper functions produce correct protobuf messages
by inspecting the message queue directly.
"""

from __future__ import annotations

import os
import sys

# Add _lib to path
sys.path.insert(0, os.path.join(os.path.dirname(__file__), ".."))

import runner


def setup_function() -> None:
    """Reset module state before each test."""
    runner._msg_iter = runner._MessageIterator()
    runner._stream = None
    runner._stub = None
    runner._cache_refs = {}


def _drain_messages(msg_iter: runner._MessageIterator, count: int) -> list[object]:
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


def test_cache_release_sends_message() -> None:
    runner.cache_release("some_key")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert len(msgs) == 1
    assert msgs[0].HasField("cache_release")
    assert msgs[0].cache_release.key == "some_key"


def test_cache_get_raises_without_connection() -> None:
    """cache_get must raise RuntimeError if connect() was never called."""
    import pytest

    with pytest.raises(RuntimeError, match="not connected"):
        runner.cache_get("any_key")


def test_open_file_dialog_raises_without_connection() -> None:
    """open_file_dialog must raise RuntimeError if connect() was never called."""
    import pytest

    with pytest.raises(RuntimeError, match="not connected"):
        runner.open_file_dialog()


def test_save_file_dialog_raises_without_connection() -> None:
    """save_file_dialog must raise RuntimeError if connect() was never called."""
    import pytest

    with pytest.raises(RuntimeError, match="not connected"):
        runner.save_file_dialog()


def test_progress_converts_types() -> None:
    """progress() should accept float-like values and convert them."""
    runner.progress(1.5, 10.0, "test")
    msgs = _drain_messages(runner._msg_iter, 1)
    assert msgs[0].HasField("progress")
    assert msgs[0].progress.current == 1
    assert msgs[0].progress.total == 10
