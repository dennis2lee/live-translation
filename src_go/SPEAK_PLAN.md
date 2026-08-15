# Hearing the translation — design

Play the model's translated speech through a chosen output device, off by
default. Written 2026-08-15, after the measurements in
`spikes/RESULTS.md` §"Hearing the translation". Nothing below is guessed; where
a number appears it was measured, and the spike that measured it is checked in.

---

## 1. The three findings that shape this

**The audio is already there, and already paid for.** The model has been
returning translated speech since 1.0.0. `genaiSession.Receive` counts the
bytes and drops them, because failing to read them stalls the stream. So this
feature adds **no API cost and no extra request** — the same session, the same
bill, one more consumer of a stream we already drain.

**It arrives in real time.** 90 s of Korean returned 93.0 s of English with a
peak backlog of **0.57 s**. A short jitter buffer is the whole problem; nothing
has to be dropped, sped up, or resynchronised. Beware the 10-second version of
this measurement, which says 1.3× and is an artefact of the tail.

**The voice cannot be chosen.** This is the one part of the request that cannot
be built — see §3.

---

## 2. What the user asked for, and what each part costs

| Asked for | Status |
|---|---|
| Play the translation as audio | **Straightforward.** The bytes are in hand; a player is ~200 lines. |
| Choose the output device in Preferences | **Straightforward.** Playback devices enumerate exactly like capture devices. |
| Test the output device from Preferences | **Straightforward**, mirroring the input level test. §7.2. |
| A volume slider | **Straightforward** — a gain in a loop that already exists. §6. |
| Cancel / Apply / OK behave properly | **Already solved** by the existing revert; the new controls only have to join it. §7.3. |
| Choose a voice model in Preferences | **Not possible on this model.** §3. |
| Turn the whole feature on and off | **Straightforward**, and it must default to *off*. §5. |

Decided against: a menu item and a number key for muting. Preferences is the
only entry point.

---

## 3. There is no voice to choose

`genai.LiveConnectConfig.SpeechConfig` exists, and reading the SDK it looks
like a prebuilt voice is one field away. It is not, on
`gemini-3.5-live-translate-preview`.

Two independent checks:

- `NoSuchVoiceZZ9` is **accepted without error**. A validated field rejects a
  name that cannot exist.
- Median pitch is unchanged. Control 185/170/174 Hz; Charon — documented as a
  deep voice — 162/173/186 Hz; Kore 195; Puck 189. **The ranges overlap.** A
  first Charon run at 162 Hz looked like a real effect until it was repeated.

**Recommendation: ship no voice control.** A dropdown that changes nothing is
worse than its absence — the user would hear no difference and conclude the
audio is broken rather than the setting. `SpeechConfig` joins the list of
things this preview model ignores, alongside batch mode, caching, tools and
structured output.

The spike stays checked in so this is one command to re-test when the model
version moves:

```bash
go run ./spikes/v1_voice -voice Charon -out /tmp/charon.wav
```

`echo_target_language` turns out to be inert in the same way — `true` and
`false` produce the same run twice. That matters for safety, not for voices,
and it is why §5 exists.

---

## 4. Design

### 4.1 Where the code goes

Playback goes in **`internal/audio`**, beside capture, not in a new package.
The deciding reason is `acquireContext`: malgo contexts are expensive and the
process has exactly one, guarded by `contextMu` and released once from
shutdown. A second package would either duplicate that or export it, and
exporting it invites a second context.

The package doc currently opens "Package audio is the capture pipeline" and
must be rewritten — it is the audio pipeline, both directions.

```
internal/audio/devices.go   + OutputDevice, ListOutputDevices, ResolveOutputDevice
internal/audio/player.go    + Player  (new)
internal/audio/playring.go  + playRing (new)
```

### 4.2 The path the bytes take

```
genaiSession.Receive        part.InlineData.Data, 24 kHz PCM16 mono
  -> Message.Audio []byte   (copied — see 4.3)
  -> Callbacks.OnAudio      translator's receive goroutine
  -> Player.Write           converts 24 kHz -> device rate, appends to playRing
  -> device callback        drains playRing into the device buffer
```

