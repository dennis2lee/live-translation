# LiveCap Go Port — Plan

A feature-equivalent rewrite of LiveCap in Go, living in `src_go/`, leaving the
Python implementation in `src/` untouched and shipping until the port passes
the parity gate (§12).

**Status, 2026-08-06.** This started as a plan and is now also a record. G0 to
G6 are written and green — `go vet`, `gofmt` and `go test -race ./...` all pass,
and `dist/LiveCap.app` runs its own `--selftest`. Where the code and this
document disagree, the code won and the paragraph was corrected; the reasoning
is kept so the choices can be argued with. What remains is measurement, not
construction: see §10 and the parity gate in §12.

Facts marked **[verified]** were checked on this machine on 2026-08-05, not
assumed. Everything else that could gate a decision is assigned to a spike in
§10 rather than guessed at.

---

## 1. Ground rules carried over unchanged

Every hard rule in `CLAUDE.md` applies to the Go code with only the SDK name
translated:

- SDK is `google.golang.org/genai` (v1.67.0 **[verified]** — the official Go
  Gen AI SDK). Model name exactly `gemini-3.5-live-translate-preview`.
- `ResponseModalities` must be `["AUDIO"]`; never send text input; received
  audio is consumed and discarded, or the stream stalls.
- Audio to the API: 16 kHz / 16-bit PCM LE / mono / 100 ms (3200-byte) chunks.
- Open devices at their **native** rate and resample ourselves. Never ask the
  OS for 16 kHz directly.
- Devices stored by **name**, not index.
- UI objects touched only from the UI thread; audio callback does no
  allocation, no locks, no UI.
- No Electron and no browser-based UI. (This rules out Wails, which renders in
  WKWebView.)
- `CaptionWindow.shutdown()`-equivalent is the single exit path; idempotent;
  every step individually guarded.
- The four caption-buffer invariants (audited 2026-08-05) hold, checked by the
  same fuzz test ported to Go.
- API keys: env var → encrypted store → Keychain. Never hardcoded, never
  logged. The Go binary must read the **existing** `credentials.enc`
  byte-for-byte (§6.5), so users migrate with zero action.
- Language policy: Go code comments and all Markdown in English; UI strings in
  English; Korean test fixtures stay Korean.

Rules that do not carry over: anything `.env`-related (the store already
replaced it; the Go port reads env vars and the store, and never parses
`.env`), and PyQt-specific threading rules (replaced by their Go equivalents in
§4).

---

## 2. Verified stack facts

Checked directly, output on file in the session log:

- **Go 1.26.5 darwin/arm64 installed** [verified]; `libsoxr` present via
  Homebrew [verified].
- **`google.golang.org/genai` v1.67.0 has the complete Live surface this app
  needs** [verified via `go doc`]:
  - `LiveConnectConfig` with `TranslationConfig` (exactly two fields:
    `TargetLanguageCode`, `EchoTargetLanguage` — same as Python, no source
    language), `InputAudioTranscription` / `OutputAudioTranscription`
    (`AudioTranscriptionConfig` with `CustomVocabulary` and the deprecated
    `AdaptationPhrases`, same descriptions as the Python SDK),
    `SessionResumption`, `ContextWindowCompression`, `RealtimeInputConfig`.
  - `LiveServerMessage` with `GoAway`, `SessionResumptionUpdate`,
    `ServerContent`; `LiveServerContent` with `TurnComplete`,
    `InputTranscription`, `OutputTranscription`, `ModelTurn`.
  - `client.Live.Connect(ctx, model, cfg)` → `*Session` with blocking
    `Receive() (*LiveServerMessage, error)`, `SendRealtimeInput(...)`,
    `Close()`. Marked *Preview* by Google — pin the version in `go.mod` and
    treat upgrades as change events.
  - Audio send shape: `LiveRealtimeInput{Audio: &Blob{Data: chunk, MIMEType:
    "audio/pcm;rate=16000"}}` [verified — `LiveSendRealtimeInputParameters`
    has an `Audio *Blob` field; `Blob` carries `Data []byte` + `MIMEType`].

This removes what would otherwise be the port's biggest risk: **no raw
WebSocket implementation is needed.** (If a future SDK release breaks the
preview surface, the fallback is hand-rolled JSON over WebSocket against
`BidiGenerateContent`; ground truth for the wire format can be captured by
logging the Python SDK's outbound setup message. Documented here so the escape
hatch is known, but it is not part of the plan.)

Proved end-to-end by spike L1 on 2026-08-05, not just present in the type
system: the config above is accepted as-is and real KO→EN fragments come back.
Two facts that cost a debugging session and must not be rediscovered:

- **`TargetLanguageCode` must be `"en"`, not `"en-US"`.** The region-qualified
  form is rejected, and rejected as a whole-config `1007 invalid payload data:
  Request contains an invalid argument` about a second *after* `setupComplete`,
  which makes it look like an SDK fault. `config.DIRECTION_TARGET` in the Python
  build is the authority on these codes.
- **`setupComplete` proves nothing.** It arrives before the server has looked at
  any audio, so a probe that reads one message after connecting will call a
  broken config healthy. Read until either a transcription or a hang-up.

Not yet verified, spike-gated (§10): `malgo` device behaviour on the Sound
Blaster X3. Fyne's Korean rendering and the resampler choice are now settled —
see `src_go/spikes/RESULTS.md`.

---

## 3. Stack decisions

