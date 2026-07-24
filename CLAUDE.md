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

API 키는 환경변수 → 리포지토리 루트의 `.env` → macOS 키체인 순으로 읽는다.
소스나 설정 파일에 키를 하드코딩하지 않는다. `.env`는 커밋하지 않으며,
`.env.example`에는 실제 키를 절대 적지 않는다.
키 값을 로그에 찍지 않는다 (`.env`에서 읽은 것은 키 '이름'만 남긴다).

## 스타일

- Python 3.12+, 타입 힌트 필수, `ruff` 준수.
- `caption_buffer.py`는 Qt·네트워크 의존성이 없어야 하며 단위 테스트로 덮는다.
- 통역 중에는 모달 다이얼로그를 띄우지 않는다. 상태는 캡션 영역에 표시한다.

## 검증된 환경 메모 (PLAN.md 대비 정정 사항)

- `soxr.ResampleStream`의 스트리밍 메서드는 `.process()`가 아니라
  **`.resample_chunk(x, last=False)`** 이다. PLAN.md §5.2의 의사코드는 이 점이
  틀렸으므로 `.resample_chunk()`를 쓴다.
- 검증 완료 버전: google-genai 2.14.0, PyQt6 6.11.0, soxr 1.1.0,
  sounddevice 0.5.5, numpy 2.5.1 / Python 3.14.
- `types.LiveConnectConfig`에 `translation_config`, `input_audio_transcription`,
  `output_audio_transcription`, `context_window_compression`,
  `session_resumption` 필드가 모두 존재함을 확인했다.
