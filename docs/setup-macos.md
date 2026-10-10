# Setting up LiveCap on a Mac

A step-by-step guide for a first install. Allow about 15 minutes, most of it
spent getting an API key.

> **Using Windows?** See [setup-windows.md](setup-windows.md).

## What you need

| | |
|---|---|
| **A Mac with Apple silicon** | M1 or later. The download is built for arm64 only, so Intel Macs cannot run it. |
| **macOS 13 or later** | Tested on macOS 26.5. |
| **A microphone or audio interface** | The built-in microphone works. A USB interface fed from a mixer works best for events. |
| **An internet connection** | About 0.3 Mbit/s upload while translating. |
| **An API key, with billing** | From Google (Gemini) or OpenAI. Step 4 covers this. Translation costs about **$0.035 per minute** of use. |
| **Access to the GitHub repository** | It is private, so you need to be invited before you can download. |

---

## Step 1 — Download

1. Open the repository's **Releases** page on GitHub:
   `https://github.com/dennis2lee/live-translation/releases`
2. Under the newest release, click **`LiveCap-<version>-macos-arm64.zip`**.
3. Your browser saves it to **Downloads**.

> The release notes list a SHA-256 checksum for each file. If you want to
> check the download, run `shasum -a 256 ~/Downloads/LiveCap-*-macos-arm64.zip`
> in Terminal and compare.

## Step 2 — Install

1. Double-click the zip in **Downloads**. **LiveCap.app** appears next to it.
2. Drag **LiveCap.app** into **Applications**.

## Step 3 — Open it the first time

LiveCap is not notarized by Apple, so the first launch is blocked once. You
only have to allow it once.

1. Double-click **LiveCap** in Applications. macOS says it *cannot verify the
   developer*. Click **Done** (or **Cancel**).
2. Open **System Settings → Privacy & Security**.
3. Scroll down to the message about **"LiveCap" was blocked** and click
   **Open Anyway**. Confirm with your password or Touch ID.
4. LiveCap opens: a black window with an empty caption box at the bottom.

> **Shortcut for Terminal users:** the same thing in one command.
>
> ```bash
> xattr -dr com.apple.quarantine /Applications/LiveCap.app
> ```

## Step 4 — Get an API key

LiveCap does the translating through a cloud model, and you pay the provider
directly for what you use. Pick **one** provider. You can switch at any time
in Preferences.

### Option A — Google Gemini (the default)

1. Go to <https://aistudio.google.com/apikey> and sign in with a Google account.
2. Click **Create API key** and copy it. It starts with `AIza`.
3. **Turn on billing.** The translation model is not on the free tier, so a key
   without billing fails with *"credits are depleted"*.
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
| Spoken voice | changes at every reconnect | follows the speaker's voice |
| Name and jargon hints (Words tab) | yes | no |
| Cost | ~$0.037 / min | ~$0.034 / min |

Captions work the same with either.

> **Set a budget alert** with your provider. A 30-minute session costs about
> $1.10.

## Step 5 — Enter the key

1. In LiveCap, open **LiveCap → Preferences…** (or press **⌘,**).
2. On the **API** tab, set **Provider** to the one you chose.
3. Paste the key into the key field.
4. Click **Test Key**. You should see **Key accepted.**
   - OpenAI's test cannot see your credit balance, so it also says so. Make
     sure you added credit in step 4.
5. Optionally tick **Also store in the macOS Keychain**.
6. Click **OK**.

The key is saved encrypted on your Mac and is never written to a settings file
or a log.

## Step 6 — Choose your audio input

1. Open **Preferences… → Input**.
2. Pick your microphone or interface from the list. **System Default Input**
   follows whatever macOS uses.
3. Click **Start Level Test** and speak. The meter should move, and the reading
   should say **good**.
   - Around **−12 dBFS** at your loudest is ideal.
   - A reading near **−88 dBFS** means nothing is reaching that input. Check
     the cable, the mixer send, or pick another device.
4. Click **OK**.

You can also change the input later from **Settings → Audio Input**.

## Step 7 — Start translating

1. Press **0** (zero), or choose **Translate → Start Translating** (**⌘R**).
2. The first time, macOS asks to let LiveCap use the **microphone**. Click
   **Allow**.
3. The small dot at the bottom-right of the caption box blinks green while it
   connects, then stays **green**.
4. Speak Korean. English captions appear within a few seconds.

| Key | What it does |
|---|---|
| **0** | Start or stop translating |
| **1** | Korean → English |
| **2** | English → Korean |
| **9** | Show only finished phrases (steadier, a beat later) |
| **f** | Full screen on and off |
| **m** | Mute the spoken translation |
| **⌘,** | Preferences |

**Translate → Clear Captions** empties the caption box.

The keys work with the Korean input source too.

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

- **Model's voice**: the cloud model's own speech.
- **A voice on this Mac**: read aloud by macOS, which keeps the same voice all
  session. Click **Preview** to hear it. To add voices, go to
  **System Settings → Accessibility → Spoken Content → System Voice → Manage
  Voices**.

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

Every session is saved as a Markdown file in **Documents → LiveCap**, named
after the time it started. Open the folder from **Translate → Open Transcript
Folder**. Turn it off under **Translate → Save Transcript**.

### Long sessions

The provider replaces a session about every nine minutes. LiveCap prepares the
new session in advance and switches over in a pause, so you should not notice.
This is **Preferences → Behaviour → Warm session handover**, on by default.

---

## Troubleshooting

| What you see | What to do |
|---|---|
| *"LiveCap" cannot be opened* / *cannot verify the developer* | Step 3: System Settings → Privacy & Security → **Open Anyway**. |
| The app will not start at all on an Intel Mac | The build is Apple silicon only. |
| *API key required — enter it in Preferences* | Step 5. The key must be for the provider selected on the API tab. |
| *Billing credits exhausted* or *Billing required* (Gemini) | Turn on billing for the **same project** as the key. |
| *OpenAI credit exhausted* | Add credit under Settings → Billing on platform.openai.com. |
| *API key is not valid* | Copy the key again. No spaces before or after it. |
| Green dot, but no captions | macOS is blocking the microphone: **System Settings → Privacy & Security → Microphone**, turn LiveCap on, then restart LiveCap. Also run the level test (step 6). |
| My USB interface is not in the list | Plug it in, then **Settings → Audio Input → Refresh Device List**. |
| Captions are too small | **Settings → Font Size**, or Preferences → Captions. |
| The translation is echoed and repeated | The speakers are feeding the microphone. Use headphones, or turn off **Speak the translation aloud**. |
| Anything else | **Preferences → Behaviour** shows where the log file is. Include it when reporting a problem. It never contains your key. |

## Uninstalling

1. Quit LiveCap and drag it from **Applications** to the Bin.
2. To remove settings, the saved key and the log as well, delete
   `~/Library/Application Support/LiveCap`.
3. If you ticked the Keychain option, open **Keychain Access**, search for
   **LiveCap** and delete the entries.

Transcripts in **Documents → LiveCap** are left alone. Delete them yourself if
you want them gone.
