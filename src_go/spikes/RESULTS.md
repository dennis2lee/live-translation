# G0 spike results

Measured on this machine — macOS 26.5 arm64, Go 1.26.5, 2026-08-05. The same
discipline as the Python build's `spikes/RESULTS.md`: numbers, not impressions,
and the gaps named as gaps.

Stack under test: Fyne v2.8.0, malgo v0.11.25 (miniaudio), libsoxr 0.1.3
(Homebrew), google.golang.org/genai v1.71.0, fernet-go, go-keyring.

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

## Colours in Preferences, and a hint for the 9 key (2026-08-08)

Two gaps against the Python build, closed:

**The colour controls did not survive the port.** `Settings` carried
`bg_color`, `box_color` and `text_color` the whole time — validated, persisted,
applied by the caption view — but the Appearance tab never grew the buttons, so
the only way to change them was editing settings.json. `colourPick` is the Fyne
counterpart of the Python `_ColorButton`: a swatch plus a button showing the
hex value, opening `dialog.NewColorPicker` in advanced mode seeded with the
current colour. As in the Python dialog, the picker's alpha is dropped — the
box's transparency belongs to the Box opacity slider, and letting the picker
smuggle in a second alpha would give two controls over one value.

**The 9 key toggled complete-phrases mode silently.** Now
`ToggleCompletePhrases` announces "Complete phrases only: on/off" under the
caption box in the same style as the direction hint. Unlike the direction hint
it is not gated on ShowDirectionHint: that setting quiets an announcement that
repeats a menu click, while this is the only confirmation a bare keypress gets.
The menu item routes through the same method.

Verified in the running app, not only in tests: the hint appears and fades for
both directions of the toggle; the three colour rows show their swatches; the
picker opens titled and seeded; choosing a colour updates swatch and hex;
Apply recolours the live caption box; Cancel puts the original colour back.
The screen-driving lesson from this pass: a 2-second hint cannot be caught by
type-then-screenshot across two tool calls — batch the keypress and the
capture, or conclude falsely that the hint never showed.

## Seven caption observations from a 67-minute session (2026-08-08)

Reported after a real sermon in complete-phrases mode. `spikes/capscenario`
replays sessions against the real buffer on the same 100 ms tick the window
uses, with audio peaks at measured speech levels, and reports what the reader
would have seen. Five of the seven reproduced; one was already correct; one was
not a defect at all.

| # | Observation | Verdict |
|---|---|---|
| 1 | clear 2 s after the backlog drains in silence | **already correct** — measured 2.0 s |
| 2 | a full stop sometimes shows at once, sometimes not | **reproduced** — 13-rune sentence +1.2 s, 69-rune +0.1 s |
| 3 | settled sentences arrive in a batch | **reproduced** — 2 of 5 never displayed |
| 4 | the box width is not used | **reproduced** — rows filled 57–87%, one at 12% |
| 5 | the previous sentence is wiped mid-talk | **reproduced** |
| 6 | captions vanish while a long sentence arrives | **reproduced** |
| 7 | reconnects in the transcript | **not a defect** — see below |

**The root cause of 5 and 6 was a threshold, and the level-meter work is what
made it visible.** `SpeechPeak` was 0.05 — **−26 dBFS, above the level people
actually speak at.** Measured speech into the built-in microphone peaks −32.7
to −38.4 dBFS, and CLAUDE.md has recorded −36 since the Python build. So
`peak >= SpeechPeak` was essentially never true, the buffer believed the room
was silent throughout a sermon, and the "somebody is talking, do not clear"
guard never once engaged. Both builds shipped this. Now 0.0056 (−45 dBFS),
which sits in the measured gap: 7 dB below the quietest speech, 8 dB above the
loudest reading from a quiet room. `TestSpeechPeakSitsBetweenSpeechAndAQuietRoom`
pins it to those measurements rather than to a number somebody liked.

**2 — a full stop that worked only sometimes.** In complete-phrases mode a full
stop needed 20 runes behind it (`MinSentenceChars`), so "Yes, I agree." waited
out the 1.2 s idle timer while a long sentence appeared immediately. The length
rule was a proxy for "is this an abbreviation"; `endsWithAbbreviation` asks that
question directly, from a deliberately short list plus the single-letter initial
case. Applied in both modes now — the transcript was getting "Dr." on a line of
its own.

**3 — a burst skipped lines.** Past `max_lines` behind, the release stopped
pacing entirely and drained the whole queue in one pass, so intermediate lines
entered `committed` and left it inside a single repaint. They reached the
transcript, which is what made it hard to see. Pacing is now two-tier: half the
hold up to `CatchUpFactor*max_lines` behind, and only beyond that the old
dump. **The latency bound is unchanged** — the queue is still bounded after
every tick, at `CatchUpFactor*max_lines` instead of `max_lines`, and the fuzz
invariant was updated to state that rather than quietly dropped.

**4 — every sentence wasted a row.** The view wrapped each committed sentence
as its own block, so each one's tail sat on a part-empty row. Reflowing the
settled sentences as one paragraph: 6 rows → 5 for the same three sentences,
mean fill 78.5%. A real gain, not a dramatic one — the remaining slack is
ordinary greedy word wrap at 44 px.

**6 — an empty box says nothing.** Complete-phrases mode withheld unsettled
text unconditionally, so a long sentence left the box blank for as long as it
took to translate. It now shows the text in progress **when there is no settled
line on screen**: there is nothing to protect from rewriting, and blank is
strictly worse. The moment a settled line exists it withholds again.

**7 — the reconnects are the API, not the app.** Seven in 67 minutes, at 09:00,
18:01, 27:01, 36:01, 45:03, 54:04 and 01:03:04 — one every nine minutes, which
is exactly the `go_away` cadence CLAUDE.md records from the Python spikes. Gaps
373–870 ms, against 370–459 ms measured then. Nothing in the transcript is lost
across them. This is the server ending a session on schedule and the client
swapping in a new one, which is what the resumption handling is for.

