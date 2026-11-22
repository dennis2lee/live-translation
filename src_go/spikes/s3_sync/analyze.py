"""Read a recording made by spikes/s3_sync and say where the caption text sits
against the translated speech.

    python3 spikes/s3_sync/analyze.py /tmp/sync

Three things come out of it:

  * the fragment clock - how far apart caption fragments arrive;
  * the spoken segments of the translated audio, found by its pauses, with the
    time each arrived;
  * each sentence against the voice: when its first and last fragments
    arrived, and when the voice started up after them.

Times are arrival times. The listener hears the audio a player's buffer later
(audio.PlayPrebufferMS, 200 ms), so the captions are that much further ahead.

Standard library only.
"""

import array
import json
import math
import re
import statistics
import sys
import wave

RATE = 24000
FRAME = RATE // 50          # 20 ms
PAUSE = 0.30                # a gap this long ends a spoken segment


def main(directory):
    events = [json.loads(line) for line in open(directory + "/events.jsonl")]
    with wave.open(directory + "/out.wav") as w:
        pcm = array.array("h")
        pcm.frombytes(w.readframes(w.getnframes()))

    rms = []
    for i in range(0, len(pcm) - FRAME, FRAME):
        rms.append(math.sqrt(sum(x * x for x in pcm[i:i + FRAME]) / FRAME))
    threshold = max(rms) * 0.02
    voiced = [r > threshold for r in rms]

    segments, start, last = [], None, None
    for i, v in enumerate(voiced):
        t = i * 0.02
        if not v:
            continue
        if start is None:
            start = t
        elif t - last > PAUSE:
            segments.append((start, last + 0.02))
            start = t
        last = t
    if start is not None:
        segments.append((start, last + 0.02))

    # Where in the audio each blob begins, and when it arrived.
    blobs, position = [], 0.0
    for e in events:
        if e.get("audio"):
            blobs.append((position, e["t"] / 1000))
            position += e["audio"] / (RATE * 2)

    def arrival(p):
        at = blobs[0][1]
        for begins, t in blobs:
            if begins > p:
                break
            at = t
        return at

    fragments, position = [], 0.0
    for e in events:
        if e.get("target"):
            fragments.append((e["t"] / 1000, position, e["target"]))
        position += e.get("audio", 0) / (RATE * 2)

    print(f"{len(fragments)} caption fragments, {position:.1f} s of translated audio, "
          f"{len(segments)} spoken segments")
    sizes = sorted({e["audio"] for e in events if e.get("audio")})
    print(f"audio blobs: {len(blobs)}, sizes {sizes[:6]} bytes, first at {blobs[0][1]:.2f} s")
    behind = max(t - blobs[0][1] - p for p, t in blobs)
    ahead = max(p - (t - blobs[0][1]) for p, t in blobs)
    print(f"audio against the clock: at most {behind:.2f} s late, {ahead:.2f} s early")

    gaps = [b[0] - a[0] for a, b in zip(fragments, fragments[1:])]
    print(f"fragment gaps: median {statistics.median(gaps):.2f} s, "
          f"min {min(gaps):.2f}, max {max(gaps):.2f}")

    print("\nspoken segments (position in the audio; when it arrived)")
    for a, b in segments:
        print(f"  {a:6.2f}-{b:6.2f}  {b - a:5.2f} s   arrived {arrival(a):6.2f}-{arrival(b):6.2f}")

    # Each sentence against the voice. There are no word timings, so the two
    # are lined up at the one place they can be: a sentence's first fragment,
    # and the next time the voice starts up after a pause. A short sentence
    # spoken without a pause in front of it ("Yes.") has no onset of its own
    # and is paired with the following one - read those rows with the table
    # of segments above, not on their own.
    onsets = [arrival(a) for a, _ in segments]
    ends = [arrival(b) for _, b in segments]
    print("\nsentences: first fragment, last fragment, and the voice")
    print("  (a caption drawn word by word appears at the first; drawn whole, at the last)")
    current, early, late = [], [], []
    for t, _, text in fragments:
        current.append((t, text))
        if not re.search(r"[.?!]\s*$", text):
            continue
        sentence = "".join(x for _, x in current).strip()
        t0, t1 = current[0][0], current[-1][0]
        current = []
        k = next((i for i, o in enumerate(onsets) if o >= t0 - 0.3), None)
        if k is None:
            continue
        early.append(onsets[k] - t0)
        late.append(t1 - onsets[k])
        print(f"  {t0:6.2f} {t1:6.2f}   voice {onsets[k]:6.2f}-{ends[k]:6.2f}   "
              f"word by word {t0 - onsets[k]:+5.2f} s, whole {t1 - onsets[k]:+5.2f} s   {sentence[:44]}")
    print(f"\nword by word, the first words are on screen before the voice starts them by "
          f"median {statistics.median(early):.2f} s (min {min(early):.2f}, max {max(early):.2f})")
    print(f"drawn whole, the caption appears after the voice started the sentence by "
          f"median {statistics.median(late):.2f} s (min {min(late):.2f}, max {max(late):.2f})")


if __name__ == "__main__":
    main(sys.argv[1])