| Concern | Python today | Go choice | Alternatives considered |
|---|---|---|---|
| Live API | `google-genai` | `google.golang.org/genai` v1.67.0, pinned | Raw WebSocket (escape hatch only) |
| GUI | PyQt6 | **Fyne v2** (decided — see §3.1) | DarwinKit (rejected: macOS-only, and Windows full-screen is a requirement), Gio (last resort: no menu system) |
| Audio capture | sounddevice/PortAudio | **`github.com/gen2brain/malgo`** (miniaudio; cgo but vendors its own C — no external install) | `gordonklaus/portaudio` (needs brew portaudio + dylib bundling) |
| Resampling | soxr | **libsoxr via cgo on macOS, pure-Go windowed sinc elsewhere**, chosen by build tag. Both measured in one test run; with the rolloff corrected the pure-Go filter matches libsoxr (`spikes/RESULTS.md`) | Shipping a cross-built libsoxr DLL on Windows — rejected, it would turn a one-file installer into two |
| Keychain | `keyring` | `github.com/zalando/go-keyring` (shells to `/usr/bin/security`, no cgo) | `keybase/go-keychain` (cgo) |
| Encrypted store | `cryptography` Fernet | `github.com/fernet/fernet-go` + `golang.org/x/crypto/pbkdf2` — **must decrypt the existing file** | — |
| Settings | QSettings plist | JSON at `~/Library/Application Support/LiveCap/settings.json`, one-time import from the plist | Writing the plist (rejected: ties us to CFPreferences quirks) |
| Display sleep | ctypes → IOKit | cgo → IOKit (5 lines of C) | `caffeinate` subprocess (rejected: another process to babysit) |
| Websocket/HTTP/net | urllib/socket | stdlib `net`, `net/http` | — |
| Logging | `logging` | stdlib `log/slog`, stderr | — |
| Lint/format | ruff | `gofmt` + `go vet` + `staticcheck` | golangci-lint (heavier; adopt only if wanted) |

### 3.1 The GUI decision, in detail — **decided: Fyne** (2026-08-05)

Requirements the toolkit must meet (from the running app, plus one new
requirement Dennis set while reviewing this plan): native macOS menu bar with
submenus, checkable items and an app menu (About / Preferences / Quit); a
maximised or full-screen window painted almost entirely by us (rounded caption
box, per-line alpha, bottom-aligned text, Korean + Latin in one line, pulsing
status dot, fading direction hint); a six-tab Preferences dialog (entries,
sliders, combo boxes, checkboxes, colour picker, progress-bar level meter,
multi-line vocabulary editors, Apply/OK/Cancel); plain-digit global key
bindings (`0`, `1`, `2`, `9`); cursor hiding; and — **new, decisive** —
**true full-screen on Windows: the caption window must be able to cover the
title bar, the menu and the taskbar completely.**

The Windows full-screen requirement settles the choice by elimination:

- **DarwinKit rejected.** macOS-only; the moment a Windows build is a real
  target, choosing it means writing the entire UI twice. It is out as primary
  *and* as fallback.
- **Fyne chosen.** `fyne.Window.SetFullScreen(true)` drives GLFW's
  monitor-fullscreen mode, which by documented GLFW behaviour covers the whole
  monitor — on Windows that includes the taskbar, on macOS the menu bar and
  Dock hide. One codebase for both platforms. This cannot be *measured* from
  this Mac (the same limitation `CLAUDE.md` records for PyInstaller);
  verifying it on real Windows is the first item of the future Windows phase
  (§9), and the claim rests on documented GLFW semantics, not hope.
- **Gio** demoted to last resort (only if Fyne proves fundamentally unusable):
  cross-platform and excellent text shaping, but it has no menu system at all,
  so the entire menu would be hand-built.
- **Wails** rejected outright: browser-based rendering violates a hard rule.

Fyne covers the rest on paper: `fyne.MainMenu` maps to the native macOS menu
bar, `MenuItem.Checked` exists, `container.NewAppTabs`,
`dialog.NewColorPicker`, `widget.ProgressBar`, `widget.Slider`,
`canvas.Rectangle` has `CornerRadius`, `fyne.Animation` for the dot pulse and
hint fade, `Canvas().SetOnTypedRune` for plain-digit keys (menu items will not
display a bare-digit accelerator — acceptable; the rune handler reproduces the
behaviour and the item text notes the key, as Preferences already does with
"(9)").

Two unknowns remain spike-gated (§10), but **neither can force a toolkit
change any more** — each has a deterministic in-toolkit fix:

- **F1 Korean glyph rendering.** Fyne bundles a Latin font. Fix path: load
  Apple SD Gothic Neo from the system, or embed Noto Sans KR (~5 MB) via a
  custom theme — embedding is the guaranteed endpoint and also gives Windows
  identical rendering with no system-font dependency.
- **F2 native menu fidelity** (About/Preferences roles, separators, runtime
  check-state). Fix path if the native mapping falls short: render the menu
  in-window on macOS too — which is exactly what Fyne does on Windows anyway,
  where no global menu bar exists.

Windows-specific design note, recorded now for the future Windows phase: on
Windows the Fyne menu renders **inside** the window's top edge, and would stay
visible in full screen. During a live show that strip is unwanted; since the
digit keys are the primary controls, the plan is to hide the in-window menu
while full-screen (menu restored on leaving full screen). Decided at G4-W, not
now.

Trade-offs made explicit: Fyne renders with OpenGL (deprecated on macOS but
functional) and its widgets will not look pixel-identical to Qt's. The caption
window — what the audience sees — is custom-painted and will look identical.
The Preferences dialog will look like Fyne, not like Qt. Accepted; the parity
gate (§12) is behavioural, not pixel-level.

### 3.2 The audio decision, in detail

`malgo` wraps miniaudio, which compiles into the package via cgo — **no
external library to install or bundle**. Capture flow:

- Enumerate: `ctx.Devices(malgo.Capture)` → names + IDs; resolve stored name →
  ID at open time, fall back to system default when missing (same semantics as
  `resolve_device`).
- Open at native rate: request `SampleRate: 0` and `Channels: 0` so miniaudio
  passes the device's native format through instead of converting — the moral
  equivalent of the "never ask CoreAudio for 16 kHz" rule. Format `f32`.
