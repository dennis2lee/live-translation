"""Build test audio from a fixed script using the macOS TTS voices.

Because the script is known, "no sentence lost across a reconnect" can be
checked mechanically instead of by ear.

    python spikes/make_tts_fixture.py                       # one pass, ko_fixture.wav
    python spikes/make_ko_fixture.py --repeat 12 --out ko_30min.wav
    python spikes/make_ko_fixture.py --voice Yuna --rate 180

Output is 16 kHz mono 16-bit PCM WAV, as the Live API requires.
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
    """Concatenate the parts, inserting silence between sentences."""
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
    ap.add_argument("--voice", default="Yuna", help="list them with: say -v '?'")
    ap.add_argument("--rate", type=int, default=190, help="speaking rate, words per minute")
    ap.add_argument("--gap", type=float, default=0.6, help="silence between sentences, seconds")
    ap.add_argument("--repeat", type=int, default=1, help="how many times to repeat the script")
    args = ap.parse_args()

    if sys.platform != "darwin":
        print("macOS only — this uses say and afconvert", file=sys.stderr)
        return 1

    sentences = read_script(args.script)
    if not sentences:
        print(f"script is empty: {args.script}", file=sys.stderr)
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
