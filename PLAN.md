# LiveCap — 한→영 실시간 동시통역 캡션 앱 구현 계획서

> **이 문서는 Claude Code가 그대로 실행할 수 있는 구현 명세입니다.**
> 리포지토리 루트에 `PLAN.md`로 두고, 부록 A의 내용을 `CLAUDE.md`로 저장한 뒤 작업을 시작하십시오.

**작성일** 2026-07-24
**대상 환경** MacBook Air 13" M5 / 32GB / macOS, USB 오디오 인터페이스(외부 믹서 직결)
**엔진** Google Gemini `gemini-3.5-live-translate-preview` (Live API)

---

## 0. Claude Code에게 — 시작 전 반드시 읽을 것

이 프로젝트는 **2026년 6월에 공개된 API**를 사용합니다. 학습 데이터에 없을 가능성이 높으므로, 아래 사항을 추측으로 바꾸지 마십시오.

**절대 하지 말 것**

- `google-generativeai` (구 SDK) 사용 금지. **`google-genai`** 를 쓰십시오. 두 패키지는 완전히 다릅니다.
- 모델명을 바꾸지 마십시오. 정확히 `gemini-3.5-live-translate-preview` 입니다.
- `response_modalities`를 `["TEXT"]`로 바꾸지 마십시오. **이 모델은 오디오 출력 전용**이며, 텍스트는 `output_audio_transcription`을 통해서만 나옵니다. `["AUDIO"]`가 아니면 세션이 실패합니다.
- 오디오 출력을 재생하려 하지 마십시오. 요구사항은 캡션뿐이므로 수신 후 폐기합니다. 다만 **수신 자체는 반드시 해야 합니다**(안 읽으면 스트림이 막힘).
- 텍스트 입력을 보내지 마십시오. 이 모델은 오디오 전용입니다.

**미확정 사항 — 코드로 확인해야 함**

`outputTranscription`이 **글자 단위로 점진적으로 오는지, 발화 단위로 한 번에 오는지 Google이 문서화하지 않았습니다.** 공식 WebSocket 레퍼런스는 전사 메시지가 "다른 서버 메시지와 독립적으로 전송되며 순서가 보장되지 않는다"고만 하고, partial/final 플래그도 없습니다. 형제 모델(Gemini 3.1 Flash)에서 점진 스트리밍이 발화 단위로 회귀했다는 개발자 포럼 신고도 있습니다.

**따라서 Phase 0(스파이크)을 반드시 먼저 수행하고, 그 결과에 따라 `CaptionBuffer`의 커밋 정책을 정하십시오.** 이 결과를 모른 채 UI를 먼저 만들지 마십시오.

---

## 1. 제품 요구사항

한국어 발화를 실시간으로 영어로 통역해 화면에 캡션으로 띄우는 macOS 데스크탑 앱.

오디오는 USB 오디오 인터페이스를 통해 외부 믹서에서 라인 레벨로 직접 들어옵니다. 화면은 검은 배경이 전체를 채우고, 통역된 영어 텍스트만 하단에 어두운 하늘색 라운드 박스 위에 표시됩니다. 원문 한국어는 기본적으로 표시하지 않습니다.

메뉴바에서 통역을 시작하고 중지할 수 있어야 하며, 배경색과 캡션 박스 색을 사용자가 바꿀 수 있어야 합니다. 한 세션은 **30분 연속**을 무중단으로 버텨야 합니다.

---

## 2. 기술 스택

| 영역 | 선택 | 이유 |
|---|---|---|
| 언어 | Python 3.12+ | 오디오·API·UI를 한 프로세스에서 처리, 지연 최소 |
| GUI | PyQt6 | macOS 시스템 메뉴바에 `QMenuBar`가 네이티브로 붙음 |
| 오디오 입력 | `sounddevice` (PortAudio→CoreAudio) | USB 인터페이스를 이름으로 직접 선택 가능 |
| 리샘플링 | `soxr` | 48k→16k 고품질, 의존성 가벼움 |
| API | `google-genai` | Live API 공식 SDK, WebSocket 처리 내장 |
| 비동기 | `asyncio` (전용 스레드) | Qt 이벤트 루프와 분리 |
| 설정 저장 | `QSettings` | macOS plist에 네이티브 저장 |
| API 키 | `keyring` | macOS 키체인 |

