# LiveCap

Real-time speech translation captions for macOS and Windows.

Speak Korean, see English captions on a full-screen display — or flip the
direction and speak English to see Korean. Audio comes in through any CoreAudio
input device, typically a USB interface fed from a mixer.

- **Engine** — Google Gemini Live API, `gemini-3.5-live-translate-preview`
- **UI** — Go with Fyne, native macOS menu bar, no Electron
- **Latency** — 3.1 s to the first caption Korean → English, 3.8 s English → Korean, then about one fragment per second
- **Endurance** — verified for 30 minutes unbroken across 3 automatic reconnects

See [INSTALL.md](INSTALL.md) to set it up on a new machine.

## Quick start

- Run `./run.sh`
- Open **Preferences… (⌘,) → API Key**, paste your Gemini API key, press **Test Key**
- A paid-tier key is required — see [INSTALL.md](INSTALL.md)
- Press `0` to start

## Preferences

**⌘,** opens the one place everything is configured. There is no `.env` file
and no config file to hand-edit.

| Tab | What it holds |
|---|---|
| API Key | The Gemini key, **Test Key**, and where the key in force came from |
| Audio | Input device, and a **live level meter** to check it |
| Network | **Run Speed Test** — latency to the API, upload and download speed |
| Vocabulary | Terms that bias speech recognition, for names and jargon the model mishears |
| Appearance | Background, box and text colours; position, size, lines, margins |
| Behaviour | Target language, transcript, caption timing, silence gate, verbose logging |

**Apply** applies the changes and keeps the window open, **OK** applies and
closes, and **Cancel** puts back whatever was in force when the window opened —
including undoing an Apply you have already pressed. Anything a live session
needs is restarted in both directions, so cancelling a target-language change
reconnects back to the original language.

> Cancel restores the encrypted key store as it found it. A key that Apply also
> copied into the Keychain stays there — there is no way to tell a fresh copy
> from one that was already present.

### The API key

Paste it and press **Test Key**. That lists the models the key can reach —
free, and it never opens a billed session — so a wrong key, a project without
billing, or a network problem is reported before you rely on it.

The key is encrypted and written to
`~/Library/Application Support/LiveCap/credentials.enc`, mode 0600.

> The passphrase is part of the application, so anyone holding a copy can
> decrypt the file. Treat it as keeping the key out of plain sight — out of
> backups, screen shares and stray `cat` commands — rather than as real
> secrecy. Tick **Also store in the macOS Keychain** for storage your login
> password protects. A `GEMINI_API_KEY` environment variable still wins over
> both.

### The level meter

**Audio → Start Level Test** opens the selected device and shows what it is
actually delivering, with a plain reading — `-12 dBFS — good`, `-88 dBFS —
silent, check the input`. It answers "is this device even connected?" without
starting a translation session or spending anything.

### The speed test

**Network → Run Speed Test** measures the three things that decide whether a
session will hold up, and says so in one line — `Good — plenty of headroom`,
`Too slow — 120 kbit/s up cannot carry the 256 kbit/s of audio a session needs`.

| Measurement | Why it matters |
|---|---|
| Latency to `generativelanguage.googleapis.com` | Sets how far the captions trail the speaker |
| Jitter | What actually produces stalls — a steady 200 ms is easier to watch than 80 ms that spikes to 400 |
| Upload | Carries the audio: **256 kbit/s** continuously, 16 kHz × 16-bit mono |
| Download | Only the transcription fragments come back, so this is never the limit |

- Latency is timed against the API host itself, with TCP handshakes — free, no session is opened and no key is needed
- Throughput uses the public `speed.cloudflare.com` endpoints; the payload is zeros, so no audio, transcript or key material leaves the machine
- Runs on its own thread, so the captions never stall while it measures

### Vocabulary hints

**Preferences → Vocabulary** biases speech recognition towards names and jargon
the model would otherwise mishear or spell inconsistently, one term per line.

