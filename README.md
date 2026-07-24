# LiveCap

한국어 발화를 영어로 실시간 통역해 전체화면 캡션으로 띄우는 macOS 앱.
엔진은 Google Gemini Live API의 `gemini-3.5-live-translate-preview`.

전체 명세는 [PLAN.md](PLAN.md), 작업 규칙은 [CLAUDE.md](CLAUDE.md)를 따릅니다.

## 설치

```bash
python3 -m venv .venv
.venv/bin/python -m pip install -e '.[dev]'
```

Python 3.12 이상이 필요합니다 (검증 환경: Python 3.14).

## API 키

`.env.example`을 복사해 값을 채우십시오.

```bash
cp .env.example .env
```

```
GEMINI_API_KEY=AIza...
```

`.env`는 `.gitignore`에 있어 커밋되지 않습니다. `.env.example`에는 절대 실제
키를 적지 마십시오 — 그쪽은 커밋됩니다.

조회 순서는 다음과 같습니다.

1. `GEMINI_API_KEY` (또는 `GOOGLE_API_KEY`) 환경변수
2. 리포지토리 루트의 `.env`
3. macOS 키체인 (`LiveCap` / `gemini-api-key`)
4. 앱 **환경설정…**(⌘,)에서 입력 → 키체인에 저장

`.env.example`의 안내 문구를 지우지 않은 채 두면 키가 없는 것으로 간주합니다.

> `gemini-3.5-live-translate-preview`는 **무료 등급에서 동작하지 않습니다.**
> 키를 발급받은 Google Cloud 프로젝트에 결제 계정이 연결되어 있어야 합니다.

## 실행

```bash
./run.sh
```

메뉴바에서 **통역 → 통역 시작**(⌘R)으로 시작합니다.

## 오디오 입력 장치 선택

**설정 → 오디오 입력 장치** 서브메뉴에서 고릅니다. 메뉴를 열 때마다 목록을
새로 감지하므로 USB 인터페이스를 도중에 뽑았다 꽂아도 다시 선택할 수 있습니다.
장치는 인덱스가 아니라 **이름**으로 저장되며, 저장된 장치가 없으면 시스템 기본
입력으로 폴백하고 화면에 알립니다.

믹서 피크는 **-12dBFS 근처**로 잡으십시오. 앱에서 소프트웨어 게인을 걸지 않습니다.

## 캡션 박스

기본은 **고정 박스**입니다 — 폭·높이·위치가 항상 같고, 캡션이 없어도 사라지지
않으며, 기본 2줄 높이입니다. 텍스트는 아래쪽 정렬이라 1줄이든 2줄이든 최신 줄이
같은 자리에 남습니다.

`설정 → 캡션 박스 항상 표시`를 끄면 PLAN.md §6.2의 원래 동작(내용에 맞춰 크기가
변하고, 비면 사라짐)으로 돌아갑니다. 높이는 `캡션 줄 수`(1~4)를 따릅니다.

## 단축키

| 동작 | 단축키 |
|---|---|
| 통역 시작 | ⌘R |
| 통역 중지 | ⌘. |
| 캡션 지우기 | ⌘K |
| 환경설정 | ⌘, |
| 종료 | ⌘Q |

## 개발

```bash
pytest                                  # 전체 테스트
ruff check .                            # 린트

python spikes/s0_transcription_timing.py          # Phase 0 — 전사 도착 패턴 측정
python spikes/s1_capture_check.py --seconds 60    # Phase 1 — 캡처 품질 검증
```

`spikes/s0_transcription_timing.py`는 API 키가 필요합니다. 측정 결과는
[spikes/RESULTS.md](spikes/RESULTS.md)에 기록하고, 그에 맞춰 환경설정의
**캡션 커밋 정책**을 고정하십시오. 측정 전에는 `자동 판별`로 동작합니다.

## 구조

```
src/livecap/
├── app.py               진입점
├── config.py            Settings + QSettings 영속화 + 키체인
├── audio.py             AudioCapture, RingBuffer (48k→16k, int16 LE)
├── translator.py        LiveTranslator (asyncio, 세션 재개·선제 재연결)
├── caption_buffer.py    전사 조각 → 표시 줄 (Qt·네트워크 의존성 없음)
└── ui/
    ├── main_window.py   CaptionWindow + 시스템 메뉴바
    ├── caption_view.py  배경 + 라운드 캡션 박스 페인팅
    └── settings_dialog.py
```

세 개의 스레드가 돕니다. 오디오 콜백에서 Qt 객체를 만지지 않고, asyncio
스레드에서 위젯을 직접 갱신하지 않습니다. 스레드 경계는 항상 `pyqtSignal`을
경유합니다.

## 비용

입력 $3.50/1M 토큰, 출력 $21.00/1M — 실효 분당 약 $0.0368, **30분 세션당 약
$1.10**. 캡션만 쓰더라도 모델이 오디오 전용이라 오디오 출력 토큰은 그대로
과금됩니다.
