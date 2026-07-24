# LiveCap

Real-time speech translation captions for macOS.

Speak Korean, see English captions on a full-screen display — or flip the
direction and speak English to see Korean. Audio comes in through any CoreAudio
input device, typically a USB interface fed from a mixer.

- **Engine** — Google Gemini Live API, `gemini-3.5-live-translate-preview`
- **UI** — PyQt6, native macOS menu bar, no Electron
- **Latency** — ~3.1 s to first caption, fragments arriving about once per second
- **Endurance** — verified for 30 minutes unbroken across 3 automatic reconnects

See [INSTALL.md](INSTALL.md) to set it up on a new machine.

## Quick start

```bash
cd ~/work/live-translation && cp .env.example .env
```

- Put your Gemini API key in `.env` (paid tier required — see [INSTALL.md](INSTALL.md))
- Run `./run.sh`
- Press `0` to start

## Keyboard shortcuts

| Key | Action |
|---|---|
| `0` | Toggle translation on / off |
| `1` | Korean → English |
| `2` | English → Korean |
| ⌘R | Start translation |
| ⌘. | Stop translation |
| ⌘K | Clear captions |
| ⌘, | Preferences |
| ⌘Q | Quit |

`0` toggles, but the menu deliberately keeps **Start** and **Stop** as two
separate items so a mis-click cannot stop a live session.

## Status indicator

A dot at the bottom-right of the caption box reports connection state. There is
deliberately **no "connecting…" text** — it would cover the captions.

| Dot | Meaning |
|---|---|
| Grey, steady | Idle — at launch, or after stopping |
| Green, blinking | Connecting or reconnecting |
| Green, steady | Connected and translating |
| Red, steady | Error |

- Errors also print a short message in the caption area, because you cannot act on a red dot alone
- Examples: `API 키가 필요합니다`, `오디오 입력 장치를 찾을 수 없습니다`

## Translation direction

| Preset | Key | Target language |
|---|---|---|
| Korean → English | `1` | `en` |
| English → Korean | `2` | `ko` |

- The Live API has **no source-language field** — the model detects it
- So a "direction" is really just a target language
- Speech already in the target language produces no caption (`echo_target_language=False`)
- Translating both directions at once would need two concurrent sessions and cost twice as much; this is not supported

Switching while translating restarts only the translator session:

- Audio capture and the ring buffer keep running, so nothing is lost in the gap
- The transcript file stays open and records the switch point
- The direction appears briefly at the bottom-right as `KO → EN`, then fades

## Caption box

- Fixed size and position by default, sized for **2 lines**, and it stays on screen even when empty
- Text is bottom-aligned, so the newest line never moves as lines accumulate
- Default width is **90%** of the window (5% margin each side), adjustable from 60–100%
- Turn off **설정 → 캡션 박스 항상 표시** to make the box hug its content and disappear when empty

| Setting | Default | Range |
|---|---|---|
| Background colour | `#000000` | any |
| Box colour | `#1B4965` | any |
| Box opacity | 100% | 60–100% |
| Text colour | `#FFFFFF` | any |
| Corner radius | 24 px | 0–48 |
| Box max width | 90% | 60–100% |
| Bottom margin | 8% | 2–20% |
| Font size | 44 px | 24–96 |
| Max lines | 2 | 1–4 |

Unconfirmed text is drawn at 55% alpha so you can see a phrase being revised
before it settles.

## Clearing on silence

- Captions clear **3 seconds** after the last transcription fragment arrives
- Adjustable from 0–10 s in Preferences; `0` keeps captions on screen indefinitely
- The trigger is fragment arrival, not audio level — fragments keep coming about once a second while someone is speaking, so captions never clear mid-sentence

## Audio input

Pick a device under **설정 → 오디오 입력 장치**.

- The list is rescanned every time you open the menu, so a USB interface can be unplugged and reconnected
- Devices are stored **by name**, not index — indexes change across reconnects
- If a stored device is missing, LiveCap falls back to the system default and says so
- Set your mixer peak near **-12 dBFS**; the app applies no software gain

