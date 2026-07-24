"""LiveTranslator session lifetime, pinned against a fake session.

  - on go_away, open the replacement first and close the old session after
  - keep new_handle and pass it to the next connection
  - audio in the ring buffer survives a reconnect
  - repeated failures back off and end in the error state
"""

from __future__ import annotations

import asyncio
import time
from types import SimpleNamespace

import pytest

from livecap import translator as tr
from livecap.audio import CHUNK_BYTES, RingBuffer
from livecap.translator import LiveTranslator, State


def wait_until(pred, timeout: float = 6.0) -> bool:
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if pred():
            return True
        time.sleep(0.02)
    return False


# --- fake server messages -------------------------------------------------


def msg(*, go_away=None, handle=None, out=None, src=None):
    content = None
    if out is not None or src is not None:
        content = SimpleNamespace(
            input_transcription=SimpleNamespace(text=src) if src else None,
            output_transcription=SimpleNamespace(text=out) if out else None,
            model_turn=None,
        )
    return SimpleNamespace(
        go_away=SimpleNamespace(time_left="60s") if go_away else None,
        session_resumption_update=(
            SimpleNamespace(resumable=True, new_handle=handle) if handle else None
        ),
        server_content=content,
    )


class FakeSession:
    def __init__(self, recorder, index: int, script: list) -> None:
        self._rec = recorder
        self._index = index
        self._script = script
        self.sent: list[bytes] = []

    async def send_realtime_input(self, *, audio) -> None:
        self.sent.append(audio.data)
        self._rec.sent_by_session.setdefault(self._index, []).append(audio.data)

    async def receive(self):
        for item in self._script:
            await asyncio.sleep(0.01)
            yield item
        # Imitate a server still holding the connection open, until cancelled.
        await asyncio.Event().wait()


class FakeConnect:
    def __init__(self, recorder, index: int, script: list, fail: bool) -> None:
        self._rec = recorder
        self._index = index
        self._script = script
        self._fail = fail

    async def __aenter__(self):
        if self._fail:
            self._rec.events.append(("connect_fail", self._index))
            raise RuntimeError("simulated connect failure")
        self._rec.events.append(("open", self._index))
        session = FakeSession(self._rec, self._index, self._script)
        self._rec.sessions.append(session)
        return session

    async def __aexit__(self, *exc) -> bool:
        self._rec.events.append(("close", self._index))
        return False


class FakeClient:
    """scripts[i] is what connection i emits. None makes that connection fail."""

    def __init__(self, recorder, scripts: list) -> None:
        self._rec = recorder
        self._scripts = scripts
        self.aio = SimpleNamespace(live=SimpleNamespace(connect=self._connect))

    def _connect(self, *, model: str, config):
        index = self._rec.connect_count
        self._rec.connect_count += 1
        self._rec.configs.append(config)
        script = self._scripts[index] if index < len(self._scripts) else []
        return FakeConnect(self._rec, index, script or [], fail=script is None)


class Recorder:
    def __init__(self) -> None:
        self.events: list[tuple[str, int]] = []
        self.sessions: list[FakeSession] = []
        self.configs: list = []
        self.connect_count = 0
        self.sent_by_session: dict[int, list[bytes]] = {}

    def index_of(self, kind: str, n: int) -> int:
        return self.events.index((kind, n))


@pytest.fixture
def rec():
    return Recorder()


def make_translator(rec, scripts, ring=None, **kwargs) -> LiveTranslator:
    ring = ring or RingBuffer(CHUNK_BYTES * 100)
    t = LiveTranslator(ring, "fake-key", **kwargs)
    t._client = lambda: FakeClient(rec, scripts)  # type: ignore[method-assign]
    return t


# --- tests ----------------------------------------------------------------


def test_transcriptions_are_emitted_to_callbacks(rec):
    targets: list[str] = []
    sources: list[str] = []
    t = make_translator(
        rec,
        [[msg(src="안녕하세요", out="Hello"), msg(out=" there.")]],
        on_target=targets.append,
        on_source=sources.append,
    )
    t.start()
    try:
        assert wait_until(lambda: targets == ["Hello", " there."])
        assert sources == ["안녕하세요"]
    finally:
        t.stop()