Scenario verdicts after the fixes: all five green.

## What a reconnect costs, in complete-phrases mode (2026-08-08)

Asked whether the nine-minute reconnect breaks the translation, with
complete-phrases mode the worry. Three separate questions, and they have
different answers.

**Audio: nothing is lost, by construction.** The sender loop does not read the
ring while there is no session — it polls and waits (`translator.go`, the
`holder == nil || holder.isClosed()` branch). The ring keeps filling and drains
into the new session once it opens. `RingSeconds` is 30, against a measured
swap of 373–870 ms: **34× headroom on the worst swap seen.** The 30-minute
endurance run scored 360/360 sentences against the Python scorer with no lost
sentences, and `Stats.MaxBacklogMS` — the measurement that separates a
client-side stall from the API answering in bursts — stayed near zero.

**Ordering: the new session is opened before the old one is closed**, which is
the third of the three mechanisms in the package doc. There is no window where
audio has nowhere to go.

**The display: there was a real defect, and complete-phrases mode is exactly
where it showed.** `NoteReconnect` asks the buffer to treat the next fragment
as a continuation of the last settled line — correct when the old session was
cut off mid-sentence, wrong when the line was already a whole sentence. A line
settles two ways: it reached sentence-final punctuation, or the idle timer gave
up waiting. **Only the second kind was interrupted.** In complete-phrases mode
every settled line is the first kind by definition, so every one of the seven
reconnects in an hour glued two unrelated sentences together:

```
before: "… | The reading is finished here."
after:  "… | The reading is finished here. and then the speaker carried on."
```

`looksFinished` now gates the resume. A finished sentence stays its own line and
the new session starts a new one; a line the idle timer settled mid-sentence is
still rejoined, which is what the resume exists for. Both cases are pinned by
tests and by `spikes/capscenario` scenario 6.

This was a caption defect, not a session defect: the audio and the transcript
crossed the reconnect intact all along. It was the reader's line that was
being corrupted.

## Re-examining the reconnect claim, and what the transcript really says (2026-08-08)

Dennis asked whether the reconnect merge was genuinely a problem. Checking it
against the 67-minute transcript, none of the seven reconnects showed two
sentences joined — which looked like a refutation and was not one.

**The transcript cannot see the merge.** `commit` deliberately keeps it out:
when a resumed line has already been handed over, `resumedPrefix` emits only
the continuation, so the transcript records the two halves separately while the
display shows them joined. Demonstrated directly — same buffer, same input:

```
display:    "So the staff translated into Chinese. Yes. It was only two days."
transcript: ["Yes. It was only two days."]
```

So the transcript is not evidence either way, and the earlier check was the
wrong instrument. What the timestamps do say is that at least two reconnects
(27:01 and 01:03:04) landed mid-flow, with the previous line one second old, so
a settled line was almost certainly on screen for the resume to swallow. The
defect is real and reachable; the claim that it fired on all seven was not
supported and should not have been made.

**A regression of my own, found while re-checking.** The abbreviation list
written the same day contained `no`, `etc`, `inc` and `ltd`. Each ends a
sentence far more often than it abbreviates, so **"The answer is no." stopped
settling on its full stop** and waited out the idle timer — reintroducing, for
one very common word, exactly the defect the list was written to remove. The
list is now titles plus `vs` only, and the test pins the three cases that
caught it.

### The largest remaining caption problem is the idle timer

Counting the transcript rather than guessing:

| section | lines | settled with no sentence punctuation |
|---|---:|---:|
| worship and singing (to 25:00) | 230 | **53%** |
| spoken sermon (from 25:00) | 532 | **30%** |
| whole session | 762 | 37% |

Fifty-six lines are six characters or fewer — "Love", "Lord,", "My", "arise".
These are not sentences; they are the 1.2 s idle timer firing between fragments
and settling whatever had arrived. `IdleCommitSeconds` is 1.2 s against a
fragment interval measured at "about one per second", which leaves **0.2 s of
slack before an ordinary pause splits a sentence**. It is also capped in
practice: `Tick` force-commits pending at `ClearAfterSeconds` (2.0 s) as well,
so no idle value above 2.0 has any effect.

**This is not being tuned by guesswork.** Nothing recorded fragment
inter-arrival, so `slog.Debug("target fragment", "gap_ms", …)` now does, per
session so a reconnect's swap is not counted as a speaker's pause. One real
session with debug logging on gives the distribution, and the constant can be
set from it the way every other constant here was.

**The 318-second gap is unexplained.** After the 18:01 reconnect the next
caption is at 22:47 — the largest gap in the session by a factor of eight (next
largest 41 s). The session was healthy either side: no extra reconnect, and the
next `go_away` arrived on schedule at 27:01. Most likely five minutes of
singing that produced no translation worth emitting, but the transcript cannot
distinguish that from output having stopped, and the fragment log above is what
would settle it next time.

## Full-codebase review, and what it found (2026-08-08)

A review-only pass over every non-spike Go file — about 4,800 lines — recorded
in `code_review_2026-08-08.md`. Twelve findings, all fixed the same day. The
three that mattered:

### The key test displayed the API key on screen

`TestAPIKey` sent the key in the query string. `net/http` wraps transport
failures in a `*url.Error` whose `Error()` prints the whole URL, and
Preferences prints that string verbatim in a label. Reproduced against an
unresolvable host:

```
Get "https://…/v1beta/models?pageSize=1&key=AIzaSy…": dial tcp: no such host
                                                 ^^^ the key, in clear
```

Measured before changing anything, because moving the key was only worth doing
if the endpoint accepts it elsewhere:

| how the key was sent | answer |
|---|---|
| nothing | 403 `PERMISSION_DENIED`, "unregistered callers" |
| `x-goog-api-key` header | 400 `API_KEY_INVALID` |
| `?key=` query string | 400 `API_KEY_INVALID` |