Four small edits carry it:

- `Message` gains `Audio []byte`, replacing the `AudioBytes int` counter — the
  length is still available and the stats line keeps working.
- `Callbacks` gains `OnAudio func([]byte)`, with the same nil-guarded wrapper
  as its five siblings.
- The receive loop's step 6 stops being a comment about discarding and starts
  calling `t.cb.audio(msg.Audio)`.
- `window.go` constructs a `Player` when the setting is on and points
  `OnAudio` at it.

### 4.3 The bytes must be copied

`part.InlineData.Data` is owned by the SDK. Whether it reuses that buffer
across messages is not documented, and the failure it would cause — audio that
is subtly wrong only under load — is the kind that takes a week to find. The
adapter copies once, in `Receive`, where every other normalisation already
happens.

### 4.4 The player

```go
type Player struct {
    deviceName string
    ring       *playRing
    resampler  atomic.Pointer[resample.Stream]   // exactly as Capture
    ...
}

func NewPlayer(deviceName string) *Player
func (p *Player) Start() (OutputDevice, error)
func (p *Player) Write(pcm24k []byte)            // network goroutine
func (p *Player) Stop()
func (p *Player) Starved() int64                 // atomic counters, for the log
```

Three rules carry over from capture unchanged, and they are not optional:

1. **The device is opened at its own rate**, and the audio is converted to meet
   it. Never ask CoreAudio to resample. This is the capture rule read
   backwards, and the device list proves it is load-bearing: the speakers on
   this Mac run at 44.1 kHz while the default device runs at 48. A hardcoded
   output rate is wrong on the first machine tried.
2. **The device callback allocates nothing, takes no UI lock, and touches no
   Fyne object.** Scratch buffers are sized at `Start`.
3. **`Stop` retires the resampler before `device.Stop()`**, because `Uninit` is
   what waits for the callback to return. This is bug M3 exactly; writing the
   player without it reintroduces a race the project has already paid for.

### 4.5 The ring, and why a starved player is different

The capture ring drops the oldest audio when the consumer stalls. The playback
ring does the same — in live translation the newest speech is the useful
speech — but it differs in one way that matters:

**A starved player must emit silence, not repeat its last block.** Silence is
correct and inaudible; a repeated block is a buzz. `Read` zero-fills the
remainder and counts the starvation rather than logging it, because from inside
one callback a momentary gap and a permanent deficit look identical.

Size: **2 s at the device rate** (`PlayRingMS`).

The first draft of this said 500 ms, "small enough that the listener is not
held further behind". That number was wrong on its own evidence: the peak
backlog was *measured* at 570 ms, and the backlog is precisely what this buffer
holds, so a 500 ms ring would have dropped audio mid-sentence at the worst
moment of every burst. Two seconds gives the measurement room to grow over a
session longer than the 90 s it was taken from, and still caps how stale
anything on its way out can be. In practice it never fills: the live run peaked
at 283 ms.

**A prebuffer is needed before the first sound.** V2 measured 33 starved
callbacks, all at the start: the feeder delivers every 100 ms while the device
asks every 20. Hold ~200 ms before starting the device, or it clicks in.

### 4.6 Lifecycle

- **Start/stop** follows the translator, not the window. Speech with no session
  is nothing to play.
- **Reconnect** (every ~9 minutes) needs no special handling. Blobs stop and
  restart; the ring drains to silence and refills. Do **not** clear it — the
  tail is real speech the user has not heard yet, and the 500 ms bound already
  stops anything stale surviving.
- **Shutdown** goes through the one existing exit path, individually guarded,
  so a player that will not stop still leaves the capture device released and
  the transcript closed.
- **Device failure is never fatal.** A missing or unopenable output device
  stops the audio and logs it; captions carry on. Report it in the caption area
  — no modal dialogs while translating.

---

