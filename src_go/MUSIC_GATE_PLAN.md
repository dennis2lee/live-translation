# Plan: translate speech, not songs

Requested 2026-08-23. When the input audio is singing or music, stop feeding it
to the translator; resume the moment it is speech again. The classifier is
YAMNet, downloaded once and run locally. The feature is off by default, has its
own switch in Preferences, no shortcut key, and the classification must be
tunable from the settings window.

Status: **implemented through phase 5, 2026-08-23; revised 2026-08-30 after
use.** Phase 6 ran informally — the user reported the gate working but leaking
in bursts — and it found a real defect in the release policy rather than a
tuning problem. The gate opened on the *first* clear frame, and a single
sub-threshold frame is the commonest event inside real music; measured end to
end, that policy leaked 18 000 ms of 18 000 ms. A release hold (1.5 s,
`Settings.MusicGateRelease`) plus a pre-roll derived from it brings that to
3 000 ms, which is the close latency, with no speech eaten. The sweep and the
run-length tables are in `spikes/RESULTS.md` ("Why the gate flapped"); the
spike is `spikes/y5_flap`. Sung vocals are **still** unmeasured — pitch-modulated
`say` was tried and reads as Speech, so it is not a singing fixture.

Original status: **implemented through phase 5, 2026-08-23.** Every *measure* below was
produced; the results live in `spikes/RESULTS.md` ("The music gate") and the
decisions they forced are recorded there. Phase 6 — real music, real
microphone, the user's ear — is the only open line. Notable deltas from this
plan: the runtime decision went pure-Go without needing the fallback (52 ms
per patch against the 480 ms hop); the tflite file turned out to carry the
frontend as constant tensors, so the mel matrix is extracted rather than
re-implemented; and the model arrives as a tar.gz from Kaggle's anonymous API,
not a bare file — TF Hub is dead.

## 1. What exists that this builds on

- **The chunks are already in YAMNet's format.** The capture path resamples
  everything to 16 kHz mono PCM16 and hands the send loop 100 ms chunks
  (`audio.TargetRate`, `ChunkBytes`). YAMNet's input is 16 kHz mono float32 —
  a scale by 1/32768 away from what flows past today.
- **A gate already stands where this one goes.** `sendLoop` in
  `internal/translator` holds the silence gate: read a chunk, decide, `continue`
  to skip the send. The music gate is a second decision at the same point, and
  skipping a send is already known to be safe — the silence gate has shipped
  since 1.0.x, and a skipped stretch longer than the server tolerates ends in
  an ordinary reconnect, which the supervisor and (since the overlap work) the
  retirement machinery already absorb.
- **The precedent for "no shared libraries".** The Windows build note in
  `build_windows.sh`: *there is no libsoxr to ship — the Windows build uses the
  pure-Go filter in `internal/resample/fir.go` instead*. Both platforms ship a
  single self-contained binary. That precedent decides more of this design
  than YAMNet does; see §3.
- **The settings window has a live-test culture.** Input has a level test,
  Output has a chime test. The classifier gets the same: a live verdict meter,
  because the only tuning that means anything is done against the user's own
  music, playing into their own microphone.

## 2. What YAMNet is, pinned

- MobileNet v1 (α=1.0) over log-mel spectrogram patches; 521 AudioSet classes;
  Apache 2.0 (redistribution and bundling both permitted — the licence is not
  why we download).
- Input: 16 kHz mono float32 in [-1, 1]. Frontend: STFT 25 ms window / 10 ms
  hop → 64 mel bands (125–7500 Hz) → log(mel + 0.001) → patches of 0.96 s
  (96 frames × 64 bands), one prediction per 0.48 s hop.
- Output: 521 scores per patch. The classes this feature reads: `Speech` and
  its children on one side; `Singing`, `Music`, and the instrument family on
  the other. Exact indices come from `yamnet_class_map.csv` — *measure* (spike
  Y1 dumps the map; the CSV is embedded in the binary at build time, it is
  20 KB of Apache-2.0 text and does not need downloading).
- Model file: the TFLite export from the canonical Google source. Size, URL
  stability, and SHA-256 — *measure* (Y1). The hash is pinned in source;
  the app refuses a file that does not match.

## 3. The runtime decision: pure Go, with a spike gate

Three ways to run YAMNet locally, and the constraint that sorts them is
packaging, not speed:

| option | cost |
|---|---|
| `go-tflite` (cgo + `libtensorflowlite_c`) | a dylib to build, bundle, codesign on macOS; a DLL the Windows cross-build cannot produce and would have to vendor prebuilt — the exact thing the soxr/FIR split exists to avoid |
| `onnxruntime_go` | the same, with a bigger library |
| **pure Go inference of the fixed graph** | ~2 layers of real work: the log-mel frontend and a MobileNet v1 forward pass (conv, depthwise conv, ReLU, average pool, one dense). No interpreter — the graph is known and frozen, we only *extract weights* from the downloaded flatbuffer |