**Electron을 쓰지 마십시오.** 유휴 상태에서도 150~250MB를 점유하고, 오디오 스레드와 렌더 스레드 사이에 IPC 경계가 생깁니다. 요구사항이 "지연과 처리 부하 최소"입니다.

```
python = ">=3.12"
PyQt6 = ">=6.7"
google-genai = ">=1.0"
sounddevice = ">=0.4.7"
numpy = ">=1.26"
soxr = ">=0.5"
keyring = ">=25.0"
```

---

## 3. 아키텍처

### 3.1 스레드 모델

세 개의 스레드가 돕니다. 경계를 넘는 통신은 **반드시 아래 방식만** 쓰십시오.

```
[PortAudio 콜백 스레드]
    sounddevice InputStream callback
    - 블로킹 금지, 할당 최소화
    - 다운믹스 + 리샘플 후 lock-free ring buffer에 write
         │
         │  loop.call_soon_threadsafe()
         ▼
[asyncio 워커 스레드]  ← 별도 threading.Thread에서 loop.run_forever()
    LiveTranslator
    - send_task:    ring buffer → session.send_realtime_input()
    - recv_task:    session.receive() → 파싱 → Qt 시그널 emit
    - supervisor:   GoAway / 재연결 / 세션 재개
         │
         │  pyqtSignal (Qt가 큐잉해서 메인 스레드로 전달)
         ▼
[메인 스레드]
    Qt GUI — CaptionWindow, MenuBar, SettingsDialog
```

**금지 사항**: 오디오 콜백에서 Qt 객체를 만지지 마십시오. asyncio 스레드에서 위젯을 직접 갱신하지 마십시오. 반드시 시그널을 경유합니다.

### 3.2 파일 구조

```
livecap/
├── PLAN.md                  # 이 문서
├── CLAUDE.md                # 부록 A
├── pyproject.toml
├── README.md
├── run.sh                   # 개발용 실행 스크립트
├── src/livecap/
│   ├── __init__.py
│   ├── app.py               # 진입점, QApplication 부트스트랩
│   ├── config.py            # Settings 데이터클래스 + QSettings 영속화
│   ├── audio.py             # AudioCapture, RingBuffer
│   ├── translator.py        # LiveTranslator (asyncio)
│   ├── caption_buffer.py    # 전사 조각 → 표시 줄 조립
│   ├── ui/
│   │   ├── __init__.py
│   │   ├── main_window.py   # CaptionWindow + QMenuBar
│   │   ├── caption_view.py  # 검은 배경 + 라운드 캡션 박스 페인팅
│   │   └── settings_dialog.py
│   └── util/
│       ├── __init__.py
│       └── logging.py
├── spikes/
│   └── s0_transcription_timing.py   # Phase 0
└── tests/
    ├── test_ring_buffer.py
    ├── test_resample.py
    └── test_caption_buffer.py
```

---

## 4. API 연동 명세 — 검증된 코드

아래는 공식 문서에서 확인한 내용입니다. **필드명을 임의로 바꾸지 마십시오.**

### 4.1 세션 설정

```python
import asyncio
from google import genai
from google.genai import types

client = genai.Client()  # GEMINI_API_KEY 환경변수 또는 api_key= 인자

MODEL = "gemini-3.5-live-translate-preview"

config = types.LiveConnectConfig(
    response_modalities=["AUDIO"],                       # 필수, 변경 불가
    input_audio_transcription=types.AudioTranscriptionConfig(),
    output_audio_transcription=types.AudioTranscriptionConfig(),
    translation_config=types.TranslationConfig(
        target_language_code="en",
        echo_target_language=False,   # 이미 영어인 발화를 되울리지 않음
    ),
    context_window_compression=types.ContextWindowCompressionConfig(
        sliding_window=types.SlidingWindow(),
    ),
    session_resumption=types.SessionResumptionConfig(handle=None),  # 재개 시 handle 전달
)

async with client.aio.live.connect(model=MODEL, config=config) as session:
    ...
```

REST/WebSocket으로 직접 갈 경우의 camelCase 매핑:

```json
{
  "setup": {
    "model": "models/gemini-3.5-live-translate-preview",
    "generationConfig": {
      "responseModalities": ["AUDIO"],
      "inputAudioTranscription": {},
      "outputAudioTranscription": {},
      "translationConfig": {
        "targetLanguageCode": "en",
        "echoTargetLanguage": false
      }
    }
  }
}
```

