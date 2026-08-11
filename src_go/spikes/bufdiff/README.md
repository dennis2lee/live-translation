# bufdiff — differential fuzz of the caption buffer against the Python reference

`src/` is the executable specification, so the strongest check the Go port can
get is byte equality on identical inputs. This harness drives both
implementations with the same random op script and diffs the full observable
state after every op.

```bash
cd src_go
go build ./spikes/bufdiff
for seed in $(seq 1 100); do
  python3 spikes/bufdiff/gen.py "$seed" 800 > /tmp/script.jsonl
  python3 spikes/bufdiff/ref.py ../src/livecap/caption_buffer.py \
    < /tmp/script.jsonl | jq -cS . > /tmp/py.out
  ./bufdiff < /tmp/script.jsonl | jq -cS . > /tmp/go.out
  diff /tmp/py.out /tmp/go.out || echo "DIVERGENCE at seed $seed"
done
```

The op scripts cover feeds (with duplicates, Korean, abbreviations, missing
leading spaces, long utterances), ticks at deltas that straddle every timing
window, turn_complete, reconnect, clear, flush, and live changes to max_lines,
complete-phrases mode and the policy.

One trap, found the first time this ran: `captions.Options` treats a zero
`ClearAfterSeconds` as *disabled*, while the Python dataclass defaults it to
2.0. The application always passes the configured value, so only a harness that
builds a bare `Options{}` hits the difference — pass
`captions.ClearAfterSeconds` explicitly.

**Deliberate deviations.** Since 2026-08-08 the Go buffer departs from the
frozen reference in six named ways, listed and applied at runtime in
`deviations.py` — a full stop settles by abbreviation rather than by length, a
backlog is paced in two tiers instead of dumped, complete-phrases mode shows
the text in progress rather than an empty box, a reconnect resumes the
previous line only when a timer cut it off (recorded at commit time, since
the server's turn_complete finishes a turn that carries no punctuation), the settled history
is bounded by `DISPLAY_HISTORY_FACTOR` sentences per row rather than by
`max_lines` (because the Go renderer reflows sentences into shared rows and
trims to `max_lines` rows itself), and the idle timer is 1.6 s rather than
1.2, measured against the API's one-second fragment clock. `ref.py` applies them by default
so the harness still reports only the divergences nobody intended; pass
`--no-deviations` to compare against untouched 0.5.1 behaviour.

Result recorded in spikes/RESULTS.md.
