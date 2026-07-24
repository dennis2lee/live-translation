"""Phase 0 스파이크 — 전사 조각의 도착 패턴 측정. PLAN.md §9 Phase 0.

    python spikes/s0_transcription_timing.py                 # 마이크 30초
    python spikes/s0_transcription_timing.py --wav ko.wav    # 파일 입력
    python spikes/s0_transcription_timing.py --seconds 60

확인할 것
  1. 조각이 글자/단어 단위로 오는가, 문장 단위로 오는가 → 정책 A/B 결정
  2. 첫 텍스트까지의 지연(초)
  3. input_transcription도 같은 패턴인가
  4. session_resumption_update가 몇 초 간격으로 오는가

측정이 끝나면 요약을 stdout에 찍는다. 그 답을 spikes/RESULTS.md에 기록할 것.
"""

from __future__ import annotations

import argparse
import asyncio
import statistics
import sys
import time
import wave
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from google import genai  # noqa: E402
from google.genai import types  # noqa: E402

from livecap.audio import CHUNK_BYTES, TARGET_RATE, AudioCapture  # noqa: E402
from livecap.config import load_api_key  # noqa: E402
from livecap.translator import (  # noqa: E402
    AUDIO_MIME,
    MODEL,
    RESPONSE_MODALITIES,
    fatal_reason,
)
from livecap.util import logging as log  # noqa: E402


class Recorder:
    def __init__(self, t0: float) -> None:
        self.t0 = t0
        self.output: list[tuple[float, str]] = []
        self.source: list[tuple[float, str]] = []
        self.resumption: list[float] = []
        self.go_away: list[tuple[float, object]] = []

    def note(self, bucket: list, text: object) -> None:
        bucket.append((time.monotonic() - self.t0, text))

    def report(self) -> None:
        print("\n" + "=" * 72)
        print("Phase 0 결과 요약")
        print("=" * 72)

        for label, rows in (("output_transcription", self.output),
                            ("input_transcription", self.source)):
            print(f"\n### {label}")
            if not rows:
                print("  (조각 없음)")
                continue
            lens = [len(t) for _, t in rows]
            gaps = [b - a for (a, _), (b, _) in zip(rows, rows[1:], strict=False)]
            print(f"  조각 수      : {len(rows)}")
            print(f"  첫 조각까지  : {rows[0][0]:.3f} s")
            print(f"  길이 median  : {statistics.median(lens):.0f} chars "
                  f"(min {min(lens)}, max {max(lens)})")
            if gaps:
                print(f"  도착 간격 med: {statistics.median(gaps) * 1000:.0f} ms")
            verdict = "정책 B (발화 단위)" if statistics.median(lens) >= 25 else "정책 A (점진)"
            print(f"  → 판정       : {verdict}")

        print("\n### session_resumption_update")
        if len(self.resumption) >= 2:
            iv = [b - a for a, b in zip(self.resumption, self.resumption[1:], strict=False)]
            print(f"  수신 {len(self.resumption)}회, 간격 median {statistics.median(iv):.1f} s")
        else:
            print(f"  수신 {len(self.resumption)}회 (간격 산출 불가)")

        print("\n### go_away")
        print(f"  {len(self.go_away)}회 " + (f"— {self.go_away}" if self.go_away else ""))
        print("\n이 결과를 spikes/RESULTS.md에 기록하고 Phase 3으로 넘어가십시오.")