Recommendation: **pure Go**. The graph never changes, so a general TFLite
interpreter is not needed; the flatbuffer is parsed once to pull named weight
tensors, and the pinned hash means the parser targets exactly one file ever.
Correctness is provable: spike Y1 generates golden outputs (frontend
spectrograms and final scores for fixture audio) from the reference
implementation, the goldens are checked in, and the Go port must match them to
tolerance before anything else proceeds.

The gate for this decision is performance — *measure* (Y2): one patch is
roughly 70–140 M multiply-accumulates, needed once per 0.48 s. If the pure-Go
forward pass cannot do a patch in well under the hop on the oldest supported
hardware, fallbacks in order: widen the hop to 0.96 s (halves the work,
decision latency still bounded by the hold times in §5), then cgo on macOS
with the feature compiled out on Windows (the soxr precedent, in reverse).

New package `internal/classify`, and it follows the `caption_buffer` rule: no
Fyne, no genai, no network at inference time, fully unit-tested.

```
internal/classify/
  frontend.go    STFT → mel → log patches      (golden-tested)
  weights.go     flatbuffer → fixed tensor set  (hash-pinned)
  infer.go       conv / dwconv / dense / pool   (golden-tested)
  yamnet.go      streaming Feed(pcm) → Frame{scores, at}
  gate.go        the hysteresis machine of §5   (pure, fuzz-tested)
```

## 4. Getting the model onto the machine

The user asked for download-and-run-local, and download it is:

- Fetched when the feature is first enabled, from the canonical URL, with the
  progress shown in the settings status label (the same label the classifier
  test uses). Stored in the app-support directory next to the log.
- SHA-256 pinned in source. A mismatched or truncated file is deleted and
  reported; the feature stays off. **Never fatal** — captions are the product.
- No credentials, no query parameters, a `User-Agent` set (the Cloudflare 403
  lesson in `netcheck` — do not learn it twice).
- *Measure* (Y1): whether the canonical URL serves without auth or redirect
  games. If it has rotted behind Kaggle sign-in, the fallback is bundling the
  model into the app (Apache 2.0 permits it; costs ~4–16 MB of bundle) — noted
  here so the decision is made by evidence, not by surprise at implementation
  time.
- The repo, private at the time, could not host it: measured this session, its release assets
  and even its releases page answer 404 unauthenticated.

## 5. The gate itself

A hysteresis machine in `classify/gate.go`, pure and testable without a model —
it consumes `Frame{speech, music float64}` values, not audio.

- **Verdict per frame.** `speech` = score of the Speech family; `music` = max
  over the music/singing family. The frame is *musical* when
  `music ≥ threshold && music ≥ speech + margin`. Threshold and margin defaults
  — *measure* (Y3, on real fixtures; the sliders exist because no default
  survives contact with a real room).
- **Gate closes slowly.** Only after `HoldSeconds` (default ~2.0, tunable) of
  consecutive musical frames does the gate close. A verse's first bars getting
  translated is a nuisance; a sentence's first words getting eaten is a defect.
  The asymmetry is deliberate and matches the same choice made for
  clear-on-silence.
- **Gate opens instantly, and retroactively.** The first non-musical frame
  reopens it. Because the decision lags the audio by up to a patch (~1 s), the
  gate keeps a **pre-roll**: the last ~1.5 s of chunks that were suppressed.
  On reopen the pre-roll is flushed to the session first, so the words that
  triggered the reopening are translated, not clipped. The server handles a
  burst fine — the reconnect path has always flushed an accumulated ring at
  swap time.
- **The classifier never blocks the send loop.** Inference runs on its own
  goroutine; `sendLoop` consults an atomic verdict and a mutex-guarded
  pre-roll. The send cadence is 10 chunks/s and `Stats.MaxBacklogMS` exists
  precisely to catch a stall — the integration test asserts it stays near zero
  with the gate enabled.
- **While closed, everything still flows into the classifier** (it must hear
  the music end) and the pre-roll (it must be able to un-eat the last second).
  Nothing flows to the API — which is also where the cost saving comes from:
  gated minutes are unbilled minutes (~$0.037/min).

Wiring: `translator.Options` gains an optional `Filter` interface —
`Filter(chunk []byte) [][]byte` (nil filter = today's behaviour; empty result =
suppressed; multi-chunk result = pre-roll flush). The translator stays ignorant
of YAMNet; `internal/classify` provides the implementation; both sides are
testable alone.

## 6. Settings and the tuning UI

