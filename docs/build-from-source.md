# Building LiveCap from source on a new Mac

From `git clone` to captions on screen, step by step. Allow 20–30 minutes,
most of it spent waiting for downloads.

> **Just want to use it?** Download the ready-made app from
> [Releases](https://github.com/dennis2lee/live-translation/releases) and
> follow [setup-macos.md](setup-macos.md) or [setup-windows.md](setup-windows.md)
> instead. Building from source is for working on the code, or for running
> the very latest `main`.
>
> **On Windows?** There is no native Windows build from source. The Windows
> installer is cross-built on a Mac (`./packaging/build_windows.sh`). Use the
> `.msi` from Releases.

Building it yourself for your own noncommercial use is allowed. Distributing
the result, or a changed version, is not — see [LICENSE](../LICENSE).

## What you need

| | |
|---|---|
| **A Mac with Apple silicon** | M1 or later. The build links against Homebrew's libraries under `/opt/homebrew`. |
| **macOS 26 or later** | The app is built for the macOS it is built on, and the release targets 26. |
| **An administrator account** | For installing the developer tools and Homebrew. |
| **An API key, with billing** | Google Gemini or OpenAI. See step 7. |
| **About 3 GB of disk** | Mostly the Xcode command line tools and Go's module cache. |

---

## Step 1 — Install the Xcode command line tools

LiveCap uses C libraries (the audio device and the resampler) through cgo,
so it needs Apple's compiler.

```bash
xcode-select --install
```

A dialog appears. Click **Install** and wait for it to finish. If it says the
tools are already installed, carry on.

## Step 2 — Install Homebrew

If `brew --version` already prints a version, skip this step.

```bash
/bin/bash -c "$(curl -fsSL https://raw.githubusercontent.com/Homebrew/install/HEAD/install.sh)"
```

At the end the installer prints two `echo … >> ~/.zprofile` and `eval …`
lines. **Run them**, or open a new Terminal window, so that `brew` is on your
`PATH`. Check:

```bash
brew --version
```

## Step 3 — Install Go, libsoxr and git

```bash
brew install go libsoxr git
```

- **go** — the project needs Go 1.26.5 or newer (`go.mod`). If Homebrew's Go
  is older, Go downloads the right toolchain by itself the first time you
  build.
- **libsoxr** — the high-quality resampler LiveCap uses on macOS. The build
  looks for it at `/opt/homebrew/opt/libsoxr`.
- **git** — macOS ships one with the command line tools; Homebrew's is newer.

Check:

```bash
go version
ls /opt/homebrew/opt/libsoxr/lib/libsoxr*.dylib
```

## Step 4 — Get the source

```bash
mkdir -p ~/work && cd ~/work
git clone https://github.com/dennis2lee/live-translation.git
cd live-translation/src_go
```

Everything from here on runs in `live-translation/src_go`.

## Step 5 — Check the build works

Download the dependencies and run the test suite. It runs entirely offline —
no key and no network are needed after the download — and takes a few
minutes the first time.

```bash
go mod download
go test ./...
```

Every line should end in `ok` (or `no test files`). A few music-filter tests
skip because the classifier model is not downloaded; that is expected (see
*Optional* below).

## Step 6 — Build the app

```bash
./packaging/build_app.sh
```

This builds `dist/LiveCap.app`, copies libsoxr into it so it does not depend
on Homebrew's path, signs it ad hoc, and runs its self-test.

**On a new machine the self-test ends with `FAIL api key` and `selftest
failed`.** That is expected — there is no key on this Mac yet. The app was
still built; everything above the key line should say `ok`, in particular
`resampler`, `audio input` and `live api reachable`.

Install it:

```bash
cp -R dist/LiveCap.app /Applications/
```

Because you built it on this Mac, macOS opens it without the "cannot verify
the developer" warning that a downloaded copy gets.

> **Running without building a bundle.** `./run.sh` runs straight from
> source, which is quicker while working on the code. It is not how to use the
> app day to day: macOS attributes the microphone to your Terminal rather than
> to LiveCap, and the menu bar shows the binary's name instead of "LiveCap".

## Step 7 — Get an API key

Pick one provider; you can switch later in Preferences.

- **Google Gemini** (the default): <https://aistudio.google.com/apikey>.
  Create a key (it starts with `AIza`) and **turn on billing for the same
  project** — the translation model is not on the free tier.
- **OpenAI**: <https://platform.openai.com/api-keys>. Create a key (it starts
  with `sk-`) and add credit under **Settings → Billing**.

Both cost about $0.035 per minute of translation.

## Step 8 — First run

1. Open **LiveCap** from Applications.
2. **LiveCap → Preferences…** (⌘,) → **API** tab: choose the **Provider**,
   paste the key, click **Test Key** (you should see **Key accepted.**), then
   **OK**. The key is stored in an encrypted file under
   `~/Library/Application Support/LiveCap`, never in the source tree.
3. **Input** tab: pick your microphone and click **Start Level Test** to see
   the meter move.
4. Close Preferences and press **0** to start translating. Allow the
   microphone when macOS asks.
5. Speak Korean; English captions appear within a few seconds. **1** and
   **2** switch direction, **m** mutes, **f** goes full screen.

To confirm the key from the command line as well, rerun the self-test — it
should now pass:

```bash
/Applications/LiveCap.app/Contents/MacOS/LiveCap --selftest
```

The day-to-day details — keys, the status dot, speaking the translation, the
music filter — are in [setup-macos.md](setup-macos.md) from step 7 on.

---

## Updating to a newer version

```bash
cd ~/work/live-translation
git pull
cd src_go
go test ./...
./packaging/build_app.sh
rm -rf /Applications/LiveCap.app && cp -R dist/LiveCap.app /Applications/
```

Settings and keys live outside the app, so replacing it keeps them.

## Optional

- **Music-filter tests.** Fetch the classifier model so its tests run instead
  of skipping (the app itself downloads its own copy from Preferences →
  Filter):

  ```bash
  ./spikes/fixtures/fetch_yamnet.sh
  ```

- **The race detector**, as the release checklist runs it:

  ```bash
  go test -race ./...
  ```

- **The Windows installer**, cross-built from this Mac:

  ```bash
  brew install mingw-w64 msitools
  ./packaging/build_windows.sh
  ```

  It writes `dist/windows/LiveCap-<version>-x64.msi`.

## Troubleshooting

| What you see | What to do |
|---|---|
| `xcrun: error: invalid active developer path` | Step 1 — the command line tools are missing. |
| `fatal error: 'soxr.h' file not found` or `library 'soxr' not found` | `brew install libsoxr` (step 3). On an Intel Mac Homebrew lives in `/usr/local`; set `CGO_CFLAGS=-I/usr/local/opt/libsoxr/include CGO_LDFLAGS=-L/usr/local/opt/libsoxr/lib` before building — but the shipped build targets Apple silicon only. |
| `go: go.mod requires go >= 1.26.5` and no download | Your network blocks Go's toolchain download. `brew upgrade go`, or install the newest Go from <https://go.dev/dl/>. |
| `brew: command not found` after installing Homebrew | Run the two lines the installer printed at the end, or open a new Terminal window. |
| Self-test `FAIL api key` | Expected before step 8. Enter the key in Preferences, then rerun the self-test. |
| `cp: /Applications/LiveCap.app: Permission denied` | Your account is not an administrator. Copy it into `~/Applications` instead, or ask an administrator. |
| Green dot but no captions | System Settings → Privacy & Security → **Microphone**: turn LiveCap on, then restart it. |
