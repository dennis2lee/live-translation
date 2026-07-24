"""Run the real pipeline headless for a long stretch.

A WAV is fed into the ring buffer at real-time speed and LiveTranslator runs
unchanged. Identical to the app except that there is no UI.

    # five minutes
    python spikes/s2_headless_run.py --wav spikes/fixtures/ko_6min.wav --minutes 5

    # thirty minutes, compared against the script
    python spikes/s2_headless_run.py --wav spikes/fixtures/ko_30min.wav --minutes 30 \
        --script spikes/fixtures/ko_script.txt --repeat 20

Reports
  - reconnect count and the gap in ms for each
  - sentences lost, by comparing the transcription against the script
  - measured cost
"""

from __future__ import annotations

import argparse
import difflib
import re
import sys
import threading
import time
import wave
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from livecap.audio import CHUNK_BYTES, TARGET_RATE, RingBuffer  # noqa: E402
from livecap.caption_buffer import CaptionBuffer, Policy  # noqa: E402
from livecap.config import load_api_key  # noqa: E402
from livecap.translator import LiveTranslator, State  # noqa: E402
from livecap.util import logging as log  # noqa: E402

# $0.0053/min input, $0.0315/min output
COST_PER_MINUTE = 0.0053 + 0.0315


def normalize(text: str) -> str:
    """Normalise for comparison: drop whitespace and punctuation."""
    return re.sub(r"[^\w]", "", text)


def coverage(sentence: str, haystack: str) -> float:
    """How much of a script sentence survives in the transcription, 0 to 1.

    The ASR rewrites numbers as digits and spaces words differently, so scoring
    by the single longest matching run flags healthy sentences as lost. Sum every
    matching block instead.
    """
    needle = normalize(sentence)
    if not needle:
        return 1.0
    matcher = difflib.SequenceMatcher(None, needle, haystack, autojunk=False)
    # Ignore single-character coincidences; they inflate the score.
    matched = sum(b.size for b in matcher.get_matching_blocks() if b.size >= 2)
    return min(1.0, matched / len(needle))


def read_script(path: Path) -> list[str]:
    return [
        line.strip()
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.strip().startswith("#")
    ]


