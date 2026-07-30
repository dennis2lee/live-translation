# Measurement Results

Everything here was measured against the live API, not inferred. Code should
follow these numbers rather than re-deriving them.

- **Model** — `gemini-3.5-live-translate-preview`
- **Environment** — google-genai 2.14.0, Python 3.14, macOS 26.5 arm64
- **Date** — 2026-07-24

Test audio is synthesised from a fixed script with the macOS TTS voices, so
transcripts can be compared against a known original.

```bash
python spikes/make_tts_fixture.py --repeat 20 --out spikes/fixtures/ko_30min.wav
```

---

## Phase 0 — How transcription fragments arrive

The question that had to be answered before building the UI: does
`output_transcription` stream incrementally, or arrive one utterance at a time?

```bash
python spikes/s0_transcription_timing.py --wav spikes/fixtures/ko_fixture.wav --drain 10
```

**Answer: incremental.** Caption policy A. Fixed in `Settings.caption_policy`.

| Metric | KO → EN | EN → KO |
|---|---|---|
| `output_transcription` fragments | 83 | 82 |
| Median fragment length | 14 chars | 6 chars |
| Median inter-arrival gap | 1005 ms | 1001 ms |
| Time to first output | 3.142 s | 3.756 s |
| Time to first input transcription | 2.951 s | 3.557 s |
| Verdict | Policy A | Policy A |

- Korean output arrives in smaller pieces because it says the same thing in fewer characters
- The **arrival rate is the same in both directions** — about one fragment per second
- `session_resumption_update` arrives roughly every second, so the resumption handle is always fresh
- `go_away` did not appear in a 1.5-minute session — see Phase 3
- `interim_input_transcription` exists in the SDK but this model never sends it; nothing depends on it

### Fragment joining

Fragments carry **their own leading space**, in both languages:

```
t=+80.208s  OUT  len=7    text=' 핵심 역량에'
t=+81.200s  OUT  len=5    text=' 집중하여'
t=+82.236s  OUT  len=9    text=' 상반기에 채용을'
t=+83.201s  OUT  len=10   text=' 늘릴 계획입니다.'
```

- Concatenating directly produces correct spacing in both languages
- The feared `안녕` + `하세요` → `안녕 하세요` failure does not occur on the normal path
- `_join()` still refuses to insert a space between CJK characters, in case a fragment ever splits mid-word (`계획` + `입니다.`)
- English keeps the insert, since English words must stay separated
- The spike now reports the leading-space ratio on every run; if it is ever below 100%, revisit this rule

Sentence-final punctuation rides along with the fragments (`' separately.'`),
so policy A's punctuation commit works. The 1.2 s idle commit stays as a
fallback for phrases that end without punctuation.

---

## Phase 1 — Audio capture

```bash
python spikes/s1_capture_check.py --seconds 60
```

| Criterion | Measured | |
|---|---|---|
| Byte count within ±1% of 16000 × 2 × seconds | 0.833% drift | ✅ |
| Callback underruns | 0 | ✅ |
| Ring buffer drops | 0 bytes | ✅ |

Measured with the built-in microphone at 48 kHz. **Not yet verified against the
USB interface.**

---

## Phase 2 — Five minutes unattended

```bash
python spikes/s2_headless_run.py --wav spikes/fixtures/ko_6min.wav --minutes 5 \
    --script spikes/fixtures/ko_script.txt --repeat 4
```

| Metric | Value |
|---|---|
| Duration | 5.1 min |
| Reconnects | 0 |
| Chunks sent | 3000 (= exactly 300 s of audio) |
| Ring buffer drops | 0 bytes |
| Cost | $0.19 |
| Sentences lost | none |

---

## Phase 3 — Thirty minutes unattended ✅

```bash
python spikes/s2_headless_run.py --wav spikes/fixtures/ko_30min.wav --minutes 30 \
    --script spikes/fixtures/ko_script.txt --repeat 20 --out phase3_transcript.md
```

