"""Audio capture pipeline.

Opens the USB interface at its native rate, then downmixes to mono, resamples
to 16 kHz with the streaming soxr converter, converts to int16 LE, and writes
into a ring buffer.

The ring buffer keeps filling regardless of connection state, so audio survives
a reconnect: whatever accumulated during the gap is pushed to the new session.
"""

from __future__ import annotations

import threading
from dataclasses import dataclass

import numpy as np
import sounddevice as sd
import soxr

from .util import logging as log

_log = log.get(__name__)

TARGET_RATE = 16_000
BYTES_PER_SAMPLE = 2
CHUNK_MS = 100
CHUNK_BYTES = TARGET_RATE * BYTES_PER_SAMPLE * CHUNK_MS // 1000  # 3200
RING_SECONDS = 30
CALLBACK_MS = 20


class RingBuffer:
    """Single-producer (audio callback) / single-consumer (asyncio send task) buffer.

    Lock-free. Only the producer advances the write position and only the
    consumer advances the read position; both are monotonic Python ints, whose
    reads and writes are atomic under the GIL.

    When full, the producer overwrites rather than blocking, and the consumer
    detects on its next read that it was lapped and skips the oldest data.
    """

    def __init__(self, capacity_bytes: int) -> None:
        if capacity_bytes <= 0:
            raise ValueError("capacity_bytes must be positive")
        self._cap = capacity_bytes
        self._buf = bytearray(capacity_bytes)
        self._write_pos = 0  # producer only, cumulative bytes
        self._read_pos = 0  # consumer only, cumulative bytes
        self._dropped = 0  # consumer only

    @property
    def capacity(self) -> int:
        return self._cap

    @property
    def dropped_bytes(self) -> int:
        """Total bytes discarded to overflow."""
        return self._dropped

    def available(self) -> int:
        """Readable bytes, before accounting for overwritten data."""
        return min(self._write_pos - self._read_pos, self._cap)

    def write(self, data: bytes) -> None:
        """Producer only. Never blocks."""
        n = len(data)
        if n == 0:
            return
        if n >= self._cap:
            # A single write larger than the buffer — keep only the last cap bytes.
            data = data[-self._cap :]
            n = self._cap

        start = self._write_pos % self._cap
        end = start + n
        if end <= self._cap:
            self._buf[start:end] = data
        else:
            split = self._cap - start
            self._buf[start:] = data[:split]
            self._buf[: end - self._cap] = data[split:]
        self._write_pos += n

    def read(self, n: int) -> bytes | None:
        """Consumer only. Returns None until n bytes are available."""
        if n <= 0 or n > self._cap:
            raise ValueError("n must be in 1..capacity")

        # If we were lapped, drop the oldest data and catch up.
        lag = self._write_pos - self._read_pos
        if lag > self._cap:
            skipped = lag - self._cap
            self._read_pos += skipped
            self._dropped += skipped
            _log.warning("ring buffer overflow — dropped %d bytes", skipped)

        if self._write_pos - self._read_pos < n:
            return None

        start = self._read_pos % self._cap
        end = start + n
        if end <= self._cap:
            out = bytes(self._buf[start:end])
        else:
            out = bytes(self._buf[start:]) + bytes(self._buf[: end - self._cap])

        # If the producer lapped us mid-copy, that data is corrupt — retry.
        if self._write_pos - self._read_pos > self._cap:
            return self.read(n)

        self._read_pos += n
        return out

    def clear(self) -> None:
        """Consumer only. Discard everything buffered."""
        self._read_pos = self._write_pos


@dataclass(frozen=True)
class InputDevice:
    index: int
    name: str
    channels: int
    default_samplerate: float


def list_input_devices() -> list[InputDevice]:
    """Enumerate devices that have input channels. Call this every time the menu opens."""
    out: list[InputDevice] = []
    try:
        devices = sd.query_devices()
    except Exception as exc:  # PortAudio is wedged
        _log.error("could not query audio devices: %s", exc)
        return out
    for i, d in enumerate(devices):
        if d["max_input_channels"] > 0:
            out.append(
                InputDevice(
                    index=i,
                    name=str(d["name"]),
                    channels=int(d["max_input_channels"]),
                    default_samplerate=float(d["default_samplerate"]),
                )
            )
    return out