**단, 이 프로젝트는 SDK 경로를 쓰십시오.** 재연결·재개·GoAway 처리를 SDK가 상당 부분 감춰줍니다.

### 4.2 오디오 전송

```python
await session.send_realtime_input(
    audio=types.Blob(
        data=chunk,                      # bytes, 16-bit PCM LE
        mime_type="audio/pcm;rate=16000"
    )
)
```

- **포맷: 16kHz, 16-bit signed PCM, little-endian, 모노**
- **청크: 100ms = 1600 샘플 = 3200 바이트**

### 4.3 응답 수신

```python
async for response in session.receive():
    # 1) 종료 예고 — 재연결 트리거
    if response.go_away is not None:
        on_go_away(response.go_away.time_left)

    # 2) 세션 재개 핸들 — 최신값을 항상 보관
    if response.session_resumption_update is not None:
        upd = response.session_resumption_update
        if upd.resumable and upd.new_handle:
            self._handle = upd.new_handle

    sc = response.server_content
    if sc is None:
        continue

    # 3) 원문 한국어 (기본 미표시, 설정으로 노출 가능)
    if sc.input_transcription:
        emit_source(sc.input_transcription.text)

    # 4) 번역된 영어 — 화면에 띄울 텍스트
    if sc.output_transcription:
        emit_target(sc.output_transcription.text)

    # 5) 오디오는 반드시 소비하고 폐기 (안 읽으면 스트림이 막힘)
    if sc.model_turn:
        for part in sc.model_turn.parts:
            if part.inline_data:
                pass  # discard
```

### 4.4 세션 수명과 재연결 — 이 프로젝트의 핵심

**제약**: 오디오 전용 세션 기본 15분, WebSocket 연결 수명 약 10분, 종료 약 60초 전 `GoAway` 수신. 재개 토큰은 마지막 종료 후 2시간 유효.

**30분을 버티려면 세 가지를 전부 구현해야 합니다.**

1. `context_window_compression=SlidingWindow()` — 세션 자체를 연장
2. `session_resumption` — `new_handle`을 항상 보관하고 새 연결에 넘김
3. `go_away` 수신 시 **선제적으로** 새 연결을 열어 교체

**재연결 중 오디오 유실 방지가 가장 중요합니다.** 링 버퍼는 연결 상태와 무관하게 계속 채워지고, 새 세션이 열리면 버퍼에 쌓인 것부터 밀어넣습니다. 이 처리가 없으면 재연결마다 한 문장씩 증발합니다.

```
[GoAway 수신]
   → 새 세션을 handle과 함께 연결 (구 세션은 아직 유지)
   → 새 세션 준비 완료
   → send 타깃을 새 세션으로 원자적 교체
   → 구 세션 close
```

**수용 기준**: 30분 세션에서 재연결이 3~4회 발생하되, 캡션이 끊기거나 문장이 유실되지 않아야 합니다.

### 4.5 알려진 한계 (Preview)

문서에 명시된 것들입니다. 캡션만 쓰므로 음성 관련 항목은 무관합니다.

- 긴 침묵 뒤 목소리 전환, 성별 오배정, 다화자 시 목소리 고착 → **무관** (오디오 폐기)
- 강한 억양·유사 언어·급격한 언어 전환에서 언어 감지 흔들림 → 발화자가 한국어 고정이면 영향 적음
- 배경 소음 완전 억제 실패 → **USB 라인 직결로 대부분 해소**
- Batch/캐싱/함수호출/구조화출력/검색그라운딩 **전부 미지원** — 쓰려고 시도하지 마십시오

---

## 5. 오디오 파이프라인 명세

### 5.1 장치 열기

USB 오디오 인터페이스는 대부분 44.1kHz 또는 48kHz로 동작합니다. **16000을 직접 요청하지 마십시오.** CoreAudio가 내부 리샘플을 하면서 품질과 지연이 장치마다 달라집니다.

```python
import sounddevice as sd

# 1) 장치 목록: 설정 메뉴에서 이름으로 선택
devices = [d for d in sd.query_devices() if d['max_input_channels'] > 0]

# 2) 장치의 네이티브 레이트로 열기
info = sd.query_devices(device_index)
native_rate = int(info['default_samplerate'])   # 보통 48000
channels = min(info['max_input_channels'], 2)

stream = sd.InputStream(
    device=device_index,
    samplerate=native_rate,
    channels=channels,
    dtype='float32',
    blocksize=int(native_rate * 0.02),   # 20ms — 콜백을 짧게
    callback=on_audio,
    latency='low',
)
```

