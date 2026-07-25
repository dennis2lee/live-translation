# go_dist — Go port

A Go rewrite of the Python LiveCap, for distribution as a compiled binary.

**Status: core ported and verified. UI and audio capture not started.**

## What is done

| Package | Ported from | State |
|---|---|---|
| `internal/captionbuffer` | `caption_buffer.py` | ✅ 40 tests, ported one-for-one from `tests/test_caption_buffer.py` |
| `internal/audio` (ring buffer, PCM) | `audio.py` | ✅ 13 tests, ported from `tests/test_ring_buffer.py` and `test_resample.py` |
| `internal/translator` | `translator.py` | ✅ compiles against the real SDK; not yet run against the live service |

```bash
cd go_dist && go test ./... && go vet ./...
```

The behaviour tests were translated case by case rather than rewritten, so
parity with the Python original is demonstrated, not assumed. That covers the
subtle parts: the CJK join rule, reconnect line-continuation without
duplicating a recorded line, clear-on-silence, and ring buffer overwrite.

## SDK verification

`google.golang.org/genai v1.65.0` has everything the Python SDK needed:

| Needed | Present |
|---|---|
| `TranslationConfig{TargetLanguageCode, EchoTargetLanguage}` | ✅ identical to Python |
| `SessionResumption`, `ContextWindowCompression` | ✅ |
| `InputAudioTranscription`, `OutputAudioTranscription` | ✅ |
| `Session.SendRealtimeInput`, `Session.Receive` | ✅ |

So the session lifetime work — sliding-window compression, handle reuse, and
opening a replacement session *before* closing the old one on `GoAway` — ports
directly. That was the hardest part of the Python version and it survived the
translation.

## What is not done

- **Audio capture** — device enumeration, opening at the native rate, mono downmix
- **Resampling** — native rate to 16 kHz
- **UI** — caption window, native menu bar, status indicator, direction hint, preferences
- **Config and transcript** — mechanical ports, deliberately left until the UI decides how settings are stored

## Open decisions

These are genuine trade-offs, not oversights. They should be settled before
more code lands, because they shape everything above.

### 1. A Go binary is not automatically dependency-free

The reason to port is distribution, so it matters that the two remaining
subsystems both pull in cgo:

| Need | Option | Cost |
|---|---|---|
| Audio capture | `malgo` (miniaudio) | cgo, but the C source is vendored — no system package to install |
| | `portaudio` bindings | cgo **and** requires PortAudio installed on the target Mac |
| Resampling | cgo binding to `libsoxr` | another system dependency |
| | hand-written polyphase resampler | pure Go, but quality must be proven against the existing test |

`malgo` plus a hand-written resampler keeps the single-binary property. The
resampler is the part that needs care: `tests/test_resample.py` already
measures seam discontinuity and drift, and that test ports over as the bar to
clear.

Worth noting: the Python build has no system dependency either — `sounddevice`
bundles `libportaudio`. The real distribution win of Go is not avoiding
dependencies but shipping one file instead of a 600 MB virtualenv.

### 2. The UI toolkit has no like-for-like replacement

PyQt6 gave a native macOS menu bar, a custom-painted caption box, and property
animations. In Go:

| Option | Native menu bar | Custom painting | Notes |
|---|---|---|---|
| Fyne | Approximated | Canvas objects | Easiest, least native feel |
| Gio | Manual | Immediate mode | Full control, most work |
| `darwinkit` (Cocoa bindings) | Real AppKit | Real CoreGraphics | Closest match, macOS only, cgo |
| Wails | — | — | **Excluded** — browser-based, which the project rules forbid |

"Same functionality" is achievable with any of them; "same look and feel"
realistically means `darwinkit`, which is also the heaviest.

## Suggested order

1. Settle the two decisions above
2. Port the resampler and prove it against the ported `test_resample` bar
3. Audio capture, verified with the equivalent of `s1_capture_check.py`
4. Headless run against the live API — the Go equivalent of `s2_headless_run.py`, which is what would actually prove the reconnect logic
5. UI last, once the engine is known good
