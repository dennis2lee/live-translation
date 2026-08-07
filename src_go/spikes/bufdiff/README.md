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

Result recorded in spikes/RESULTS.md.