Identical, so the header costs nothing. Two tests pin it: one drives a server
that accepts and drops the connection and asserts the key is absent from the
error, one asserts the header carries it and the URL does not. Both fail on the
old code.

### Debug logging did nothing, and an installed build had nowhere to log

`setupLogging` ran once in `main`. The Preferences checkbox saved a setting
that nothing read until the next launch, silently. Worse, the log went to
stderr alone — an `.app` launched from Finder has no terminal, so **every line
it wrote was discarded**. The fragment-gap measurement that `IdleCommitSeconds`
is waiting on was uncollectable from the builds actually in use.

`internal/logging` now holds the level in a `slog.LevelVar` (so Apply changes
the running process) and writes to `~/Library/Application Support/LiveCap/
livecap.log` at 0600, rolled at 8 MB. The path is shown in Preferences and in
`-selftest`.

### A data race between capture shutdown and the device callback

`Stop` cleared `c.resampler` and only then called `device.Stop()` — and
`device.Stop`/`Uninit` is what waits for in-flight callbacks, so the write and
the callback's unsynchronised read genuinely overlap. An interface value is two
words; a torn read is a real crash. `go test -race` could not see it because no
test drives a device callback.

The field is an `atomic.Pointer` now, and a test calls `onAudio` directly from
a goroutine while retiring the resampler — which is the only way to put this
concurrency in front of the race detector. Reverting the field to a plain
`resample.Stream` makes the detector fire on `capture.go:229`.

### The other nine

- **An abandoned session could outlive its supervisor.** A connect completing
  just after `OpenTimeout` produced a session owned by nobody, still receiving
  and still writing `t.handle`, for up to ten minutes — its stale resumption
  handle could overwrite the live session's newer one. `adopt`/`abandon` now
  hand ownership to exactly one side, and the loser closes the session.
- **A failed send dropped the chunk it was carrying**, while the log said it
  would be retried. The chunk is kept and goes to the replacement session.
- **A direction change with the key forgotten did nothing, in silence.** It
  says so now.
- **Forget Key was not undone by Cancel** — unless an unrelated Apply had also
  happened. The dialog's `dirty` flag is set by both.
- **The pure-Go resampler allocated once per callback**, against the package's
  own rule. Pinned at zero with `testing.AllocsPerRun`.
- **Ring overflow warned at callback rate** — 50 lines a second for as long as
  a stall lasted. One line at the start, one at the end with the cost.
- **`CaptionFontFamilies` was deleted.** It listed font *family names*, which
  is a Qt API shape Fyne does not have — a theme takes a `.ttf` resource, not
  names — so nothing could consume it and nothing called it. F1 had already
  settled that Hangul renders correctly with the bundled font.
- **The caption clock was wall time.** Every buffer deadline is a difference,
  so an NTP step moved all of them at once. `time.Since` from process start is
  monotonic.
- Nits: a 2 MB copy in the netcheck upload, `truncate` splitting a UTF-8 rune,
  the malgo context global now guarded, and Preferences limited to one window.

`go test -race ./...` clean, `go vet` clean, `gofmt` clean, bufdiff 0/30 after
the changes.

## Three-line captions showed one packed row and two blank ones (2026-08-08)

Reported from live use of 1.0.5: with three caption lines configured, the
bottom row often carried the only text and the two rows above it were blank.

**Two of this week's fixes were fighting each other.** The reflow fix packed
rows full — settled sentences join into one paragraph and wrap, so several
short sentences share a row. But the buffer still kept only `max_lines`
*sentences* of history, which was the Python build's rule from when one
sentence was one row. Under reflow, three short sentences are one row, and
there was nothing left to put in the other two.

Reproduced in a test before changing anything: twelve short settled sentences
("Love.", "Lord.", "My soul." — the shape the 67-minute transcript measured,
56 lines of six runes or fewer) into a three-line default-width box:

```
the box shows 1 of its 3 rows: ["Give thanks. Hallelujah. Glory."]
```

The fix moves the row cap to where the rows are made: the buffer keeps
`DisplayHistoryFactor` (8) sentences per display row, and the renderer —
which is the only place wrapping happens — trims to `max_lines` rows after
reflowing. Eight is measured, not guessed: the default layout fits about 50
runes on a row, and the worst measured sentences run about six runes plus a
joining space. The transcript is unaffected either way — it is fed by
`drain_new_commits`, not by the display history.

This is the fifth deliberate deviation from the frozen reference, mirrored in
`bufdiff/deviations.py` (`__post_init__` and `set_max_lines` re-wrap the
deque at the larger bound, `display_lines` stops truncating). bufdiff after
the change: 0/30 divergences; the same seed against `--no-deviations`
diverges, so the mirror is load-bearing, not vacuous. The fuzz invariant
moved with the contract: committed is bounded by the history factor, and the
queue bound is unchanged.

What this does *not* change: clear-on-silence still empties the box after two
quiet seconds, so the first sentence after a pause still starts alone on the
bottom row — that is the clear working, not this defect. A reader who wants
the box to stay full through pauses raises `clear_after_seconds`.

## Fragment inter-arrival, measured at last (2026-08-08)

A two-minute debug recording, 93 gaps. `IdleCommitSeconds` has been waiting on
this since the constant was written.

**Fragments do not arrive at the pace of speech. They arrive on a fixed
one-second server clock.** The timestamps land at X.90–X.93 almost every
second, so the distribution is not continuous — it is quantised:

| gap | count |
|---|---:|
| 600–900 ms | 9 |
| **900–1200 ms** | **73** |
| 1200–1500 ms | 7 |
| 1500–2000 ms | 3 |
| 2000–3000 ms | 1 |

median 1000 ms, p90 1239, p95 1498, max 2003, mean 1039.

The long gaps are not pauses in the speech. Every one of them is a beat that
slipped, and the beat after it lands back on the clock:

```
1252 → 743      1239 → 753      1250 → 751      1246 → 748
```

Each pair sums to ~2000 ms. The rest are whole skipped beats (1992, 1996,
1997, 2003).

**So `IdleCommitSeconds = 1.2` sits in the worst place available**: 240 ms
above the modal 1000 ms, exactly where a late beat lands. Eleven of 93 gaps
(11.8%) crossed it, and seven of those eleven were late beats — a sentence
split for no reason a reader could see. Raising it to about **1.6 s** clears
every late beat while still catching a genuinely skipped one. Above 2.0 s
there is no point: `Tick` force-commits pending text at `ClearAfterSeconds`
anyway.

Not yet changed, because two minutes is a small sample and the constant is
worth setting from a session with real pauses in it. What this recording does
establish beyond doubt is the *shape*: a one-second clock with a ±250 ms
jitter, which is enough to say 1.2 is the wrong number.

Session health, for the record: 0 reconnects, 0 underruns, 0 dropped bytes,
max backlog 330 ms, 1007 chunks sent. The transcript settled 3 of 30 lines
without sentence punctuation (10%), against 30–53% in the 67-minute session —
clear speech rather than singing.

## Captions are left-aligned (2026-08-08)

Centring was inherited from the Python build, where one sentence was one row:
a row was its own unit and centring read fine. Since settled sentences reflow
into shared rows the box carries a paragraph, and a centred paragraph moves
its left edge on every row — the reader has to find where each line starts.
A ragged right edge costs nothing next to that.

Pinned by rendering rather than by reading the field back: a short row and a
long row go into the same box and their first inked column has to match. With
the alignment put back to centre the test reports `x=112` and `x=418`.

The status line keeps its centring — one line, a different kind of message,
not something being read continuously.

## The idle timer, set from the fragment clock (2026-08-08)

A debug session finally measured what nothing had recorded: how fast
translation fragments actually arrive. 93 gaps, from two sessions totalling
about two minutes.

| | ms |
|---|---:|
| min | 699 |
| p25 | 990 |
| **median** | **1000** |
| p75 | 1014 |
| p90 | 1239 |
| p95 | 1498 |
| max | 2003 |

```
   600-900ms    9  #########
  900-1200ms   73  #########################################################################
 1200-1500ms    7  #######
 1500-2000ms    3  ###
 2000-3000ms    1  #
```

**Fragments arrive on a one-second server clock, not at the pace of speech.**
That is the finding. 73 of 93 gaps sit in a 300 ms band around one second, and
the distribution has a hard floor at 699 ms — speech does not produce a shape
like that.

The tail is not the speaker pausing either. Every long gap is followed by a
short one: 1252 then 743, 1239 then 753, 1246 then 760 — each pair summing to
about two seconds. A beat arrives late and the next lands back on the clock.

**So `IdleCommitSeconds = 1.2` sat 240 ms above the normal interval, inside the
jitter.** Eleven of 93 gaps crossed it, and each one settled a sentence the
speaker had not finished. That is the mechanism behind the short lines counted
in the 67-minute transcript — 30% of sermon lines with no sentence
punctuation, 56 of them six runes or fewer.

Raised to **1.6 s**. It clears a late beat with room to spare and still fires
well inside the two-second missed beat. It cannot usefully go higher at the
default settings: `Tick` force-commits pending text at `ClearAfterSeconds`
(2.0) as well, so anything above that is unreachable — pinned by
`TestIdleTimerFitsInsideTheClearWindow`.

Sixth deliberate deviation from the frozen reference, mirrored in
`deviations.py`. bufdiff 0/30 after; putting 1.2 back into the mirror diverges,
so it is load-bearing.

**What this measurement does not cover.** Two minutes, and the speaker barely
paused. It establishes the *clock* — which is the part that was unknown and is
what makes 1.2 s wrong — but it does not show what a real pause looks like on
the far side of 1.6 s. A longer session with genuine silences would confirm
whether 1.6 is also the right ceiling, or whether the clear-on-silence path is
doing that work already.

## The reconnect merge, closed properly (2026-08-08)

The gate added earlier that day judged a settled line by its punctuation. That
left one way through, flagged in the review and now fixed: **the server's
`turn_complete` settles whatever is buffered, whether or not it ends in a full
stop.** "Thank you" is then a finished turn that reads exactly like a phrase cut
off mid-sentence, so the gate resumed it. Reproduced before changing anything:

```
a completed turn was glued to the next session: "Thank you So we begin."
```

The buffer now records *how* each line settled rather than inferring it from
the text. `commit` takes a `finished` argument — true for a sentence end, for
`turn_complete`, and for the whole-utterance policy; false when a timer gave
up — and punctuation is still honoured on top, so a caller that cannot tell
may pass false safely. One flag covers the whole buffer, because `commit`
appends to the queue and the line a reconnect resumes is always the newest
settled one.

The distinction is the settling, not the wording, and a pair of tests says so:
"Thank you" ended by `turn_complete` is left alone, and the same text left
hanging by the idle timer is still rejoined into "Thank you for coming."

`endsASentence` now delegates to `looksFinished`. They were identical line for
line — the review's warning was that the next change to what counts as a
sentence end would land in one and miss the other, and this was that change.

Mirrored in `deviations.py`, which needed the same restructuring: the hint is
set around `feed` and `note_turn_complete` and consumed in a wrapped `_commit`.
bufdiff 0/40 after; putting the punctuation-only gate back into the mirror
diverges on **40 of 40** seeds, so it is thoroughly load-bearing.

## Hearing the translation: what the server actually sends (2026-08-15)

Spikes `v1_voice` and `v2_play`, run before any of "play the translation" was
designed. The application has always counted the model's audio and thrown it
away, so nothing here had ever looked at it.

### The audio is 24 kHz, and it arrives in real time

```
mime:        "audio/pcm;rate=24000"   (every chunk, every run)
90 s of Korean in  ->  93.0 s of English audio out
max backlog: 0.57 s at 1m12s
```

