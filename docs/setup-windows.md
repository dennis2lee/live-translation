# Setting up LiveCap on Windows

A step-by-step guide for a first install. Allow about 15 minutes, most of it
spent getting an API key.

> **Using a Mac?** See [setup-macos.md](setup-macos.md).

> **Please read first.** LiveCap has been installed and run on a real Windows
> PC (August 2026, version 1.0), but the newer features — the OpenAI
> provider, warm session handover and the local Windows voices — **have not
> been run on Windows yet.** If something in this guide does not match what you
> see, that is worth reporting — please include the log file (see
> *Troubleshooting*).

## What you need

| | |
|---|---|
| **Windows 10 or 11, 64-bit (x64)** | ARM-based Windows PCs are not supported. |
| **A microphone or audio interface** | A headset or USB microphone works. A USB interface fed from a mixer works best for events. |
| **An internet connection** | About 0.3 Mbit/s upload while translating with Gemini, 0.5 Mbit/s with OpenAI. |
| **An API key, with billing** | From Google (Gemini) or OpenAI. Step 4 covers this. Translation costs about **$0.035 per minute** of use (see step 4). |
| **Administrator rights** | The installer installs for all users. |

---

## Step 1 — Download

1. Open the repository's **Releases** page on GitHub:
   `https://github.com/dennis2lee/live-translation/releases`
2. Under the newest release, click **`LiveCap-<version>-x64.msi`**.
3. If your browser warns that the file *isn't commonly downloaded*, choose
   **Keep** (in Edge: **⋯ → Keep → Keep anyway**).

> The release notes list a SHA-256 checksum for each file. To check the
> download, run this in PowerShell and compare:
>
> ```powershell
> Get-FileHash $HOME\Downloads\LiveCap-*-x64.msi -Algorithm SHA256
> ```

## Step 2 — Install

1. Double-click the `.msi` file.
2. LiveCap is not code-signed, so Windows may show **"Windows protected your
   PC"**. Click **More info**, then **Run anyway**.
3. Approve the administrator prompt.
4. The installer puts LiveCap in `C:\Program Files\LiveCap` and adds a
   **LiveCap** entry to the Start menu and a shortcut on the desktop.

## Step 3 — Allow microphone access

Windows blocks desktop apps from the microphone unless this is switched on.
Do it now, before the first start.

1. Open **Settings → Privacy & security → Microphone**
   (Windows 10: **Settings → Privacy → Microphone**).
2. Turn on **Microphone access**.
3. Turn on **Let desktop apps access your microphone**.

If this is off, LiveCap connects normally (the dot turns green) but hears only
silence, so no captions ever appear.

## Step 4 — Get an API key

LiveCap does the translating through a cloud model, and you pay the provider
directly for what you use. Pick **one** provider. You can switch at any time
in Preferences.

### Option A — Google Gemini (the default)

1. Go to <https://aistudio.google.com/apikey> and sign in with a Google account.
2. Click **Create API key** and copy it. It starts with `AIza`.
3. **Turn on billing.** The translation model is not on the free tier, so a key
   without billing stops with *"Billing credits exhausted"* or *"Billing
   required"* in the caption box.
   - In AI Studio, open the key's project and set up billing, or go to
     <https://console.cloud.google.com/billing>.
   - The key and the billing account must be in the **same project**. This is
     the most common mistake.

### Option B — OpenAI

1. Go to <https://platform.openai.com/api-keys> and sign in.
2. Click **Create new secret key** and copy it. It starts with `sk-`.
3. **Add credit** under **Settings → Billing**. Without credit, the key is
   accepted but every session is refused.

**Which to choose?**

