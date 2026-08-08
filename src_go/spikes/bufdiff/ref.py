#!/usr/bin/env python3
"""Drive the frozen Python CaptionBuffer with an op script; print the state.

Loads caption_buffer.py directly by path so nothing else in the package (Qt,
google-genai) is imported. One JSON state line per op, compared byte-for-byte
against the Go harness's output.
"""

import importlib.util
import json
import pathlib
import sys

spec = importlib.util.spec_from_file_location("caption_buffer", sys.argv[1])
cb = importlib.util.module_from_spec(spec)
sys.modules["caption_buffer"] = cb  # dataclasses resolve types via sys.modules
spec.loader.exec_module(cb)

# The Go buffer deliberately departs from the frozen reference in three ways
# (spikes/bufdiff/deviations.py). Apply them here so bufdiff keeps reporting
# only the divergences nobody intended. Pass --no-deviations to compare against
# the untouched 0.5.1 behaviour.
if "--no-deviations" not in sys.argv:
    sys.path.insert(0, str(pathlib.Path(__file__).parent))
    import deviations
    deviations.apply(cb)

buf = cb.CaptionBuffer()
SPEECH = {"unknown": None, "quiet": False, "active": True}

for i, raw in enumerate(sys.stdin):
    op = json.loads(raw)
    drained = None
    kind = op["op"]
    if kind == "feed":
        buf.feed(op["text"], op["now"])
    elif kind == "tick":
        buf.tick(op["now"], speaking=SPEECH[op["speech"]])
    elif kind == "turn_complete":
        buf.note_turn_complete(op["now"])
    elif kind == "reconnect":
        buf.note_reconnect()
    elif kind == "clear":
        buf.clear()
    elif kind == "flush":
        buf.flush()
    elif kind == "set_max_lines":
        buf.set_max_lines(op["n"])
    elif kind == "set_complete_phrases":
        buf.complete_phrases_only = op["on"]
    elif kind == "set_policy":
        buf.set_policy(cb.Policy(op["policy"]))
    elif kind == "drain":
        drained = buf.drain_new_commits()
    else:
        raise SystemExit(f"unknown op {kind}")
    state = {
        "i": i,
        "display": [[t, s] for t, s in buf.display_lines()],
        "pending": buf.pending_text,
        "queued": buf.queued_lines,
        "committed": buf.committed_lines,
        "drained": drained,
    }
    print(json.dumps(state, ensure_ascii=False, sort_keys=True))
