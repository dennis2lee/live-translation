# Phase 0 — 전사 도착 패턴 측정 결과

**측정일** 2026-07-24
**모델** `gemini-3.5-live-translate-preview`
**SDK** google-genai 2.14.0 / Python 3.14
**입력** `spikes/fixtures/ko_fixture.wav` — 원고 18문장, 1.5분, 16kHz mono 16-bit
(macOS TTS `say -v Yuna`로 합성, 원고는 `spikes/fixtures/ko_script.txt`)

재현:

```bash
python spikes/make_ko_fixture.py
python spikes/s0_transcription_timing.py --wav spikes/fixtures/ko_fixture.wav --drain 10
```

---

## 1. 조각 단위 — 정책 A/B 결정

| 항목 | 값 |
|---|---|
| `output_transcription` 조각 수 | 83 |
| 조각 길이 median | **14 chars** (min 4, max 33) |
| 조각 도착 간격 median | **1005 ms** |
| **판정** | **정책 A — 점진 표시** |

조각은 단어~짧은 구 단위로 약 1초에 하나씩 도착합니다. 발화 전체가 한 번에
오는 정책 B 형태가 **아닙니다.**

관측된 조각 예 (연속 도착):

```
t=+67.084s  OUT  len=8    text=' This is'
t=+68.089s  OUT  len=22   text=' something the finance'
t=+69.102s  OUT  len=19   text=' team is monitoring'
t=+70.690s  OUT  len=12   text=' separately.'
```

조각은 **앞쪽에 공백을 포함**해서 옵니다(`' This is'`). 그대로 이어 붙이면 되며,
`CaptionBuffer._join`이 이 경우 공백을 중복으로 넣지 않습니다.

문장 종결 부호가 조각 끝에 실려 옵니다(`' separately.'`). 따라서 정책 A의
종결 부호 커밋이 실제로 동작합니다. 1.2초 유휴 커밋은 종결 부호 없이 끊기는
구간의 보조 수단으로 남깁니다(조각 간격 median이 1.0초이므로 여유가 크지 않아
1.2초를 그대로 유지합니다).

## 2. 첫 텍스트까지의 지연

| 항목 | 값 |
|---|---|
| 오디오 전송 시작 → 첫 `output_transcription` | **3.142 s** |
| 오디오 전송 시작 → 첫 `input_transcription` | 2.951 s |

## 3. `input_transcription`도 같은 패턴인가

| 항목 | 값 |
|---|---|
| 조각 수 | 89 |
| 조각 길이 median | 5 chars (min 2, max 10) |
| 도착 간격 median | 1016 ms |
| output과 같은 패턴인가 | **예** (양쪽 모두 점진) |

원문 한국어는 영어보다 조각이 더 잘게 옵니다(`' 재무팀에서'`, `' 별도로'`).
원문 표시는 1줄 요약이므로 문제되지 않습니다.

| 항목 | 값 |
|---|---|
| `interim_input_transcription` 수신 여부 | **아니오** (1.5분 동안 0회) |

SDK 2.14.0에 필드는 존재하나 이 모델에서는 오지 않았습니다. 코드에서 의존하지
않습니다.

## 4. `session_resumption_update` 주기

| 항목 | 값 |
|---|---|
| 수신 횟수 | 85 |
| 간격 median | **1.0 s** |
| `go_away` 수신 | **0회** |

핸들이 약 1초마다 갱신되므로 재연결 시 항상 최신 핸들을 쓸 수 있습니다.
`go_away`는 1.5분 세션에서는 나오지 않습니다 — 세션 수명(약 10~15분)에
가까워져야 옵니다. **Phase 3의 30분 실행에서 검증해야 합니다.**

---

## 결론 → 코드에 반영한 것

- `Settings.caption_policy` 기본값을 **`incremental`(정책 A)** 로 고정했습니다.
  자동 판별(AUTO)은 선택지로 남겨두었습니다.
- 미확정 텍스트를 55% 알파로 흐리게 표시하는 단계가 실제로 쓰입니다
  (조각이 1초마다 들어오므로 확정 전 표시 구간이 존재합니다).
- 조각의 선행 공백을 그대로 살리는 `_join` 동작이 맞습니다.

## 남은 미검증 항목

- `go_away` 실제 수신과 선제 재연결 동작 → Phase 3 (30분 실행)
- 재연결 구간 문장 유실 → Phase 3 (`ko_script.txt` 원고와 대조)
