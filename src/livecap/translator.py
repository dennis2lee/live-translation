"""Gemini Live API 연동 — PLAN.md §4.

전용 asyncio 스레드에서 돈다. 세션 수명(오디오 전용 기본 15분, WebSocket 약 10분)을
넘겨 30분을 버티기 위해 세 가지를 모두 쓴다.

  1. context_window_compression=SlidingWindow()  — 세션 자체를 연장
  2. session_resumption                          — new_handle 보관·재사용
  3. go_away 수신 시 선제적으로 새 연결을 열어 교체

재연결 중에도 링 버퍼는 계속 채워지고, 새 세션이 열리면 쌓인 것부터 밀어 넣으므로
문장이 유실되지 않는다.
"""

from __future__ import annotations

import asyncio
import contextlib
import threading
import time
from collections.abc import Callable
from dataclasses import dataclass, field
from enum import StrEnum

import numpy as np
from google import genai
from google.genai import types

from .audio import CHUNK_BYTES, RingBuffer
from .util import logging as log

_log = log.get(__name__)

# --- 프리뷰 모델 규격 상수 (변경 시 여기만 고친다) --------------------------
MODEL = "gemini-3.5-live-translate-preview"
AUDIO_MIME = "audio/pcm;rate=16000"
RESPONSE_MODALITIES = ["AUDIO"]  # 이 모델은 오디오 출력 전용. TEXT로 바꾸면 세션이 실패한다.

BACKOFF_SECONDS = (1.0, 2.0, 4.0, 8.0)
MAX_CONSECUTIVE_FAILURES = 5
IDLE_POLL_SECONDS = 0.02
SILENCE_RMS_THRESHOLD = 0.003
SILENCE_HOLD_SECONDS = 3.0


class State(StrEnum):
    IDLE = "idle"
    CONNECTING = "connecting"
    RUNNING = "running"
    RECONNECTING = "reconnecting"
    ERROR = "error"


@dataclass
class Stats:
    reconnects: int = 0
    gaps_ms: list[float] = field(default_factory=list)
    chunks_sent: int = 0
    started_at: float = 0.0

    def summary(self) -> str:
        gaps = ", ".join(f"{g:.0f}" for g in self.gaps_ms) or "-"
        elapsed = time.monotonic() - self.started_at if self.started_at else 0.0
        return (
            f"elapsed={elapsed / 60:.1f}min reconnects={self.reconnects} "
            f"gaps_ms=[{gaps}] chunks_sent={self.chunks_sent}"
        )


class _SessionHolder:
    """하나의 Live 세션과 그 수신 태스크를 묶는다."""

    def __init__(self) -> None:
        self.session: object | None = None
        self.ready = asyncio.Event()
        self.closed = asyncio.Event()
        self.go_away = asyncio.Event()
        self.task: asyncio.Task | None = None
        self.error: BaseException | None = None

    async def wait_for_replacement_trigger(self) -> None:
        """종료 예고를 받았거나 연결이 끊기면 깨어난다."""
        waiters = [
            asyncio.create_task(self.go_away.wait()),
            asyncio.create_task(self.closed.wait()),
        ]
        try:
            await asyncio.wait(waiters, return_when=asyncio.FIRST_COMPLETED)
        finally:
            for w in waiters:
                w.cancel()


