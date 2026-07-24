"""Measure how transcription fragments arrive.

    python spikes/s0_transcription_timing.py                 # 30 s from the mic
    python spikes/s0_transcription_timing.py --wav ko.wav    # from a file
    python spikes/s0_transcription_timing.py --seconds 60

Questions this answers
  1. per word or per utterance — decides caption policy A or B
  2. seconds until the first text
  3. whether input_transcription behaves the same
  4. how often session_resumption_update arrives

A summary is printed at the end. Record it in spikes/RESULTS.md.
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
        print("Fragment arrival summary")
        print("=" * 72)

        for label, rows in (("output_transcription", self.output),
                            ("input_transcription", self.source)):
            print(f"\n### {label}")
            if not rows:
                print("  (no fragments)")
                continue
            lens = [len(t) for _, t in rows]
            gaps = [b - a for (a, _), (b, _) in zip(rows, rows[1:], strict=False)]
            leading = sum(1 for _, t in rows if t.startswith((" ", "\n")))
            print(f"  fragments    : {len(rows)}")
            print(f"  leading space: {leading}/{len(rows)} "
                  f"({leading / len(rows) * 100:.0f}%) "
                  f"— if not 100%, revisit the CaptionBuffer._join rule")
            print(f"  first at     : {rows[0][0]:.3f} s")
            print(f"  median length: {statistics.median(lens):.0f} chars "
                  f"(min {min(lens)}, max {max(lens)})")
            if gaps:
                print(f"  median gap   : {statistics.median(gaps) * 1000:.0f} ms")
            median_len = statistics.median(lens)
            verdict = "policy B (per utterance)" if median_len >= 25 else "policy A (incremental)"
            print(f"  → verdict    : {verdict}")

        print("\n### session_resumption_update")
        if len(self.resumption) >= 2:
            iv = [b - a for a, b in zip(self.resumption, self.resumption[1:], strict=False)]
            print(
                f"  {len(self.resumption)} received, "
                f"median interval {statistics.median(iv):.1f} s"
            )
        else:
            print(f"  {len(self.resumption)} received (too few for an interval)")

        print("\n### go_away")
        print(f"  {len(self.go_away)} received " + (f"— {self.go_away}" if self.go_away else ""))
        print("\nRecord these numbers in spikes/RESULTS.md.")


async def feed_from_wav(session, path: Path, rec: Recorder) -> None:
    with wave.open(str(path), "rb") as w:
        if w.getframerate() != TARGET_RATE or w.getnchannels() != 1 or w.getsampwidth() != 2:
            raise SystemExit(
                f"WAV must be {TARGET_RATE} Hz mono 16-bit; got "
                f"{w.getframerate()} Hz {w.getnchannels()}ch {w.getsampwidth() * 8}-bit"
            )
        while data := w.readframes(CHUNK_BYTES // 2):
            await session.send_realtime_input(audio=types.Blob(data=data, mime_type=AUDIO_MIME))
            await asyncio.sleep(0.1)  # feed at real-time speed


async def feed_from_mic(session, seconds: float, rec: Recorder) -> None:
    cap = AudioCapture()
    dev = cap.start()
    print(f"  mic: {dev.name!r} @ {cap.native_rate} Hz — start speaking")
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
            "No GEMINI_API_KEY.\n"
            "  export GEMINI_API_KEY=...   or store one in the Keychain via Preferences.",
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
        print(f"\nconnection failed: {reason}\n  original error: {str(exc)[:200]}", file=sys.stderr)
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
                # Undocumented field — watch it in case incremental text arrives here.
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
            # Give the last transcriptions time to arrive.
            await asyncio.sleep(args.drain)
        finally:
            recv_task.cancel()
            rec.report()
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--wav", help="16 kHz mono 16-bit WAV; omit to use the microphone")
    ap.add_argument("--seconds", type=float, default=30.0, help="microphone capture length")
    ap.add_argument("--drain", type=float, default=5.0, help="how long to wait after sending")
    ap.add_argument("--target", default="en")
    args = ap.parse_args()
    log.setup()
    try:
        return asyncio.run(run(args))
    except KeyboardInterrupt:
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
