# LiveCapGo

The Go port of LiveCap. It is called **LiveCapGo** while the Python build is
still the one that ships: both install as an application with the same name and
the same caption window, and they were mistaken for each other more than once
during the port. The name goes back to LiveCap at the cutover.

Real-time speech translation captions. Korean → English by default, English →
Korean on demand. The engine is the Google Gemini Live API,
`gemini-3.5-live-translate-preview`.

This is the Go port of the Python application in `../src`. The plan it follows,
and the reasoning behind every stack choice, is in [PLAN.md](PLAN.md); what was
actually measured on this machine is in [spikes/RESULTS.md](spikes/RESULTS.md).

## Running it

```bash
./run.sh                     # run from source
go test ./...                # tests
go test -race ./...          # the same, with the race detector
go vet ./... && gofmt -l .
./packaging/build_app.sh     # assemble dist/LiveCapGo.app
./packaging/build_windows.sh # cross-build the .exe and the .msi
```

The Windows build is produced from macOS and needs
`brew install mingw-w64 msitools`. It cross-builds and the MSI is structurally
correct, but nothing here can run it — see `spikes/RESULTS.md`.

`--selftest` checks everything the application needs — settings path, key,
resampler, capture devices, reachability of the API — and exits:

```bash
go run ./cmd/livecap --selftest
```

## Keys and menus

| Key | Does |
|---|---|
| `0` | start or stop translating |
| `1` | Korean→English |
| `2` | English→Korean |
| `9` | complete phrases only |

The menu bar carries the same commands. Start and Stop are separate items so a
mis-click cannot stop a live session; `0` is the shortcut for people who want
one. Preferences lives in the **Settings** menu rather than the application
menu, because Fyne builds the application menu itself and offers no hook for the
conventional ⌘, slot.

## Layout

```
cmd/livecap/          flags, wiring, signal handling
internal/
  audio/              device enumeration, capture, the ring buffer
  captions/           fragments to display lines — no UI, no network
  config/             settings schema, JSON persistence, API-key lookup
  credentials/        the encrypted store, byte-compatible with the Python build
  netcheck/           latency, upload and download against what a session needs
  platform/           the only package that branches on the operating system
  resample/           libsoxr through cgo
  transcript/         the Markdown transcript
  translator/         the Live API: supervisor, sender, receivers
  ui/                 caption view, menus, Preferences, the window
packaging/            .app assembly, Info.plist, entitlements
spikes/               the G0 measurements, kept runnable
```

## Requirements

- Go 1.26+, macOS 12+.
- `libsoxr` from Homebrew (`brew install libsoxr`), for the macOS build only.
  The build script copies it into the bundle and repoints the binary at the
  copy, so an installed application does not need Homebrew. The Windows build
  uses the pure-Go filter and needs nothing.
- A Gemini API key, entered in Preferences. It is never written to the settings
  file and never logged.

## Notes that cost time to learn

- The target language code must be bare — `"en"`, not `"en-US"`. The API rejects
  the region-qualified form, and rejects it a second *after* `setupComplete`, so
  it looks like an SDK fault.
- `setupComplete` proves nothing: it arrives before the server has looked at any
  audio.
- Fyne renders `"KO → EN"` as an arrow followed by a replacement glyph, while
  `"KO→EN"` is correct. No spaces around the arrow, anywhere.
- `NSMicrophoneUsageDescription` is load-bearing. Without it macOS kills the
  process on first capture rather than prompting.
- A bare Go binary has no `CFBundleName`, so the application menu cannot show
  the right name until the bundle exists.

## Licence

Proprietary. Copyright © 2026 Pyeongwoo Lee. All rights reserved.