def test_reaches_running_state(rec):
    states: list[str] = []
    t = make_translator(rec, [[]], on_state=lambda s, _d: states.append(s.value))
    t.start()
    try:
        assert wait_until(lambda: State.RUNNING.value in states)
    finally:
        t.stop()


def test_go_away_opens_the_replacement_before_closing_the_old_session(rec):
    """The old session must survive until the replacement is ready."""
    t = make_translator(rec, [[msg(go_away=True)], []])
    t.start()
    try:
        assert wait_until(lambda: ("open", 1) in rec.events and ("close", 0) in rec.events)
        assert rec.index_of("open", 1) < rec.index_of("close", 0)
    finally:
        t.stop()


def test_first_reconnect_reports_reconnecting_not_connecting(rec):
    """The first reconnect must also report reconnecting (found in a real log)."""
    states: list[str] = []
    t = make_translator(
        rec, [[msg(go_away=True)], []], on_state=lambda s, _d: states.append(s.value)
    )
    t.start()
    try:
        assert wait_until(lambda: t.stats.reconnects >= 1)
        assert states[0] == State.CONNECTING.value  # the initial connection
        assert State.RECONNECTING.value in states
        assert states.count(State.CONNECTING.value) == 1
    finally:
        t.stop()


def test_resumption_handle_is_stored_and_passed_to_the_next_connection(rec):
    t = make_translator(rec, [[msg(handle="HANDLE-1"), msg(go_away=True)], []])
    t.start()
    try:
        assert wait_until(lambda: rec.connect_count >= 2)
        assert rec.configs[0].session_resumption.handle is None
        assert rec.configs[1].session_resumption.handle == "HANDLE-1"
    finally:
        t.stop()


def test_reconnect_is_counted_and_the_gap_is_recorded(rec):
    t = make_translator(rec, [[msg(go_away=True)], []])
    t.start()
    try:
        assert wait_until(lambda: t.stats.reconnects >= 1)
        assert len(t.stats.gaps_ms) == t.stats.reconnects
        assert all(g >= 0 for g in t.stats.gaps_ms)
    finally:
        t.stop()


def test_on_reconnect_callback_fires_so_captions_can_continue_the_line(rec):
    hits: list[int] = []
    t = make_translator(rec, [[msg(go_away=True)], []], on_reconnect=lambda: hits.append(1))
    t.start()
    try:
        assert wait_until(lambda: len(hits) >= 1)
    finally:
        t.stop()


def test_audio_buffered_before_the_session_opens_is_not_lost(rec):
    """The buffer fills regardless of connection state and drains once open."""
    ring = RingBuffer(CHUNK_BYTES * 100)
    payload = bytes([7]) * CHUNK_BYTES
    for _ in range(5):
        ring.write(payload)

    t = make_translator(rec, [[]], ring=ring)
    t.start()
    try:
        assert wait_until(lambda: t.stats.chunks_sent >= 5)
        assert rec.sessions[0].sent[:5] == [payload] * 5
    finally:
        t.stop()


def test_audio_written_during_a_reconnect_reaches_the_new_session(rec):
    ring = RingBuffer(CHUNK_BYTES * 100)
    t = make_translator(rec, [[msg(go_away=True)], []], ring=ring)
    t.start()
    try:
        assert wait_until(lambda: rec.connect_count >= 2)
        marker = bytes([42]) * CHUNK_BYTES
        ring.write(marker)
        assert wait_until(lambda: marker in rec.sent_by_session.get(1, []))
    finally:
        t.stop()


def test_repeated_connect_failures_end_in_the_error_state(rec, monkeypatch):
    monkeypatch.setattr(tr, "BACKOFF_SECONDS", (0.01, 0.01, 0.01, 0.01))
    states: list[tuple[str, str]] = []
    t = make_translator(
        rec, [None] * 6, on_state=lambda s, d: states.append((s.value, d))
    )
    t.start()
    try:
        assert wait_until(lambda: any(s == State.ERROR.value for s, _ in states), timeout=10.0)
        assert rec.connect_count >= tr.MAX_CONSECUTIVE_FAILURES
    finally:
        t.stop()


