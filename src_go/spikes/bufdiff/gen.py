#!/usr/bin/env python3
"""Generate a random caption-buffer op script, one JSON object per line.

Deterministic per seed. Time only moves forward, in steps chosen to straddle
every window the buffer has: the 0.25 s duplicate window, the 0.7 s line hold,
the 1.2 s idle commit, the 2 s clear, and the 15 s ceiling.
"""

import json
import random
import sys

FRAGMENTS = [
    " the", " plan", " is", " ready.", " Dr.", " Smith", " agreed",
    " 안녕하세요", " 오늘의", " 계획", "입니다.", " 회의를", " 시작하겠습니다.",
    " What", " time", " is", " it?", " Now!", " yes", " 네.",
    " This is a whole utterance translated at once, long enough to matter.",
    " 이것은 한 번에 통째로 번역된 긴 문장입니다. 자동 감지에 걸릴 만큼 깁니다.",
    "no-leading-space", "약어", " vs.", " Mr.", " a.",
]

DTS = [0.0, 0.05, 0.1, 0.24, 0.26, 0.3, 0.69, 0.71, 1.0, 1.19, 1.21, 2.1, 3.0, 15.5]


def main(seed: int, steps: int) -> None:
    rng = random.Random(seed)
    now = 0.0
    last_text = None
    for _ in range(steps):
        now += rng.choice(DTS)
        r = rng.random()
        if r < 0.45:
            if last_text is not None and rng.random() < 0.15:
                text = last_text  # deliberate duplicate
            else:
                text = rng.choice(FRAGMENTS)
            last_text = text
            op = {"op": "feed", "text": text, "now": now}
        elif r < 0.75:
            op = {"op": "tick", "now": now,
                  "speech": rng.choice(["unknown", "quiet", "active"])}
        elif r < 0.80:
            op = {"op": "turn_complete", "now": now}
        elif r < 0.84:
            op = {"op": "reconnect"}
        elif r < 0.87:
            op = {"op": "clear"}
        elif r < 0.90:
            op = {"op": "flush"}
        elif r < 0.93:
            op = {"op": "set_max_lines", "n": rng.randint(1, 4)}
        elif r < 0.96:
            op = {"op": "set_complete_phrases", "on": rng.random() < 0.5}
        elif r < 0.98:
            op = {"op": "set_policy",
                  "policy": rng.choice(["auto", "incremental", "utterance"])}
        else:
            op = {"op": "drain"}
        print(json.dumps(op, ensure_ascii=False))
    print(json.dumps({"op": "flush"}))
    print(json.dumps({"op": "drain"}))


if __name__ == "__main__":
    main(int(sys.argv[1]), int(sys.argv[2]) if len(sys.argv) > 2 else 400)