async def feed_from_wav(session, path: Path, rec: Recorder) -> None:
    with wave.open(str(path), "rb") as w:
        if w.getframerate() != TARGET_RATE or w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise SystemExit(
                f"WAV must be {TARGET_RATE} Hz mono 16-bit; got "
                f"{w.getframerate()} Hz {w.getnchannels()}ch {w.getsampwidth() * 8}-bit"
            )
        while data := w.readframes(CHUNK_BYTES // 2):
            await session.send_realtime_input(audio=types.Blob(data=data, mime_type=AUDIO_MIME))
            await asyncio.sleep(0.1)  # 실시간 속도로 흘린다


async def feed_from_mic(session, seconds: float, rec: Recorder) -> None:
    cap = AudioCapture()
    dev = cap.start()
    print(f"  mic: {dev.name!r} @ {cap.native_rate} Hz — 한국어로 말씀하십시오")
    deadline = time.monotonic() + seconds
    try:
        while time.monotonic() < deadline:
            chunk = cap.ring.read(CHUNK_BYTES)
            if chunk is None:
                await asyncio.sleep(0.01)
                continue
            await session.send_realtime_input(audio=types.Blob(data=chunk, mime_type=AUDIO_MIME))
    finally:
        cap.stop()


async def run(args: argparse.Namespace) -> int:
    api_key = load_api_key()
    if not api_key:
        print(
            "GEMINI_API_KEY가 없습니다.\n"
            "  export GEMINI_API_KEY=...   또는 앱 환경설정에서 키체인에 저장하십시오.",
            file=sys.stderr,
        )
        return 2

    client = genai.Client(api_key=api_key)
    config = types.LiveConnectConfig(
        response_modalities=RESPONSE_MODALITIES,
        input_audio_transcription=types.AudioTranscriptionConfig(),
        output_audio_transcription=types.AudioTranscriptionConfig(),
        translation_config=types.TranslationConfig(
            target_language_code=args.target, echo_target_language=False
        ),
        context_window_compression=types.ContextWindowCompressionConfig(
            sliding_window=types.SlidingWindow(),
        ),
        session_resumption=types.SessionResumptionConfig(handle=None),
    )

    print(f"connecting to {MODEL} …")
    try:
        return await stream(client, config, args)
    except Exception as exc:
        reason = fatal_reason(exc)
        if reason is None:
            raise
        print(f"\n연결 실패: {reason}\n  원본 오류: {str(exc)[:200]}", file=sys.stderr)
        return 3


async def stream(client, config, args: argparse.Namespace) -> int:
    async with client.aio.live.connect(model=MODEL, config=config) as session:
        rec = Recorder(time.monotonic())
        print("connected. streaming audio.\n")

        async def receiver() -> None:
            async for response in session.receive():
                t = time.monotonic() - rec.t0
                if response.go_away is not None:
                    rec.note(rec.go_away, getattr(response.go_away, "time_left", None))
                    print(f"t=+{t:.3f}s  GO_AWAY time_left={response.go_away.time_left}")
                upd = response.session_resumption_update
                if upd is not None and upd.new_handle:
                    rec.resumption.append(t)
                    print(f"t=+{t:.3f}s  RESUMPTION_UPDATE resumable={upd.resumable}")

                sc = response.server_content
                if sc is None:
                    continue
                if sc.input_transcription and sc.input_transcription.text:
                    txt = sc.input_transcription.text
                    rec.note(rec.source, txt)
                    print(f"t=+{t:.3f}s  IN   len={len(txt):<4} text={txt!r}")
                if sc.output_transcription and sc.output_transcription.text:
                    txt = sc.output_transcription.text
                    rec.note(rec.output, txt)
                    print(f"t=+{t:.3f}s  OUT  len={len(txt):<4} text={txt!r}")
                # 문서화되지 않은 필드 — 점진 전사가 여기로 올 가능성이 있어 함께 관측한다.
                interim = getattr(sc, "interim_input_transcription", None)
                if interim is not None and getattr(interim, "text", None):
                    print(f"t=+{t:.3f}s  INTERIM len={len(interim.text):<4} "
                          f"text={interim.text!r}")

        recv_task = asyncio.create_task(receiver())
        try:
            if args.wav:
                await feed_from_wav(session, Path(args.wav), rec)
            else:
                await feed_from_mic(session, args.seconds, rec)
            # 마지막 전사가 흘러나올 시간을 준다.
            await asyncio.sleep(args.drain)
        finally:
            recv_task.cancel()
            rec.report()
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--wav", help="16kHz mono 16-bit WAV 파일 (생략 시 마이크)")
    ap.add_argument("--seconds", type=float, default=30.0, help="마이크 캡처 길이")
    ap.add_argument("--drain", type=float, default=5.0, help="전송 후 대기 시간")
    ap.add_argument("--target", default="en")
    args = ap.parse_args()
    log.setup()
    try:
        return asyncio.run(run(args))
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