Internally the device is opened at its native rate (usually 48 kHz) and
resampled to 16 kHz with `soxr`, never by asking CoreAudio for 16 kHz directly.

## Transcripts

Each session writes a Markdown file named after its start time —
`2026-07-24_143052.md`, in `~/Documents/LiveCap` by default.

```markdown
# LiveCap 번역 기록

- **시작** 2026-07-24 14:30:52
- **대상 언어** en
- **모델** gemini-3.5-live-translate-preview

---

`00:03` Good morning everyone.

`00:07` Revenue is up 14% year over year.

> _재연결 — 10:02 지점, 공백 842ms_
```

- Every line is flushed immediately, so a crash cannot lose what was already written
- Reconnects and direction switches are marked inline
- A session that produced no captions deletes its own file on exit
- Toggle with **통역 → 번역 기록 저장**; open the folder with **통역 → 기록 폴더 열기**

## Cost

| Metric | Value |
|---|---|
| Input | $3.50 / 1M tokens (~$0.0053/min) |
| Output | $21.00 / 1M tokens (~$0.0315/min) |
| Effective | **~$0.037/min** |
| 30-minute session | **~$1.10** (measured: $1.11) |

Audio output tokens are billed even though LiveCap discards the audio — the
model is audio-only and the output cannot be switched off.

## Architecture

Three threads. Crossing between them is only ever done in the ways listed here.

| Thread | Responsibility |
|---|---|
| PortAudio callback | Downmix to mono, resample to 16 kHz, write to a lock-free ring buffer. No allocation, no locks, no Qt. |
| asyncio worker | Send 100 ms chunks to the Live API, receive transcriptions, manage reconnects. Emits Qt signals. |
| Qt main | All widgets. Never touched from the other two threads. |

```
src/livecap/
├── app.py               entry point
├── config.py            Settings, QSettings persistence, .env and Keychain lookup
├── audio.py             AudioCapture, RingBuffer (48k → 16k, int16 LE)
├── translator.py        LiveTranslator — sessions, resumption, pre-emptive reconnect
├── transcript.py        Markdown transcript writer
├── caption_buffer.py    transcription fragments → display lines (no Qt, no network)
└── ui/
    ├── main_window.py   CaptionWindow and the system menu bar
    ├── caption_view.py  background, caption box, status indicator
    └── settings_dialog.py
```

### Session endurance

An audio session lasts about 15 minutes and the WebSocket about 10, so lasting
30 minutes needs all three of:

- `context_window_compression` with a sliding window, to extend the session itself
- `session_resumption`, keeping the latest `new_handle` for the next connection
- Opening a replacement session **before** closing the old one when `go_away` arrives

Measured over 30 minutes: `go_away` arrived 3 times at 9-minute intervals, each
swap took 370–459 ms, and no sentence was lost.

## Development

```bash
cd ~/work/live-translation && .venv/bin/python -m pytest -q
```

```bash
cd ~/work/live-translation && .venv/bin/python -m ruff check .
```

- 181 tests, none requiring an API key or network
- UI tests render offscreen and inspect pixels directly
- `caption_buffer.py` has no Qt or network dependency and is covered by unit tests
- Reconnect logic is tested against a fake Live session

Measurement scripts live in `spikes/`, and their findings are recorded in
[spikes/RESULTS.md](spikes/RESULTS.md).

| Script | Purpose |
|---|---|
| `s0_transcription_timing.py` | How transcription fragments arrive (drives caption policy) |
| `s1_capture_check.py` | Audio capture accuracy, underruns, WAV dump |
| `s2_headless_run.py` | Long unattended runs, reconnect stats, transcript vs. script |
| `make_tts_fixture.py` | Build known-script test audio with the macOS TTS voices |

## License

MIT — see [LICENSE](LICENSE).
