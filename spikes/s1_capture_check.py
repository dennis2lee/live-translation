"""Capture from a real device and check byte count, underruns and audio quality.

    python spikes/s1_capture_check.py [--seconds 60] [--device "name"] [--out capture.wav]

Acceptance
  1. bytes read from the ring buffer within 1% of 16000 * 2 * seconds
  2. zero callback underruns
  3. the dumped WAV plays without clicks, dropouts or wrong pitch
"""

from __future__ import annotations

import argparse
import sys
import time
import wave
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1] / "src"))

from livecap.audio import (  # noqa: E402
    BYTES_PER_SAMPLE,
    CHUNK_BYTES,
    TARGET_RATE,
    AudioCapture,
    list_input_devices,
)
from livecap.util import logging as log  # noqa: E402


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--seconds", type=float, default=60.0)
    ap.add_argument("--device", default="", help="device name; omit for the system default")
    ap.add_argument("--out", default="capture.wav")
    ap.add_argument("--list", action="store_true", help="list input devices and exit")
    args = ap.parse_args()

    log.setup()

    if args.list:
        for d in list_input_devices():
            print(f"  [{d.index}] {d.name!r}  ch={d.channels}  rate={d.default_samplerate:.0f}")
        return 0

    cap = AudioCapture(device_name=args.device)
    try:
        dev = cap.start()
    except RuntimeError as exc:
        print(f"FAIL: {exc}", file=sys.stderr)
        return 1

    print(f"capturing {args.seconds:.0f}s from {dev.name!r} @ {cap.native_rate} Hz …")
    collected = bytearray()
    deadline = time.monotonic() + args.seconds
    try:
        while time.monotonic() < deadline:
            chunk = cap.ring.read(CHUNK_BYTES)
            if chunk is None:
                time.sleep(0.01)
                continue
            collected += chunk
        # Stop the stream, then drain whatever is left.
        cap.stop()
        while (chunk := cap.ring.read(CHUNK_BYTES)) is not None:
            collected += chunk
    except KeyboardInterrupt:
        cap.stop()

    expected = int(TARGET_RATE * BYTES_PER_SAMPLE * args.seconds)
    drift = abs(len(collected) - expected) / expected if expected else 1.0

    out_path = Path(args.out)
    with wave.open(str(out_path), "wb") as w:
        w.setnchannels(1)
        w.setsampwidth(BYTES_PER_SAMPLE)
        w.setframerate(TARGET_RATE)
        w.writeframes(bytes(collected))

    print()
    print(f"  bytes collected : {len(collected)}  (expected ~{expected})")
    print(f"  drift           : {drift * 100:.3f} %   {'OK' if drift < 0.01 else 'FAIL (>1%)'}")
    print(f"  underruns       : {cap.underruns}      {'OK' if cap.underruns == 0 else 'FAIL'}")
    print(f"  ring drops      : {cap.ring.dropped_bytes} bytes")
    print(f"  wav written     : {out_path}  ← play it and listen for clicks or dropouts")

    return 0 if (drift < 0.01 and cap.underruns == 0) else 1


if __name__ == "__main__":
    raise SystemExit(main())