| Field | What it is for |
|---|---|
| Source terms | How a name or term is **spoken**, in the language you're speaking — a Korean name or technical term the model keeps mangling. Because the caption comes from what the model heard, fixing this is what actually improves the translation |
| Caption terms | How a name should **appear in the captions**, in the target language — for a name that is heard correctly but spelled inconsistently between sentences |

- Sent as the Live API's `custom_vocabulary` on the input and output transcriptions, respectively
- Up to 200 terms per list; they ride in the setup message of every session and every reconnect, so a long list slows each one
- Fixed when a session connects, like the target language — changing the list while translating reconnects the translator, audio and the transcript keep going
- Whether the preview model actually honours these hints has not been measured — the API accepts them, which is not the same thing

## Keyboard shortcuts

| Key | Action |
|---|---|
| `0` | Toggle translation on / off |
| `1` | Korean → English |
| `2` | English → Korean |
| `9` | Complete phrases only, on / off |
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
- Examples: `API key required — enter it in Preferences`, `No audio input device found`

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

Preferences behave the same way. Colours, sizes and the clear delay apply as
soon as you click OK; the target language and the silence gate reopen the
translator session, and changing the audio device restarts capture. Nothing
waits for a relaunch.

## Quitting mid-session

Closing the window, ⌘Q, Ctrl-C and `SIGTERM` all run the same shutdown, so a
live session is never left half-closed. It works the same in both directions.

- The Live API session closes and the audio device is released
- The last unfinished sentence is settled and written to the transcript
- The transcript gets its footer — end time, duration, line count, reconnects
- Settings are saved

A failure in any one step does not stop the others: if the translator refuses
to shut down, the microphone is still released and the transcript still closes.

## Caption box

- Fixed size and position by default, sized for **2 lines**, and it stays on screen even when empty
- Text is bottom-aligned, so the newest line never moves as lines accumulate
- Default width is **90%** of the window (5% margin each side), adjustable from 60–100%
- Turn off **Settings → Always Show Caption Box** to make the box hug its content and disappear when empty

### Position

**Settings → Caption Position** moves the box vertically — useful when the
lower part of the screen is covered by something else.

| Choice | Where the box sits |
|---|---|
| Top | Anchored below the top edge |
| Middle | Centred vertically |
| Bottom | Anchored above the bottom edge (default) |

The bottom-margin setting becomes the **top** margin when anchored to the top,
and is ignored when centred. The direction hint and the status dot always stay
just below the box, wherever it is.

| Setting | Default | Range |
|---|---|---|
| Background colour | `#000000` | any |
| Box colour | `#1B4965` | any |
| Box opacity | 100% | 60–100% |
| Text colour | `#FFFFFF` | any |
| Corner radius | 24 px | 0–48 |
| Box max width | 90% | 60–100% |
| Bottom margin | 8% | 2–20% |
| Caption position | Bottom | Top / Middle / Bottom |
| Font size | 44 px | 24–96 |
| Max lines | 2 | 1–4 |
| Hold each line | 0.7 s | 0–3 s |
| Clear after silence | 2 s | 0–10 s |

Unconfirmed text is drawn at 55% alpha so you can see a phrase being revised
before it settles.

## Caption timing

The model translates a whole utterance at a time, so several sentences can settle
at once. Two settings control what happens then.

### Holding each line

Settled lines are queued and released **one per 0.7 s**, so a burst is read out
rather than scrolling past between two repaints. Measured on a five-sentence
burst: 2 of the 5 lines used to reach the screen, now 4 do.

- No more than `max_lines` lines are ever held back, so the captions trail the audio by at most **1.4 s** at the defaults — pacing smooths a burst, it never accumulates
- A burst longer than that still loses its oldest line from the screen; two lines cannot show five sentences. The transcript keeps every one of them regardless
- `0` shows everything the instant it settles, which is the old behaviour

### Complete phrases only

The model streams its translation a fragment at a time and rewrites as it goes,
so by default the caption visibly churns while it works — the unsettled line is
drawn dimmed and keeps changing under the reader.

**Settings → Complete Phrases Only**, or the **`9`** key, withholds a phrase
until it has settled. Nothing half-written is ever drawn.

A phrase settles at whichever of these comes first:

