"""Phase 2·3 수용 기준 검증 — 실제 파이프라인을 헤드리스로 장시간 돌린다.

WAV를 실시간 속도로 링 버퍼에 흘려 넣고 LiveTranslator를 그대로 구동한다.
UI만 없을 뿐 앱과 같은 경로다.

    # Phase 2 — 5분 연속
    python spikes/s2_headless_run.py --wav spikes/fixtures/ko_6min.wav --minutes 5

    # Phase 3 — 30분 완주 + 원고 대조
    python spikes/s2_headless_run.py --wav spikes/fixtures/ko_30min.wav --minutes 30 \
        --script spikes/fixtures/ko_script.txt --repeat 20

검사 항목
  - 재연결 횟수와 각 재연결의 공백 시간(ms)
  - 원문 전사와 원고를 대조한 문장 유실 (difflib 유사도)
  - 실측 비용 추정
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

# PLAN.md §11 — 입력 $0.0053/분, 출력 $0.0315/분
COST_PER_MINUTE = 0.0053 + 0.0315


def normalize(text: str) -> str:
    """대조용 정규화 — 공백·구두점 제거."""
    return re.sub(r"[^\w]", "", text)


def coverage(sentence: str, haystack: str) -> float:
    """원고 문장이 전사에 얼마나 남아 있는지 0~1로.

    ASR은 숫자를 아라비아 숫자로 바꿔 쓰고(십일월→11월, 삼십 퍼센트→30%)
    띄어쓰기도 다르게 넣는다(영업이익률→영업이익 율). 따라서 '가장 긴 연속
    일치' 하나로 재면 멀쩡한 문장이 유실로 잡힌다. 일치 구간을 모두 합산한다.
    """
    needle = normalize(sentence)
    if not needle:
        return 1.0
    matcher = difflib.SequenceMatcher(None, needle, haystack, autojunk=False)
    # 1글자 우연 일치는 세지 않는다 — 한국어에서는 흔해서 점수를 부풀린다.
    matched = sum(b.size for b in matcher.get_matching_blocks() if b.size >= 2)
    return min(1.0, matched / len(needle))


def read_script(path: Path) -> list[str]:
    return [
        line.strip()
        for line in path.read_text(encoding="utf-8").splitlines()
        if line.strip() and not line.strip().startswith("#")
    ]


def feed_wav(ring: RingBuffer, wav_path: Path, stop: threading.Event, minutes: float) -> float:
    """WAV를 실시간 속도로 링 버퍼에 흘려 넣는다. 실제 흘린 초를 돌려준다."""
    deadline = time.monotonic() + minutes * 60
    started = time.monotonic()
    with wave.open(str(wav_path), "rb") as w:
        if (w.getframerate(), w.getnchannels(), w.getsampwidth()) != (TARGET_RATE, 1, 2):
            raise SystemExit(f"WAV must be {TARGET_RATE} Hz mono 16-bit")
        next_at = time.monotonic()
        while not stop.is_set() and time.monotonic() < deadline:
            data = w.readframes(CHUNK_BYTES // 2)
            if not data:
                w.rewind()  # 음원이 짧으면 반복
                continue
            ring.write(data)
            next_at += CHUNK_BYTES / 2 / TARGET_RATE
            sleep = next_at - time.monotonic()
            if sleep > 0:
                time.sleep(sleep)
    return time.monotonic() - started


def report_script_match(sentences: list[str], recognised: str, threshold: float) -> int:
    """원고와 원문 전사를 대조해 결과를 찍고, 유실이 있으면 1을 돌려준다."""
    hay = normalize(recognised)
    print(f"\n  원고 대조: {len(sentences)}문장")
    scores = [coverage(s, hay) for s in sentences]
    missing = [
        (i, s, r) for i, (s, r) in enumerate(zip(sentences, scores, strict=True), 1)
        if r < threshold
    ]
    if scores:
        print(f"  문장 유사도 최저 {min(scores):.2f} / 평균 {sum(scores) / len(scores):.2f}")
    if missing:
        print(f"  ⚠ 유실 의심 {len(missing)}문장 (임계 {threshold}):")
        for i, sentence, ratio in missing[:20]:
            print(f"      #{i:3d} ratio={ratio:.2f}  {sentence[:40]}")
        return 1
    print(f"  ✅ 유실 문장 없음 (전 문장 유사도 ≥ {threshold})")
    return 0


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--wav", type=Path, required=True)
    ap.add_argument("--minutes", type=float, default=5.0)
    ap.add_argument("--script", type=Path, help="원고 — 문장 유실 대조용")
    ap.add_argument("--repeat", type=int, default=1, help="음원 안에서 원고가 반복된 횟수")
    ap.add_argument("--threshold", type=float, default=0.5, help="유실 판정 유사도 임계")
    ap.add_argument("--out", type=Path, help="전체 전사를 저장할 경로")
    ap.add_argument(
        "--compare-only",
        type=Path,
        help="저장된 전사(--out 결과)로 원고 대조만 다시 수행한다. API를 쓰지 않는다.",
    )
    args = ap.parse_args()

    log.setup()

    if args.compare_only:
        if not args.script:
            print("--compare-only 에는 --script 가 필요합니다", file=sys.stderr)
            return 2
        text = args.compare_only.read_text(encoding="utf-8")
        recognised = text.split("# 원문 전사")[-1].split("# 번역 출력")[0]
        return report_script_match(
            read_script(args.script) * args.repeat, recognised, args.threshold
        )
    api_key = load_api_key()
    if not api_key:
        print("GEMINI_API_KEY가 없습니다 (.env 확인)", file=sys.stderr)
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
        time.sleep(8)  # 마지막 전사가 흘러나올 시간
    except KeyboardInterrupt:
        print("\ninterrupted")
    finally:
        stop.set()
        translator.stop()

    elapsed_min = (time.monotonic() - t0) / 60
    recognised = "".join(source_parts)
    translated = "".join(target_parts)

    print("\n" + "=" * 72)
    print("Phase 2/3 결과")
    print("=" * 72)
    print(f"  실행 시간        : {elapsed_min:.1f} 분")
    print(f"  재연결 횟수      : {translator.stats.reconnects}")
    gaps = translator.stats.gaps_ms
    print(f"  재연결 공백(ms)  : {[f'{g:.0f}' for g in gaps] if gaps else '-'}")
    if gaps:
        print(f"  공백 최대        : {max(gaps):.0f} ms")
    sent_seconds = translator.stats.chunks_sent / 10
    print(f"  전송 청크        : {translator.stats.chunks_sent} (= {sent_seconds:.0f}s 오디오)")
    print(f"  링 버퍼 유실     : {ring.dropped_bytes} bytes")
    print(f"  원문 전사 길이   : {len(recognised)} chars")
    print(f"  번역 출력 길이   : {len(translated)} chars")
    est = elapsed_min * COST_PER_MINUTE
    print(f"  추정 비용        : ${est:.2f}  (분당 ${COST_PER_MINUTE:.4f})")
    if fatal:
        print(f"  치명적 오류      : {fatal}")

    exit_code = 0

    if args.script and report_script_match(
        read_script(args.script) * args.repeat, recognised, args.threshold
    ):
        exit_code = 1

    if args.out:
        args.out.write_text(
            f"# 원문 전사\n\n{recognised}\n\n# 번역 출력\n\n{translated}\n", encoding="utf-8"
        )
        print(f"\n  전사 저장: {args.out}")

    if fatal:
        exit_code = 2
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
