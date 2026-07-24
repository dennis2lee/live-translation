"""검증용 한국어 음원 생성 — macOS 내장 TTS를 써서 원고가 확정된 WAV를 만든다.

원고가 고정되어 있으므로 Phase 3의 "재연결 구간에서 문장 유실 없음" 판정을
사람 귀 없이 기계적으로 대조할 수 있다.

    python spikes/make_ko_fixture.py                        # 원고 1회, ko_fixture.wav
    python spikes/make_ko_fixture.py --repeat 12 --out ko_30min.wav
    python spikes/make_ko_fixture.py --voice Yuna --rate 180

출력은 Live API가 요구하는 16kHz / 모노 / 16-bit PCM WAV다.
"""

from __future__ import annotations

import argparse
import subprocess
import sys
import tempfile
import wave
from pathlib import Path

FIXTURE_DIR = Path(__file__).resolve().parent / "fixtures"
DEFAULT_SCRIPT = FIXTURE_DIR / "ko_script.txt"
TARGET_RATE = 16_000


def read_script(path: Path) -> list[str]:
    lines = []
    for raw in path.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if line and not line.startswith("#"):
            lines.append(line)
    return lines


def synthesize(text: str, voice: str, rate: int, out_wav: Path) -> None:
    """say → AIFF → afconvert → 16k mono 16-bit WAV."""
    with tempfile.TemporaryDirectory() as tmp:
        aiff = Path(tmp) / "raw.aiff"
        subprocess.run(
            ["say", "-v", voice, "-r", str(rate), "-o", str(aiff), text],
            check=True,
            capture_output=True,
        )
        subprocess.run(
            [
                "afconvert", str(aiff), str(out_wav),
                "-f", "WAVE", "-d", "LEI16", "-c", "1", "-r", str(TARGET_RATE),
            ],
            check=True,
            capture_output=True,
        )


def concat(parts: list[Path], out: Path, gap_seconds: float) -> None:
    """여러 WAV를 이어 붙이고 문장 사이에 무음을 넣는다."""
    gap = b"\x00\x00" * int(TARGET_RATE * gap_seconds)
    with wave.open(str(out), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(2)
        w.setframerate(TARGET_RATE)
        for i, part in enumerate(parts):
            with wave.open(str(part), "rb") as r:
                w.writeframes(r.readframes(r.getnframes()))
            if i != len(parts) - 1:
                w.writeframes(gap)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--script", type=Path, default=DEFAULT_SCRIPT)
    ap.add_argument("--out", type=Path, default=FIXTURE_DIR / "ko_fixture.wav")
    ap.add_argument("--voice", default="Yuna", help="say -v '?' 로 목록 확인")
    ap.add_argument("--rate", type=int, default=190, help="말하기 속도 (words/min)")
    ap.add_argument("--gap", type=float, default=0.6, help="문장 사이 무음 (초)")
    ap.add_argument("--repeat", type=int, default=1, help="원고 반복 횟수")
    args = ap.parse_args()

    if sys.platform != "darwin":
        print("macOS 전용입니다 (say/afconvert 사용)", file=sys.stderr)
        return 1

    sentences = read_script(args.script)
    if not sentences:
        print(f"원고가 비어 있습니다: {args.script}", file=sys.stderr)
        return 1

    args.out.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory() as tmp:
        parts: list[Path] = []
        total = len(sentences) * args.repeat
        for rep in range(args.repeat):
            for i, sentence in enumerate(sentences):
                part = Path(tmp) / f"{rep:03d}_{i:03d}.wav"
                synthesize(sentence, args.voice, args.rate, part)
                parts.append(part)
                done = rep * len(sentences) + i + 1
                print(f"\r  synthesizing {done}/{total}", end="", flush=True)
        print()
        concat(parts, args.out, args.gap)

    with wave.open(str(args.out), "rb") as r:
        seconds = r.getnframes() / r.getframerate()
    print(f"wrote {args.out}  —  {seconds / 60:.1f} min, "
          f"{len(sentences) * args.repeat} sentences, {TARGET_RATE} Hz mono 16-bit")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
