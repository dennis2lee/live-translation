# G0 spike results

Measured on this machine — macOS 26.5 arm64, Go 1.26.5, 2026-08-05. The same
discipline as the Python build's `spikes/RESULTS.md`: numbers, not impressions,
and the gaps named as gaps.

Stack under test: Fyne v2.8.0, malgo v0.11.25 (miniaudio), libsoxr 0.1.3
(Homebrew), google.golang.org/genai v1.67.0, fernet-go, go-keyring.

| Spike | Verdict |
|---|---|
| F1 Korean text | **pass** — renders with no font work at all |
| F2 menus, keys, full screen | **needs a human** — the macOS menu bar is outside the window |
| A1 malgo capture | **pass on mechanics**, level and X3 blocked (see below) |
| R1 resampler | **decided: libsoxr**, pure-Go fallback measured and viable |
| L1 live smoke | **pass** — real KO→EN fragments through the Go SDK |

---

## F1 — Korean text in Fyne

`go run ./spikes/f1_text` renders through Fyne's software painter and writes
`out.png`, so the glyphs can be checked without a display.

Fyne 2.8 renders Hangul correctly **with the bundled theme font**, with no
font loading, no custom theme and no embedded face. Korean, Latin, digits and
punctuation sit on one line at 44 px:

```
영업이익률 up 14%. Dr. Smith agrees.
```

- `/System/Library/Fonts/AppleSDGothicNeo.ttc` **fails to load** — Fyne rejects
  font collections outright ("collections not allowed", `internal/painter/font.go`).
  Any plan that reaches for a system Korean face on macOS must use a plain
  `.ttf`; `Supplemental/AppleGothic.ttf` loads and renders fine.
- `canvas.Text.FontSource` (Fyne ≥ 2.5) sets a face per text object, so the
  caption view can pick its own font without a custom theme.

**Consequence for the plan.** The F1 risk row in PLAN §11 is closed. Embedding
Noto Sans KR is no longer a fix for a defect — it stays on the list only as a
*deliberate* choice for cross-platform determinism, decided in G4:

- default face: Latin comes from Fyne's bundled font, Hangul from a system
  fallback, so the two scripts have different metrics (visible in `out.png` as
  a full stop that sits differently after Hangul than after Latin);
- one embedded face: identical rendering on macOS and Windows, at ~5 MB.

## F2 — menu bar, digit keys, full screen

Not automatable: the macOS menu bar is painted outside the window, so no
screenshot the program can take would contain it.

```bash
cd src_go && go run ./spikes/f2_menus
```

The window lists what to check. The six items are the app menu's identity and
contents, four menus, a check mark that toggles from both the menu and the `9`
key, bare digits arriving with no modifier, `F` for full screen, and the Korean
caption through the **OpenGL** painter rather than the software one F1 used.

## A1 — malgo capture

`go run ./spikes/a1_capture -device "MacBook Air"`.

Capture mechanics pass, against the Python build's acceptance numbers:

| | measured |
|---|---|
| rate opened | 48000 Hz, the device's native rate, not resampled to 16 k |
| bytes in 5.02 s | 480000 — **99.6% of nominal**, bar is 98% |
| callbacks | 50, every one exactly 4800 frames (100 ms) |
| longest gap | 170.7 ms |

Device enumeration reports native formats per device, which is what the
open-at-native-rate rule needs:

```
[0] MacBook Air Microphone  [default]
    native: 44100/48000/88200/96000 Hz, 1ch, f32
```

**Two parts are not measured, and neither is malgo's fault.**

- **Level metering.** Peak read `-Inf dBFS`, i.e. digital silence. Python's
  `sounddevice` reads peak 0 from the same process tree, so this is the
  microphone TCC permission the automation process does not hold, not a capture
  bug. Rerun from a terminal that has microphone access to get the
  −36 dBFS / −88 dBFS comparison the Python build recorded.
- **Sound Blaster X3 at 96 kHz/2ch.** The device was not connected. This is the
  device that bit the Python build, so the A1 acceptance run is not complete
  until it has been repeated with the X3 plugged in:
  `go run ./spikes/a1_capture -device "X3"`.

## R1 — libsoxr or pure Go

`go run ./spikes/r1_resample`. Both resamplers get the same tones through the
same 100 ms blocking the real capture path uses.

48 kHz → 16 kHz (44.1 k and 96 k are the same picture):