| Trigger | Notes |
|---|---|
| A sentence ending in `.` `?` `!` | Needs at least 20 characters behind it, so `Dr.` and `vs.` do not settle a two-word fragment |
| `turn_complete` from the server | The only end-of-turn signal the API reports reliably — `Transcription.finished` exists in the SDK but does not arrive in practice |
| 1.2 s without a new fragment | The fallback, so a short utterance is never stranded by the length guard |

Steadier to read, at the cost of a sentence arriving a beat later. Off by
default. The transcript is unaffected either way — it is written from a separate
path.

### Clearing on silence

Captions clear **2 seconds** after **both** of these hold:

- the queue has drained — everything received has been shown
- the input has been quiet, judged from the actual audio level, not from fragment arrival

The audio condition is what stops captions vanishing under a speaker mid-sentence:
the model can go quiet for several seconds while it works, and fragment arrival
alone cannot tell that apart from a finished sentence.

- Adjustable from 0–10 s in Preferences; `0` keeps captions on screen indefinitely
- Captions never linger more than **15 s** past the last fragment whatever the input is doing, so a noisy room cannot pin stale text on screen
- Speech is anything above **-26 dBFS**, well clear of the -36 a quiet room reads on a built-in mic

## Audio input

Pick a device under **Settings → Audio Input**.

- The list is rescanned every time you open the menu, so a USB interface can be unplugged and reconnected
- Devices are stored **by name**, not index — indexes change across reconnects
- If a stored device is missing, LiveCap falls back to the system default and says so
- Set your mixer peak near **-12 dBFS**; the app applies no software gain
- Any sample rate and channel count works — the device is opened at its own rate and downmixed to mono

If the chosen device delivers nothing for 12 seconds, LiveCap logs
`no signal from <device>` to stderr — a misrouted input opens and streams
silence rather than failing, so a dead device otherwise looks exactly like a
quiet room. Deliberately log-only: a silent input is often intentional, and
nothing is allowed to cover the captions. Levels for reference: a quiet room
on a built-in mic reads about **-36 dBFS**, while an input with nothing routed
into it reads about **-88**.

## Display sleep

While translating, LiveCap holds a `NoDisplaySleepAssertion`, so the screen
stays lit on battery as well as on mains — you are watching captions, not
typing, so the idle timer would otherwise blank them. The assertion is
released the moment translation stops, and it does not override closing the
lid or locking the screen yourself.

Internally the device is opened at its native rate (usually 48 kHz) and
resampled to 16 kHz with `soxr`, never by asking CoreAudio for 16 kHz directly.

## Transcripts

Each session writes a Markdown file named after its start time —
`2026-07-24_143052.md`, in `~/Documents/LiveCap` by default.

```markdown
# LiveCap transcript

- **Started** 2026-07-24 14:30:52
- **Target language** en
- **Model** gemini-3.5-live-translate-preview

---

`00:03` Good morning everyone.

`00:07` Revenue is up 14% year over year.

> _reconnected at 10:02, gap 842 ms_
```

- Every line is flushed immediately, so a crash cannot lose what was already written
- Reconnects and direction switches are marked inline
- A session that produced no captions deletes its own file on exit
- Toggle with **Translate → Save Transcript**; open the folder with **Translate → Open Transcript Folder**

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

Go. Three goroutine domains, and crossing between them is only ever done in the
ways listed here.

| Goroutine | Responsibility |
|---|---|
| miniaudio device callback | Downmix to mono, resample to 16 kHz, write to the ring buffer. No allocation, no locks, no UI. |
| translator (supervisor, sender, one receiver per session) | Send 100 ms chunks to the Live API, receive transcriptions, manage reconnects. Reaches the UI only through `fyne.Do`. |
| Fyne UI | Every widget, the caption buffer, the transcript. Never touched from the other two. |

