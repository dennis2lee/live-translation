"""Gemini Live API integration.

Runs on a dedicated asyncio thread. An audio session lasts about 15 minutes and
the WebSocket about 10, so surviving 30 minutes needs all three of:

  1. context_window_compression=SlidingWindow()  — extend the session itself
  2. session_resumption                          — keep and reuse new_handle
  3. open a replacement session when go_away arrives, before closing the old one

The ring buffer keeps filling during a reconnect and whatever accumulated is
pushed to the new session, so no sentence is lost.
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

# --- Preview model contract. Change these here and nowhere else. -----------
MODEL = "gemini-3.5-live-translate-preview"
AUDIO_MIME = "audio/pcm;rate=16000"
RESPONSE_MODALITIES = ["AUDIO"]  # Audio-only model. Setting TEXT fails the session.

BACKOFF_SECONDS = (1.0, 2.0, 4.0, 8.0)
MAX_CONSECUTIVE_FAILURES = 5

# Failures that retrying cannot fix. Report immediately instead of backing off.
# (marker, message shown to the user)
FATAL_ERRORS: tuple[tuple[str, str], ...] = (
    ("credits are depleted", "Billing credits exhausted — set up billing in AI Studio"),
    ("billing", "Billing required — this model is not on the free tier"),
    ("api key not valid", "API key is not valid"),
    ("api_key_invalid", "API key is not valid"),
    ("unauthenticated", "API key authentication failed"),
    ("permission_denied", "No permission to access this model"),
    ("was not found", "Model not found — check the model name"),
)


def fatal_reason(exc: BaseException | None) -> str | None:
    """Return the message to show for a non-retryable error, else None."""
    if exc is None:
        return None
    text = str(exc).lower()
    for marker, message in FATAL_ERRORS:
        if marker in text:
            return message
    return None

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
    """One Live session bundled with its receive task."""

    def __init__(self) -> None:
        self.session: object | None = None
        self.ready = asyncio.Event()
        self.closed = asyncio.Event()
        self.go_away = asyncio.Event()
        self.task: asyncio.Task | None = None
        self.error: BaseException | None = None

    async def wait_for_replacement_trigger(self) -> None:
        """Wake when the server warns of shutdown, or the connection drops."""
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
    """Pushes PCM from the ring buffer to the Live API and emits transcriptions.

    Callbacks run on the asyncio thread. Never touch a Qt widget from them —
    cross to the main thread with pyqtSignal.
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
        self._last_error: BaseException | None = None
        self._reconnect_requested = asyncio.Event()
        self._state = State.IDLE
        self.stats = Stats()

    # --- public API, called from the main thread --------------------------

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
            # The supervisor waits on asyncio events, which a threading.Event
            # cannot wake. Cancel the task instead, so its cleanup actually runs.
            loop.call_soon_threadsafe(self._cancel_supervisor)
        if thread is not None:
            thread.join(timeout=timeout)
            if thread.is_alive():
                _log.error("translator thread did not exit within %.1fs", timeout)
        self._thread = None
        self._loop = None
        self._set_state(State.IDLE, "")
        _log.info("translator stopped — %s", self.stats.summary())

    # --- asyncio thread ---------------------------------------------------

    def _cancel_supervisor(self) -> None:
        """Runs on the asyncio thread, via call_soon_threadsafe."""
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
        """Open sessions, replace them on go_away or drop, and keep the sender alive."""
        client = self._client()
        sender = asyncio.create_task(self._send_loop(), name="send")
        failures = 0

        try:
            while not self._stop.is_set():
                # Having a session to replace means this is a reconnect. Using
                # stats.reconnects would mislabel the first one, since it is still 0.
                self._set_state(
                    State.RECONNECTING if self._active is not None else State.CONNECTING, ""
                )
                gap_started = time.monotonic()
                holder = await self._open_session(client)

                if holder is None:
                    reason = fatal_reason(self._last_error)
                    if reason is not None:
                        # Credit, auth and permission problems do not heal with time.
                        _log.error("fatal: %s", self._last_error)
                        self._set_state(State.ERROR, reason)
                        break
                    failures += 1
                    if failures >= MAX_CONSECUTIVE_FAILURES:
                        self._set_state(State.ERROR, "Repeated connection failures")
                        break
                    delay = BACKOFF_SECONDS[min(failures - 1, len(BACKOFF_SECONDS) - 1)]
                    _log.warning("connect failed (%d/%d) — retry in %.0fs", failures,
                                 MAX_CONSECUTIVE_FAILURES, delay)
                    await self._sleep(delay)
                    continue

                failures = 0
                previous = self._active
                self._active = holder  # atomically swap the send target
                gap_ms = (time.monotonic() - gap_started) * 1000.0

                if previous is not None:
                    self.stats.reconnects += 1
                    self.stats.gaps_ms.append(gap_ms)
                    _log.info("session swapped — gap %.0f ms (reconnect #%d)",
                              gap_ms, self.stats.reconnects)
                    self._on_reconnect()
                    await self._close_session(previous)

                self._set_state(State.RUNNING, "")

                # Keep translating on this session until it warns or drops. The old
                # session stays alive past go_away, until the replacement is ready.
                await holder.wait_for_replacement_trigger()
                if self._stop.is_set():
                    break
        finally:
            sender.cancel()
            if self._active is not None:
                await self._close_session(self._active)
                self._active = None

    async def _open_session(self, client: genai.Client) -> _SessionHolder | None:
        """Open a session and return its holder once it is ready."""
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
            self._last_error = None
            return holder

        holder.task.cancel()
        self._last_error = holder.error
        if holder.error is not None:
            _log.warning("session open failed: %s", holder.error)
        else:
            _log.warning("session open timed out after 30s")
        return None

    async def _session_main(self, client: genai.Client, holder: _SessionHolder) -> None:
        """Run one session's receive loop, setting `closed` when it ends."""
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

            # 1) Shutdown warning — triggers the pre-emptive reconnect.
            # Returning here would exit the async with and close this session at once.
            # We must keep receiving until the swap completes, so only set a flag.
            if response.go_away is not None and not holder.go_away.is_set():
                left = getattr(response.go_away, "time_left", None)
                _log.info("go_away received (time_left=%s) — opening replacement", left)
                holder.go_away.set()

            # 2) Resumption handle — always keep the newest
            upd = response.session_resumption_update
            if upd is not None and upd.resumable and upd.new_handle:
                self._handle = upd.new_handle

            sc = response.server_content
            if sc is None:
                continue

            # 3) Source-language transcription (hidden by default)
            if sc.input_transcription and sc.input_transcription.text:
                self._on_source(sc.input_transcription.text)

            # 4) The translation — this is what goes on screen
            if sc.output_transcription and sc.output_transcription.text:
                self._on_target(sc.output_transcription.text)

            # 5) Audio must be consumed and discarded, or the stream stalls
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
        """Ring buffer to the active session. With no session, audio just accumulates."""
        silent_since: float | None = None
        while not self._stop.is_set():
            holder = self._active
            if holder is None or holder.session is None or holder.closed.is_set():
                # Reconnecting. The buffer keeps filling; we drain it once a session opens.
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
                        continue  # skip the send to save cost
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
        """Sleep that notices a stop request."""
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