`rate=24000` comes off the MIME type the server stamps on each blob, not from
an assumption: **guessing this wrong plays the translation at the wrong pitch,
which sounds like a fault in the model rather than in us.** Mono, PCM16, so it
is the capture format at a different rate — the same converter runs, backwards.

The backlog number is the one that decides whether the feature is viable at
all. It is what a player would fall behind by if it played every blob the
instant it arrived, and it stays **under 0.6 s over 90 seconds**. A jitter
buffer is enough; nothing has to be dropped or sped up.

**Short clips say the opposite, and they are wrong.** 10–12 second runs
measured 1.25–1.33× — 12 s of Korean returning 15 s of English — which reads
like the translation outrunning the speaker and falling permanently behind. It
is the tail: sending stops, the model finishes the sentence it had already
heard, and three seconds of tail against a ten-second clip is a third of it.
The ratio only means anything once the run is long enough for the tail to stop
dominating. Measure this on 90 s, not on 10.

First audio arrives **3.3–5.2 s** after speech starts (3.26, 3.32, 3.46, 4.28,
5.24 across five runs). That is the fixed cost of hearing a translation, and no
buffering choice changes it.

### The voice cannot be chosen

`LiveConnectConfig.SpeechConfig` exists in the SDK, so the obvious reading is
that a prebuilt voice can be picked. It cannot — not on this model.

```
NoSuchVoiceZZ9   accepted, session ran normally, no error
```

An unknown voice name is not rejected, which is the first sign the field is
being dropped rather than honoured. A server that ignores `SpeechConfig` and
one that honours it both connect and both return speech, so the field was
measured by pitch instead — median F0 over voiced frames, autocorrelation,
60–400 Hz:

```
control (no SpeechConfig)   185, 170, 174 Hz
Charon (a deep voice)       162, 173, 186 Hz
Kore                        195 Hz
Puck                        189 Hz
NoSuchVoiceZZ9              188 Hz
```

**The ranges overlap completely.** A single Charon run at 162 Hz looked like a
real effect until it was repeated: run-to-run variation on the same voice is
larger than the gap between voices.

Do not add a voice control to Preferences on the strength of the SDK type. Add
`SpeechConfig` to the list this preview model ignores, next to batch mode,
caching, tools and structured output. Worth re-measuring when the model
version changes; the spike is checked in for that.

> **Corrected 2026-08-23.** This section originally ended "there is one voice,
> and asking for another changes nothing". The second half holds; the first half
> was wrong, and the evidence for it was sitting in the table above. Run-to-run
> variation on the same voice name is not measurement error — **the model picks
> a voice per session**, which is why the control varied too. See "The voice
> changes at every reconnect" below.

### `echo_target_language` is inert too, and there is a feedback loop

The SDK documents it as: if false, no audio is produced for the target
language. That would have made an open-speaker setup mostly safe — our own
English going back into the microphone would produce nothing.

It does not work that way. English in, `target=en`:

```
echo=false   68 chunks, 816000 bytes, 17.0 s of audio, full transcription
echo=true    65 chunks, 780000 bytes, 16.2 s of audio, full transcription
```

The two are the same run twice. **Target-language input produces a full
re-utterance regardless**, so speakers plus a microphone is a real feedback
loop: our English is heard, translated, spoken, heard again. This is measured,
not feared.

It also poisons two things that read the microphone peak rather than the text:
clear-on-silence never sees a quiet room, and the "no signal" warning never
fires. Headphones are not a nicety for this feature.

### Playback works, and the device rate is not 48 kHz

`v2_play` opens a playback device and feeds it 100 ms at a time, the way the
network will:

```
playback devices on this Mac:
    MacBook Air Speakers      44100 Hz  2ch
    Jump Desktop Microphone   48000 Hz  8ch
  * Jump Desktop Audio        48000 Hz  8ch   (default)

15.0 s of audio played in 15.388 s wall clock
max buffered 112 ms, 0 bytes dropped, 33 starved callbacks
```

Two things fall out of the device list. **The speakers run at 44.1 kHz while
the default device runs at 48** — so 24 kHz has to be converted per device, and
a hardcoded output rate would be wrong on the very first machine tried. And the
system default here is a *virtual* device (remote desktop), not the speakers,
which is exactly why the user has to be able to choose.

The starved callbacks are all at the start: the feeder sleeps 100 ms while the
device asks every 20, so the ring is empty for the first few. A player needs a
short prebuffer before it starts, or it clicks in.

### Building it: three numbers and one wrong constant (2026-08-15)

The design above survived contact, with one exception worth recording because
it was wrong *on its own evidence*.

**`PlayRingMS` was drafted at 500 ms.** The peak backlog had already been
measured at 570 ms, and the backlog is exactly what the playback buffer holds —
so the first number would have dropped audio at the worst moment of every burst,
having been chosen "small enough that the listener is not held further behind".
It is 2000 ms now. In practice it never fills: the live run peaked at 283 ms.

**The prebuffer is worth 32 gaps.** `PlayPrebufferMS` (200) holds playback until
there is a cushion, and re-arms whenever the buffer runs dry, so each utterance
starts with one:

```
hand-rolled spike, no prebuffer      33 starved callbacks
Player, WAV on a 100 ms metronome     1 starved,  16 ms silence, 0 dropped
Player, live API audio, 45 s          1 starved,  18 ms silence, 0 dropped
```

The third line is the one that was not a given. A WAV fed on a tidy tick is a
metronome; live blobs are not, and whether the jitter buffer copes with real
arrival is a different question. It does.

**The output rate is per device, and the default is not the speakers.** Already
noted above, but it bit immediately: this Mac's speakers run at 44.1 kHz, so the
first end-to-end run resampled 24000→44100 rather than the 48000 every earlier
test had used.

Two smaller things the build settled:

- **`Player` needs a mutex where `Capture` needs an atomic.** Capture's
  converter is touched by the device callback, which cannot take a lock and
  which `Uninit` is what waits for — the ordering dance in `Capture.Stop` exists
  for that. The player's converter is touched by `Write`, on the translator's
  receive goroutine, so an ordinary lock retires it safely. What the callback
  *does* touch is published as one immutable `playState` through an
  `atomic.Pointer`, which is M3's answer with a whole struct instead of one
  field.
- **The audio callback is the one place `fyne.Do` is skipped.** Every other
  translator callback hops to the UI goroutine. `OnAudio` must not: it touches
  no widget, and ten blobs a second through the UI thread would make the sound
  hostage to a repaint.

## The voice changes at every reconnect, and so does the silence (2026-08-23)

Spike `v3_voiceid`, prompted by two reports from real use: the spoken
translation changes voice partway through a session, and it drops into silence
for a few seconds — with the voice usually different on the other side of the
silence. The suspicion that these are one fault turns out to be right.

### The 1.2.0 conclusion was half wrong

That study measured `SpeechConfig` by median F0 across whole sessions and
concluded "there is one voice, and asking for another changes nothing". The
second half stands. **The first half does not: there are many voices, and the
model picks one per session.** The control runs varied as much as the treatment
runs did, and that variance was filed as measurement error when it was the
finding. One short session per run cannot tell "the voice changed between runs"
from "the voice changes within a run" — both produce the same number.

### Calibrating the instrument first

Median F0 alone cannot answer "same voice or not", so the fingerprint is median
F0 plus spectral centroid over voiced frames, and the two are combined into a
distance. The scale for that distance comes from a case where the answer is
already known: macOS `say` reading a fixed passage as one voice, and a second
file switching Fred → Kathy → Fred at known times.

```
within one voice          0.14 .. 1.16   (8 windows of 5 s)
at a real voice change    7.76, 10.69    (both boundaries, and no others)
```

More than a factor of six of empty space, so `voiceChangeThreshold` is 3.0, in
the middle of it. The detector found both true boundaries and invented none.
**Do not read any distance below without this calibration in mind** — the first
draft used 1.0 and flagged five "changes" inside a single uninterrupted session.

### What the API actually does

Input is a fixed 103 s macOS-TTS Korean fixture, byte-identical every run, so
nothing about the speaker varies. Windows are 10 s of received audio.

```
run  reconnects  SpeechConfig   f0 across windows       silence
A    none        none           200 .. 224 Hz  (24)     0.00 s
B    every 30 s  none (resume)  163 .. 229 Hz  (65)     ~2.8 s per swap
C    every 30 s  none (fresh)   198 .. 253 Hz  (54)     ~2.8 s per swap
D    every 30 s  voice=Kore     153 .. 253 Hz  (100)    8.31 s over 3 swaps
E    every 30 s  voice=Charon   172 .. 270 Hz  (98)     8.61 s over 3 swaps
```

- **An uninterrupted session is one stable voice and no silence at all.** Run A
  produced no window pair above 2.32, and its longest gap between audio blobs
  was 0.38 s against 0.57 s of buffer. Nothing to hear.
- **Every reconnect changes the voice.** Runs B–E all cross the threshold at
  session swaps. The spread across windows doubles or triples against the
  control.
- **The resumption handle does not preserve it** (B against C). Resumption keeps
  the conversation, not the speaker.
- **`SpeechConfig` is inert, now measured properly.** In run D all four sessions
  were opened with `voice=Kore`; session 1 sat at 153–167 Hz and sessions 2–4 at
  220–252 Hz. Run E asked for Charon, a documented deep voice, and got 270 Hz.
  Same config, different voices, within the same run.

### Where the silence comes from

It is not the network, and no jitter buffer could have covered it. A
replacement session needs **3.3–5.2 s of input before it says anything** — that
number is from the 1.2.0 study and it is the fixed cost of a session, not a
symptom. The application swaps the send target and closes the old session in the
same breath, so for those seconds nothing is producing audio at all. The 0.57 s
of buffer runs out almost immediately.

```
connect takes          243 .. 391 ms      (never the problem)
first audio after that 2.8 .. 4.2 s       (always the problem)
```

The first cut of this measurement tracked a running reserve and reported ~147 s
of silence in a 110 s run, because a reserve that goes negative once stays
negative and every later gap re-reported the whole backlog. A player does not
accumulate a debt: it goes quiet, and starts again from the next blob. The
model is now an `availableUntil` instant, and the numbers above come from it.

### `turn_complete` does not arrive during a continuous stream

The 103 s control run produced exactly one turn. Any analysis that segments by
turn will therefore average a whole session into a single number — which is
part of how the 1.2.0 study missed this. Segment by time.

### Overlapping the swap removes the silence entirely

The old session was closed the moment the replacement connected. But a session
that stops being fed does not stop working — it finishes translating what it
already heard, and that tail is about as long as the replacement's warm-up.
`-overlap` holds the replaced session open until the new one produces audio:

```
                      silence per swap     across 3 swaps
close at the swap     2.8 - 4.2 s          8.3 - 8.6 s
overlap               —                    0.00 s
```

With the overlap on, the swap points do not appear in the gap list at all: the
largest arrival gap anywhere in the run was 0.39 s, indistinguishable from the
uninterrupted control. Shipped in `internal/translator` as retire/watchRetirement,
with `RetireGrace` (12 s) bounding the case where nobody is speaking and the
replacement therefore never says anything.

The voice still changes at the swap — the overlap fixes the hole, not the
speaker. There is nothing client-side left to fix that with.

## The music gate: a pure-Go YAMNet, verified against the reference (2026-08-23)

Spikes Y1 (model provenance + goldens) and y3_gatecal (threshold calibration),
and the `internal/classify` package they gate.

### Getting the model