| | libsoxr HQ | pure-Go Kaiser sinc |
|---|---|---|
| 200 Hz – 6 kHz | ±0.00 dB | ±0.00 dB |
| 7 kHz | −0.00 dB | −0.37 dB |
| 7.6 kHz | **−2.88 dB** | **−17.76 dB** |
| stopband 8.4–20 kHz | −86 to −87 dB | −91 dB to unmeasurable |
| cost for 2 s of audio | 0.4–0.6 ms (0.02–0.03% realtime) | 5.7–11.7 ms (0.29–0.59%) |

**Decision: libsoxr**, for three measured reasons.

1. **The transition band.** soxr is still within 3 dB at 7.6 kHz where the
   pure-Go filter is 17.7 dB down. That band carries fricative energy — the
   difference between "s" and "sh" — and it is the input to a recogniser, not
   to a listener.
2. **Cost.** 10–20× cheaper. Neither is remotely expensive, but the audio
   thread has a hard deadline and soxr leaves more room.
3. **Parity during the port.** Identical resampling to the Python build means
   one fewer variable when transcripts from the two implementations are diffed
   at the parity gate.

Bundling the dylib into the `.app` (`install_name_tool` rpath fix, PLAN §10 G6)
is the known cost of this choice.

**The pure-Go fallback is now proven, not hypothetical.** With the filter
support scaled by the decimation ratio it reaches −91 dB stopband at 0.6% of
realtime, so if dylib bundling ever turns painful the fallback is a real
option rather than a hope. Its narrower passband is a consequence of the 0.92
rolloff constant, not a limit of the method.

**One measurement caveat, so the table is not misread.** The "spur" column in
the raw spike output reads −40 dB or so for both resamplers at some tones.
`-probe 6000` locates that energy at 5900 and 6100 Hz — spectral leakage from
the Goertzel window, sitting either side of the tone, and identical for both
implementations. It is a property of the measurement, not of the resamplers.
Judge on the stopband column.

## L1 — live smoke against the Live API

`go run ./spikes/l1_live -target en -seconds 25`, streaming
`spikes/fixtures/ko_fixture.wav` in 3200-byte chunks at real time.

**Real Korean → English translation came back through the Go SDK**:

> Hello. Today's quarterly earnings meeting will now begin. First, let's look
> at the third-quarter revenue. […] up 14%.

| | measured |
|---|---|
| connect | 292 ms |
| fragments | 22, first at 2.86 s |
| audio received | 110 chunks, 1 320 000 bytes — consumed, never played |
| session_resumption updates | 23 in 25 s (≈ 1/s, matching the Python build) |
| turn_complete | 0 — none in 25 s of continuous speech |
| max send backlog | 102 ms (one tick; the ticker fires before the first send) |

No raw WebSocket implementation is needed. `LiveConnectConfig` with
`ResponseModalities`, both transcriptions, `TranslationConfig`,
`ContextWindowCompression` and `SessionResumption` is accepted as-is, and the
audio blob shape `{Audio: &Blob{Data: chunk, MIMEType: "audio/pcm;rate=16000"}}`
is right.

**The trap this spike walked into, recorded so it is not walked into twice.**
The first run was closed by the server with `1007 invalid payload data:
Request contains an invalid argument` — no field named. Bisecting the config
(`-bisect`) pinned it to `TranslationConfig`, and the cause was
`TargetLanguageCode: "en-US"`. The Python build sends `"en"`
(`config.DIRECTION_TARGET`). **The API rejects the region-qualified form**, and
rejects it as a whole-config error a second after setupComplete, so it looks
like an SDK problem rather than a bad language code.

A second lesson from the same run: reading a single message after connecting
proves nothing. `setupComplete` arrives before the server has looked at any
audio, so the first bisect pass reported every variant "ok". A probe has to
keep reading until either a transcription or a hang-up.

---

## What G0 changes in the plan

- §11 risk "Fyne Korean rendering unacceptable" — **closed**, it renders
  untouched.
- §11 risk "genai Go SDK Preview surface changes under us" — unchanged, but the
  surface is now proven working, not just proven present.
- §3 "Resampling" — **decided: libsoxr**; the pure-Go row moves from
  "spike compares" to "measured fallback".
- §10 G0 — F1, R1, L1 done; F2 awaits a look; A1 awaits the X3 and a process
  with microphone permission.
