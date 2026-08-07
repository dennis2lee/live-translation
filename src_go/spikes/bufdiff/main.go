// Command bufdiff drives the caption buffer with an op script from stdin and
// prints one JSON state line per op — the Go half of the differential harness
// whose reference half runs the frozen Python CaptionBuffer on the same script.
// The two outputs are compared byte for byte; src/ is the executable
// specification, so any divergence is a bug here until proven otherwise.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
)

type op struct {
	Op     string  `json:"op"`
	Text   string  `json:"text"`
	Now    float64 `json:"now"`
	Speech string  `json:"speech"`
	N      int     `json:"n"`
	Policy string  `json:"policy"`
	On     bool    `json:"on"`
}

type state struct {
	I         int      `json:"i"`
	Display   [][2]any `json:"display"`
	Pending   string   `json:"pending"`
	Queued    []string `json:"queued"`
	Committed []string `json:"committed"`
	Drained   []string `json:"drained"`
}

var speech = map[string]captions.Speech{
	"unknown": captions.SpeechUnknown,
	"quiet":   captions.SpeechQuiet,
	"active":  captions.SpeechActive,
}

func main() {
	buf := captions.New(captions.Options{ClearAfterSeconds: captions.ClearAfterSeconds})
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	i := 0
	for sc.Scan() {
		var o op
		if err := json.Unmarshal(sc.Bytes(), &o); err != nil {
			fmt.Fprintln(os.Stderr, "bad op:", err)
			os.Exit(1)
		}
		var drained []string
		switch o.Op {
		case "feed":
			buf.Feed(o.Text, o.Now)
		case "tick":
			buf.Tick(o.Now, speech[o.Speech])
		case "turn_complete":
			buf.NoteTurnComplete(o.Now)
		case "reconnect":
			buf.NoteReconnect()
		case "clear":
			buf.Clear()
		case "flush":
			buf.Flush()
		case "set_max_lines":
			buf.SetMaxLines(o.N)
		case "set_complete_phrases":
			buf.SetCompletePhrasesOnly(o.On)
		case "set_policy":
			buf.SetPolicy(captions.Policy(o.Policy))
		case "drain":
			drained = buf.DrainNewCommits()
			if drained == nil {
				drained = []string{}
			}
		default:
			fmt.Fprintln(os.Stderr, "unknown op:", o.Op)
			os.Exit(1)
		}
		disp := buf.DisplayLines()
		s := state{
			I:         i,
			Display:   make([][2]any, 0, len(disp)),
			Pending:   buf.PendingText(),
			Queued:    buf.QueuedLines(),
			Committed: buf.CommittedLines(),
			Drained:   drained,
		}
		for _, l := range disp {
			s.Display = append(s.Display, [2]any{l.Text, l.Settled})
		}
		if s.Queued == nil {
			s.Queued = []string{}
		}
		if s.Committed == nil {
			s.Committed = []string{}
		}
		if err := enc.Encode(s); err != nil {
			panic(err)
		}
		i++
	}
}