TF Hub is dead — the tfhub.dev URL and the tfhub-lite-models bucket both answer
403/404 anonymously. Kaggle's model API serves it with **no authentication**,
but the slug is case-sensitive: the `tflite` casing answers 200, the `tfLite`
casing Kaggle's own UI shows answers 404. The bundle is a tar.gz, 3 240 957
bytes, byte-identical across fetches; inside is one file, `1.tflite`,
4 126 810 bytes, sha256 `10c95e…17de` — pinned in `classify.ModelSHA256`.

### What is inside the file

Not what the docs imply. The frontend (framing 400/160, Hann, rfft 512, a
64×257 mel matrix, +0.001, ln) is *in the graph as float ops with constant
tensors*, so the port extracts the window and the mel matrix from the file
rather than re-deriving them — the whole "does my mel formula match" bug class
gone. The MobileNet tower is **int8-quantized, per-channel**, 27 layers; the
head is mean-pool, fc to 521, sigmoid, dequant.

### The port and its proof

Pure Go: a ~200-line flatbuffer reader (hash-pinned to this one file), the
frontend, and a float tower with weights dequantized at load and every
activation snapped to its stored int8 grid. Verified three ways against the
reference interpreter (ai-edge-litert 2.2.0):

- 73 constant tensors byte-identical (CRC32) to what the interpreter sees;
- frontend log-mel within 4.1e-4 of the reference on six synthetic inputs;
- final scores on those inputs: **four of six bit-identical**, worst 0.082 —
  under one logit grid step (0.347) — and every top class agrees.

The quantize clamps never bind on audio: log-mel spans −6.91..+3.5 against a
grid reaching 5.33, full-scale input included. Measured before believing the
snap did anything; it is kept for fidelity, and the mutation that removes it
is recorded as unobservable at tolerance.

**52 ms per patch** (M-series, one core), against a 480 ms decision cadence —
11% of a core. This is why there is no cgo, no TFLite dylib, no ONNX runtime:
the soxr precedent, reconfirmed.

### Where the gate defaults come from (y3_gatecal)

Per-hop family scores, `speech` = max(Speech family), `music` = max(Singing ∪
Music blocks):

```
material                        speech(median)  music(median)
Korean TTS fixture, 103 s        0.992           0.000  (max 0.004)
synth band (chords+bass+hats)    0.000           0.996
speech mixed over it  x0.5       0.004           0.891
                      x1.0       0.020           0.801
                      x2.0       0.109           0.500
                      x4.0       0.586           0.199
piano transition (real, Apple)   0.004           0.668  (p25 0.414)
bugle (real, Apple)              0.000           0.891
```