### 5.2 콜백 처리

```python
def on_audio(indata, frames, time_info, status):
    # 1) 모노 다운믹스
    mono = indata.mean(axis=1) if indata.shape[1] > 1 else indata[:, 0]
    # 2) 48000 → 16000 리샘플 (soxr, 스트리밍 모드로 상태 유지)
    out = resampler.process(mono)          # soxr.ResampleStream 사용
    # 3) float32 → int16 LE
    pcm = np.clip(out * 32767.0, -32768, 32767).astype('<i2')
    # 4) 링 버퍼에 write (블로킹 없음)
    ring.write(pcm.tobytes())
```

**주의사항**

- `soxr.resample()`(일회성)이 아니라 **`soxr.ResampleStream`(상태 유지)** 을 쓰십시오. 청크마다 새로 리샘플하면 경계에서 클릭 노이즈가 생깁니다.
- 콜백 안에서 메모리 할당·로깅·락을 최소화하십시오. 언더런이 나면 오디오가 끊깁니다.
- 클리핑은 믹서에서 잡는 것이 정석입니다. **믹서 피크를 -12dBFS 근처로 잡고 앱에서는 소프트웨어 게인을 걸지 마십시오.**

### 5.3 링 버퍼

- 용량 **30초**(16000 × 2바이트 × 30 = 960KB). 재연결 지연을 충분히 흡수합니다.
- 오버플로 시 가장 오래된 데이터를 버리고 카운터를 증가시켜 로그에 남깁니다.
- 단일 생산자(오디오 콜백) / 단일 소비자(asyncio send task) 전제로 구현하십시오.
- 소비 측은 **3200바이트(100ms) 단위**로 꺼냅니다.

### 5.4 무음 구간 비용 절감 (선택)

간단한 RMS 게이트를 두고, 임계 이하가 3초 이상 지속되면 전송을 잠시 멈출 수 있습니다. 다만 **VAD를 과하게 만들지 마십시오** — 한국어는 문장 중간 휴지가 길고, 잘못 자르면 문맥이 끊깁니다. 기본값은 **비활성**으로 두고 설정으로만 켜지게 하십시오.

---

## 6. UI 명세

### 6.1 창

- `QMainWindow`. 기본은 **최대화된 창**(`showMaximized()`), 진짜 전체화면은 메뉴 토글로 제공.
  - 이유: macOS에서 진짜 전체화면이면 시스템 메뉴바가 자동으로 숨겨져 통역 시작/중지가 불편합니다. 기본은 메뉴바가 항상 보이는 최대화 창이 실용적입니다.
- 배경: 창 전체를 단색으로 채움. 기본 `#000000`, 설정 가능.
- 커서: 통역 중 3초간 마우스 이동이 없으면 커서를 숨김.

### 6.2 캡션 박스

```
┌─────────────────── 화면 (배경색, 기본 #000000) ────────────────────┐
│                                                                    │
│                                                                    │
│                                                                    │
│                                                                    │
│         ╭──────────────────────────────────────────────╮           │
│         │                                              │           │
│         │   We're going to look at the Q3 numbers      │           │
│         │   before moving on to the roadmap.           │           │
│         │                                              │           │
│         ╰──────────────────────────────────────────────╯           │
│                                                                    │
└────────────────────────────────────────────────────────────────────┘
                        ↑ 하단 여백 = 화면 높이의 8%
```

| 속성 | 기본값 | 설정 가능 |
|---|---|---|
| 배경색 | `#000000` | ✅ |
| 캡션 박스 색 | `#1B4965` (어두운 하늘색) | ✅ |
| 박스 불투명도 | 100% | ✅ (60~100%) |
| 확정 텍스트 색 | `#FFFFFF` | ✅ |
| 미확정 텍스트 색 | 확정색의 55% 알파 | 파생 |
| 모서리 반경 | 24px | ✅ (0~48) |
| 박스 최대 폭 | 화면 폭의 80% | ✅ (60~95%) |
| 하단 여백 | 화면 높이의 8% | ✅ (2~20%) |
| 내부 여백 | 좌우 32px / 상하 24px | — |
| 폰트 | SF Pro Display, 없으면 Helvetica Neue | — |
| 폰트 크기 | 44px | ✅ (24~96) |
| 최대 줄 수 | 2 | ✅ (1~4) |
| 정렬 | 가운데 | — |

