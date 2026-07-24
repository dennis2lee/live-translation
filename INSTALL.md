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

```bash
cp .env.example .env
```

Edit `.env` and set your key:

```
GEMINI_API_KEY=your-key-here
```

- `.env` is gitignored and will not be committed
- Never put a real key in `.env.example` — that file *is* committed
- Leaving the placeholder text in place is treated as "no key"

The key is looked up in this order:

| Order | Source |
|---|---|
| 1 | `GEMINI_API_KEY` / `GOOGLE_API_KEY` environment variable |
| 2 | `.env` in the repository root |
| 3 | macOS Keychain (`LiveCap` / `gemini-api-key`) |
| 4 | Entered in **Preferences…** (⌘,), then stored in the Keychain |

## 7. Run

```bash
cd ~/work/live-translation && ./run.sh
```

- macOS will ask for **microphone permission** the first time — allow it
- If you deny it, captions never appear and the indicator stays grey
- Grant it later under System Settings → Privacy & Security → Microphone

Press `0` to start translating, or use **Translate → Start Translating** (⌘R).

## 8. Verify the install

```bash
cd ~/work/live-translation && .venv/bin/python -m pytest -q
```

- 181 tests should pass; none of them need an API key or network access
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

Confirm the API key reaches the service (costs a fraction of a cent):

```bash
cd ~/work/live-translation && .venv/bin/python -c "
import sys; sys.path.insert(0,'src')
from google import genai
from livecap.config import load_api_key
from livecap.translator import MODEL
names=[m.name for m in genai.Client(api_key=load_api_key()).models.list()]
print('model reachable:', any(MODEL in n for n in names))
"
```

## 9. Optional — test audio fixtures

The spike scripts can synthesise their own speech using the macOS TTS voices,
which gives you a known script to compare transcripts against.

```bash
cd ~/work/live-translation && .venv/bin/python spikes/make_tts_fixture.py --repeat 20 --out spikes/fixtures/ko_30min.wav
```

- Requires the Korean voice `Yuna` (English uses `Samantha`)
- Install voices under System Settings → Accessibility → Spoken Content → System Voice → Manage Voices
- List what you have with `say -v '?'`

## Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| `ModuleNotFoundError: No module named 'livecap'` | Package not installed into the venv | `.venv/bin/python -m pip install -e .` |
| `Your prepayment credits are depleted` | No billing on the key's project | Attach billing, confirm key and billing share a project |
| `API key required` in the caption area | Key not found | Check `.env`; placeholder text counts as missing |
| Indicator stays grey, no captions | Microphone permission denied | System Settings → Privacy & Security → Microphone |
| `No audio input device found` | No CoreAudio input device | Plug in an interface; re-open the device menu to rescan |
| USB interface missing from the menu | Menu was built before you plugged it in | Close and re-open the menu — it rescans each time |
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
