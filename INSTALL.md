# Installation Guide

Setting LiveCap up on a fresh Mac.

## 1. Requirements

| Item | Requirement | Notes |
|---|---|---|
| OS | macOS 13+ | Verified on macOS 26.5 (arm64) |
| Python | 3.12 or newer | Verified on 3.14 |
| Gemini API key | Required, **paid tier** | The model does not run on the free tier |
| Network | Outbound WebSocket (443) | Live API streams over WebSocket |
| Audio input | Any CoreAudio input device | USB interface, built-in mic, etc. |
| Disk | ~600 MB | Mostly PyQt6 and Qt frameworks |

**You do not need to install PortAudio.** The `sounddevice` wheel bundles
`libportaudio.dylib`. No Homebrew audio packages are required.

Xcode / command line tools are not required either — every dependency ships as
a prebuilt wheel for macOS arm64.

## 2. Install Python 3.12+

Check what you have:

```bash
python3 --version
```

If it is older than 3.12, install a newer one:

```bash
brew install python@3.14
```

- If you do not have Homebrew, get it from https://brew.sh
- Alternatively use the official installer from https://www.python.org/downloads/macos/

## 3. Get the code

```bash
git clone https://github.com/dennis2lee/live-translation.git ~/work/live-translation
```

The repository is private, so `git` needs your GitHub credentials:

- With the GitHub CLI: run `gh auth login` once, then clone
- Or use an SSH remote if you have a key registered

## 4. Create the virtual environment and install

```bash
cd ~/work/live-translation && python3 -m venv .venv && .venv/bin/python -m pip install -r requirements.txt -r requirements-dev.txt && .venv/bin/python -m pip install -e .
```

- `requirements.txt` pins every runtime dependency to a verified version
- `requirements-dev.txt` adds `pytest` and `ruff`
- `pip install -e .` puts the `livecap` package on the path so `python -m livecap.app` works

> `./run.sh` performs this install automatically the first time it cannot
> import `livecap`, so you can skip this step and just run the app.

## 5. Get a Gemini API key

- Go to https://aistudio.google.com/apikey and sign in
- Accept the terms — a default Google Cloud project is created for you
- Click **Create API key** and copy it

**Billing is mandatory.** `gemini-3.5-live-translate-preview` is not included
in the free tier. Without a payment method you will see:

```
Your prepayment credits are depleted.
```

- Attach billing from the AI Studio key page, or at https://console.cloud.google.com/billing
- The key and the billing account must belong to the **same project** — this is the most common mistake
- Set a budget alert: the app costs roughly **$0.037/min** (~$1.10 per 30 minutes)

## 6. Configure the key

Everything is configured in the app. Start it, open **Preferences… (⌘,) → API
Key**, paste the key, and press **Test Key** to confirm it works before you
close the dialog.

- The key is encrypted and written to `~/Library/Application Support/LiveCap/credentials.enc`, mode 0600
- **Test Key** lists the models the key can reach — it is free and does not open a session
- Tick **Also store in the macOS Keychain** if you would rather rely on the Keychain

There is no `.env` file. An older install that has one will have its key
imported into the encrypted store automatically, once, at the next launch;
after that the file is ignored and can be deleted.

The key is looked up in this order:

| Order | Source |
|---|---|
| 1 | `GEMINI_API_KEY` / `GOOGLE_API_KEY` environment variable |
| 2 | Encrypted store (written by Preferences) |
| 3 | macOS Keychain (`LiveCap` / `gemini-api-key`) |

> **On the encryption.** The passphrase is part of the application, so anyone
> holding a copy can decrypt the file. It keeps the key out of plain sight —
> out of backups, screen shares and stray `cat` commands — but it is not a
> substitute for the Keychain, which your login password protects.

## 7. Run

```bash
cd ~/work/live-translation && ./run.sh
```

- macOS will ask for **microphone permission** the first time — allow it
- If you deny it the app still connects, so the dot turns green, but the input
  is silence and no captions ever appear
- Grant it later under System Settings → Privacy & Security → Microphone

Press `0` to start translating, or use **Translate → Start Translating** (⌘R).

## 8. Verify the install

```bash
cd ~/work/live-translation && .venv/bin/python -m pytest -q
```

- Everything should pass; no test needs an API key or network access
- Lint with `.venv/bin/python -m ruff check .`

Check that your audio hardware is seen:

```bash
cd ~/work/live-translation && .venv/bin/python spikes/s1_capture_check.py --list
```

Record 60 seconds and confirm the capture pipeline end to end:

```bash
cd ~/work/live-translation && .venv/bin/python spikes/s1_capture_check.py --seconds 60
```

- Drift must stay under 1% and underruns must be 0
- Play the resulting `capture.wav` and listen for clicks, dropouts, or wrong pitch

Confirm the API key reaches the service. Listing models is free — it does not
open a Live session, so nothing is billed:

```bash
cd ~/work/live-translation && .venv/bin/python -c "
import sys; sys.path.insert(0,'src')
from google import genai
from livecap.config import load_api_key
from livecap.translator import MODEL
client = genai.Client(api_key=load_api_key())
names = [m.name for m in client.models.list()]
print('model reachable:', any(MODEL in n for n in names))
"
```

Expect `model reachable: True`. Keep the client in a variable as shown — the
result is a lazy pager, and inlining the client closes it mid-iteration.

## 9. Optional — test audio fixtures

The spike scripts can synthesise their own speech using the macOS TTS voices,
which gives you a known script to compare transcripts against.

```bash
cd ~/work/live-translation && .venv/bin/python spikes/make_tts_fixture.py --repeat 20 --out spikes/fixtures/ko_30min.wav
```

- Requires the Korean voice `Yuna` (English uses `Samantha`)
- Install voices under System Settings → Accessibility → Spoken Content → System Voice → Manage Voices
- List what you have with `say -v '?'`

## 10. Building a distributable .app

Everything above installs from source. To hand LiveCap to someone who has no
Python at all, build a bundle:

```bash
cd ~/work/live-translation && packaging/build_app.sh
```

- Produces `dist/LiveCap.app`, roughly 110 MB, arm64
- Regenerates the icon, builds with PyInstaller, then runs the bundle's own self-test
- The self-test runs from `/tmp`, so it reflects a machine with no repository present

The bundle carries its own Python, Qt, `libportaudio` and the soxr extension.
The receiving Mac needs nothing installed.

Check any build with:

```bash
dist/LiveCap.app/Contents/MacOS/LiveCap --selftest
```

```
  ok    audio backend — 4 input device(s), first 'MacBook Air Microphone'
  ok    resampler — soxr 1.1.0, 0 samples from a 20 ms block
  ok    Qt — Qt 6.11.0
  ok    Gemini SDK — google-genai types resolve
  ok    Keychain — backend Keyring
  ok    credentials — no API key (set one in Preferences)
```

`--selftest` never opens a window and never calls the paid API.

### Where a packaged app looks for the key

The repository root does not exist inside a bundle, so the order differs:

| Order | Source |
|---|---|
| 1 | `GEMINI_API_KEY` / `GOOGLE_API_KEY` environment variable |
| 2 | `~/Library/Application Support/LiveCap/credentials.enc` |
| 3 | macOS Keychain |

The bundle needs no extra files beside it. For a recipient,
**Preferences… (⌘,) → API Key** is the whole setup: paste, **Test Key**, done.

### Signing and notarisation

An unsigned build runs on the machine that built it. Gatekeeper blocks it
anywhere else.

```bash
export LIVECAP_SIGN_IDENTITY="Developer ID Application: Your Name (TEAMID)"
packaging/build_app.sh --sign
```

- Requires a paid Apple Developer account and a Developer ID Application certificate
- `packaging/entitlements.plist` already carries what the hardened runtime needs: audio input, plus the JIT and unsigned-memory entitlements CPython requires
- The script prints the `notarytool` commands to run afterwards

Without an Apple Developer account, the recipient can still open it by
right-clicking → Open, or via System Settings → Privacy & Security → Open Anyway.

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ModuleNotFoundError: No module named 'livecap'` | Package not installed into the venv | `.venv/bin/python -m pip install -e .` |
| `Your prepayment credits are depleted` | No billing on the key's project | Attach billing, confirm key and billing share a project |
| `API key required` in the caption area | Key not found | Preferences → API Key, then **Test Key**; placeholder text counts as missing |
| Dot is green but no captions appear | Microphone permission denied — the stream delivers silence | System Settings → Privacy & Security → Microphone |
| Dot turns red saying "Session keeps dropping" | The service accepted the connection then closed it repeatedly | Check the network, then retry; LiveCap backs off rather than reconnecting in a loop |
| `No audio input device found` | No CoreAudio input device | Plug in an interface; re-open the device menu to rescan |
| USB interface missing from the menu | Menu was built before you plugged it in | Close and re-open the menu — it rescans each time |
| Connected and green, but no captions ever | The device streams, but nothing is routed into its input — `no signal from <device>` appears in the log | Check the physical input, its gain, and that macOS shows the level moving under System Settings → Sound → Input |
| A USB DAC or headphone amp captures nothing | Its input is the line/mic jack, not what the Mac is playing | Plug a source into the jack; to capture system audio you need a loopback device instead |
| Captions look tiny on a 4K display | Font size is in pixels, not relative | Settings → Font Size → 72 or 96 |
| New defaults do not take effect | Saved settings win over code defaults | Settings → Reset to Defaults, or `defaults delete com.livecap.LiveCap` while the app is closed |

## Uninstall

```bash
rm -rf ~/work/live-translation
defaults delete com.livecap.LiveCap
security delete-generic-password -s LiveCap -a gemini-api-key
```

- The first line removes the code and the virtual environment
- The second clears saved preferences
- The third removes the Keychain entry (skip it if you never stored a key there)
- Saved transcripts in `~/Documents/LiveCap` are left alone — delete them yourself if you want them gone
