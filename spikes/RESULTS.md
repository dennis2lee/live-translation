# Phase 0 — 전사 도착 패턴 측정 결과

> **상태: 미측정.** API 키가 없어 아직 실행하지 못했습니다.
> 아래 표는 채워 넣을 자리이며, **측정값이 아닙니다.**

측정 방법:

```bash
export GEMINI_API_KEY=...
python spikes/s0_transcription_timing.py --seconds 30          # 마이크
python spikes/s0_transcription_timing.py --wav ko_30s.wav      # 16kHz mono 16-bit WAV
```

스크립트가 끝나면 요약을 출력합니다. 그 값을 그대로 아래에 옮겨 적으십시오.

## 1. 조각 단위 — 정책 A/B 결정

| 항목 | 값 |
|---|---|
| `output_transcription` 조각 수 | |
| 조각 길이 median (chars) | |
| 조각 도착 간격 median (ms) | |
| **판정** | 정책 A(점진) / 정책 B(발화 단위) |

판정 기준: 조각 길이 median이 25자 미만이면 정책 A, 이상이면 정책 B.

## 2. 첫 텍스트까지의 지연

| 항목 | 값 |
|---|---|
| 발화 시작 → 첫 `output_transcription` | ___ s |

## 3. `input_transcription`도 같은 패턴인가

| 항목 | 값 |
|---|---|
| 조각 길이 median (chars) | |
| 첫 조각까지 | ___ s |
| output과 같은 패턴인가 | 예 / 아니오 |

`interim_input_transcription` 필드도 함께 관측합니다 (SDK 2.14.0에 존재하나
문서화되지 않음). 여기로 점진 전사가 온다면 기록해 두십시오.

| 항목 | 값 |
|---|---|
| `interim_input_transcription` 수신 여부 | 예 / 아니오 |

## 4. `session_resumption_update` 주기

| 항목 | 값 |
|---|---|
| 수신 횟수 | |
| 간격 median | ___ s |
| `go_away` 수신 여부 / `time_left` | |

---

## 결론 → 코드에 반영할 것

측정이 끝나면 **환경설정 → 캡션 커밋 정책**을 `정책 A` 또는 `정책 B`로 고정하고,
기본값을 바꾸려면 `Settings.caption_policy`를 수정하십시오.

측정 전까지는 `자동 판별(AUTO)`로 동작합니다. 첫 8개 조각의 길이 median으로
A/B를 런타임에 고르며, 이는 측정의 대체물이지 측정 결과가 아닙니다.