| Acceptance criterion | Measured | |
|---|---|---|
| 30 minutes unbroken | 30.1 min | ✅ |
| Reconnect count and gap logged | 3 × (370, 403, 459 ms) | ✅ |
| No sentence lost across reconnects | 360/360, worst 0.78, mean 0.96 | ✅ |
| Cost within ±20% of the $1.10 estimate | **$1.11** (+0.9%) | ✅ |

Also: 18000 chunks sent (exactly 1800 s), **0 bytes** dropped by the ring buffer.

### Reconnect behaviour

`go_away` arrived **three times at 9-minute intervals**, each with
`time_left=50s` — matching the 3–4 predicted.

```
15:40:54  live session open (resumed=False)
15:49:54  go_away received (time_left=50s) — opening replacement
15:49:55  live session open (resumed=True)
15:49:55  session swapped — gap 370 ms (reconnect #1)
15:58:55  go_away ... gap 403 ms (reconnect #2)
16:07:55  go_away ... gap 459 ms (reconnect #3)
```

- All three swaps report `resumed=True`, so the stored `new_handle` really was reused
- Opening the replacement **before** closing the old session keeps the gap under half a second
- Captions run straight through the swap:

```
[1080.0s] We expect to achieve it.
          go_away received (time_left=50s) — opening replacement
[1081.0s] Of course, exchange rate fluctuations are still the biggest variable.
```

### Defect found by this run

The first reconnect reported its state as *connecting* rather than
*reconnecting*, because the decision used `stats.reconnects`, which is still 0
at that moment. Now decided by whether a session is present to replace.

---

## Comparing transcripts against the script

`s2_headless_run.py --script` scores each original sentence against the
recognised Korean and flags anything below a threshold.

- Scoring sums **all** matching blocks, not the single longest run
- An earlier version used the longest run and produced false positives, because the ASR normalises numbers: `십일월` → `11월`, `삼십 퍼센트` → `30%`, and spaces differently (`영업이익률` → `영업이익 율`)
- Re-score a saved transcript without spending API credits with `--compare-only`

---

## Captions arriving in a burst

Reported symptom: nothing on screen for a while, then everything at once.

Reproduced offline, no API needed, by feeding five settled sentences 20 ms apart
— which is what a turn-based model does when it finishes translating a long
utterance:

| | Lines that reached a repaint |
|---|---|
| Before | **2 of 5** |
| After pacing the release | **4 of 5** |

Cause: `CaptionBuffer._committed` was capped at `max_lines`, so each new settled
line evicted one immediately. Qt coalesces `update()` calls, so all five arrived
inside a single repaint and the reader saw only the final pair. Nothing was lost
from the transcript — that is written from `drain_new_commits()`, which is
independent of the display.

Fix: settled lines join a queue and are released one per `line_hold_seconds`
(default 0.7 s). No more than `max_lines` are ever held back, so the display
trails the audio by at most 1.4 s at the defaults. A burst longer than that still
loses its oldest line from the screen — two lines cannot show five sentences.

**Not the cause, and unchanged:** the audio path. `Stats.max_backlog_ms` now
records the most unsent audio ever held in the ring, so a client-side stall can
be told apart from the API answering in bursts. A backlog near zero means the
audio went out as fast as it arrived.

---

## Network baseline

Measured by the new `netcheck` module on the development machine, 2026-07-29:

| Measurement | Value |
|---|---|
| Latency to `generativelanguage.googleapis.com` | 12–18 ms median, 5–43 ms jitter |
| Upload | 15.8–31.2 Mbit/s |
| Download | 132–176 Mbit/s |

A session needs **256 kbit/s upstream** (16 kHz × 16-bit mono), so this has
roughly 60× the headroom. Network capacity was therefore ruled out as a cause of
the bursty captions.

Cloudflare's `speed.cloudflare.com` endpoints answer `Python-urllib/3.x` with
**403**; a `User-Agent` header is required.

---

## Still unverified

- Capture from the actual USB audio interface — everything so far used the built-in microphone or file input
- Latency measurement by screen recording (speech start → caption appears)
- Side-by-side quality comparison against the Google Translate app
- Fault injection: Wi-Fi off for 10 s, USB interface unplugged and reconnected, screen locked and unlocked
- A 30-minute run in the English → Korean direction; only Korean → English was measured