- The data callback runs on miniaudio's audio thread: downmix to mono, track
  peak (atomic store), feed the streaming resampler, write PCM16 into the ring.
  No allocation after start (preallocated scratch buffers), no locks, no UI.

Spike A1 (§10) reruns the Python project's capture acceptance test on this
stack: 5 s from the X3 at 96 kHz/2ch must yield ≥ 98% of nominal bytes, 0
drops — the same numbers `spikes/RESULTS.md` records for the Python pipeline.

**A1, measured** (`src_go/spikes/RESULTS.md`): the built-in mic opened at its
native 48 kHz and delivered 99.6% of nominal in 5 s, every callback exactly
4800 frames. Two corrections to the paragraph above: `SampleRate: 0` does ask
for the native rate and works, but `Channels: 0` is not how malgo expresses
"native" — the channel count is read from `DeviceInfo.Formats[0]` and passed
explicitly. Requesting `FormatS16` is fine: miniaudio converts the format
without resampling, so the rule that matters — never let the backend resample —
is still kept. The X3 run and the level check are still outstanding, for the
reasons `RESULTS.md` records.

---

## 4. Concurrency map

Python's three threads become three goroutine domains with the same crossing
rules:

```
Python                              Go
──────────────────────────────────  ─────────────────────────────────────────
PortAudio callback thread           miniaudio callback (OS thread, cgo)
  └─ writes lock-free ring            └─ writes the same ring (atomics)
asyncio worker thread               session goroutines:
  supervisor task                     supervisor goroutine (owns reconnects)
  send loop task                      sender goroutine (ring → SendRealtimeInput)
  per-session recv loop               per-session receiver goroutine (blocking Receive())
  emits pyqtSignal                    sends on a buffered events channel
Qt main thread                      UI main goroutine (Fyne owns main())
  slots update widgets                one dispatcher drains the events channel
                                      and applies updates via fyne.Do(...)
```

Rules, stated as sharply as the Python ones:

- The ring buffer is the **only** structure shared between the audio callback
  and the sender goroutine. Positions are `atomic.Int64`; Python's reliance on
  GIL atomicity becomes explicit (§6.1).
- Session goroutines never touch UI state. They send typed events on one
  channel: `chan uiEvent` where `uiEvent` is a small sum type
  (`targetText`, `sourceText`, `stateChanged`, `reconnected`, `turnComplete`).
  This channel **is** the `_Bridge`.
- The dispatcher goroutine is the only consumer; it marshals onto the UI
  thread with `fyne.Do`. The 100 ms tick is a `time.Ticker` handled in the
  same dispatcher, so buffer mutations are single-goroutine by construction —
  `CaptionBuffer` needs no lock, exactly like the Python one.
- Shutdown: `context.Context` cancellation replaces the
  `threading.Event` + `call_soon_threadsafe` dance. `stop()` cancels the
  supervisor's context, waits on a done channel with the same 6 s timeout, and
  logs a hard error if it is exceeded. Signals: `signal.Notify(SIGINT,
  SIGTERM)` → the one shutdown function → `fyne.App.Quit()`.

---

## 5. Package layout

```
src_go/
├── PLAN.md                      this file
├── go.mod                       module github.com/dennis2lee/live-translation/src_go
├── run.sh                       go run ./cmd/livecap
├── cmd/livecap/main.go          flag parsing (--selftest), wiring, signal handling
├── internal/
│   ├── audio/
│   │   ├── ring.go              SPSC ring buffer (port of RingBuffer)
│   │   ├── capture.go           malgo device open/close, callback, peak
│   │   └── devices.go           enumeration, resolve-by-name
│   ├── resample/
│   │   └── soxr.go              cgo binding to libsoxr's stream API
│   ├── captions/
│   │   └── buffer.go            port of caption_buffer.py — no UI, no net deps
│   ├── translator/
│   │   ├── translator.go        supervisor, sender, states, stats, backoff
│   │   └── session.go           liveSession interface + genai-backed impl + fake
│   ├── config/
│   │   ├── settings.go          Settings struct, clamping, JSON persistence
│   │   ├── migrate.go           one-time QSettings-plist import
│   │   └── apikey.go            env → store → keychain lookup, source label
│   ├── credentials/store.go     Fernet store, byte-compatible with Python's
│   ├── netcheck/netcheck.go     latency / upload / download / verdicts
│   ├── transcript/writer.go     Markdown transcript, footers, reconnect notes
│   ├── platform/                the only package that branches on GOOS
│   │   ├── dirs.go              app-support dir (build tags for the pinned
│   │   ├── displaysleep_darwin.go   windows/linux branches, like the Python
│   │   ├── displaysleep_other.go    tests pin them without those machines)
│   │   ├── fonts.go             caption font stacks per platform
│   │   └── reveal.go            open-folder command
│   └── ui/
│       ├── window.go            caption window, key bindings, shutdown owner
│       ├── captionview.go       custom-painted box, dot, hint
│       ├── menus.go             native menu bar
│       ├── prefs.go             Preferences dialog, Apply/OK/Cancel
│       └── events.go            uiEvent types + dispatcher
└── packaging/
    ├── build_app.sh             .app assembly + soxr dylib + codesign hook
    └── Info.plist.tmpl          NSMicrophoneUsageDescription etc.
```

`internal/platform` inherits `platform_support.py`'s contract verbatim: every
platform difference lives there, pinned by tests that fake the platform (in
Go: interfaces + build-tag shims so the windows/linux stubs compile and test
on macOS).

---

## 6. Module-by-module port specs

Snippets are indicative, not final code; field names against external APIs are
the verified ones from §2.

### 6.1 Ring buffer (`internal/audio/ring.go`)

