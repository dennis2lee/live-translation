"""오디오 캡처 파이프라인 — PLAN.md §5.

USB 인터페이스를 네이티브 레이트로 열고, 모노 다운믹스 → soxr 스트리밍 리샘플
→ int16 LE → 링 버퍼 순으로 흘린다. 링 버퍼는 연결 상태와 무관하게 계속
채워지므로 재연결 중에도 오디오가 유실되지 않는다.
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
    """단일 생산자(오디오 콜백) / 단일 소비자(asyncio send task) 링 버퍼.

    락을 쓰지 않는다. 쓰기 위치는 생산자만, 읽기 위치는 소비자만 갱신하며
    둘 다 단조 증가하는 파이썬 int라 GIL 하에서 읽기/쓰기가 원자적이다.
    가득 차면 생산자는 블로킹하지 않고 그대로 덮어쓰며, 소비자가 다음 읽기에서
    자신이 추월당했음을 감지해 가장 오래된 데이터를 건너뛴다.
    """

    def __init__(self, capacity_bytes: int) -> None:
        if capacity_bytes <= 0:
            raise ValueError("capacity_bytes must be positive")
        self._cap = capacity_bytes
        self._buf = bytearray(capacity_bytes)
        self._write_pos = 0  # 생산자만 갱신 (누적 바이트 수)
        self._read_pos = 0  # 소비자만 갱신 (누적 바이트 수)
        self._dropped = 0  # 소비자만 갱신

    @property
    def capacity(self) -> int:
        return self._cap

    @property
    def dropped_bytes(self) -> int:
        """오버플로로 버려진 누적 바이트 수."""
        return self._dropped

    def available(self) -> int:
        """읽을 수 있는 바이트 수 (덮어쓰기 손실 반영 전 값)."""
        return min(self._write_pos - self._read_pos, self._cap)

    def write(self, data: bytes) -> None:
        """생산자 전용. 절대 블로킹하지 않는다."""
        n = len(data)
        if n == 0:
            return
        if n >= self._cap:
            # 한 번에 용량을 넘는 쓰기 — 마지막 cap 바이트만 남긴다.
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
        """소비자 전용. n바이트가 모이지 않았으면 None."""
        if n <= 0 or n > self._cap:
            raise ValueError("n must be in 1..capacity")

        # 추월당했으면 가장 오래된 데이터를 버리고 따라잡는다.
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

        # 복사 도중 생산자에게 추월당했는지 확인. 당했다면 그 데이터는 오염됐다.
        if self._write_pos - self._read_pos > self._cap:
            return self.read(n)

        self._read_pos += n
        return out

    def clear(self) -> None:
        """소비자 전용. 쌓인 것을 모두 버린다."""
        self._read_pos = self._write_pos


@dataclass(frozen=True)
class InputDevice:
    index: int
    name: str
    channels: int
    default_samplerate: float


def list_input_devices() -> list[InputDevice]:
    """입력 채널이 있는 장치만 열거한다. 메뉴를 열 때마다 호출할 것."""
    out: list[InputDevice] = []
    try:
        devices = sd.query_devices()
    except Exception as exc:  # PortAudio가 죽어 있는 경우
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
    """이름으로 장치를 찾는다. 인덱스는 USB 재연결 시 바뀌므로 쓰지 않는다."""
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
    """float32 [-1,1] → 16-bit signed PCM little-endian."""
    return np.clip(x * 32767.0, -32768.0, 32767.0).astype("<i2").tobytes()


class AudioCapture:
    """장치를 네이티브 레이트로 열고 16k PCM을 링 버퍼로 밀어 넣는다."""

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
        """스트림을 연다. 장치를 못 찾으면 RuntimeError."""
        with self._lock:
            if self._stream is not None:
                raise RuntimeError("capture already started")

            dev = resolve_device(self._device_name)
            if dev is None:
                raise RuntimeError("오디오 입력 장치를 찾을 수 없습니다")

            self._native_rate = int(dev.default_samplerate) or 48_000
            self._channels = min(dev.channels, 2)
            self._underruns = 0
            # 스트리밍 리샘플러는 청크 경계 상태를 유지한다. 일회성 soxr.resample()을
            # 청크마다 부르면 경계에서 클릭 노이즈가 생긴다.
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

    # --- PortAudio 콜백 스레드 ---------------------------------------------
    # 여기서는 Qt 객체를 만지지 않고, 락도 잡지 않으며, 할당을 최소화한다.

    def _on_audio(self, indata: np.ndarray, frames: int, time_info: object, status: object) -> None:
        if status:
            # CallbackFlags는 bool로 평가되면 언더런/오버플로가 있었다는 뜻이다.
            self._underruns += 1
        resampler = self._resampler
        if resampler is None:
            return
        mono = indata.mean(axis=1) if indata.shape[1] > 1 else indata[:, 0]
        out = resampler.resample_chunk(np.ascontiguousarray(mono, dtype=np.float32))
        if out.size:
            self.ring.write(float_to_pcm16(out))
