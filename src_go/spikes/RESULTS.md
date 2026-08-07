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
| R1 resampler | **libsoxr on macOS, pure-Go elsewhere** — see the correction below |
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


---

## R1 revisited, and the Windows build (2026-08-06)

Building for Windows forced the resampler question open again, and the answer
changed.

**The pure-Go filter was never 17.8 dB down at 7.6 kHz. The rolloff constant
was.** R1 measured it at `firRolloff = 0.92`, which is where the transition band
starts. Raising it to 0.97 — the same place libsoxr puts it — gives, measured by
`TestFrequencyResponseOfEveryImplementation` against both filters in the same
run:

| 48 kHz → 16 kHz | libsoxr HQ | pure-Go Kaiser sinc |
|---|---|---|
| 200 Hz – 7 kHz | flat within 0.5 dB | flat within 0.5 dB |
| **7.6 kHz** | **−2.88 dB** | **−2.40 dB** |
| 9 / 12 / 20 kHz folding back | below −60 dB | below −60 dB |

The pure-Go filter is now *slightly ahead* in the fricative band. R1's
conclusion — "libsoxr, because the pure-Go filter loses the fricatives" — rested
on a constant, not on the method, and R1 said so at the time without following
it up.

**So the two ship side by side**, chosen by build tag: libsoxr on macOS, because
it keeps the numeric path identical to the Python build for the parity gate; the
pure-Go filter everywhere else, because it makes the Windows application a
single file.

### The Windows artefacts

`./packaging/build_windows.sh`, run from macOS with `mingw-w64` and `msitools`:

- **`LiveCapGo.exe`, 30 MB**, PE32+ GUI, built with `CGO_ENABLED=1
  GOOS=windows CC=x86_64-w64-mingw32-gcc`. Every one of Fyne's GLFW, miniaudio
  and the genai SDK cross-compiled without a change.
- **Imports Windows system DLLs only** — kernel32, user32, gdi32, shell32,
  opengl32 and the UCRT. No libgcc, no libwinpthread, no libsoxr. The build
  script fails if that ever stops being true, because the first machine missing
  a stray DLL would fail at launch with a dialog naming the DLL rather than the
  application.
- **`LiveCapGo-1.0.0-x64.msi`, 14 MB**, built with `wixl`. Verified with
  `msiinfo`: correct ProductName, ProductVersion, Manufacturer and UpgradeCode,
  a Start-menu and a desktop shortcut, and one payload file.

**None of it has been run.** There is no Windows machine here. "It cross-builds,
it imports nothing unusual, and the MSI is structurally correct" is the
strongest claim available; the build script prints the five things to check on
real hardware, starting with the one that chose the toolkit — whether full
screen covers the taskbar.


---

## G3 — the 30-minute endurance run (2026-08-06)

Headless, by decision: the fixture is fed into the ring buffer at real time and
the real translator, caption buffer and transcript writer run above it — the
same objects the window drives, minus the painting and the microphone.
`spikes/g3_endurance` is the harness.

### First run: every counter passed

| | measured | bar |
|---|---|---|
| elapsed | 30 m 10 s | 30 min |
| reconnects | **3**, at 9:01 / 18:01 / 27:01 | ≥ 3 |
| gaps | **442 / 471 / 413 ms** | < 1 s each |
| max send backlog | 300 ms (start-up; flat after) | < 1500 ms |
| chunks sent | 18 000 — exactly 30 min × 600 | no audio dropped |
| fragments / lines | 1 549 / 483, first at 3.14 s | — |

`go_away` arrived every 9 minutes with `time_left=50s`, matching both the
Python run and the API's documented cadence.

### The measurement that was almost done wrong

Reading the *English* transcript around each swap showed thinner output than
the same passage translated cleanly elsewhere in the loop, and that first read
as sentences lost. It is the wrong test: the model rephrases freely on every
pass, so English prose cannot distinguish "lost across the swap" from
"translated differently this time".

The Python build never judged it that way. Its "no lost sentences —
360/360, worst 0.78, mean 0.96" comes from `s2_headless_run.py`, which collects
the **Korean input transcription** and scores every script sentence's coverage
in it with a SequenceMatcher. The harness now saves the collected source
transcription in exactly that scorer's input format, and the **same Python
scorer** grades the Go build — no ported scorer to drift:

```
.venv/bin/python spikes/s2_headless_run.py --compare-only <out>/source.txt \
    --script spikes/fixtures/ko_script.txt --repeat 20
```

