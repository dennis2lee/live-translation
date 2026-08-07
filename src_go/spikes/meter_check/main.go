// Command meter_check drives the capture the Preferences level test opens and
// prints, side by side, what the meter shows and what the old linear bar showed.
//
// It exists because the level meter looked broken and the argument for why was
// arithmetic: a progress bar is linear, hearing is not, and a normal speaking
// peak of −36 dBFS is 0.016 of full scale. Rather than assert that, this prints
// both columns against a real microphone.
//
//	go run ./spikes/meter_check
//
// Measured here on 2026-08-06, built-in MacBook Air microphone, speech played
// into the room: peaks −32.7 to −38.4 dBFS, which is 45–56% on the dBFS scale
// and 1.2–2.3% on the linear one. A quiet room reads −53 to −73.
package main

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

const (
	ticks    = 80
	interval = 100 * time.Millisecond
	barWidth = 40
)

// fraction mirrors peakToFraction in internal/ui/prefs.go.
func fraction(peak float64) float64 {
	if peak <= 0 {
		return 0
	}
	db := 20 * math.Log10(peak)
	return math.Max(0, math.Min(1, (db+60)/60))
}

func main() {
	c := audio.NewMeterCapture("")
	dev, err := c.Start()
	if err != nil {
		fmt.Println("could not open the device:", err)
		return
	}
	defer c.Stop()
	fmt.Printf("listening to %s (%d ch / %.0f Hz)\n\n",
		dev.Name, dev.Channels, dev.DefaultSampleRate)

	loudest := math.Inf(-1)
	for range ticks {
		time.Sleep(interval)
		peak := c.TakePeak()
		db := math.Inf(-1)
		if peak > 0 {
			db = 20 * math.Log10(peak)
		}
		if db > loudest {
			loudest = db
		}
		fmt.Printf("%6.1f dBFS  |%-*s| dBFS bar %3.0f%%   linear bar %4.1f%%\n",
			db, barWidth, strings.Repeat("=", int(fraction(peak)*barWidth)),
			fraction(peak)*100, peak*100)
	}

	fmt.Printf("\nloudest %.1f dBFS — dBFS bar %.0f%%, linear bar %.1f%%\n",
		loudest, fraction(math.Pow(10, loudest/20))*100, math.Pow(10, loudest/20)*100)
	// The meter observes a running session through Peak, which must not reset.
	before := c.Peak()
	fmt.Printf("Peek is non-destructive: %v twice\n", before == c.Peak())
}