**동작**

- 표시할 텍스트가 없으면 **박스 자체를 숨깁니다** (빈 박스가 떠 있으면 지저분함).
- 박스 높이는 실제 줄 수에 맞춰 자동 조절. 1줄이면 1줄 높이.
- 나타남/사라짐은 150ms 페이드. 그 이상 화려한 애니메이션 금지.
- 새 줄이 들어오면 위로 밀어 올림. 스크롤 애니메이션 없이 즉시 교체(지연 체감을 늘리지 않기 위함).
- 텍스트가 최대 줄 수를 넘으면 **앞부분을 잘라내고 최신 내용을 보여줍니다**(뒤를 자르지 않음).

**구현**: `QWidget` 서브클래스에서 `paintEvent`로 직접 그리십시오. `QLabel` + 스타일시트로는 라운드 박스 자동 크기 조절과 2줄 클리핑을 깔끔하게 처리하기 어렵습니다. `QPainterPath.addRoundedRect` + `QFontMetrics.boundingRect`로 레이아웃을 계산하고, `setRenderHint(Antialiasing)`를 켜십시오.

### 6.3 메뉴바 (macOS 시스템 메뉴바)

```
LiveCap
  ├─ LiveCap 정보
  ├─ 환경설정…                    ⌘,
  └─ LiveCap 종료                 ⌘Q

통역
  ├─ 통역 시작                     ⌘R
  ├─ 통역 중지                     ⌘.
  ├─ ──────────
  ├─ 캡션 지우기                    ⌘K
  └─ ──────────
  └─ 상태: 대기 중 / 연결 중 / 통역 중 / 재연결 중   (비활성 항목, 표시 전용)

설정
  ├─ 배경색…
  ├─ 캡션 박스 색…
  ├─ 글자색…
  ├─ ──────────
  ├─ 글자 크기          ▸  24 / 32 / 44 / 56 / 72 / 96
  ├─ 캡션 줄 수         ▸  1 / 2 / 3 / 4
  ├─ 오디오 입력 장치    ▸  (감지된 장치 목록, 라디오 선택)
  ├─ ──────────
  ├─ ☐ 원문(한국어) 함께 표시
  ├─ ☐ 전체화면
  └─ ──────────
  └─ 기본값으로 되돌리기
```

**구현 노트**

- `QMenuBar`는 macOS에서 자동으로 시스템 메뉴바에 붙습니다. `setNativeMenuBar(True)`가 기본이니 그대로 두십시오.
- 색상 선택은 `QColorDialog.getColor()`를 쓰고, `QColorDialog.ShowAlphaChannel`은 박스 색에만 허용합니다.
- 통역 시작/중지는 **동일 액션의 토글이 아니라 별도 항목 두 개**로 두고, 상태에 따라 `setEnabled()`로 하나씩만 활성화하십시오. 실수로 중지를 누르는 사고를 줄입니다.
- 오디오 장치 목록은 메뉴를 열 때마다 갱신하십시오(`aboutToShow` 시그널). USB 인터페이스는 도중에 연결·해제될 수 있습니다.
- "원문 함께 표시"를 켜면 캡션 박스 위에 작은 회색 텍스트로 한국어를 1줄 표시합니다(박스 밖, 배경색 위).

### 6.4 상태 표시

연결 실패·재연결 중에는 캡션 박스 위치에 **어두운 회색 텍스트로 상태만** 표시합니다. 팝업 다이얼로그를 띄우지 마십시오 — 통역 중에 모달이 뜨면 화면을 가립니다.

```
연결 중…              /  재연결 중…  /  오디오 장치를 찾을 수 없습니다
API 키가 필요합니다 (환경설정에서 입력)
```

---

## 7. CaptionBuffer 설계

`output_transcription.text`는 **partial/final 구분이 없고 순서 보장도 없습니다.** 따라서 조각을 모아 화면 줄로 만드는 책임은 전적으로 클라이언트에 있습니다.

Phase 0 결과에 따라 두 정책 중 하나를 채택하십시오.

**정책 A — 조각이 글자/단어 단위로 자주 도착하는 경우**

들어오는 텍스트를 `pending`에 이어 붙여 미확정 색으로 즉시 표시합니다. 문장 종결 부호(`. ? !`)를 만나거나 **1.2초간 새 조각이 없으면** `pending`을 확정 줄로 커밋하고 비웁니다.