The first run predates the source capture, so its counters stand but its
sentence-loss question is unanswered; the rerun below answers it.

### Rerun with source capture: the sentence-loss question answered

Same shape as the first run — 3 reconnects at the 9-minute marks, gaps
**367 / 439 / 453 ms**, backlog flat at 300 ms, 18 000 chunks, 1 578 fragments,
488 transcript lines — plus 1 777 Korean source fragments collected.

Scored by the Python build's own scorer, unmodified:

```
script comparison: 360 sentences
similarity worst 0.78 / mean 0.96
✅ no sentence lost (all above 0.5)
```

**360/360, worst 0.78, mean 0.96 — the same numbers the Python build's
30-minute run recorded**, to the second decimal place. Same fixture, same
scorer, same model; the port sits exactly on the bar the Python build set.
The G3 endurance line of the parity gate is closed.

The thinner English around the swaps in the first run is thereby explained:
nothing was lost from the audio or the recognition — the model simply phrases
each pass differently, and the English is the wrong place to measure.


---

## G4/G5 — transcript parity and the live direction switch (2026-08-06)

### Transcript byte-format: identical

`spikes/g4_transcript_golden` feeds the identical fragment script — same start
time, same lines including Korean, same elapsed values, same reconnect and
direction notes, same trailing-space and empty-line cases — to the Go writer
and to the Python `TranscriptWriter`, and diffs the files:

```
IDENTICAL apart from the Ended wall-clock line
```

Even the filenames match (`2026-07-24_143052.md`). The one normalised line is
`- **Ended**`, which is wall clock in both implementations by design.

### Direction switch, live

`spikes/g5_direction` mimics `Window.restartTranslator` exactly: one ring
buffer and one transcript writer survive the switch, the translator alone is
stopped and reopened with the new target, and the switch is noted in the
transcript. Korean audio fed to the KO→EN half, English to the EN→KO half:

```
target=en  fragments=73  with-hangul=0
  first: Hello. Today's quarterly earnings meeting
target=ko  fragments=76  with-hangul=75
  first: 여러분, 분기별 실적 회의를 시작하겠습니다.
PASS — one transcript, both directions, switch noted
```

EN→KO is thereby proven live, not just configured — the gate line closes.

### Display sleep, and a first shutdown route

- `TestAssertionIsVisibleToPmset` acquires the same `DisplaySleepBlocker` the
  window uses and greps `pmset -g assertions` for it, then releases and greps
  again. Green — the assertion really appears and disappears.
- SIGTERM against a running (idle) instance logged
  `shutdown complete goroutines=9` and exited cleanly. The mid-session version
  of all four routes still needs the screen, and screen access was declined
  while the Python build was live — deferred, not skipped.

### A cosmetic warning, kept honest

The app logs Fyne's "not been migrated to the fyne.Do threading model" warning
once at launch. The tick loop now starts with the event loop (armed inside
`Lifecycle().SetOnStarted`, which also un-hangs the test driver), but the
warning persists, so its trigger is elsewhere — possibly Fyne-internal. It is
cosmetic today and a crash risk at the next major Fyne release, so it is filed
as follow-up work rather than forgotten.


---

## Shutdown routes, menus and Preferences (2026-08-06)

### A real defect, found by the gate

The first mid-session SIGINT **crashed** — `libc++abi: terminating due to
uncaught exception of type NSException`, `signal arrived during cgo execution`.
Shutdown tears down window state, and AppKit aborts when that happens off the
main thread; the signal handler is just another foreign thread, subject to the
same rule as the audio callback.

Marshalling it with `fyne.DoAndWait` stopped the crash and exposed a worse
one: the app exited **without writing the transcript footer**, and with no
shutdown log at all. `Shutdown` ran on the UI goroutine and waited on the tick
goroutine, which was itself parked in `fyne.Do` waiting for that same UI
goroutine. A deadlock — and the window-close button took the identical path, so
it was latent there too, on the route a user takes every day.

The fix is two rules, both now written into the code:

- **Shutdown never waits on the UI goroutine.** The tick is signalled and
  `onTick` turns itself into a no-op; nothing is joined.
- **The signal handler queues Shutdown with `fyne.Do`, not `DoAndWait`**, and
  waits on a channel with a 5-second deadline, so a wedged UI goroutine cannot
  stop the process dying on Ctrl-C.