def feed_wav(ring: RingBuffer, wav_path: Path, stop: threading.Event, minutes: float) -> float:
    """Feed the WAV into the ring buffer at real-time speed; return seconds fed."""
    deadline = time.monotonic() + minutes * 60
    started = time.monotonic()
    with wave.open(str(wav_path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (TARGET_RATE, 1, 2):
            raise SystemExit(f"WAV must be {TARGET_RATE} Hz mono 16-bit")
        next_at = time.monotonic()
        while not stop.is_set() and time.monotonic() < deadline:
            data = w.readframes(CHUNK_BYTES // 2)
            if not data:
                w.rewind()  # loop if the file is shorter than the run
                continue
            ring.write(data)
            next_at += CHUNK_BYTES / 2 / TARGET_RATE
            sleep = next_at - time.monotonic()
            if sleep > 0:
                time.sleep(sleep)
    return time.monotonic() - started


def report_script_match(sentences: list[str], recognised: str, threshold: float) -> int:
    """Compare script against transcription, print the result, return 1 if lost."""
    hay = normalize(recognised)
    print(f"\n  script comparison: {len(sentences)} sentences")
    scores = [coverage(s, hay) for s in sentences]
    missing = [
        (i, s, r) for i, (s, r) in enumerate(zip(sentences, scores, strict=True), 1)
        if r < threshold
    ]
    if scores:
        print(f"  similarity worst {min(scores):.2f} / mean {sum(scores) / len(scores):.2f}")
    if missing:
        print(f"  ⚠ {len(missing)} sentences may be lost (threshold {threshold}):")
        for i, sentence, ratio in missing[:20]:
            print(f"      #{i:3d} ratio={ratio:.2f}  {sentence[:40]}")
        return 1
    print(f"  ✅ no sentence lost (all above {threshold})")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--wav", type=Path, required=True)
    ap.add_argument("--minutes", type=float, default=5.0)
    ap.add_argument("--script", type=Path, help="script to compare the transcription against")
    ap.add_argument(
        "--repeat", type=int, default=1, help="how many times the script repeats"
    )
    ap.add_argument(
        "--threshold", type=float, default=0.5, help="similarity below which a sentence is lost"
    )
    ap.add_argument("--out", type=Path, help="where to save the full transcription")
    ap.add_argument(
        "--compare-only",
        type=Path,
        help="re-score a saved transcription without calling the API",
    )
    args = ap.parse_args()

    log.setup()

    if args.compare_only:
        if not args.script:
            print("--compare-only requires --script", file=sys.stderr)
            return 2
        text = args.compare_only.read_text(encoding="utf-8")
        recognised = text.split("# Source transcription")[-1].split("# Translation")[0]
        return report_script_match(
            read_script(args.script) * args.repeat, recognised, args.threshold
        )
    api_key = load_api_key()
    if not api_key:
        print("No GEMINI_API_KEY — check .env", file=sys.stderr)
        return 2

    ring = RingBuffer(TARGET_RATE * 2 * 30)
    buffer = CaptionBuffer(max_lines=2, policy=Policy.INCREMENTAL)
    source_parts: list[str] = []
    target_parts: list[str] = []
    states: list[tuple[float, str, str]] = []
    t0 = time.monotonic()
    fatal: list[str] = []

    def on_source(text: str) -> None:
        source_parts.append(text)

    def on_target(text: str) -> None:
        target_parts.append(text)
        if buffer.feed(text, time.monotonic()):
            lines = buffer.committed_lines
            if lines:
                print(f"\r[{time.monotonic() - t0:7.1f}s] {lines[-1]}", flush=True)

    def on_state(state: State, detail: str) -> None:
        states.append((time.monotonic() - t0, state.value, detail))
        print(f"\r--- [{time.monotonic() - t0:7.1f}s] state={state.value} {detail}", flush=True)
        if state is State.ERROR:
            fatal.append(detail)

    translator = LiveTranslator(
        ring, api_key,
        on_source=on_source, on_target=on_target,
        on_state=on_state, on_reconnect=buffer.note_reconnect,
    )

    stop = threading.Event()
    translator.start()
    print(f"streaming {args.wav.name} for {args.minutes:.0f} min …\n")
    try:
        feed_wav(ring, args.wav, stop, args.minutes)
        time.sleep(8)  # let the last transcriptions arrive
    except KeyboardInterrupt:
        print("\ninterrupted")
    finally:
        stop.set()
        translator.stop()

    elapsed_min = (time.monotonic() - t0) / 60
    recognised = "".join(source_parts)
    translated = "".join(target_parts)

    print("\n" + "=" * 72)
    print("Run summary")
    print("=" * 72)
    print(f"  duration        : {elapsed_min:.1f} min")
    print(f"  reconnects      : {translator.stats.reconnects}")
    gaps = translator.stats.gaps_ms
    print(f"  reconnect gaps  : {[f'{g:.0f}' for g in gaps] if gaps else '-'} ms")
    if gaps:
        print(f"  worst gap       : {max(gaps):.0f} ms")
    sent_seconds = translator.stats.chunks_sent / 10
    print(f"  chunks sent     : {translator.stats.chunks_sent} (= {sent_seconds:.0f}s audio)")
    print(f"  ring drops      : {ring.dropped_bytes} bytes")
    print(f"  source chars    : {len(recognised)}")
    print(f"  translated chars: {len(translated)}")
    est = elapsed_min * COST_PER_MINUTE
    print(f"  estimated cost  : ${est:.2f}  (${COST_PER_MINUTE:.4f}/min)")
    if fatal:
        print(f"  fatal errors    : {fatal}")

    exit_code = 0

    if args.script and report_script_match(
        read_script(args.script) * args.repeat, recognised, args.threshold
    ):
        exit_code = 1

    if args.out:
        args.out.write_text(
            f"# Source transcription\n\n{recognised}\n\n# Translation\n\n{translated}\n",
            encoding="utf-8",
        )
        print(f"\n  transcription saved: {args.out}")

    if fatal:
        exit_code = 2
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