## 5. The feedback loop, and why this defaults to off

Measured, not feared. English in with `target=en` produced **17.0 s of English
audio and a full transcription**, with `echo_target_language` false *and* true.
So speakers plus a microphone is a closed loop: our English is heard,
translated, spoken, heard again.

It also poisons two things that read the microphone peak rather than the text:

- **clear-on-silence never fires** — the room is never quiet, so captions never
  clear.
- **the "no signal" warning never fires** — the input always looks live.

Consequences for the design:

- `SpeakTranslation` defaults to **false**, and a fresh install is unaffected.
- The Preferences control carries the reason in one line, not a manual: *"Use
  headphones. Speakers feed the translation back into the microphone."*
- Nothing tries to detect headphones. macOS will not tell us reliably, and a
  wrong guess that silently disables the feature is worse than a plain warning.

---

## 6. Settings

```go
// Speak the translation aloud. Off by default: with speakers rather than
// headphones the output returns through the microphone and is translated
// again (spikes/RESULTS.md).
SpeakTranslation bool   `json:"speak_translation"`

// Empty means the system default output. Stored by name, never index —
// indexes move when a USB device is reconnected.
OutputDeviceName string `json:"output_device_name"`

// SpeechVolume scales the translated speech, 0.0 to 1.0. Its own control
// because the translation competes with a live speaker in the same room,
// and the system slider moves everything else with it.
SpeechVolume float64 `json:"speech_volume"`
```

Default `1.0`, and `Clamped()` gains one line: `clamp(s.SpeechVolume, 0, 1)`.

The trap this walks past is worth naming, because it is invisible and it would
mute every existing install. An upgraded settings file has no `speech_volume`
key; a loader that unmarshalled into a zero value would read 0.0, clamp it to a
perfectly legal 0.0, and produce silence that looks like a broken feature
rather than a wrong setting. **Checked, not assumed** — `config.Load` starts
from `Default()` and unmarshals into it, so an absent key keeps its default
(`store.go:36`, and the comment there says exactly this). A new field with a
non-zero default is safe here. A test pins it anyway: settings JSON with no
`speech_volume` must load as 1.0.

The other two need no clamping case: a bool cannot be out of range, and an
unknown device name already falls back to the default at resolve time, exactly
as `AudioDeviceName` does.

**Gain is applied in the conversion loop, not by reopening the device.** The
loop that turns float samples into interleaved PCM16 already multiplies by
32767; the volume is one more factor. It is read from an `atomic.Uint64`
(float bits, as `Capture.peak` already does), so moving the slider takes effect
on the next block with no gap in the audio and no lock in the callback.

---

## 7. Preferences

### 7.1 The Audio tab

It gains an output group below the existing input controls, built the same way
so the tab reads as one thing rather than two bolted together:

```
Input device        [ MacBook Air Microphone          v ]
Input level         [======                            ]  -36 dBFS
                    Not running.
[ Start Level Test ]
Aim for peaks around -12 dBFS. ...
[x] Skip sending silence (saves cost)

--------------------------------------------------------

[ ] Speak the translation aloud
    Use headphones. Through speakers the translation returns
    to the microphone and is translated again.
Output device       [ MacBook Air Speakers            v ]
Volume              [--------------o------]  80%
[ Play Test Tone ]
```

The device dropdown mirrors `deviceSelect` exactly: a parallel
`p.outputNames []string` whose index 0 is `""` with the label
`System Default Output`, and `SetSelectedIndex` driven from
`p.working.OutputDeviceName`. The list is rebuilt every time the tab is built,
because devices come and go — that is why `ListInputDevices` is called there
and not cached, and the same reasoning applies unchanged.

The warning line sits under the checkbox rather than in a tooltip. It is the
one thing a first-time user of this feature has to know, and §5 measured why.

### 7.2 The test tone

**A tone, not a speech sample.** It needs no bundled asset, it cannot be
mistaken for a caption, and what is being tested is routing — is sound coming
out of the right box — not quality.