All four routes then passed mid-session, each releasing the audio device and
the display-sleep assertion and logging its goroutine count:

| route | result |
|---|---|
| SIGINT | `audio capture stopped` → `display sleep no longer blocked` → `shutdown complete goroutines=9` |
| SIGTERM | same, `goroutines=9` |
| window close button | same, `goroutines=12` |
| Quit (Apple Event, as the Quit menu item sends) | same, `goroutines=12` |

The transcript vanished on these runs by design, not by fault:
`removed empty transcript` — a session that captured nothing is not kept. The
footer path itself was proved by the microphone run earlier the same day and by
the golden-file diff.

### Menus and Preferences: tested, not clicked

macOS treats the menu bar as the desktop shell, and screen access to it was
declined. Driving the menu items and the Preferences dialog **through their own
Go functions** turned out to be the better test anyway: reproducible, in CI,
and able to assert state a screenshot cannot.

`menus_test.go` — every item enumerated including submenu parents; no enabled
item without an action; the twenty labels the Python build has; every checkable
item drives its setting *and* follows it back; direction, font size and box
position behave as exclusive groups; Start/Stop enablement keyed off the
session objects, so a failed start leaves Start usable; the status line carries
the cause; Clear Captions does not strand lines; Reset restores the defaults and
the ticks follow.

`prefs_test.go` — the three-button contract in full: Apply applies and leaves
the dialog working, a second Apply stacks on the first, Cancel after Apply
restores exactly what was in force when the dialog opened, Cancel without Apply
touches nothing, and **Cancel restores the encrypted key store** — including
the "there was no key" case, which must empty the store rather than write an
empty string. Also: all nineteen fields across the six tabs survive a round
trip, the two vocabularies stay separate, values are clamped on the way in, the
dialog opens on the settings in force, and the key status names its source and
never the key.

What was checked on screen instead, and passed: the `9`, `2` and `1` keys drove
their settings and persisted them, and the `EN→KO` direction hint rendered
correctly — no tofu, because the label carries no spaces around the arrow.


---

## Windows, on real hardware (2026-08-06)

Dennis installed the MSI and went full screen. Two findings, one good:

- **Full screen covers the taskbar.** The requirement that chose Fyne over
  DarwinKit holds on real Windows, and it is no longer resting on documented
  GLFW behaviour.
- **Fyne's in-window menu strip stayed on top of the captions.** On macOS the
  menu lives in the system bar, which full screen hides by itself; on Windows
  it is an overlay on the window, so it survived. PLAN §3.1 anticipated exactly
  this and it went unhandled until the screenshot came back.

Fixed by `Window.syncMenuVisibility`: on the platforms that draw the menu in
the window, full screen clears the main menu and leaving it puts the menu back.
`SetMainMenu(nil)` removes the overlay outright rather than leaving an empty
strip — checked against Fyne's `buildMenu`, which returns early on nil.

### A second pass, and a white frame that was not the menu (2026-08-06)

The menu strip was gone in the next photo, and a **white border still framed the
whole display** — Windows only, macOS clean. It was not a window decoration.

Fyne insets window content by `theme.Padding()` on all four sides unless the
window is told otherwise (`glfw/canvas.go` — `contentPos`, `contentSize`), and
paints the exposed strip with the canvas background, which follows the OS
light/dark setting. **That is why it looked like a macOS/Windows difference and
was really a light/dark one**: the Mac runs dark, so the strip is black and
lands invisibly on the caption view's own black background; the Windows laptop
runs light, so the strip is white and outlines the screen.

`Window.SetPadded(false)` at construction. `TestWindowContentReachesTheEdge`
pins it by capturing the canvas under the light theme and reading the four
corner pixels — with the fix reverted all four are (255,255,255), which is the
photograph.

The lesson is the same one the menu strip taught: **a caption window has to be
checked against both theme variants, not just both platforms.** Neither defect
was reachable from a dark-mode Mac.

### A third pass: the bar across the top (2026-08-06, IMG_2372)

`SetPadded(false)` held — the left, right and bottom edges came back clean. A
white bar remained across the top only. That is not padding; it is the menu's
reserved height, and the mechanism is in Fyne's `glfw/canvas.go`:
`setMenuOverlay(nil)` sets `c.menu = nil` **but the content re-layout sits
behind an `if c.menu != nil` guard**, so removing the menu never gives its
space back. The full-screen resize had already laid the content out below a
menu that was then removed, and the exposed strip is painted with the canvas
background — white in a light theme, again.

