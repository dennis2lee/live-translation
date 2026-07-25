"""Transcript writing."""

from __future__ import annotations

from datetime import datetime

import pytest

from livecap.caption_buffer import CaptionBuffer, Policy
from livecap.transcript import TranscriptWriter, _format_elapsed, resolve_dir

STARTED = datetime(2026, 7, 24, 14, 30, 52)


@pytest.fixture
def writer(tmp_path):
    return TranscriptWriter(tmp_path, target_language="en", model="test-model")


# --- filename -------------------------------------------------------------


def test_filename_is_based_on_start_datetime(writer):
    path = writer.open(STARTED)
    assert path is not None
    assert path.name == "2026-07-24_143052.md"


def test_second_session_in_the_same_second_gets_a_suffix(tmp_path):
    first = TranscriptWriter(tmp_path)
    first.open(STARTED)
    first.append_line("one", 0.0)
    first.close()

    second = TranscriptWriter(tmp_path)
    path = second.open(STARTED)
    assert path is not None
    assert path.name == "2026-07-24_143052-2.md"


def test_directory_is_created_if_missing(tmp_path):
    target = tmp_path / "deep" / "nested"
    w = TranscriptWriter(target)
    assert w.open(STARTED) is not None
    assert target.is_dir()


def test_resolve_dir_expands_home():
    assert not str(resolve_dir("~/Documents/LiveCap")).startswith("~")


# --- content --------------------------------------------------------------


def test_header_records_start_language_and_model(writer):
    path = writer.open(STARTED)
    writer.append_line("hello", 1.0)
    writer.close()
    text = path.read_text(encoding="utf-8")
    assert "# LiveCap transcript" in text
    assert "2026-07-24 14:30:52" in text
    assert "**Target language** en" in text
    assert "test-model" in text


def test_lines_are_written_with_elapsed_timestamps(writer):
    path = writer.open(STARTED)
    writer.append_line("First sentence.", 3.0)
    writer.append_line("Second sentence.", 75.0)
    writer.close()
    text = path.read_text(encoding="utf-8")
    assert "`00:03` First sentence." in text
    assert "`01:15` Second sentence." in text


def test_elapsed_format_switches_to_hours():
    assert _format_elapsed(0) == "00:00"
    assert _format_elapsed(75) == "01:15"
    assert _format_elapsed(3661) == "01:01:01"
    assert _format_elapsed(-5) == "00:00"


def test_blank_lines_are_skipped(writer):
    path = writer.open(STARTED)
    writer.append_line("   ", 1.0)
    writer.append_line("real", 2.0)
    writer.close()
    assert writer.lines_written == 1
    assert "real" in path.read_text(encoding="utf-8")


def test_footer_records_duration_and_reconnects(writer):
    path = writer.open(STARTED)
    writer.append_line("hello", 1.0)
    writer.close(elapsed_seconds=1830.0, reconnects=3)
    text = path.read_text(encoding="utf-8")
    assert "**Duration** 30:30" in text
    assert "**Reconnects** 3" in text
    assert "**Lines** 1" in text


def test_reconnect_marks_are_recorded(writer):
    path = writer.open(STARTED)
    writer.append_line("before", 10.0)
    writer.note_reconnect(600.0, 842.0)
    writer.append_line("after", 601.0)
    writer.close()
    text = path.read_text(encoding="utf-8")
    assert "reconnected at 10:00, gap 842 ms" in text
    assert text.index("before") < text.index("reconnected at") < text.index("after")


def test_content_survives_without_close(writer):
    """A crash must not lose what was already written."""
    path = writer.open(STARTED)
    writer.append_line("written before the crash", 1.0)
    assert "written before the crash" in path.read_text(encoding="utf-8")


def test_empty_transcript_is_deleted_on_close(writer):
    """A transcript that captured nothing is not kept."""
    path = writer.open(STARTED)
    assert path.exists()
    assert writer.close() is None
    assert not path.exists()


# --- robustness -----------------------------------------------------------


def test_append_without_open_is_a_noop(writer):
    writer.append_line("nothing", 1.0)  # must not raise
    assert writer.lines_written == 0


def test_close_without_open_returns_none(writer):
    assert writer.close() is None


def test_open_twice_returns_the_same_path(writer):
    first = writer.open(STARTED)
    second = writer.open(STARTED)
    assert first == second


def test_close_survives_a_failing_footer_write(writer):
    """A write error during close() must not crash the app on the way out."""
    writer.open(STARTED)
    writer.append_line("recorded", 1.0)

    class _Broken:
        def write(self, _text):
            raise OSError("disk full")

        def flush(self):
            pass

        def close(self):
            pass

    writer._file = _Broken()
    assert writer.close(elapsed_seconds=5.0) is not None  # must not raise
    assert writer.is_open is False


def test_unwritable_directory_does_not_raise(tmp_path):
    """A recording failure must never interrupt translation."""
    blocker = tmp_path / "blocked"
    blocker.write_text("i am a file, not a directory")
    w = TranscriptWriter(blocker / "sub")
    assert w.open(STARTED) is None
    assert w.is_open is False
    w.append_line("ignored", 1.0)  # silently ignored
    assert w.close() is None


# --- CaptionBuffer integration --------------------------------------------


def test_buffer_hands_over_each_committed_line_once():
    buf = CaptionBuffer(policy=Policy.UTTERANCE, clear_after_seconds=0.0)
    buf.feed("first line", 0.0)
    buf.feed("second line", 1.0)
    assert buf.drain_new_commits() == ["first line", "second line"]
    assert buf.drain_new_commits() == []


def test_clearing_the_display_does_not_drop_undrained_lines():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("kept for the record.", 0.0)
    buf.tick(3.0)  # the display clears
    assert buf.is_empty
    assert buf.drain_new_commits() == ["kept for the record."]


def test_short_clear_window_still_records_pending_text():
    """Recording survives clear_after_seconds shorter than idle_commit_seconds."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.5)
    buf.feed("no terminator here", 0.0)
    buf.tick(0.6)
    assert buf.is_empty
    assert buf.drain_new_commits() == ["no terminator here"]


def test_reconnect_does_not_duplicate_an_already_recorded_line():
    """Resuming an already-written line emits only the continuation."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Revenue is up 14%.", 0.0)
    assert buf.drain_new_commits() == ["Revenue is up 14%."]  # written to the file

    buf.note_reconnect()
    buf.feed(" We will continue.", 1.0)
    assert buf.drain_new_commits() == ["We will continue."]
    # The display keeps the merged line.
    assert buf.committed_lines == ["Revenue is up 14%. We will continue."]


def test_reconnect_before_drain_emits_the_merged_line_once():
    """If nothing was written yet, the merged line is emitted once."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Revenue is up 14%.", 0.0)
    buf.note_reconnect()
    buf.feed(" We will continue.", 1.0)
    assert buf.drain_new_commits() == ["Revenue is up 14%. We will continue."]


def test_flush_commits_the_trailing_sentence():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("unfinished thought", 0.0)
    assert buf.flush() is True
    assert buf.drain_new_commits() == ["unfinished thought"]
    assert buf.flush() is False
