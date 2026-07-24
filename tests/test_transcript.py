"""번역 기록 저장 검증."""

from __future__ import annotations

from datetime import datetime

import pytest

from livecap.caption_buffer import CaptionBuffer, Policy
from livecap.transcript import TranscriptWriter, _format_elapsed, resolve_dir

STARTED = datetime(2026, 7, 24, 14, 30, 52)


@pytest.fixture
def writer(tmp_path):
    return TranscriptWriter(tmp_path, target_language="en", model="test-model")


# --- 파일명 ---------------------------------------------------------------


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


# --- 내용 -----------------------------------------------------------------


def test_header_records_start_language_and_model(writer):
    path = writer.open(STARTED)
    writer.append_line("hello", 1.0)
    writer.close()
    text = path.read_text(encoding="utf-8")
    assert "# LiveCap 번역 기록" in text
    assert "2026-07-24 14:30:52" in text
    assert "**대상 언어** en" in text
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
    assert "**길이** 30:30" in text
    assert "**재연결** 3회" in text
    assert "**줄 수** 1" in text


def test_reconnect_marks_are_recorded(writer):
    path = writer.open(STARTED)
    writer.append_line("before", 10.0)
    writer.note_reconnect(600.0, 842.0)
    writer.append_line("after", 601.0)
    writer.close()
    text = path.read_text(encoding="utf-8")
    assert "재연결 — 10:00 지점, 공백 842ms" in text
    assert text.index("before") < text.index("재연결") < text.index("after")


def test_content_survives_without_close(writer):
    """앱이 갑자기 죽어도 그때까지 쓴 내용은 남아야 한다."""
    path = writer.open(STARTED)
    writer.append_line("written before the crash", 1.0)
    assert "written before the crash" in path.read_text(encoding="utf-8")


def test_empty_transcript_is_deleted_on_close(writer):
    """한 줄도 못 받은 기록 파일은 남기지 않는다."""
    path = writer.open(STARTED)
    assert path.exists()
    assert writer.close() is None
    assert not path.exists()


# --- 견고성 ---------------------------------------------------------------


def test_append_without_open_is_a_noop(writer):
    writer.append_line("nothing", 1.0)  # 예외가 나면 안 된다
    assert writer.lines_written == 0


def test_close_without_open_returns_none(writer):
    assert writer.close() is None


def test_open_twice_returns_the_same_path(writer):
    first = writer.open(STARTED)
    second = writer.open(STARTED)
    assert first == second


def test_unwritable_directory_does_not_raise(tmp_path):
    """기록 실패가 통역을 중단시키면 안 된다."""
    blocker = tmp_path / "blocked"
    blocker.write_text("i am a file, not a directory")
    w = TranscriptWriter(blocker / "sub")
    assert w.open(STARTED) is None
    assert w.is_open is False
    w.append_line("ignored", 1.0)  # 조용히 무시
    assert w.close() is None


# --- CaptionBuffer 연동 ---------------------------------------------------


def test_buffer_hands_over_each_committed_line_once():
    buf = CaptionBuffer(policy=Policy.UTTERANCE, clear_after_seconds=0.0)
    buf.feed("first line", 0.0)
    buf.feed("second line", 1.0)
    assert buf.drain_new_commits() == ["first line", "second line"]
    assert buf.drain_new_commits() == []


def test_clearing_the_display_does_not_drop_undrained_lines():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("kept for the record.", 0.0)
    buf.tick(3.0)  # 화면은 비워진다
    assert buf.is_empty
    assert buf.drain_new_commits() == ["kept for the record."]


def test_short_clear_window_still_records_pending_text():
    """clear_after_seconds가 idle_commit_seconds보다 짧아도 기록은 남는다."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.5)
    buf.feed("no terminator here", 0.0)
    buf.tick(0.6)
    assert buf.is_empty
    assert buf.drain_new_commits() == ["no terminator here"]


def test_reconnect_does_not_duplicate_an_already_recorded_line():
    """이미 기록에 쓰인 줄을 이어붙일 때는 이어진 부분만 내보낸다."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Revenue is up 14%.", 0.0)
    assert buf.drain_new_commits() == ["Revenue is up 14%."]  # 파일에 기록됨

    buf.note_reconnect()
    buf.feed(" We will continue.", 1.0)
    assert buf.drain_new_commits() == ["We will continue."]
    # 화면에는 합본이 남는다.
    assert buf.committed_lines == ["Revenue is up 14%. We will continue."]


def test_reconnect_before_drain_emits_the_merged_line_once():
    """아직 기록되지 않았다면 합본을 통째로 한 번만 내보낸다."""
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