**정책 B — 발화 단위로 뭉텅이로 도착하는 경우**

도착한 텍스트를 곧바로 확정 줄로 커밋합니다. 미확정 상태가 없으므로 회색 텍스트 단계를 생략하고, 대신 발화 사이 공백 구간에 **직전 줄을 계속 남겨둡니다**(화면이 비는 것을 방지).

**공통 규칙**

- 확정 줄은 최대 `max_lines`개까지 보관하는 `deque`로 관리.
- 같은 텍스트가 연속 중복 도착하면 무시(순서 미보장 대비).
- 재연결 직후 첫 조각은 이전 문장의 이어짐일 수 있으므로 **줄을 새로 시작하지 말고 이어 붙이십시오.**
- 이 클래스는 Qt·네트워크 의존성이 없어야 하고, **단위 테스트로 커버하십시오**(`tests/test_caption_buffer.py`).

---

## 8. 설정 스키마

```python
@dataclass
class Settings:
    # 색상 (hex)
    bg_color: str = "#000000"
    box_color: str = "#1B4965"
    text_color: str = "#FFFFFF"
    box_opacity: float = 1.0          # 0.6 ~ 1.0

    # 레이아웃
    corner_radius: int = 24           # 0 ~ 48
    box_max_width_pct: int = 80       # 60 ~ 95
    bottom_margin_pct: int = 8        # 2 ~ 20
    font_size: int = 44               # 24 ~ 96
    max_lines: int = 2                # 1 ~ 4

    # 동작
    audio_device_name: str = ""       # 빈 문자열이면 시스템 기본 입력
    show_source_text: bool = False
    fullscreen: bool = False
    silence_gate: bool = False

    # API
    target_language_code: str = "en"
    echo_target_language: bool = False
```

- `QSettings("LiveCap", "LiveCap")`으로 영속화 → `~/Library/Preferences/`에 plist 생성.
- **오디오 장치는 인덱스가 아니라 이름으로 저장**하십시오. 인덱스는 USB 장치 재연결 시 바뀝니다. 시작 시 이름으로 조회하고, 없으면 기본 장치로 폴백하며 상태 영역에 알립니다.
- API 키는 `Settings`에 넣지 마십시오. 우선순위: `GEMINI_API_KEY` 환경변수 → macOS 키체인(`keyring`) → 환경설정 다이얼로그에서 입력받아 키체인 저장.

---

## 9. 단계별 작업

각 Phase는 **수용 기준을 통과해야 다음으로 넘어갑니다.** 통과 여부를 애매하게 넘기지 마십시오.

### Phase 0 — 스파이크: 전사 도착 패턴 확인 ⚠️ 최우선

`spikes/s0_transcription_timing.py`

한국어 음성 파일(또는 마이크 30초)을 100ms 청크로 밀어넣고, `output_transcription` 메시지가 도착할 때마다 다음을 stdout에 찍습니다.

```
t=+2.341s  len=7   text='We're '
t=+2.512s  len=5   text='going'
```

**확인할 것**

1. 조각이 글자/단어 단위로 오는가, 문장 단위로 오는가 → **정책 A/B 결정**
2. 첫 텍스트까지의 지연(초)
3. `input_transcription`도 같은 패턴인가
4. `session_resumption_update`가 몇 초 간격으로 오는가

**수용 기준**: 위 4개 항목의 답을 `spikes/RESULTS.md`에 기록. 이 파일 없이 Phase 3으로 가지 마십시오.

### Phase 1 — 오디오 파이프라인

`audio.py` + `tests/`

- 장치 열거, 이름으로 선택, 네이티브 레이트로 스트림 오픈
- 모노 다운믹스 → `soxr.ResampleStream`으로 16k 변환 → int16 LE
- 링 버퍼 (30초, 오버플로 카운터)

**수용 기준**
- 60초 캡처 후 링 버퍼에서 꺼낸 바이트 수가 `16000 × 2 × 60`의 ±1% 이내
- 캡처 결과를 WAV로 덤프해 재생했을 때 클릭·끊김·피치 이상이 없음
- 콜백 언더런 0회
- `test_ring_buffer.py`, `test_resample.py` 통과

### Phase 2 — Live API 연동 (헤드리스)

`translator.py`

- SDK 세션 연결, 4장 설정 그대로
- send/recv 태스크, 오디오 폐기
- 전사 텍스트를 콜백으로 방출

