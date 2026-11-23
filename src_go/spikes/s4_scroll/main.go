// Spike S4 — what smooth caption scrolling looks like, without a session.
//
// Opens the real caption view in a window and feeds it the recorded session
// fragment by fragment (the fixture the ui tests use, spikes/s3_sync), so the
// rows' move up can be watched and judged by eye. No key, no network, no
// audio. Runs until the window is closed; the fixture repeats.
//
//	go run ./spikes/s4_scroll                  # a fragment every 0.6 s
//	go run ./spikes/s4_scroll -interval 1.0    # the server's own pace
//	go run ./spikes/s4_scroll -lines 1 -off    # one row, animation off
package main

import (
	"flag"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/ui"
)

// spoken is a stretch of a real session as the server sent it — the same
// fragments as internal/ui/inplace_test.go.
var spoken = []string{
	"Hello.", " Today's quarterly", " earnings meeting", " will now begin.",
	" First,", " let's review", " the third-quarter", " sales.",
	" Total", " revenue is", " higher than the same", " period last year", " by 14%.",
	" However,", " the domestic", " market alone", " shows a slight", " slowdown in growth.",
	" Overseas", " sales accounted", " for more than half", " for the first time.",
	" This is", " particularly significant.",
}

func main() {
	interval := flag.Float64("interval", 0.6, "seconds between fragments")
	lines := flag.Int("lines", 2, "caption rows")
	off := flag.Bool("off", false, "draw moves at once, as before 1.14.0")
	size := flag.Int("font", 44, "font size")
	flag.Parse()

	s := config.Default()
	s.MaxLines = *lines
	s.FontSize = *size
	s.SmoothScroll = !*off

	a := app.New()
	w := a.NewWindow("S4 — smooth scrolling")
	v := ui.NewCaptionView(s)
	w.SetPadded(false)
	w.SetContent(v)
	w.Resize(fyne.NewSize(1100, 420))

	b := captions.New(captions.Options{
		MaxLines: *lines, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	go func() {
		start := time.Now()
		now := func() float64 { return time.Since(start).Seconds() }
		for i := 0; ; i++ {
			frag := spoken[i%len(spoken)]
			fyne.Do(func() {
				b.Feed(frag, now())
				v.SetLines(b.DisplayLines())
			})
			// Tick the buffer as the window does, every 100 ms.
			end := time.Now().Add(time.Duration(*interval * float64(time.Second)))
			for time.Now().Before(end) {
				time.Sleep(100 * time.Millisecond)
				fyne.Do(func() {
					if b.Tick(now(), captions.SpeechActive) {
						v.SetLines(b.DisplayLines())
					}
				})
			}
		}
	}()
	w.ShowAndRun()
}