Three new fields, all with the migration test extended the way SpeechVolume's
was:

```go
MusicGate          bool    `json:"music_gate"`           // default false
MusicGateThreshold float64 `json:"music_gate_threshold"` // default from Y3, clamped 0..1
MusicGateHold      float64 `json:"music_gate_hold"`      // seconds, default ~2.0, clamped
```

UI — a new **Filter** tab (the Input tab has no room: 247×310 today against a
380 px content budget, and this group needs ~250 px):

- Check: *"Translate speech only — skip singing and music"*.
- One line under it: the model is downloaded on first use, size and licence
  named.
- Slider *Music sensitivity* (the threshold, shown as Low…High).
- Slider *Switch after* (the hold, shown in seconds).
- Button *Start Classifier Test* + status label: live verdict (**SPEECH /
  MUSIC / quiet**) and the top three classes with scores, refreshed at the
  frame rate, using the meter-capture pattern the level test already owns.
  This is the tuning story: play your actual music at your actual microphone
  and move the slider until the verdict is right.
- Apply/OK/Cancel behave as everywhere else; threshold and hold apply to a
  running gate without a session restart (they are read per frame); toggling
  the gate itself restarts the translator options like SilenceGate does.

The eighth tab will overflow the 560 px tab bar (measured 513 with seven).
Plan: widen the dialog to 600×480 — still far below the original 640×520 —
and let `TestTheAssembledTabsFitTheDialog` arbitrate; if it objects, shorten
tab names before growing the window further.

While the gate is closed, the caption area shows a small dim **♪** beside the
status dot. Captions going quiet mid-session must be explainable from the
screen — the same reasoning that put the error cause next to the red dot.

## 7. What is deliberately not in scope

- **No shortcut key** — requested settings-only, and `m` is now taken.
- **No per-class checklists in the UI.** The tunables are one threshold, one
  margin (advanced, maybe settings-file-only at first), one hold. If real use
  shows a class that misbehaves, it is fixed in the class-family tables in
  source, with the measurement that showed it.
- **No VAD replacement.** The silence gate keeps its job; the two gates
  compose (silence is cheaper to detect and runs first).
- **No attempt to gate the *output*.** By the time lyrics have been translated
  the money is spent and the caption is the only evidence; the point of this
  feature is to stop them going up, not to censor them coming back.

## 8. Risks

| risk | treatment |
|---|---|
| flatbuffer parsing is a rabbit hole | fixed-graph extraction only, hash-pinned to one exact file; go/no-go at the end of Y2, fallback cgo-on-macOS |
| pure-Go inference too slow | measure; widen hop to 0.96 s; then the soxr-style platform split |
| canonical model URL rotted | Y1 verifies; fallback is bundling (licence permits) |
| Korean ballads score as speech | the margin exists for this; the live test makes the failure visible; documented as a limit, not hidden |
| long gates starve the session | measure in Y3 (5 min of no sends); worst case is a reconnect the machinery already absorbs |
| classifier wrongly gates speech | the failure the defaults are chosen against: slow close, instant open, pre-roll flush; fuzz the gate machine with adversarial frame sequences |

## 9. Phases, each with a gate

| phase | contents | gate |
|---|---|---|
| Y1 spike | fetch model; pin URL/size/SHA-256; dump tensor names and class map; licence text; generate golden frontend + score outputs for fixture audio | goldens checked in; URL verdict made |
| Y2 spike→code | `classify` frontend + weights + inference in pure Go | matches goldens to tolerance; patch time measured ≪ hop |
| Y3 spike | scores on real material: the TTS Korean fixture, actual music, music-then-speech transitions; pick default threshold/margin/hold; 5-minute gated session behaviour | defaults are measured numbers with the measurement written down |
| 4 | gate machine + `Filter` wiring + stats (`gated_ms` in the session summary) | unit + fuzz tests; mutation pass (break the hysteresis, the pre-roll, the flush — each must fail a test) |
| 5 | settings fields + Filter tab + download UX + live classifier test + ♪ indicator | overflow tests pass; Apply/OK/Cancel tests extended; migration test extended |
| 6 | real session: user plays actual music and speech through the real microphone | gate closes on the music, opens on speech without eating the first words |

CLAUDE.md and `spikes/RESULTS.md` get their sections when the numbers exist,
not before.

## 10. Open decisions for review

1. **Pure Go vs cgo** — recommended pure Go with the Y2 measurement as the
   gate; say the word if a cgo dependency is acceptable and Y2 shrinks.
2. **Download vs bundle** — plan follows the request (download); if Y1 finds
   the URL unreliable, bundling is the recommended fallback rather than
   self-hosting.
3. **The ♪ indicator** — small, but it is a visible UI change; drop it if you
   want the gate entirely invisible.