Same design; the GIL assumption becomes explicit atomics. Producer advances
only `w`, consumer only `r`, both cumulative; overwrite on overflow; the
consumer detects lapping and the torn-copy retry survives translation intact:

```go
type Ring struct {
    buf []byte
    cap int64
    w   atomic.Int64 // producer only, cumulative bytes
    r   atomic.Int64 // consumer only, cumulative bytes
    dropped atomic.Int64
}

// Read returns nil until n bytes are available. Consumer only.
func (g *Ring) Read(n int64) []byte {
    for {
        w, r := g.w.Load(), g.r.Load()
        if lag := w - r; lag > g.cap { // lapped — skip the oldest
            skip := lag - g.cap
            r += skip
            g.r.Store(r)
            g.dropped.Add(skip)
        }
        if w-r < n {
            return nil
        }
        out := make([]byte, n)
        start := r % g.cap
        if start+n <= g.cap {
            copy(out, g.buf[start:start+n])
        } else {
            split := g.cap - start
            copy(out, g.buf[start:])
            copy(out[split:], g.buf[:n-split])
        }
        if g.w.Load()-r > g.cap {
            continue // producer lapped us mid-copy; that data is torn — retry
        }
        g.r.Store(r + n)
        return out
    }
}
```

The `make([]byte, n)` per read is on the sender goroutine (10 Hz), not the
audio callback — allocation there is fine. The callback-side `Write` copies
into the preallocated ring only.

Port targets: `tests/test_ring_buffer.py` → `ring_test.go`, including the
overflow/lap cases, plus a `go test -race` run with a real producer/consumer
goroutine pair (something the Python suite could not express).

### 6.2 Caption buffer (`internal/captions/buffer.go`)

A mechanical port — this module was deliberately kept free of Qt and network
so it would move. Field-for-field:

```go
type Buffer struct {
    MaxLines            int
    Policy              Policy
    IdleCommitSeconds   float64
    ClearAfterSeconds   float64
    LineHoldSeconds     float64
    CompletePhrasesOnly bool

    committed     []string // bounded by MaxLines (deque semantics inlined)
    queue         []string
    pending       string
    lastText      string
    lastFeedAt    float64
    releasedAt    float64
    audioKnown    bool
    quietSince    *float64
    continueNext  bool
    resolved      *Policy
    samples       []int
    newCommits    []string
    resumedPrefix string
}

func (b *Buffer) Feed(text string, now float64) bool
func (b *Buffer) Tick(now float64, speaking Tristate) bool // Yes/No/Unknown
func (b *Buffer) NoteTurnComplete(now float64) bool
func (b *Buffer) NoteReconnect()
func (b *Buffer) DrainNewCommits() []string
func (b *Buffer) DisplayLines() []Line // {Text string; Settled bool}
```

Decisions worth stating:

- **Time stays `float64` seconds** (monotonic, from a `func now() float64`
  injected at the edges). This keeps every constant and every test literal —
  `0.25`, `1.2`, `15.0`, the fuzz step deltas — byte-identical to Python,
  which is worth more than `time.Time` idiom in a module whose tests are its
  specification.
- `speaking bool | None` becomes a three-valued `Tristate` enum, because Go
  has no `Optional[bool]` and `*bool` in a hot path is noise.
- `_join`'s CJK logic ports directly; Go strings are UTF-8, so the
  first/last-rune checks use `utf8.DecodeRuneInString` /
  `DecodeLastRuneInString` — a port hazard called out here because a byte-index
  version would silently break the Hangul checks.

Port targets: all ~70 cases of `tests/test_caption_buffer.py`, including the
randomised invariant fuzz (same seed 20260805 via `rand.New(rand.NewSource)`,
same step distribution, same four invariants from `CLAUDE.md`), plus a native
`go test -fuzz=FuzzFeed` harness as a bonus the Python suite lacks.

### 6.3 Translator (`internal/translator/`)

The session is hidden behind an interface so the supervisor is testable
against a fake, exactly like the Python tests' `FakeSession`:

```go
type liveSession interface {
    Receive() (*genai.LiveServerMessage, error) // blocking; error on close
    SendAudio(chunk []byte) error
    Close() error
}

type connector func(ctx context.Context, handle string) (liveSession, error)
```

The real connector:

```go
func (t *Translator) connect(ctx context.Context, handle string) (liveSession, error) {
    cfg := &genai.LiveConnectConfig{
        ResponseModalities:       []genai.Modality{genai.ModalityAudio},
        InputAudioTranscription:  transcription(t.sourceTerms),
        OutputAudioTranscription: transcription(t.captionTerms),
        TranslationConfig: &genai.TranslationConfig{
            TargetLanguageCode: t.targetLanguage,
            EchoTargetLanguage: genai.Ptr(t.echoTarget),
        },
        ContextWindowCompression: &genai.ContextWindowCompressionConfig{
            SlidingWindow: &genai.SlidingWindow{},
        },
        SessionResumption: &genai.SessionResumptionConfig{Handle: handle},
    }
    return t.client.Live.Connect(ctx, Model, cfg) // wrapped to satisfy liveSession
}
```

Supervisor structure (one goroutine, same state machine):

```go
for ctx.Err() == nil {
    t.setState(stateFor(active != nil), "")     // CONNECTING vs RECONNECTING
    holder, err := t.open(ctx)                   // 30 s timeout, fatal matching
    if err != nil { ...backoff / fatal exactly as Python... ; continue }

    previous := active
    active = holder                              // atomic swap of the send target
    if previous != nil {
        t.stats.recordReconnect(gapMS)
        t.events <- evReconnected{}
        previous.close()                         // old session outlived go_away
    }
    t.setState(StateRunning, "")

    select {                                     // wait_for_replacement_trigger
    case <-holder.goAway:
    case <-holder.closed:
    case <-ctx.Done():
    }
    // MIN_SESSION_SECONDS short-life check, backoff table — verbatim port
}
```