def resolve_device(name: str) -> InputDevice | None:
    """Look a device up by name. Indexes change on USB reconnect, so never store those."""
    devices = list_input_devices()
    if not devices:
        return None
    if name:
        for d in devices:
            if d.name == name:
                return d
        _log.warning("audio device %r not found — falling back to default", name)
    try:
        default_index = sd.default.device[0]
    except Exception:
        default_index = None
    if default_index is not None:
        for d in devices:
            if d.index == default_index:
                return d
    return devices[0]


def float_to_pcm16(x: np.ndarray) -> bytes:
    """float32 in [-1, 1] to 16-bit signed PCM, little-endian."""
    return np.clip(x * 32767.0, -32768.0, 32767.0).astype("<i2").tobytes()


class AudioCapture:
    """Opens a device at its native rate and feeds 16 kHz PCM into the ring buffer."""

    def __init__(
        self,
        device_name: str = "",
        ring_seconds: int = RING_SECONDS,
        on_error: object = None,
    ) -> None:
        self._device_name = device_name
        self.ring = RingBuffer(TARGET_RATE * BYTES_PER_SAMPLE * ring_seconds)
        self._stream: sd.InputStream | None = None
        self._resampler: soxr.ResampleStream | None = None
        self._channels = 1
        self._native_rate = 48_000
        self._underruns = 0
        self._lock = threading.Lock()
        self._on_error = on_error
        self.device: InputDevice | None = None

    @property
    def native_rate(self) -> int:
        return self._native_rate

    @property
    def underruns(self) -> int:
        return self._underruns

    @property
    def is_running(self) -> bool:
        return self._stream is not None and self._stream.active

    def start(self) -> InputDevice:
        """Open the stream. Raises RuntimeError if no device can be found."""
        with self._lock:
            if self._stream is not None:
                raise RuntimeError("capture already started")

            dev = resolve_device(self._device_name)
            if dev is None:
                raise RuntimeError("No audio input device found")

            self._native_rate = int(dev.default_samplerate) or 48_000
            self._channels = min(dev.channels, 2)
            self._underruns = 0
            # The streaming resampler carries filter state across chunk boundaries.
            # Calling the one-shot soxr.resample() per chunk clicks at every seam.
            self._resampler = soxr.ResampleStream(
                self._native_rate, TARGET_RATE, 1, dtype="float32", quality="HQ"
            )
            self.ring.clear()

            self._stream = sd.InputStream(
                device=dev.index,
                samplerate=self._native_rate,
                channels=self._channels,
                dtype="float32",
                blocksize=int(self._native_rate * CALLBACK_MS / 1000),
                callback=self._on_audio,
                latency="low",
            )
            self._stream.start()
            self.device = dev
            _log.info(
                "audio capture started: %r @ %d Hz, %d ch → %d Hz mono",
                dev.name,
                self._native_rate,
                self._channels,
                TARGET_RATE,
            )
            return dev

    def stop(self) -> None:
        with self._lock:
            if self._stream is None:
                return
            try:
                self._stream.stop()
                self._stream.close()
            except Exception as exc:
                _log.warning("error closing audio stream: %s", exc)
            self._stream = None
            self._resampler = None
            self.device = None
            _log.info(
                "audio capture stopped (underruns=%d, dropped=%d bytes)",
                self._underruns,
                self.ring.dropped_bytes,
            )

    # --- PortAudio callback thread ----------------------------------------
    # No Qt objects, no locks, and as little allocation as possible.

    def _on_audio(self, indata: np.ndarray, frames: int, time_info: object, status: object) -> None:
        if status:
            # A truthy CallbackFlags means there was an underrun or overflow.
            self._underruns += 1
        resampler = self._resampler
        if resampler is None:
            return
        mono = indata.mean(axis=1) if indata.shape[1] > 1 else indata[:, 0]
        out = resampler.resample_chunk(np.ascontiguousarray(mono, dtype=np.float32))
        if out.size:
            self.ring.write(float_to_pcm16(out))