Threshold **0.5** is the x2.0 crossover: a speaker at least twice as loud as
the backing music keeps translating; music-dominant audio gates. The 0.05
margin and the 2.0 s hold come off the same table. Quiet, sparse music (the
piano's decay tails, p25 0.414) will flap the gate open — deliberately the
conservative direction: it leaks translation, it never eats speech.

Known limit, stated rather than hidden: **sung vocals were not measured** —
there is no synthesizable singing fixture. The live classifier test in
Preferences exists exactly so the defaults can be checked against the user's
real material.

### Y4 — the gate against the live API, end to end (2026-08-23)

Spike `y4_gate_live`: a real translator session with the real gate wired in,
fed 20 s of the Korean fixture, 25 s of synth music, 20 s more speech, at
real-time pace.

```
phase      chunks fed   chunks sent   caption bytes
speech #1      200          199            297
music          250           31             11
speech #2      200          205            271

gate closed during music: yes; gated 23.0 s, flushed back 1.5 s
max_backlog: 200 ms  (the filter did not stall the send loop)
```

The 31 music chunks that got through are the 3.1 s before the gate closed —
the 2.0 s hold plus ~1 s of patch fill, exactly as designed, and the reason
the hold exists. Speech #2's 205 > 200 is the pre-roll flush arriving on top.
Captions stopped for the music and resumed with the speech, mid-sentence
recognisable from the fixture script.

## Why the gate flapped, and what stopped it (y5_flap, 2026-08-30)

Reported from use: the filter works, but music leaks through in bursts. Y3
predicted this and called it the safe direction — "quiet, sparse music will
flap the gate open ... it leaks translation, it never eats speech". It is not
the safe direction. Measured, the 1.3.0 policy leaks *everything*.

### The failure mode

The gate opened on the **first** clear frame. Inside real music a single
sub-threshold frame is the commonest event there is. Ten pieces, per-hop music
scores, and the run-lengths of consecutive sub-threshold frames inside each:

```
material                     frames  musicP50  clear-runs inside      top classes
Piano Strings Transition 1       32     0.668  4x1 1x3                Gong, Timpani, Music
Bell Church                      38     0.738  3x1 2x2 1x4            Bell, Church bell
Clock Tower Chimes               41     0.414  3x2 1x3 1x14           Music, Tubular bells
Marching Band                    78     0.109  3x1 1x2 1x3 1x4 1x16 1x26   Music, Speech, Percussion
Orchestra Warm Up               177     0.586  7x1 6x2 1x3 2x5 1x7 2x9 1x11  Music, Speech, Orchestra
Bugle Taps                       75     0.891  2x1 2x2                Music, Trumpet
Drone Bells 01                   74     0.891  1x1 1x10               Music, Ambient music
Chimes 1                         16     0.500  2x1 1x4                Music, Chime
```

Runs of **one frame are the mode in every piece**. Each one used to reopen the
gate *and* flush the 1.5 s pre-roll, so one misread sent about 3.5 s of song
to be translated, and it repeated whenever the music thinned out.

Two incidental findings worth keeping. Real acoustic music recorded in a room
fires **Speech** strongly as well (Marching Band 30/78 frames, Orchestra Warm
Up 81/177) — the `music >= speech + Margin` term is doing real work. And a
marching band recorded outdoors medians 0.109: diffuse, low-confidence
everywhere. The gate will not close on material like that at any threshold
this side of useless, which is a limit to state rather than to tune away.

### The sweep

Timeline: speech 20 s / gap / music 30 s / gap / speech 20 s, ground truth from
the construction, leaked = music sent, clipped = speech suppressed and never
flushed. Identical at gaps of 0, 0.5 and 1.0 s:

```
policy                                  leaked    clipped   spurious opens
1.3.0: open on one clear frame        24 000 ms      0 ms        4
hysteresis 0.15 only                  14 000 ms      0 ms        2
release 1.0 s                         19 500 ms      0 ms        2
release 1.5 s                          5 500 ms      0 ms        1
release 2.0 s                          5 000 ms      0 ms        1
release 3.0 s                          4 000 ms      0 ms        1
hysteresis 0.15 + release 1.5 s        5 500 ms      0 ms        1
```

**A release hold is the whole fix, and hysteresis earns nothing on top of it** —
the last two rows are identical. A Schmitt band was drafted and is not shipped,
for the same reason the chime's attack envelope is not: it was measured and it
changed nothing. `ReleaseSeconds` defaults to **1.5**, the knee.

Why waiting is free is the user's own observation: a song and the speech after
it are never butt-joined, and the countdown runs during the pause. The pre-roll
covers the case where there is no pause at all:

```
pre-roll at release 1.5 s      leaked    clipped   (gap 0 s — the adversarial case)
10 chunks (1.0 s)            3 400 ms    900 ms
15 chunks (1.5 s)            3 400 ms    400 ms
20 chunks (2.0 s)            3 500 ms      0 ms
40 chunks (4.0 s)            5 500 ms      0 ms
```

**20 chunks is the knee** and it is also what the arithmetic says: hold back
exactly the countdown, 4 frames x 480 ms. `prerollFor` derives it from the
release hold rather than fixing it, so raising "Reopen after" in Preferences
grows the pre-roll with it instead of quietly starting to eat words. Bigger is
strictly worse — every extra chunk is music flushed back in.

### What the shipped gate does

`TestTheShippedGateDoesNotLeakOnMisreads` runs a real timeline through the real
`classify.Gate`. The synthetic music needed care: quiet bars move the
classifier not at all (amplitude is not what it reads, 0.80 either way), and
the first version of the test passed unchanged under the old policy. 900 ms of
silence every 3 s reproduces the real failure mode — one isolated frame at
**0.41**, within 0.004 of the piano's measured p25.

```
                       music fed   leaked      speech eaten
1.3.0 policy            18 000 ms  18 000 ms      0 ms      <- all of it
1.4.0 release hold      18 000 ms   3 000 ms      0 ms      <- the close latency
```

### Sung vocals: still not measured

Pitch-modulated `say` was tried as a singing fixture and is not one. Both a
Korean and an English "sung" line came back top-classed **Speech** (music p50
0.059 and 0.000). That is a true result about pitched speech — YAMNet needs
musical structure, not just a moving f0 — but it is not a measurement of
singing, and the gap Y3 recorded stays open. Real sung vocals still have to be
checked with the live classifier test in Preferences → Filter.

## The voice still cannot be pinned (v3_voiceid re-run, 2026-08-30)

Re-measured because the switching was reported as still happening. Three
sessions in one run, every one opened with `speech_config` prebuilt voice
`"Charon"`, fingerprinted in 8 s windows:

```
window          at    voiced  f0_median  centroid   dist
s01_t01_w01    0.0s     60%      228.6      1196    0.00
s02_t01_w01   24.0s     52%      154.8      1034    3.97   <- session swap
s03_t01_w01   48.0s     61%      233.0      1251    8.35   <- session swap
f0 across windows: 144.6 .. 233.0 Hz
```

Asking for a voice changes nothing; the model draws a new one per session and
a reconnect is not optional. The 1.2.0 conclusion stands, now with a second
independent run behind it. Two qualifications on the instrument, both against
over-reading it: within-*session* windows on this material moved by up to 2.84
and 4.04, above the 3.0 threshold calibrated against macOS `say`, so the
"changed mid-session" flags are not trustworthy — only the swap-aligned jumps
are. And the swap silence stayed fixed: **0.04 s** total attributable to
arrival across two swaps, against the 2.8–4.2 s of 1.2.0.

The consequence is a design decision, not a workaround: a voice the user picks
and keeps has to be synthesized locally. See "Choosing the voice" in CLAUDE.md.

## What the model's audio actually sounds like underneath (2026-08-30)

Reported: an electronic quality in the background of the spoken translation,
with a request not to add signal processing for it. The three session
recordings above were analysed for tonal artefacts in their quiet frames
(2048-point Hann, quiet = the bottom quartile of frame RMS):

```
session   frame dB p25   quiet-frame tonal peaks           sub-60 Hz
s01          -34.7       23.4 Hz +17.6 dB, 187.5 Hz +12.9   +15.5 dB
s02          -51.5       58.6 Hz +48.5 dB, then 246/363/421/480/527 Hz   +37.7 dB
s03         -200.0       none — the pauses are digital silence   +0.0 dB
```

Three sessions of the same model on the same input produced three different
noise floors, one of them exact zeros. **The artefact is in the generated
audio, not in the playback path** — which is a pure-Go Kaiser sinc at −91 dB
stopband (soxr on macOS), with starvation counted rather than guessed at, and
the arrival analysis above showing 0.04 s of gap in 66 s. There is nothing to
fix downstream, and a filter would be treating a symptom that varies per
session anyway.

The answer is the same as the voice's: speaking the translation with a local
voice takes the model's audio out of the output path entirely. One change,
both complaints.

**Caveat on the diagnosis**: the sessions were fed the Korean TTS fixture, not
a live room, and "quiet frames" are selected by energy, so some of what is
measured there is speech tail rather than background. What the numbers support
is "the model's own audio differs session to session and is not always clean";
they do not identify a specific tone to point at.