**수용 기준**: 링 버퍼 → 세션 → 콘솔로 영어 텍스트가 흐른다. 5분 연속 동작.

### Phase 3 — 30분 세션 유지 ⚠️ 가장 어려운 부분

`translator.py` 확장

- `go_away` 선제 재연결, `new_handle` 보관·재사용
- 컨텍스트 압축 활성화
- 재연결 중 링 버퍼 유지, 새 세션에 밀어넣기
- 실패 시 지수 백오프(1·2·4·8초), 연속 5회 실패 시 중단하고 에러 상태

**수용 기준**
- **30분 연속 실행 완주**
- 재연결 횟수와 각 재연결의 공백 시간(ms)을 로그로 남길 것
- 재연결 구간에서 문장 유실 없음 (사전 녹음한 한국어 음원으로 검증 — 원고와 출력을 대조)
- 실측 비용이 예상($1.10/30분)의 ±20% 이내

### Phase 4 — 캡션 UI

`ui/caption_view.py`, `ui/main_window.py`, `caption_buffer.py`

- 6장 명세대로 페인팅
- Phase 0 결과에 따른 CaptionBuffer 정책 적용
- 시그널 배선

**수용 기준**
- 텍스트 없을 때 박스가 보이지 않음
- 1줄/2줄에서 박스 높이가 정확히 맞음
- 창 크기를 바꿔도 폭·여백 비율이 유지됨
- 4K 디스플레이에서 텍스트가 흐리지 않음 (`devicePixelRatio` 처리 확인)
- `test_caption_buffer.py` 통과

### Phase 5 — 메뉴바와 설정

`ui/settings_dialog.py`, `config.py`

- 6.3절 메뉴 전부
- 색상 변경이 **즉시** 반영 (앱 재시작 불필요)
- QSettings 영속화, 재실행 시 복원
- 오디오 장치 목록 동적 갱신

**수용 기준**
- 배경색·박스색을 바꾸고 앱을 재시작하면 유지됨
- 통역 중에 색을 바꿔도 세션이 끊기지 않음
- USB 인터페이스를 뽑았다 꽂으면 메뉴에서 다시 선택 가능

### Phase 6 — 마감 (선택)

- `py2app` 또는 `briefcase`로 `.app` 번들
- 마이크 권한 `NSMicrophoneUsageDescription` Info.plist 항목
- 앱 아이콘

---

## 10. 검수 시나리오 — 실사용 리허설

구현 완료 후 반드시 아래를 수행하십시오.

**시나리오 1 · 30분 완주**
믹서에서 30분짜리 한국어 음원(또는 실제 발화)을 흘려보내고 끝까지 캡션이 유지되는지 확인. 재연결 횟수, 유실 문장 수, 총 비용을 기록.

**시나리오 2 · 지연 측정**
한국어 음원에 1분 간격으로 비프음을 넣고, 비프 직후 발화가 화면에 뜨는 시점을 화면 녹화로 측정. 모델 카드가 정의한 두 지표를 그대로 씁니다.
- 초기 지연: 발화 시작 → 번역 표시 시작
- 단어 단위 지연: 원문 단어 종료 → 해당 번역 표시

**시나리오 3 · Google Translate 앱 대비**
동일 음원을 휴대폰 Google Translate 앱에 들려주고 나란히 비교. 같은 모델이므로 품질은 비슷해야 정상이며, 크게 벌어지면 오디오 전처리(레벨·리샘플링)를 의심하십시오.

**시나리오 4 · 장애 주입**
통역 도중 (a) Wi-Fi를 10초간 끄기, (b) USB 인터페이스 분리 후 재연결, (c) 화면 잠금 후 해제. 각각에서 앱이 죽지 않고 상태 메시지를 보여준 뒤 복구되어야 합니다.

---

## 11. 예상 비용과 위험

**비용**: 입력 $3.50/1M 토큰(≈$0.0053/분), 출력 $21.00/1M(≈$0.0315/분), 실효 **분당 약 $0.0368**. 30분 세션당 약 **$1.10**, 시간당 $2.21. 캡션만 쓰더라도 **오디오 출력 토큰은 그대로 과금**됩니다(모델이 오디오 전용이라 끌 수 없음).

**주요 위험**