| | Gemini | OpenAI |
|---|---|---|
| First translated speech | 3–5 s after you start | under 1 s |
| Spoken voice | changes at every reconnect | follows the speaker's voice (not yet measured across reconnects) |
| Name and jargon hints (Words tab) | yes | no |
| Cost | ~$0.037 / min (measured) | $0.034 / min (OpenAI's list price) |

Captions work the same with either.

> **Set a budget alert** with your provider. A 30-minute Gemini session
> measured $1.11.

## Step 5 — Start LiveCap and enter the key

1. Start **LiveCap** from the Start menu or the desktop. A black window opens
   with an empty caption box at the bottom and a menu bar at the top
   (**Translate**, **Settings**, **Help**).
2. Open **Settings → Preferences…** (or press **Ctrl+,**).
3. On the **API** tab, set **Provider** to the one you chose.
4. Paste the key into the key field.
5. Click **Test Key**. You should see **Key accepted.**
   - OpenAI's test cannot see your credit balance, so it also says so. Make
     sure you added credit in step 4.
6. Optionally tick **Also store in the macOS Keychain**. On Windows this
   stores the key in **Windows Credential Manager** — the label has not been
   updated for Windows yet.
7. Click **OK**.

The key is kept in an encrypted file under your user profile and is never
written to a settings file or a log. The encryption only keeps it out of plain
sight — anyone with a copy of LiveCap can decrypt that file — so tick the
Keychain option (Windows Credential Manager) for real protection.

## Step 6 — Choose your audio input

1. Open **Settings → Preferences… → Input**.
2. Pick your microphone or interface from the list. **System Default Input**
   follows the Windows default recording device.
3. Click **Start Level Test** and speak. The meter should move, and the reading
   should say **good**.
   - Around **−12 dBFS** at your loudest is ideal.
   - A reading near **−88 dBFS** means nothing is reaching that input. Check
     step 3, the cable or mixer send, or pick another device.
4. Click **OK**.

You can also change the input later from **Settings → Audio Input**.

## Step 7 — Start translating

1. Press **0** (zero), or choose **Translate → Start Translating** (**Ctrl+R**).
2. The small dot at the bottom-right of the caption box blinks green while it
   connects, then stays **green**.
3. Speak Korean. English captions appear within a few seconds.

| Key | What it does |
|---|---|
| **0** | Start or stop translating |
| **1** | Korean → English |
| **2** | English → Korean |
| **9** | Show only finished phrases (steadier, a beat later) |
| **f** | Full screen on and off |
| **m** | Mute the spoken translation |
| **Ctrl+,** | Preferences |

The keys work with the Korean input method too.

> **In full screen the menu bar is hidden.** Press **f** to come back out.

**Translate → Clear Captions** empties the caption box.

**The status dot**

| Dot | Meaning |
|---|---|
| Grey | Idle |
| Green, blinking | Connecting |
| Green | Translating |
| Red | Error. The reason is printed in the caption box. |
| Left half deep red | The spoken translation is muted (**m**) |

## Step 8 — Make it look right

Everything about the captions is in **Preferences → Captions** and
**Colours**: font size, number of lines, box width, position (top, middle,
bottom), colours and opacity. The **Settings** menu has quick versions of the
common ones.

For a projector or a second screen, drag the window there and press **f**.

---

## Optional features

### Hear the translation spoken

**Preferences → Output → Speak the translation aloud**, then pick the output
device and a **Voice**:

- **Model voice**: the cloud model's own speech.
- **A voice on this PC**: read aloud by Windows, which keeps the same voice all
  session. Click **Preview** to hear it. *Not yet tested on Windows — please
  report whether your voices appear and Preview plays.* To add voices, go to
  **Settings → Time & language → Speech → Manage voices**.
  - The newer **natural voices** added for Narrator cannot be used by LiveCap.
    Voices from a language pack can.

> **Use headphones.** If the translation plays through speakers, the
> microphone hears it and translates it again.

### Skip music and singing

For services or events with songs, **Preferences → Filter**:

1. Click **Download Model (4 MB)** once.
2. Tick **Translate speech only — skip singing and music**.

A ♪ appears beside the status dot while music is being held back.

### Names and jargon (Gemini only)

**Preferences → Words** takes names and terms the model should expect to hear,
one per line, and how names should be spelled in the captions.

### Transcripts

Every session is saved as a Markdown file in **Documents\LiveCap**, named after
the time it started. Open the folder from **Translate → Open Transcript
Folder**. Turn it off under **Translate → Save Transcript**.

### Long sessions

Gemini replaces a session about every nine minutes, and LiveCap renews an
OpenAI session itself every 50. Either way LiveCap prepares the new session
in advance and switches over in a pause, so you should not notice.
This is **Preferences → Behaviour → Warm session handover**, on by default.

---

## Troubleshooting

| What you see | What to do |
|---|---|
| *Windows protected your PC* | Step 2: **More info → Run anyway**. |
| *Google Gemini API key required* or *OpenAI API key required — enter it in Preferences* | Step 5. The key must be for the provider selected on the API tab. |
| *Billing credits exhausted* or *Billing required* (Gemini) | Turn on billing for the **same project** as the key. |
| *OpenAI credit exhausted* | Add credit under Settings → Billing on platform.openai.com. |
| *API key is not valid* | Copy the key again. No spaces before or after it. |
| Green dot, but no captions | Microphone access is off for desktop apps — step 3 — then restart LiveCap. Also run the level test (step 6). |
| My USB interface is not in the list | Plug it in, then **Settings → Refresh Device List**. |
| Stuck in full screen, no menu | Press **f**. |
| A voice I installed is not in the Voice list | Only SAPI voices are visible. Narrator's natural voices are not. |
| The translation is echoed and repeated | The speakers are feeding the microphone. Use headphones, or turn off **Speak the translation aloud**. |
| Anything else | **Preferences → Behaviour** shows where the log file is (under `%APPDATA%\LiveCap`). Include it when reporting a problem. It never contains your key. |

## Uninstalling

1. Open **Settings → Apps → Installed apps**, find **LiveCap**, and choose
   **Uninstall**.
2. To remove settings, the saved key and the log as well, delete the folder
   `%APPDATA%\LiveCap` (paste that into the File Explorer address bar).
3. If you ticked the Keychain option, open **Credential Manager → Windows
   Credentials** and remove the **LiveCap** entries.

Transcripts in **Documents\LiveCap** are left alone. Delete them yourself if
you want them gone.