def test_a_transient_failure_recovers_without_erroring(rec, monkeypatch):
    monkeypatch.setattr(tr, "BACKOFF_SECONDS", (0.01, 0.01, 0.01, 0.01))
    states: list[str] = []
    t = make_translator(rec, [None, None, []], on_state=lambda s, _d: states.append(s.value))
    t.start()
    try:
        assert wait_until(lambda: State.RUNNING.value in states, timeout=10.0)
        assert State.ERROR.value not in states
    finally:
        t.stop()


def test_stop_closes_the_session_and_returns_to_idle(rec):
    t = make_translator(rec, [[]])
    t.start()
    assert wait_until(lambda: ("open", 0) in rec.events)
    t.stop()
    assert t.state is State.IDLE
    assert wait_until(lambda: ("close", 0) in rec.events)


def test_config_matches_the_preview_model_contract(rec):
    """response_modalities must be AUDIO, with transcription, compression and resumption on."""
    t = make_translator(rec, [[]], target_language_code="en", echo_target_language=False)
    t.start()
    try:
        assert wait_until(lambda: len(rec.configs) >= 1)
        cfg = rec.configs[0]
        assert cfg.response_modalities == ["AUDIO"]
        assert cfg.input_audio_transcription is not None
        assert cfg.output_audio_transcription is not None
        assert cfg.translation_config.target_language_code == "en"
        assert cfg.translation_config.echo_target_language is False
        assert cfg.context_window_compression.sliding_window is not None
    finally:
        t.stop()


def test_billing_error_fails_fast_instead_of_backing_off(rec, monkeypatch):
    """Credit and auth problems are reported at once rather than retried."""
    monkeypatch.setattr(tr, "BACKOFF_SECONDS", (5.0, 5.0, 5.0, 5.0))

    class Depleted(FakeConnect):
        async def __aenter__(self):
            self._rec.events.append(("connect_fail", self._index))
            raise RuntimeError(
                "1011 None. Your prepayment credits are depleted. Please go to AI Studio"
            )

    states: list[tuple[str, str]] = []
    t = make_translator(rec, [[]], on_state=lambda s, d: states.append((s.value, d)))
    client = FakeClient(rec, [[]])
    # aio.live.connect binds at construction, so replace that attribute directly.
    client.aio.live.connect = lambda *, model, config: Depleted(rec, 0, [], fail=True)
    t._client = lambda: client  # type: ignore[method-assign]
    t.start()
    try:
        assert wait_until(lambda: any(s == State.ERROR.value for s, _ in states), timeout=4.0)
        detail = next(d for s, d in states if s == State.ERROR.value)
        assert "credits" in detail
        # It must give up at once, not back off 5 seconds five times.
        assert rec.events.count(("connect_fail", 0)) == 1
    finally:
        t.stop()


@pytest.mark.parametrize(
    "message,expected",
    [
        ("Your prepayment credits are depleted.", "credits"),
        ("API key not valid. Please pass a valid API key.", "API key"),
        ("PERMISSION_DENIED: caller does not have permission", "permission"),
        ("models/foo was not found", "Model not found"),
    ],
)
def test_fatal_reason_recognises_non_retryable_errors(message, expected):
    reason = tr.fatal_reason(RuntimeError(message))
    assert reason is not None and expected in reason


def test_fatal_reason_passes_through_transient_errors():
    assert tr.fatal_reason(RuntimeError("connection reset by peer")) is None
    assert tr.fatal_reason(None) is None


def test_model_name_is_the_preview_translate_model():
    assert tr.MODEL == "gemini-3.5-live-translate-preview"
    assert tr.AUDIO_MIME == "audio/pcm;rate=16000"
    assert tr.RESPONSE_MODALITIES == ["AUDIO"]
