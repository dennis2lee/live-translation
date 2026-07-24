"""Phase 1 수용 기준 검증 — 실제 장치로 캡처해 바이트 수/언더런/WAV 덤프 확인.

    python spikes/s1_capture_check.py [--seconds 60] [--device "이름"] [--out capture.wav]

확인 항목 (PLAN.md §9 Phase 1)
  1. 링 버퍼에서 꺼낸 바이트 수가 16000*2*초의 ±1% 이내
  2. 콜백 언더런 0회
  3. 덤프한 WAV를 재생했을 때 클릭·끊김·피치 이상이 없음
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
    ap.add_argument("--device", default="", help="장치 이름 (생략 시 시스템 기본)")
    ap.add_argument("--out", default="capture.wav")
    ap.add_argument("--list", action="store_true", help="입력 장치만 나열하고 종료")
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
        # 스트림을 멈추고 남은 것을 마저 빼낸다.
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
    print(f"  wav written     : {out_path}  ← 재생해서 클릭/끊김/피치를 귀로 확인하십시오")

    return 0 if (drift < 0.01 and cap.underruns == 0) else 1


if __name__ == "__main__":
    raise SystemExit(main())