The button follows the level test's shape: a toggle that reads
`Play Test Tone` / `Stop Test Tone`, a status label beneath it, and a device
change while it is running restarts it, because the reason to run it at all is
to find out which device actually carries the sound.

**When a session is already speaking, the tone goes through the running
player** rather than opening a second device — this is `startMeter`'s rule read
backwards (`prefs.go:746`), and it exists for the same reason: two streams on
one device is a worse answer than one honest one. The status label says so, the
way the meter's does:

```
running session:  "Playing through the running session — MacBook Air Speakers"
otherwise:        "Playing a 440 Hz tone through MacBook Air Speakers…"
```

That leaves one honest gap, which the label above closes rather than hides: if
the dropdown has been changed but not applied, the tone tests the *running*
device, not the selected one. The level meter has exactly this gap today and
names it in the same place.

Like the meter, the tone must stop on every dismissal path — `close()` already
calls `stopMeter()` unconditionally for this reason, and `stopTone()` joins it
there.

### 7.3 Cancel, Apply and OK

The three buttons already mean three different things, and the new controls
only have to join the mechanism rather than extend it:

- **`collect()` gains three lines** — `SpeakTranslation` from the checkbox,
  `OutputDeviceName` from `p.outputNames[i]`, `SpeechVolume` from the slider.
  This is the whole of the wiring, and `TestApplyCollectsEveryTab` exists
  precisely to fail if one of them is forgotten.
- **Apply** emits, sets `dirty`, and keeps the window open. The window's
  `applySettings` starts, stops or reopens the player to match.
- **OK** applies and closes.
- **Cancel** puts back the settings in force when the dialog opened. The window
  owns that revert and already reapplies the original `Settings`, so a player
  Apply started is stopped, a device Apply changed goes back, and the volume
  returns — all through the one path, with no new revert logic.

For that to hold, `applySettings` must be **idempotent and complete** for the
player, which is the one genuinely new requirement in this section:

| Before | After | What must happen |
|---|---|---|
| off | on | Start a player, if a session is running |
| on | off | Stop the player, release the device |
| on | on, different device | Stop, then start on the new one — a short silence is correct |
| on | on, different volume | Store the gain. **Do not reopen the device**, §6 |
| on | on, unchanged | Nothing at all — no gap in the audio |

The last row is the one that gets broken by accident: an `applySettings` that
restarts the player unconditionally makes every Apply, from any tab, punch a
hole in the sound.

---

## 8. Tests

The device layer cannot be faked — malgo opens real hardware — so the design
puts everything worth testing on the near side of it.

| What | How |
|---|---|
| `playRing` under-run | A read larger than the contents zero-fills the tail and counts one starve |
| `playRing` over-run | Writing past the limit drops the **oldest** bytes, and the newest survive |
| `playRing` allocation | `testing.AllocsPerRun` == 0 for `Read` — the callback contract, pinned |
| `playRing` race | Concurrent writer and reader under `-race` |
| Translator carries audio | Fake session returns a blob; `OnAudio` receives those bytes |
| Audio is copied | Fake session mutates its buffer after returning; delivered bytes unchanged |
| Settings round-trip | The three new fields persist and survive `Clamped()` |
| Volume clamps | −1 and 5 come back as 0 and 1 |
| **Volume defaults on upgrade** | Settings JSON with no `speech_volume` loads as 1.0, not 0.0 — §6 |
| Preferences collects | Extend `TestApplyCollectsEveryTab` — it exists to fail when a control is added and not wired |
| Cancel reverts | Apply turns speech on, Cancel turns it back off; same for device and volume |
| Apply is quiet | Applying with the output settings unchanged does not restart the player — §7.3, last row |
| Device resolution | `ResolveOutputDevice` falls back to default on an unknown name |
| Tone stops | Every dismissal path leaves no device open, as `close()` already guarantees for the meter |

Plus the standing gates: `go test -race ./...`, `go vet`, `gofmt -l`, and
bufdiff unaffected (the caption buffer is not touched — **no deviation is added
to `deviations.py`**).