```
src_go/
├── cmd/livecap/main.go      flags, --selftest, wiring, signal handling
└── internal/
    ├── audio/               device enumeration, capture, the ring buffer
    ├── captions/            fragments → display lines (no UI, no network)
    ├── config/              settings schema, JSON persistence, API-key lookup
    ├── credentials/         the encrypted store (reads the Python build's file)
    ├── netcheck/            latency and throughput against what a session needs
    ├── platform/            the only package that branches on the OS
    ├── resample/            libsoxr on macOS, a pure-Go filter elsewhere
    ├── transcript/          the Markdown transcript
    ├── translator/          the Live API: supervisor, sender, receivers
    └── ui/                  caption view, menus, Preferences, the window
```

The Python implementation that this replaces is preserved in `src/` as an
executable specification. It is frozen: no feature or fix lands there.

### Session endurance

An audio session lasts about 15 minutes and the WebSocket about 10, so lasting
30 minutes needs all three of:

- `ContextWindowCompression` with a sliding window, to extend the session itself
- `SessionResumption`, keeping the latest handle for the next connection
- Opening a replacement session **before** closing the old one when `go_away` arrives

Measured over 30 minutes: `go_away` arrived 3 times at 9-minute intervals, each
swap took 367–471 ms, and sentence loss scored 360/360 against the script —
the same numbers the Python implementation recorded.

## Development

```bash
cd ~/work/live-translation/src_go
go test ./...          # the whole suite, offline
go test -race ./...    # the same, with the race detector
go vet ./... && gofmt -l .
```

- The whole suite runs offline — no test needs an API key or network access
- Tests are isolated from both places the app persists things: the encrypted key store and the settings file. Without that, a run would read and overwrite the key and the settings actually in use
- `internal/captions` has no UI or network dependency and is covered by a fuzz test that drives every entry point together — 20,000 randomised steps per mode
- Reconnect logic is tested against a fake Live session: go_away swaps, resumption handles, fatal versus retryable errors, send failures
- The menu is tested by driving each item's own action: no dead entries, ticks that both drive and follow their setting, exclusive groups
- Preferences is tested as a cycle, including that Cancel after an Apply restores the settings **and** the encrypted key store
- The Windows and Linux branches of `internal/platform` are pinned by faking the OS

Measurement scripts live in `src_go/spikes/`, and their findings are recorded in
[src_go/spikes/RESULTS.md](src_go/spikes/RESULTS.md).

| Script | Purpose |
|---|---|
| `f1_text` | Korean rendering, headless, writes a PNG |
| `f2_menus` | The menu bar, keys and full screen — needs a human |
| `a1_capture` | Capture accuracy, callback timing, input level |
| `r1_resample` | libsoxr against the pure-Go filter |
| `l1_live` | Shortest end-to-end proof against the Live API |
| `g3_endurance` | 30-minute run: reconnects, gaps, backlog, sentence loss |
| `g4_transcript_golden` | Transcript bytes against the Python writer |
| `g5_direction` | A live mid-session direction switch |

## Distribution

```bash
cd ~/work/live-translation/src_go
./packaging/build_app.sh        # dist/LiveCap.app
./packaging/build_windows.sh    # dist/windows/LiveCap-1.0.0-x64.msi
```

- The `.app` is ~28 MB arm64 and carries its own libsoxr; the receiving Mac needs nothing installed
- `Info.plist` sets `NSMicrophoneUsageDescription`, without which macOS kills the app instead of prompting for the microphone
- Verify any build with `dist/LiveCap.app/Contents/MacOS/LiveCap --selftest`
- Add `--sign` to codesign for other machines — see [INSTALL.md](INSTALL.md)
- The Windows installer cross-builds from macOS with `brew install mingw-w64 msitools`. It is one self-contained `.exe`, importing Windows system DLLs only, and the build fails if that ever stops being true

**The Windows build has never been run on Windows.** It cross-builds and the
MSI is structurally correct; nothing more than that is claimed.

## Author

Pyeongwoo Lee — <dennis2.lee@gmail.com>

Also shown in the app under **LiveCap → About LiveCap**.

## License

Copyright © 2026 Pyeongwoo Lee. All rights reserved.

Proprietary — this is not open-source software. No permission is granted to
use, copy, modify, or distribute it without prior written permission from the
copyright holder. See [LICENSE](LICENSE).
