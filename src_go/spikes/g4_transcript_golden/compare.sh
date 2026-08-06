#!/usr/bin/env bash
# Feed the identical fragment script to both transcript writers and diff.
# The "Ended" line is wall clock in both, so it alone is normalised.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT="$(mktemp -d /tmp/livecap-golden-XXXXXX)"
mkdir -p "$OUT/go" "$OUT/py"

GO_FILE="$(go run ./spikes/g4_transcript_golden "$OUT/go" | tail -1)"

PY_FILE="$(cd .. && .venv/bin/python - "$OUT/py" <<'PYEOF'
import sys
from datetime import datetime
sys.path.insert(0, "src")
from livecap.transcript import TranscriptWriter

w = TranscriptWriter(sys.argv[1], target_language="en",
                     model="gemini-3.5-live-translate-preview")
w.open(datetime(2026, 7, 24, 14, 30, 52))
w.append_line("Hello. Today's quarterly earnings meeting will now begin.", 3.2)
w.append_line("Revenue is up fourteen percent compared to last year.", 9.8)
w.note_reconnect(541.0, 412)
w.append_line("영업이익률이 십사 퍼센트 올랐습니다.", 545.5)
w.note_direction("EN→KO", 600.0)
w.append_line("  A line with surrounding spaces.  ", 3725.0)
w.append_line("", 3726.0)
print(w.close(elapsed_seconds=3730.0, reconnects=1))
PYEOF
)"

norm() { sed 's/^- \*\*Ended\*\* .*/- **Ended** (wall clock)/' "$1"; }

echo "go:     $GO_FILE"
echo "python: $PY_FILE"
if diff <(norm "$GO_FILE") <(norm "$PY_FILE"); then
  echo "IDENTICAL apart from the Ended wall-clock line"
else
  echo "DIFFER"
  exit 1
fi
