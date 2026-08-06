// Spike G4 — transcript byte-format parity.
//
// The parity gate asks for a golden-file diff: the Go writer and the Python
// writer are fed the *same* fragment script — same start time, same lines,
// same elapsed values, same reconnect and direction notes — and the two files
// must be byte-identical apart from the "Ended" line, which is wall clock.
//
// compare.sh next to this file runs both feeders and diffs the output.
package main

import (
	"fmt"
	"os"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: g4_transcript_golden <output-dir>")
		os.Exit(2)
	}
	w := transcript.New(transcript.Options{
		Dir:            os.Args[1],
		TargetLanguage: "en",
		Model:          "gemini-3.5-live-translate-preview",
	})
	started := time.Date(2026, 7, 24, 14, 30, 52, 0, time.Local)
	if w.Open(started) == "" {
		fmt.Fprintln(os.Stderr, "could not open the transcript")
		os.Exit(1)
	}

	// The shared fragment script. compare.py feeds the identical sequence.
	w.AppendLine("Hello. Today's quarterly earnings meeting will now begin.", 3.2)
	w.AppendLine("Revenue is up fourteen percent compared to last year.", 9.8)
	w.NoteReconnect(541.0, 412)
	w.AppendLine("영업이익률이 십사 퍼센트 올랐습니다.", 545.5)
	w.NoteDirection("EN→KO", 600.0)
	w.AppendLine("  A line with surrounding spaces.  ", 3725.0)
	w.AppendLine("", 3726.0) // must be dropped, not written blank
	w.Close(3730.0, 1)
	fmt.Println(w.Path())
}