macOS cannot reach this either way: with a native menu the overlay is never
built and `menuHeight` is always 0.

Three changes, one funnel:

- `applyFullScreen` is now the only path that switches the window. The `f` and
  Escape keys, the menu item, Preferences Apply and start-up all reached
  `SetFullScreen` separately, and one of them (Preferences Apply) skipped the
  menu sync entirely — a strip that would have come back through that door
  after this fix went out the other.
- On the way in, the menu comes off **before** the mode switch, so the
  full-screen layout is computed without it. On the way out the menu goes back
  after, and Fyne's non-nil path re-lays out by itself.
- After `SetMainMenu(nil)` the content is explicitly resized and moved to fill
  the canvas, compensating for the skipped re-layout whatever the event
  ordering turns out to be on the real machine.

`TestFullScreenFunnel` pins the funnel on the test driver — key, Apply, and a
diverged setting/window state all keep the two in step. The strip itself cannot
be reproduced from this machine: the reclaim runs behind
`platform.MenuIsDrawnInWindow()`, false on darwin, and the test driver has no
menu overlay. The mechanism is established by reading the Fyne source; whether
a second cause hides behind it is for the Windows machine to say.

**Confirmed on the Windows machine, 2026-08-06, with the 1.0.3 MSI: full
screen is clean.** No bar at the top, no border on any edge, taskbar covered.
That closes all three layers the photographs peeled back — the menu overlay
(1.0.1), the theme-coloured padding (1.0.2), and the menu's unreclaimed height
(1.0.3). Each was invisible from a dark-mode Mac; each took one photograph to
localise.

**The menu is the mouse route out of full screen, so removing it needs a
replacement.** `f` now toggles full screen from the keyboard. Escape stays
wired but is not relied on: spike F2 measured it not arriving in full screen.
Never hide the menu without leaving a key that brings it back.

## The caption buffer, re-verified against the Python reference (2026-08-06)

Asked to re-check the translation-caption buffering, and the port's strongest
available check is the one the project was set up for: `src/` is an executable
specification, so drive both implementations with identical inputs and demand
byte equality. `spikes/bufdiff` does that — a seeded generator produces op
scripts (feeds with duplicates, Korean, abbreviations, missing leading spaces
and long utterances; ticks at deltas that straddle every timing window;
turn_complete, reconnect, clear, flush; live changes to max_lines,
complete-phrases and the policy), and the full observable state — display
lines with settled flags, pending, queue, committed, drained commits — is
compared after every single op.

**100 seeds × 800 ops plus 50 × 400 (160,000 ops): byte-identical.**

A line-by-line review alongside it found only representational differences,
each already correct: character counts are runes rather than bytes
(`MinSentenceChars`, the auto-policy sample), the prefix slice in `commit`
uses byte indexing against a byte-verified prefix, and `deque(maxlen=…)`
becomes an explicit tail slice in `drain` and `SetMaxLines`.

The harness itself tripped over one real difference on its first run:
`captions.Options` treats a zero `ClearAfterSeconds` as *disabled*, while the
Python dataclass defaults it to 2.0. The application always passes the
configured value, so this cannot bite in production — but any future test or
tool that builds a bare `Options{}` silently gets a buffer that never clears.
Recorded in `spikes/bufdiff/README.md`.

The integration invariants were re-checked at the same time: every
`DrainNewCommits` call site drains before deciding what to do with the lines
(`recordNewLines` discards when there is no writer; shutdown flushes and
drains before closing the transcript), so the one unbounded structure cannot
grow while the app idles.

## The Preferences level meter

Reported as "audio input level testing does not work". It did not, for three
separate reasons that all trace back to the same one: the port copied the widget
and not the behaviour.

**1. There was no test to run.** The meter only mirrored `Window.capture`, which
is nil unless a translation is running, so the control was dead in the state
people actually use it — before a session, to find out which device carries the
sound. The Python build opens the selected device itself behind a Start/Stop
button. Ported.

**2. The bar was linear, and hearing is not.** `TakePeak` returns a 0–1 sample
peak and it went straight into a `ProgressBar`. Measured on the built-in
microphone with speech played into the room:

| dBFS | linear bar | dBFS bar | what it is |
|---:|---:|---:|---|
| −88 | 0.004% | 0% | nothing routed in |
| −54 | 0.20% | 10% | `SilentInputPeak` |
| −36 | 1.6% | 40% | live mic, quiet room |
| −26 | 5.0% | 56% | `SpeechPeak` |
| −12 | 25% | 80% | the target |

