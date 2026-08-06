# G0 spike results

Measured on this machine — macOS 26.5 arm64, Go 1.26.5, 2026-08-05. The same
discipline as the Python build's `spikes/RESULTS.md`: numbers, not impressions,
and the gaps named as gaps.

Stack under test: Fyne v2.8.0, malgo v0.11.25 (miniaudio), libsoxr 0.1.3
(Homebrew), google.golang.org/genai v1.67.0, fernet-go, go-keyring.

| Spike | Verdict |
|---|---|
| F1 Korean text | **pass** — renders with no font work at all |
| F2 menus, keys, full screen | **pass**, with three defects to design around |
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

The program cannot screenshot the macOS menu bar, because it is painted outside
the window. Driving the desktop from outside the app can, so this was checked by
launching the spike and reading the screen.

**One thing had to be fixed before the spike meant anything**: a bare Go binary
is not an application to macOS, and the app menu takes its name from
`CFBundleName`. Run unbundled, the check "does the app menu say LiveCap" cannot
be answered at all — which is exactly the bug the Python build had, where the
menu said "Python". The spike therefore runs from a minimal `.app` (the
`packaging/` work of G6, in miniature).

```bash
cd src_go && go run ./spikes/f2_menus     # unbundled: everything but the app-menu name
```

**Passes:**

- **App menu takes the bundle name**, with About and Quit — no "Python"-style
  wrong name.
- **Custom menus work**: File, Translate, Captions, Help, each opening with the
  right items and separators.
- **Checkable items work at runtime.** Choosing "Complete phrases only (9)" put
  a tick next to it and flipped the state; `MainMenu.Refresh()` is what makes
  the tick appear.
- **Bare digits arrive with no modifier.** `0 1 2 9` all registered through
  `Canvas().SetOnTypedRune`, and each drove its action — translating on,
  direction to EN → KO, complete-phrases off.
- **Full screen is real full screen.** `SetFullScreen(true)` removed the title
  bar, the menu bar *and* the Dock, and the window covered the whole display.
  This is the macOS half of the requirement that settled the toolkit choice;
  the Windows taskbar half still cannot be tested from here.
- **Korean renders through the OpenGL painter**, not just the software one F1
  measured — confirmed on two different strings at 44 px.

**Three defects, none fatal, all needing a decision in G4:**

1. **U+2192 breaks when it has spaces around it.** `"KO → EN"` renders as an
   arrow followed by U+FFFD, the replacement glyph. `"KO→EN"` renders
   perfectly. It is **not** a widget problem — `canvas.Text` and `widget.Label`
   fail identically, so it is Fyne's shaping, and the trigger is the space
   adjacent to a glyph that came from a fallback face. The Python UI uses
   `"→"` with spaces throughout (`config.DIRECTION_LABEL`), so the port must
   either drop the spaces or use a different separator. `·` `…` `—` `✓` all
   render correctly; `⚠` comes out as a colour emoji.
2. ~~**No Preferences item in the app menu.**~~ **This finding was wrong, and
   was corrected on 2026-08-06 by looking at the finished application.** Fyne
   2.8 recognises a menu item labelled `Preferences…` and hoists it into the
   application menu itself, with ⌘, attached — exactly where the Python build
   puts it. The spike concluded otherwise because it never created such an item,
   so there was nothing for Fyne to hoist; "the slot was empty" was read as "the
   slot cannot be filled". The item is declared in the Settings menu in
   `menus.go` and simply appears in the app menu instead.
3. **Escape did not leave full screen.** With `SetOnTypedRune` and
   `SetOnTypedKey` both set, the rune handler fired for every key tested but
   `KeyEscape` never reached the key handler while full screen. `F` toggled
   both ways, so the window itself is fine. Needs pinning down in G4 — the
   Python app leaves full screen on Escape.

Fyne also inserts a **Window** menu of its own between the last custom menu and
Help. Cosmetic, but it means the menu bar will never be exactly the Python
app's.

### What the finished application actually shows (2026-08-06)

Checked on the built bundle, not the spike:

- Application menu reads **LiveCap**, with **About LiveCap**, **Preferences… ⌘,**
  and **Quit LiveCap** — the correction above.