---

## 9. Phases

Each phase ends in a state where `go test ./...` passes and the application
still runs.

| # | Work | Done when | |
|---|---|---|---|
| 0 | Measurement | `v1_voice`, `v2_play`, RESULTS.md | **done** |
| 1 | `playRing` + tests | Under-run, over-run, allocation and race tests pass | **done** |
| 2 | `ListOutputDevices`, `ResolveOutputDevice`, `Player` | `v2_play` reimplemented on top of `Player` and still plays | **done** |
| 3 | `Message.Audio`, `OnAudio`, copy-on-receive | Fake-session tests pass; audio reaches a sink | **done** |
| 4 | Settings, the three controls, the test tone | `TestApplyCollectsEveryTab` extended and green | **done** |
| 5 | Window wiring: apply, revert, shutdown | The §7.3 table holds, row by row, including the quiet one | **done** |
| 6 | A real session, headphones, 20+ minutes | Survives ≥2 reconnects with no click, no drift, no starvation after the first second | **open** |

### What phases 1–5 measured

The starvation count is the number worth watching, because it is the one that
turns into a gap the listener hears.

| Run | Starved callbacks | Silence | Dropped |
|---|---|---|---|
| Hand-rolled spike, before the prebuffer | 33 | — | 0 |
| `Player`, WAV on a 100 ms metronome | 1 | 16 ms | 0 |
| `Player`, live API audio, 45 s, 191 blobs | 1 | 18 ms | 0 |

`PlayPrebufferMS` is what took 33 to 1, and the live run says the jitter buffer
copes with real arrival as well as with a metronome — which was not a given,
since blobs do not arrive on a tidy tick.

### Phase 6, which cannot be shortened

Everything above is testable on this machine. Whether it is *pleasant to listen
to across a reconnect* is not, and that is the question that decides whether it
ships. It needs headphones, a real speaker, and twenty minutes.

What to listen for, in order of how likely it is to be wrong:

1. **The reconnect at ~9 minutes.** Blobs stop and restart. It should sound
   like a pause, not a click or a repeat.
2. **Gaps inside sentences.** One or two starved callbacks at the start of an
   utterance is the prebuffer doing its job. A gap mid-word is not.
3. **Whether hearing it is any use.** The translation starts 3.3–5.2 s after
   the speaker does, and the captions are paced separately, so the text and the
   voice will not agree. That is inherent, not a defect — but it may make the
   feature unpleasant in a way no measurement predicts.

Afterwards `livecap.log` has the verdict in one line per session, from
`Player.Stop`: `starved_callbacks`, `silence_ms`, `dropped_ms`.

---

## 10. Explicitly out of scope

- **Recording the spoken audio.** `*.wav` is gitignored deliberately and the
  transcript is text. Saving translated speech is a separate feature with its
  own privacy shape.
- **Syncing captions to the audio.** They will not match: captions are settled
  and paced by `LineHoldSeconds` and the idle timer, while the audio is the
  model's own uninterrupted stream. Do not try to align them — that would mean
  delaying the captions to match speech the user may have switched off.
- **Ducking the source speaker.** Not something a caption overlay can do.
- **Windows.** The code is portable — malgo covers WASAPI — but this project
  ships macOS, and playback there stays unverified like everything else.

---

## 11. Decisions taken

Settled 2026-08-15, so phases 1–6 are unblocked:

- **Output device dropdown** — yes, in Preferences, stored by name.
- **Test tone** — yes, mirroring the input level test (§7.2).
- **Volume slider** — yes, its own setting, applied as a gain (§6).
- **Cancel / Apply / OK** — the new controls join the existing revert; no new
  revert mechanism (§7.3).
- **Voice selection** — dropped. Not a scope decision; the model ignores the
  field, measured two ways (§3).
- **Menu item and a number key for muting** — not wanted. Preferences is the
  only entry point, and `8` stays free.

Nothing is outstanding. Phase 1 can start.