class LiveTranslator:
    """링 버퍼의 PCM을 Live API로 밀어 넣고 전사 텍스트를 콜백으로 방출한다.

    콜백은 asyncio 스레드에서 호출된다. Qt 위젯을 직접 만지지 말고
    반드시 pyqtSignal을 경유해 메인 스레드로 넘길 것.
    """

    def __init__(
        self,
        ring: RingBuffer,
        api_key: str,
        *,
        target_language_code: str = "en",
        echo_target_language: bool = False,
        silence_gate: bool = False,
        on_source: Callable[[str], None] | None = None,
        on_target: Callable[[str], None] | None = None,
        on_state: Callable[[State, str], None] | None = None,
        on_reconnect: Callable[[], None] | None = None,
    ) -> None:
        self._ring = ring
        self._api_key = api_key
        self._target_language = target_language_code
        self._echo_target = echo_target_language
        self._silence_gate = silence_gate

        self._on_source = on_source or (lambda _t: None)
        self._on_target = on_target or (lambda _t: None)
        self._on_state = on_state or (lambda _s, _d: None)
        self._on_reconnect = on_reconnect or (lambda: None)

        self._loop: asyncio.AbstractEventLoop | None = None
        self._thread: threading.Thread | None = None
        self._supervisor: asyncio.Task | None = None
        self._stop = threading.Event()

        self._active: _SessionHolder | None = None
        self._handle: str | None = None
        self._reconnect_requested = asyncio.Event()
        self._state = State.IDLE
        self.stats = Stats()

    # --- 공개 API (메인 스레드에서 호출) ----------------------------------

    @property
    def state(self) -> State:
        return self._state

    def start(self) -> None:
        if self._thread is not None:
            raise RuntimeError("translator already started")
        self._stop.clear()
        self.stats = Stats(started_at=time.monotonic())
        ready = threading.Event()
        self._thread = threading.Thread(target=self._thread_main, args=(ready,), daemon=True)
        self._thread.start()
        ready.wait(timeout=5.0)

    def stop(self, timeout: float = 6.0) -> None:
        self._stop.set()
        loop, thread = self._loop, self._thread
        if loop is not None and not loop.is_closed():
            # supervisor는 asyncio 이벤트를 기다리고 있어 threading.Event로는 깨지 않는다.
            # 태스크를 스레드 안전하게 취소해야 정리 코드(세션 close)가 돈다.
            loop.call_soon_threadsafe(self._cancel_supervisor)
        if thread is not None:
            thread.join(timeout=timeout)
            if thread.is_alive():
                _log.error("translator thread did not exit within %.1fs", timeout)
        self._thread = None
        self._loop = None
        self._set_state(State.IDLE, "")
        _log.info("translator stopped — %s", self.stats.summary())

    # --- asyncio 스레드 ---------------------------------------------------

    def _cancel_supervisor(self) -> None:
        """asyncio 스레드에서 실행된다 (call_soon_threadsafe 경유)."""
        task = self._supervisor
        if task is not None and not task.done():
            task.cancel()

    async def _run(self) -> None:
        self._supervisor = asyncio.current_task()
        with contextlib.suppress(asyncio.CancelledError):
            await self._supervise()

    def _thread_main(self, ready: threading.Event) -> None:
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        self._loop = loop
        ready.set()
        try:
            loop.run_until_complete(self._run())
        except Exception:
            _log.exception("translator thread crashed")
        finally:
            try:
                pending = asyncio.all_tasks(loop)
                for t in pending:
                    t.cancel()
                if pending:
                    loop.run_until_complete(asyncio.gather(*pending, return_exceptions=True))
            finally:
                loop.close()

    def _set_state(self, state: State, detail: str = "") -> None:
        if state is not self._state or detail:
            self._state = state
            self._on_state(state, detail)

    def _client(self) -> genai.Client:
        return genai.Client(api_key=self._api_key)

    def _config(self) -> types.LiveConnectConfig:
        return types.LiveConnectConfig(
            response_modalities=RESPONSE_MODALITIES,
            input_audio_transcription=types.AudioTranscriptionConfig(),
            output_audio_transcription=types.AudioTranscriptionConfig(),
            translation_config=types.TranslationConfig(
                target_language_code=self._target_language,
                echo_target_language=self._echo_target,
            ),
            context_window_compression=types.ContextWindowCompressionConfig(
                sliding_window=types.SlidingWindow(),
            ),
            session_resumption=types.SessionResumptionConfig(handle=self._handle),
        )

    async def _supervise(self) -> None:
        """세션을 열고, go_away/끊김에 대응해 교체하며, 송신 루프를 유지한다."""
        client = self._client()
        sender = asyncio.create_task(self._send_loop(), name="send")
        failures = 0

        try:
            while not self._stop.is_set():
                self._set_state(
                    State.RECONNECTING if self.stats.reconnects else State.CONNECTING, ""
                )
                gap_started = time.monotonic()
                holder = await self._open_session(client)

                if holder is None:
                    failures += 1
                    if failures >= MAX_CONSECUTIVE_FAILURES:
                        self._set_state(State.ERROR, "연결에 반복 실패했습니다")
                        break
                    delay = BACKOFF_SECONDS[min(failures - 1, len(BACKOFF_SECONDS) - 1)]
                    _log.warning("connect failed (%d/%d) — retry in %.0fs", failures,
                                 MAX_CONSECUTIVE_FAILURES, delay)
                    await self._sleep(delay)
                    continue

                failures = 0
                previous = self._active
                self._active = holder  # 송신 타깃을 원자적으로 교체
                gap_ms = (time.monotonic() - gap_started) * 1000.0

                if previous is not None:
                    self.stats.reconnects += 1
                    self.stats.gaps_ms.append(gap_ms)
                    _log.info("session swapped — gap %.0f ms (reconnect #%d)",
                              gap_ms, self.stats.reconnects)
                    self._on_reconnect()
                    await self._close_session(previous)

                self._set_state(State.RUNNING, "")

                # 종료 예고를 받거나 연결이 끊길 때까지 이 세션으로 계속 통역한다.
                # go_away 이후에도 구 세션은 살려두고, 새 세션이 준비되면 그때 교체한다.
                await holder.wait_for_replacement_trigger()
                if self._stop.is_set():
                    break
        finally:
            sender.cancel()
            if self._active is not None:
                await self._close_session(self._active)
                self._active = None

    async def _open_session(self, client: genai.Client) -> _SessionHolder | None:
        """새 세션을 연다. 준비될 때까지 기다렸다가 holder를 돌려준다."""
        holder = _SessionHolder()
        holder.task = asyncio.create_task(self._session_main(client, holder), name="session")
        ready_waiter = asyncio.create_task(holder.ready.wait())
        try:
            await asyncio.wait(
                {ready_waiter, holder.task},
                return_when=asyncio.FIRST_COMPLETED,
                timeout=30.0,
            )
        finally:
            ready_waiter.cancel()

        if holder.ready.is_set():
            return holder

        holder.task.cancel()
        if holder.error is not None:
            _log.warning("session open failed: %s", holder.error)
        else:
            _log.warning("session open timed out after 30s")
        return None

    async def _session_main(self, client: genai.Client, holder: _SessionHolder) -> None:
        """하나의 세션을 열고 수신 루프를 돈다. 종료되면 closed를 세운다."""
        try:
            async with client.aio.live.connect(model=MODEL, config=self._config()) as session:
                holder.session = session
                holder.ready.set()
                _log.info("live session open (resumed=%s)", bool(self._handle))
                await self._recv_loop(session, holder)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            holder.error = exc
            _log.warning("session ended with error: %s", exc)
        finally:
            holder.closed.set()

    async def _recv_loop(self, session: object, holder: _SessionHolder) -> None:
        async for response in session.receive():  # type: ignore[attr-defined]
            if self._stop.is_set():
                return

            # 1) 종료 예고 — 선제 재연결 트리거.
            # 여기서 return하면 async with를 빠져나가며 구 세션이 즉시 닫힌다.
            # 교체가 끝날 때까지는 계속 수신해야 하므로 플래그만 세우고 루프를 유지한다.
            if response.go_away is not None and not holder.go_away.is_set():
                left = getattr(response.go_away, "time_left", None)
                _log.info("go_away received (time_left=%s) — opening replacement", left)
                holder.go_away.set()

            # 2) 세션 재개 핸들 — 최신값을 항상 보관
            upd = response.session_resumption_update
            if upd is not None and upd.resumable and upd.new_handle:
                self._handle = upd.new_handle

            sc = response.server_content
            if sc is None:
                continue

            # 3) 원문 한국어 (기본 미표시)
            if sc.input_transcription and sc.input_transcription.text:
                self._on_source(sc.input_transcription.text)

            # 4) 번역된 영어 — 화면에 띄울 텍스트
            if sc.output_transcription and sc.output_transcription.text:
                self._on_target(sc.output_transcription.text)

            # 5) 오디오는 반드시 소비하고 폐기 (안 읽으면 스트림이 막힘)
            if sc.model_turn:
                for part in sc.model_turn.parts or ():
                    if part.inline_data is not None:
                        pass  # discard

    async def _close_session(self, holder: _SessionHolder) -> None:
        if holder.task is not None and not holder.task.done():
            holder.task.cancel()
            with contextlib.suppress(asyncio.CancelledError, Exception):
                await holder.task
        holder.closed.set()

    async def _send_loop(self) -> None:
        """링 버퍼 → 활성 세션. 세션이 없으면 버퍼에 그대로 쌓아둔다."""
        silent_since: float | None = None
        while not self._stop.is_set():
            holder = self._active
            if holder is None or holder.session is None or holder.closed.is_set():
                # 재연결 중 — 링 버퍼는 계속 채워지고, 새 세션이 열리면 여기서 밀어 넣는다.
                await asyncio.sleep(IDLE_POLL_SECONDS)
                continue

            chunk = self._ring.read(CHUNK_BYTES)
            if chunk is None:
                await asyncio.sleep(IDLE_POLL_SECONDS)
                continue

            if self._silence_gate:
                now = time.monotonic()
                if _rms(chunk) < SILENCE_RMS_THRESHOLD:
                    silent_since = silent_since if silent_since is not None else now
                    if now - silent_since >= SILENCE_HOLD_SECONDS:
                        continue  # 전송을 건너뛴다 (비용 절감)
                else:
                    silent_since = None

            try:
                await holder.session.send_realtime_input(  # type: ignore[attr-defined]
                    audio=types.Blob(data=chunk, mime_type=AUDIO_MIME)
                )
                self.stats.chunks_sent += 1
            except asyncio.CancelledError:
                raise
            except Exception as exc:
                _log.warning("send failed, will retry on next session: %s", exc)
                holder.closed.set()
                await asyncio.sleep(IDLE_POLL_SECONDS)

    async def _sleep(self, seconds: float) -> None:
        """중지 요청에 반응하는 sleep."""
        step = 0.1
        waited = 0.0
        while waited < seconds and not self._stop.is_set():
            await asyncio.sleep(step)
            waited += step


def _rms(pcm: bytes) -> float:
    samples = np.frombuffer(pcm, dtype="<i2").astype(np.float32) / 32768.0
    if samples.size == 0:
        return 0.0
    return float(np.sqrt(np.mean(samples * samples)))