- Menu bar: LiveCap, Translate, Settings, Window, Help.
- The Settings menu carries every item, with the ticks matching the settings
  imported from the Python build's plist (complete phrases on, fixed box on,
  three caption lines).
- Preferences opens with all six tabs and Cancel / Apply / OK, and reports
  "Key in use, from: encrypted store".
- The caption view paints: black background, the rounded `#1B4965` box at the
  bottom in fixed-box mode, the grey idle dot at its bottom-right.
- The `0` key reaches the application: pressing it opened the audio device,
  which raised the macOS microphone prompt. `NSMicrophoneUsageDescription` is
  therefore doing its job.

**One parity gap, now decided rather than open.** The Python build opens
`showMaximized()`, so the caption window fills the screen with the menu bar
still visible. **Fyne 2.8 has no maximise API and no public screen-size API**,
so the Go window opens at a fixed 1280×800. Reaching for cgo and calling
NSWindow's `zoom:` would work and would put macOS-only code back into the UI
layer, which is the one place this port has kept free of it.

**Dennis decided (2026-08-06): leave it.** The window opens at a usable size and
the person running it maximises or goes full screen themselves. No workaround,
and the gap comes off the parity gate.

**One behaviour worth knowing.** Starting translation blocks the UI goroutine
while macOS shows the microphone prompt, because opening the device is a
blocking call made on that goroutine. It happens once per install, and the
status dot stays grey until the prompt is answered.

### End to end, from a real microphone (2026-08-06)

The `0` key on the built bundle: the status dot went green, the menu bar showed
the microphone-in-use indicator, and a transcript was created. Dennis then spoke
Korean into the built-in microphone, and the Go build translated it. Its
transcript, in full:

```
# LiveCap transcript

- **Started** 2026-08-06 08:08:44
- **Target language** en
- **Model** gemini-3.5-live-translate-preview

---

`01:59` Three, four,
`02:00` two, two, three, four.
`02:07` That's why I did it.

---

- **Ended** 2026-08-06 08:10:59
- **Duration** 02:15
- **Lines** 3
- **Reconnects** 0
```

That is the whole chain: microphone → ring buffer → libsoxr → the Live API →
fragments → the caption buffer → the transcript. The footer matters as much as
the lines do — it is only written by `Shutdown`, so the single exit path ran and
closed the file properly rather than leaving it half-written.

**One false alarm, recorded because it wasted a round trip.** Before this,
playing the Korean fixture through the speakers produced nothing. The cause was
`output volume: 0, output muted: true` — the machine was silent, so the
microphone had nothing to hear. Not a fault in the port. When two builds of the
same application are installed and both are called LiveCap, check *which*
process is running and which transcript grew before concluding anything: the
first "it went green" of the morning was the Python build, not this one.

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
- §10 G0 — F1, F2, R1, L1 done; A1 awaits the X3 and a process with microphone
  permission.
- §6.9 UI — three F2 defects to design around: no spaces around `→`, no
  Preferences slot in the app menu, and Escape not reaching the key handler in
  full screen.

---

## G6 — the bundle, and one crash worth recording

`packaging/build_app.sh` assembles `dist/LiveCap.app`: **28 MB**, against the
Python build's 110.

**The first bundle was killed on launch**, with
`SIGKILL (Code Signature Invalid)` raised by dyld while loading a dependent
library. The cause is worth keeping: `install_name_tool` rewrites the copied
`libsoxr.0.dylib` to fix its rpath, and that invalidates whatever signature the
library arrived with. On Apple silicon dyld does not report an invalidly-signed
library — it kills the process. Signing the bundle alone does not help, because
that does not reach a nested dylib; the library has to be signed first and by
name. With that in place the bundle passes `codesign --verify --deep --strict`,
launches, stays up across a rebuild-and-relaunch cycle, and its own
`--selftest` passes from inside the bundle:

```
  ok    api key                  from encrypted store
  ok    resampler                libsoxr libsoxr-0.1.3
  ok    audio input              4 device(s)
  ok    live api reachable       generativelanguage.googleapis.com, 18 ms median
```