Per-session receiver goroutine — the numbered comment sequence carries over:

```go
for {
    msg, err := s.Receive()
    if err != nil { close(h.closed); return }    // includes server disconnects
    if msg.GoAway != nil { closeOnce(h.goAway) } // pre-emptive reconnect trigger
    if u := msg.SessionResumptionUpdate; u != nil && u.Resumable && u.NewHandle != "" {
        t.handle.Store(u.NewHandle)
    }
    sc := msg.ServerContent
    if sc == nil { continue }
    if tr := sc.InputTranscription; tr != nil && tr.Text != "" { t.events <- evSource{tr.Text} }
    if tr := sc.OutputTranscription; tr != nil && tr.Text != "" { t.events <- evTarget{tr.Text} }
    if sc.TurnComplete { t.events <- evTurnComplete{} }
    _ = sc.ModelTurn // audio parts: reached and dropped — consuming IS the read
}
```

Sender goroutine: 20 ms poll of the ring, 3200-byte chunks, silence-gate RMS,
`max_backlog_ms` tracking with the 1.5 s / 15 s-grace warning — all constants
verbatim (§7). `Stats` and its `summary()` line port unchanged so log
diffing across implementations stays possible.

Port targets: all of `tests/test_translator.py` (~36 cases) against the fake:
pre-emptive swap ordering, handle reuse, short-session backoff, fatal-error
fast-fail table, buffered-audio-across-reconnect, vocabulary placement on the
correct sides, turn-complete emission.

### 6.4 Config (`internal/config/`)

```go
type Settings struct {
    BgColor          string  `json:"bg_color"`
    BoxColor         string  `json:"box_color"`
    TextColor        string  `json:"text_color"`
    BoxOpacity       float64 `json:"box_opacity"`
    // ... every field of the Python dataclass, same names in snake_case JSON
    CompletePhrasesOnly bool `json:"complete_phrases_only"`
}
```

- Persistence: pretty-printed JSON, atomic write (temp + rename), at
  `~/Library/Application Support/LiveCap/settings.json`.
- `Clamped()` ports the clamping table exactly (ranges in §7).
- **One-time migration**: if `settings.json` is absent and
  `~/Library/Preferences/com.LiveCap.LiveCap.plist` exists, read it with
  `howett.net/plist` and import the fields. Never write the plist. QSettings
  stored everything as strings — the importer parses with the same tolerance
  as `config.load()` (bad value → default).
- `ParseTerms` (newline-split, dedupe, cap 200) ports verbatim, tests included.
- API key lookup order and `APIKeySource()` labels port verbatim.

### 6.5 Credentials (`internal/credentials/store.go`) — byte compatibility

**Written and passing as of 2026-08-05** — brought forward from G1 because
spike L1 needed a real API key, which made it the cheapest way to close the
port's most delicate risk early.

The Go binary must decrypt the file the Python app wrote. Same passphrase, same
salt, same KDF, same token format:

```go
derived := pbkdf2.Key(passphrase, salt, 200_000, 32, sha256.New)
// Python builds the Fernet key as urlsafe_b64encode(derived) and Fernet
// base64-decodes it again, so Go decodes the same text to reach the same bytes.
key, _ := fernet.DecodeKey(base64.URLEncoding.EncodeToString(derived))
plaintext := fernet.VerifyAndDecrypt(blob, 0 /* no TTL */, []*fernet.Key{key})
```

`fernet.DecodeKey` on the base64 text is the working form; copying the raw 32
bytes into a `fernet.Key` by hand is not, because the two halves are the signing
and encryption keys and `DecodeKey` is what splits them.

Fernet is a fixed spec (AES-128-CBC + HMAC-SHA256 over a versioned token), so
`fernet-go` and Python `cryptography` interoperate; the **acceptance test for
this module decrypts a fixture produced by the Python implementation** —
compatibility is proven, not presumed. Writes: JSON payload, temp file, chmod
0600, rename; delete the file when the last key is removed. The store-path
override used by tests becomes a package-level variable, and the Go test
suite isolates it the same way `conftest.py` does — plus the settings path and
a stubbed keyring, honouring the Test-isolation section of `CLAUDE.md`.

### 6.6 Netcheck (`internal/netcheck/`)

Pure stdlib. Latency via TCP handshakes:

```go
start := time.Now()
conn, err := net.DialTimeout("tcp", "generativelanguage.googleapis.com:443", 3*time.Second)
if err == nil { conn.Close(); samples = append(samples, time.Since(start).Seconds()*1000) }
```

Throughput via `net/http` with `User-Agent: LiveCap/network-check` (the 403
lesson is recorded in `CLAUDE.md`; the constant carries over). All thresholds
and every verdict string port verbatim so the UI tests can assert the same
prefixes. Tests inject a dialer (`func(network, addr string) (net.Conn,
error)`) and an `http.RoundTripper` — the Go equivalent of the monkeypatched
socket/urlopen — so the suite stays fully offline.

### 6.7 Transcript (`internal/transcript/writer.go`)

Same Markdown, same header/footer fields, same `MM:SS`-stamped lines, flush
after every line (`File.Write` + `File.Sync` per line is acceptable at ~1
line/s), reconnect and direction notes inline, empty-session file deletion,
`Close` guarded against a nil file. Port `tests/test_transcript.py`, including
comparing full rendered documents against golden strings.

### 6.8 Platform (`internal/platform/`)

Display sleep, the one genuinely native piece:

```go
/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>
*/
import "C"

func (b *DisplaySleepBlocker) Acquire(reason string) bool {
    cfReason := cfString(reason)
    defer C.CFRelease(C.CFTypeRef(cfReason))
    var id C.IOPMAssertionID
    ok := C.IOPMAssertionCreateWithName(cfString("NoDisplaySleepAssertion"),
        C.kIOPMAssertionLevelOn, cfReason, &id) == C.kIOReturnSuccess
    if ok { b.id = id }
    return ok            // best-effort: a refusal is logged, never fatal
}
```

Windows/Linux shims return false (build tags), pinned by tests exactly as the
Python suite pins them. `AppSupportDir`, `CaptionFontFamilies`,
`RevealCommand` port with their tests. Verification stays `pmset -g
assertions`, same as before.

### 6.9 UI (`internal/ui/`)

The layer with the least mechanical port and the most judgment. Structure:

**Three things F2 measured that this layer has to work around** — details and
evidence in `src_go/spikes/RESULTS.md`:

- **Never put spaces around `→`.** Fyne 2.8 renders `"KO → EN"` as an arrow
  followed by U+FFFD; `"KO→EN"` is perfect. It is a shaping fault, not a widget
  one — `canvas.Text` and `widget.Label` fail identically — so it reaches the
  captions as well as the labels. `DIRECTION_LABEL` and the hint text must drop
  the spaces or change separator. `·` `…` `—` `✓` are safe.
- **The app menu has no Preferences slot.** Fyne builds About/Services/Hide/
  Show All/Quit itself and offers no hook. Preferences goes in a regular menu
  unless a native reach-in proves cheap.
- **Escape did not leave full screen** in the spike, though the rune handler
  fired for every other key. Pin this down before the caption window relies on
  it; `F`-style rune toggling works either way.

- **`captionview.go`** — a custom Fyne widget owning exactly the state the
  Python `CaptionView` owns: lines `[]Line`, status, box alpha, hint
  text/alpha, dot state/alpha. Rendered with `canvas.Rectangle` (corner
  radius, RGBA from settings), one `canvas.Text` per display line (settled at
  full alpha, pending at 55%), dot as `canvas.Circle`, animations via
  `fyne.NewAnimation` (150 ms box fade, 1200 ms dot pulse loop, 400 ms hint
  fade after a 2 s hold). Wrapping: measure with `fyne.MeasureText` and break
  to the box width — the Python `_wrap` logic, remeasured per font size.
  Fixed-box mode, three anchor positions, bottom-aligned text: the geometry
  functions (`_box_bottom`, `_nominal_box_height`) port as pure functions with
  unit tests.
- **`menus.go`** — `fyne.NewMainMenu` with the same four menus, checkable
  items updated by a `syncActions()` equivalent after every settings change.
  Digit shortcuts live in `Canvas().SetOnTypedRune`; menu items keep working
  as click targets.
- **`prefs.go`** — `container.NewAppTabs` with the same six tabs and the same
  three-button contract: Apply emits, OK applies+closes, Cancel restores the
  opening `Settings` **and** the credential-store snapshot (`storedAPIKey` /
  `restoreStoredAPIKey` port). Key test = one `GET
  /v1beta/models?key=…&pageSize=200` via `net/http` on a goroutine (no SDK
  needed, still free); level meter = a 100 ms ticker polling
  `capture.TakePeak()`; speed test on a goroutine with the settle-on-close
  discipline the Python dialog has.
- **`window.go`** — owns the buffer, the dispatcher, the tick, cursor hiding
  (`desktop.Cursor` swap to a blank cursor), `startTranslation` /
  `stopTranslation` / `shutdown` with the same guard-every-step structure and
  the same "single exit path" rule.

Port targets: the *logic* halves of `test_main_window.py` and
`test_settings_dialog.py` (state transitions, Apply/Cancel cycles, silent-input
logging, buffer wiring) using `fyne.io/fyne/v2/test` (headless test driver).
The pixel-inspection tests do not port — replaced by unit tests on the
geometry/color pure functions plus manual visual review at G4 (§10).

---

## 7. Constants — carried over verbatim

Changing any of these requires a measurement, same as today. Single table so
drift is auditable:

| Constant | Value | Home in Go |
|---|---|---|
| TARGET_RATE / BYTES_PER_SAMPLE | 16 000 / 2 | audio |
| CHUNK_MS / CHUNK_BYTES | 100 / 3200 | audio |
| RING_SECONDS / CALLBACK_MS | 30 / 20 | audio |
| SILENT_INPUT_PEAK / _SECONDS | 0.002 (−54 dBFS) / 12 | ui |
| SPEECH_PEAK | 0.05 (−26 dBFS) | ui |
| IDLE_COMMIT_SECONDS | 1.2 | captions |
| CLEAR_AFTER_SECONDS | 2.0 | captions |
| LINE_HOLD_SECONDS | 0.7 | captions |
| MAX_CLEAR_WAIT_SECONDS | 15.0 | captions |
| MIN_SENTENCE_CHARS | 20 | captions |
| DUPLICATE_WINDOW_SECONDS | 0.25 | captions |
| BACKOFF_SECONDS | 1, 2, 4, 8 | translator |
| MAX_CONSECUTIVE_FAILURES | 5 | translator |
| MIN_SESSION_SECONDS | 5.0 | translator |
| IDLE_POLL_SECONDS | 0.02 | translator |
| SILENCE_RMS_THRESHOLD / HOLD | 0.003 / 3.0 | translator |
| BACKLOG_WARN_MS / GRACE | 1500 / 15 s | translator |
| MAX_VOCABULARY_TERMS | 200 | config |
| netcheck: AUDIO_KBPS 256, latency 150/300 ms, jitter 40/100 ms, upload-comfort 4× | — | netcheck |
| FATAL_ERRORS marker table | verbatim strings | translator |
| Settings clamp ranges | as in `Settings.clamped()` | config |

---

## 8. Testing strategy

The Python suite (465 tests) is the behavioural specification. Mapping:

| Python file | Go file | Ports |
|---|---|---|
| test_caption_buffer.py | captions/buffer_test.go | all, incl. fuzz invariants |
| test_ring_buffer.py | audio/ring_test.go | all + `-race` producer/consumer |
| test_translator.py | translator/translator_test.go | all, against the fake |
| test_config_env.py | config/*_test.go | all except `.env` parsing (dropped) |
| test_credentials.py | credentials/store_test.go | all + Python-written fixture decrypt |
| test_netcheck.py | netcheck/netcheck_test.go | all, injected dialer/transport |
| test_transcript.py | transcript/writer_test.go | all |
| test_platform_support.py | platform/*_test.go | all (build-tag shims) |
| test_main_window.py | ui/window_test.go | logic tests; pixel tests replaced |
| test_settings_dialog.py | ui/prefs_test.go | logic tests |
| test_repo_files.py | repo_test.go | adapted (version consistency, licence) |
| test_resample.py | resample/soxr_test.go | adapted + sweep-spectrum check |

Rules: `go test ./...` runs offline, no key, no network — enforced the same
way (isolated store/settings paths, stubbed keyring, fake sessions, injected
dialers). CI-equivalent command block for `CLAUDE.md` at cutover:

```bash
cd src_go && go build ./... && go vet ./... && staticcheck ./... && go test -race ./...
```

---

## 9. What is deliberately not ported

- `.env` parsing and migration — the encrypted store superseded it; the Go
  port reads env vars, the existing store, and the Keychain. `dotenv`
  functions have no Go equivalent, by design.
- The AUTO caption policy's sample-based detection ports (it is part of the
  buffer), but remains as vestigial as it is in Python — policy is fixed to
  INCREMENTAL by measurement.
- Pixel-inspection UI tests (replaced per §6.9).
- `spikes/` measurement scripts — G-phase spikes get their own
  `src_go/spikes/` with results appended to the same `spikes/RESULTS.md`.
- Windows/Linux builds — still deferred, but no longer an afterthought: the
  toolkit was chosen **with Windows as a requirement** (§3.1), so the future
  Windows build keeps this one UI codebase. Recorded for that phase:
  (a) first task is verifying on real Windows that `SetFullScreen` covers the
  taskbar; (b) the in-window menu auto-hide decision (§3.1); (c) builds are
  either native on a Windows machine or cross-compiled from macOS with
  mingw-w64 / `fyne-cross` — cgo (malgo, Fyne) makes plain `GOOS=windows`
  insufficient; (d) `internal/platform` branches exist from day one, pinned by
  tests, exactly as `platform_support.py` pins them today.

---

## 10. Phases and their exit criteria

Mirrors the phase discipline that worked for the Python build. Each phase ends
green (`go vet` + `staticcheck` + `go test -race ./...`) before the next
starts.

**G0 — Spikes (decisions before code).** Run 2026-08-05; results and numbers
in `src_go/spikes/RESULTS.md`.
- ~~Go SDK Live surface~~ — already verified (§2).
- ~~**F1 Fyne text**~~ — **done, pass.** Fyne 2.8 renders Hangul with its own
  bundled theme font, no font work at all. Embedding Noto Sans KR is now a
  deliberate choice for cross-platform determinism (G4), not a fix.
- ~~**F2 Fyne menus**~~ — **done, pass.** Native menu bar, custom menus,
  a check mark toggled at runtime, bare digits `0 1 2 9`, and full screen that
  removes the title bar, the menu bar *and* the Dock. Three defects to design
  around in G4: `→` needs no spaces around it, the app menu has no Preferences
  slot, and Escape did not reach the key handler in full screen. Checked from a
  minimal `.app` — a bare Go binary has no `CFBundleName`, so the app-menu name
  cannot be judged without one.
- **A1 malgo capture** — **mechanics pass** (native 48 kHz honoured, 99.6% of
  nominal, callbacks exactly 100 ms). Outstanding: the X3 at 96 kHz/2ch, which
  was not connected, and the peak level, which reads −Inf because the
  automation process holds no microphone permission — Python's `sounddevice`
  reads 0 from the same process, so this is TCC, not malgo.
- ~~**R1 resampler**~~ — **done, decided: libsoxr.** Flat to 7 kHz, −2.9 dB at
  7.6 kHz where the pure-Go filter is −17.8 dB, −87 dB stopband, 10–20× cheaper.
  The pure-Go FIR is now a measured fallback rather than a hope.
- ~~**L1 live smoke**~~ — **done, pass.** Real KO→EN fragments through the Go
  SDK from `ko_fixture.wav`: connect 292 ms, 22 fragments, first at 2.86 s,
  resumption updates ≈ 1/s. No raw WebSocket needed.
- Gate: F1 or F2 failing no longer changes the toolkit (§3.1) — F1 has now
  passed outright; an F2 failure means an in-window menu on macOS as well. Only
  a fundamental Fyne failure (text shaping or full-screen itself broken) falls
  back to Gio with a hand-built menu, and that decision would come back to
  Dennis with evidence before any code is written on it.

Status, 2026-08-06: **G1 to G6 are written and green, and the whole chain has
run once for real** — Korean spoken into the built-in microphone came back as
English through the bundled application, with a properly closed transcript
(`src_go/spikes/RESULTS.md`). `go vet`, `gofmt` and
`go test -race ./...` all pass, and the bundle runs its own `--selftest`. What
is *not* yet done is the measuring at length: the 30-minute endurance run of G3,
the side-by-side visual comparison of G4, and the manual checklist walk. One
parity gap is also open — the Python build opens maximised and Fyne has no
maximise API. Those are the parity gate (§12), and they are what stands between
this and 1.0.0.

**G1 — Pure logic.** captions, config, credentials, netcheck, transcript,
platform (minus display-sleep wiring). Exit: every mapped test green,
including the Python-fixture decryption and the ported fuzz invariants.

**G2 — Audio.** ring, resample, capture, devices. Exit: A1 rerun through the
real pipeline classes; peak metering feeds a stub; `-race` clean.

**G3 — Translator.** Supervisor + sender + receiver against the fake (all
test_translator ports), then live: one KO→EN session, one EN→KO session, then
a 30-minute endurance run. Exit: ≥ 3 go_away swaps, gap < 1 s each, no lost
sentence versus the transcript, backlog ≈ 0 — the measured bar the Python
implementation set.

**G4 — UI.** Caption window first (visual parity against the Python app side
by side), then menus/keys, then Preferences with the Apply/OK/Cancel cycle.
Exit: ported logic tests green + a manual checklist walk (every menu item,
every key, every tab) recorded in the PR description.

**G5 — Integration.** Shutdown paths (⌘Q, close, SIGINT, SIGTERM mid-session
in both directions), display sleep (`pmset` check), silent-input warning,
`--selftest`. Exit: the same verification transcript the Python app has.

**G6 — Packaging.** `.app` with hand-written Info.plist
(`NSMicrophoneUsageDescription` is load-bearing — macOS kills, not prompts,
without it), icns reuse, libsoxr dylib copied in with `install_name_tool`
rpath fix (skipped if R1 chose pure Go), `--sign` hook. Exit: bundle passes
`--selftest`, runs from Finder, mic prompt appears, expected size ~20–40 MB
(vs 110 MB Python).

Feature policy during the port: **freeze**. Bug fixes found in either
implementation land in Python first (it ships) and are mirrored into the port
in the same session, so the spec never forks.

---

## 11. Risks

| Risk | Odds | Mitigation |
|---|---|---|
| ~~Fyne Korean rendering unacceptable~~ | **closed** | F1 measured: renders with the bundled font, untouched |
| Fyne native-menu gaps (roles, check-state) | low–medium | F2 spike; fall back to in-window menu on macOS (Windows behaviour anyway) |
| Windows full-screen not covering the taskbar | low (documented GLFW behaviour) | cannot be tested from this Mac; first item of the Windows phase, on real hardware |
| genai Go SDK "Preview" surface changes under us | medium over time | pin v1.67.0; L1 smoke re-run on every bump; raw-WS escape hatch documented. L1 proved the surface works, so a regression is now detectable in one command |
| malgo device quirks vs PortAudio (X3!) | low–medium | A1 spike uses the exact device that bit us before — **still to be run**, the X3 was not connected on 2026-08-05 |
| libsoxr bundling friction | low | rpath fix in build script; R1's pure-Go fallback, now measured rather than assumed |
| Unsigned Go binary re-prompts Keychain after every rebuild | certain, dev-only | known annoyance; documented; goes away with codesigning |
| cgo + `-race` + three native callbacks | low | race detector in CI from G1; callback code allocation-free and lock-free by rule |
| Fyne needs main goroutine; malgo/genai callbacks elsewhere | by design | §4 rules; dispatcher is the only UI writer |

---

## 12. Parity gate — definition of "equivalent"

The port ships (and the Python app is frozen in place per §13) only when
every line here checks:

- [ ] All §8 test ports green, `-race`, offline.
- [ ] Live: KO→EN and EN→KO sessions, direction switch mid-session keeps
      audio + transcript (same file, switch noted).
- [ ] 30-minute endurance: ≥ 3 seamless reconnects, no lost sentences.
- [ ] Keys `0` `1` `2` `9` and every menu item behave identically.
- [ ] Complete-phrases mode: nothing unsettled ever drawn; turn-complete,
      sentence-end (with 20-char guard), idle settle all observed live.
- [ ] Paced release: five-sentence burst shows ≥ 4 lines (the measured bar).
- [ ] Clear-on-silence: drained + quiet + 2 s; 15 s ceiling; never clears
      under active speech.
- [ ] Preferences: all six tabs; Apply/OK/Cancel contract incl. key restore;
      key test; level meter (X3 shows real levels); speed test verdicts.
- [ ] Reads the **existing** `credentials.enc` and Keychain entry; env var
      precedence; source label correct.
- [ ] Settings imported from the plist once; subsequent runs use JSON.
- [ ] Transcript byte-format identical (golden-file diff vs a Python-written
      transcript of the same fragment script).
- [ ] Shutdown from all four routes mid-session: device released, footer
      written, no orphan goroutines (verified with pprof goroutine dump).
- [ ] Display-sleep assertion appears/disappears in `pmset -g assertions`.
- [ ] `.app` runs from Finder on this Mac; `--selftest` PASS; mic permission
      prompt appears with the right description string.
- [ ] `spikes/RESULTS.md` updated with Go measurements alongside Python's.

At cutover: `CLAUDE.md` gains a Go section (SDK name, commands, layout),
`README.md` swaps the architecture/dev sections, and the first Go release is
**1.0.0** (decided — see §13).

---

## 13. Decisions (Dennis, 2026-08-05)

The three questions this plan was reviewed against, now closed:

1. **UI toolkit: Fyne.** Decided by a requirement stated at review: full
   screen must be able to cover the title bar, menu and taskbar **on
   Windows**. That eliminates DarwinKit outright (macOS-only) and settles §3.1;
   the spikes F1/F2 remain, but their failure modes now have in-toolkit fixes
   rather than a toolkit change.
2. **Cutover version: 1.0.0.** The first Go release that passes the parity
   gate (§12) ships as LiveCap 1.0.0.
3. **Python code: frozen, preserved.** After cutover, `src/` and `tests/`
   stay in the repository untouched as the executable specification. No
   feature or fix lands there after the parity gate passes; until the gate,
   the freeze-and-mirror policy of §10 applies.