| 위험 | 영향 | 대응 |
|---|---|---|
| 전사가 발화 단위로만 도착 | 캡션이 툭툭 뜸 | Phase 0에서 조기 확인, 정책 B로 UI 조정 |
| 재연결 시 문장 유실 | 통역 신뢰도 하락 | 링 버퍼 + 선제 재연결 (Phase 3 수용 기준) |
| 한국어 SOV로 조기 확정 시 의미 반전 | 오역 표시 | 미확정 텍스트를 흐린 색으로 구분, 확정 전 재작성 허용 |
| Preview 모델의 규격 변경 | 갑작스러운 동작 변화 | SDK 버전 고정, 설정 상수를 `translator.py` 상단에 모아둠 |
| 프리뷰 리전 제한 | 연결 실패 | 최초 연결 실패 시 명확한 에러 메시지 |

---

## 부록 A — `CLAUDE.md`

리포지토리 루트에 아래 내용을 그대로 저장하십시오.

```markdown
# LiveCap

한국어 발화를 영어로 실시간 통역해 전체화면 캡션으로 띄우는 macOS 앱.
엔진은 Google Gemini Live API의 `gemini-3.5-live-translate-preview`.

전체 명세는 `PLAN.md`를 따른다. 임의로 벗어나지 말 것.

## 절대 규칙

- SDK는 `google-genai`. `google-generativeai`(구 SDK)를 쓰지 않는다.
- 모델명은 정확히 `gemini-3.5-live-translate-preview`.
- `response_modalities`는 반드시 `["AUDIO"]`. 이 모델은 텍스트 출력이 없다.
  화면에 띄울 영어 텍스트는 `output_audio_transcription`으로만 나온다.
- 수신한 오디오는 재생하지 않되 반드시 소비한다(안 읽으면 스트림이 막힌다).
- 오디오는 16kHz / 16-bit PCM LE / 모노 / 100ms(3200바이트) 청크로 보낸다.
- 오디오 장치는 네이티브 레이트로 열고 `soxr.ResampleStream`으로 16k 변환한다.
  `sd.InputStream(samplerate=16000)`으로 직접 요청하지 않는다.
- 설정에 오디오 장치를 저장할 때는 인덱스가 아니라 **이름**으로 저장한다.
- 오디오 콜백에서 Qt 객체를 만지지 않는다. asyncio 스레드에서 위젯을 직접
  갱신하지 않는다. 스레드 경계는 반드시 pyqtSignal로 넘는다.
- Electron·브라우저 기반 구현으로 바꾸지 않는다.

## 미확정 사항

`output_transcription`이 점진적으로 오는지 발화 단위로 오는지는 Google이
문서화하지 않았다. `spikes/RESULTS.md`에 측정 결과가 있으면 그것을 따르고,
없으면 Phase 0 스파이크를 먼저 수행한다.

## 실행

    ./run.sh              # 개발 실행
    pytest                # 테스트
    python spikes/s0_transcription_timing.py   # 전사 타이밍 측정

API 키는 `GEMINI_API_KEY` 환경변수 또는 macOS 키체인에서 읽는다.
소스나 설정 파일에 키를 하드코딩하지 않는다.

## 스타일

- Python 3.12+, 타입 힌트 필수, `ruff` 준수.
- `caption_buffer.py`는 Qt·네트워크 의존성이 없어야 하며 단위 테스트로 덮는다.
- 통역 중에는 모달 다이얼로그를 띄우지 않는다. 상태는 캡션 영역에 표시한다.
```

---

## 부록 B — 참고 문서

- [Live translation with Gemini Live API](https://ai.google.dev/gemini-api/docs/live-api/live-translate) — 모델·설정·언어
- [Live API 개요](https://ai.google.dev/gemini-api/docs/live-api) — 오디오 규격
- [Live API 세션 관리](https://ai.google.dev/gemini-api/docs/live-session) — 압축·재개·GoAway
- [Live API 기능 가이드](https://ai.google.dev/gemini-api/docs/live-api/capabilities) — 전사 설정
- [Live API WebSockets 레퍼런스](https://ai.google.dev/api/live) — 원시 프로토콜
- [모델 페이지](https://ai.google.dev/gemini-api/docs/models/gemini-3.5-live-translate-preview) — 지원/미지원 기능
- [Gemini API 가격](https://ai.google.dev/gemini-api/docs/pricing)
- [Gemini 3.5 Audio 모델 카드](https://deepmind.google/models/model-cards/gemini-3-5-audio/) — 지연 지표 정의