So speaking into the microphone moved the bar from 0.004% to 1.6% — visually
identical to a dead input, which is the one distinction the meter exists to
make. Live capture on 2026-08-06 read −32.7 to −38.4 dBFS during speech and
−53 to −73 in the quiet room; `spikes/meter_check` prints both columns.

**3. It stole the peaks the window needs.** `TakePeak` resets. `window.go` says
"one reader per tick" and the meter was a second reader on the same capture, ten
times a second, for as long as the dialog was open — so speech detection and the
"no signal" warning both under-read while Preferences was open. `Capture.Peak`
now reads without resetting, and the meter uses it whenever it is observing a
session rather than driving its own device.

A fourth defect surfaced while measuring rather than from the report: a
meter-only capture has nothing draining its ring, so it filled in a second and
then logged an overflow on every callback — five warnings a second.
`NewMeterCapture` stops after publishing the peak, before the resampler and the
ring write. Confirmed `dropped_bytes=0` afterwards.

**A review pass found the state machine had one direction missing.** The meter
handled the session starting underneath a running test (hand the device over,
observe) but not the session *ending* underneath an observing test — `observing`
stayed true, the switch fell through to zero, and the dialog sat claiming to
listen to a session that had ended while reading "no signal" off a working
microphone. The transition is now a pure function, `meterTransition`, with all
eight states pinned in a table test — which promptly caught its own first draft:
reclaiming while an own-meter capture was somehow still open would have opened a
second stream on the device and leaked the first, so the `!ownMeter` guard is
load-bearing. A failed reopen also resets the button label, and drops
`observing` first so it cannot retry on every tick.

**A Fyne behaviour worth knowing.** `progressbar.go` fills an empty bar with
`progressBlendColor(primary)` — a dimmed version of the fill colour, not a
neutral groove. Empty and full differ in brightness, not in shape, so an idle
meter reads as a full one at a glance. The number and verdict inside the bar are
what carry the reading; do not remove them. The floor verdict says "silent;
speak, or check the input" for the same reason — a quiet room and a dead input
both land below −60 dBFS, and only one of them is a fault.

## The icon

Minecraft theme, drawn by `packaging/make_icon.py` as real pixel art on a 32x32
grid — Minecraft's **filled map**: a parchment sheet with the world drawn on it.
An item you can hold, and literally a picture of the world, which is what
translation reaches across.

Six drafts. Four never shipped, and the reasons are worth keeping because they
are all the same reason:

| draft | why it failed |
|---|---|
| two speech bubbles facing each other | at icon size they read as a pair of eyes |
| one bubble with a thick arrow | legible, but an arrow means convert / next / export / play |
| one bubble with 가 and A | right meaning, but hand-plotted glyph bitmaps look hand-plotted |
| a Minecraft sign with caption bars | clean, but it says "text", not "across languages" |

The fifth, a blocky planet, shipped in 1.0.0 and was replaced by the map on
2026-08-06.

Every draft was judged by rendering it at 16, 24, 32, 48, 64 and 128 px and
looking at the small end first, because that is where an icon either works or
does not. Five further concepts were drawn as a set — nether portal, grass
block, filled map, world-as-speech-bubble, world with a caption bar — and three
of them died at 16 px: the portal loses its obsidian frame and becomes a purple
rectangle, the caption bar becomes a grey smear beside a shrunken globe, and the
map as first drawn became a tan square with one green speck.

**The map only survives small because of two area decisions, and both were
measured, not guessed.** The border is two rings, not three — the third ring ate
the field. And there are four landmasses, not seven: at 16 px the scaler samples
every other pixel, so seven thin masses vanish. Four *tidy* rectangles survive
but then read as a window pane at 128 px, so each mass is built from overlapping
rectangles — a ragged coastline at full size, no cost in area at small size.

On macOS 26 the system insets an app icon into a rounded tile of its own, so the
art is drawn slightly smaller than the slot and gains a light surround. That is
the OS, not the `.icns`, and it is why a full-bleed square subject still reads.

It reaches three places: the `.icns` in the `.app`, the `.ico` embedded in the
Windows binary as a resource (so Explorer, the taskbar and the window all draw
it), and the MSI's `Icon` table, which is what the desktop and Start-menu
shortcuts and the Add/Remove Programs entry use.
