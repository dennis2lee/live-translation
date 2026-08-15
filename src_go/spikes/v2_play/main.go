// Spike V2 — does the playback path work, on a device the user chose?
//
// It drives `audio.Player`, the shipping code, rather than a copy of it. A
// spike that reimplements what it is testing proves only that the spike works,
// and this one has a job the test suite cannot do: malgo opens real hardware,
// so whether sound comes out of the right box is judged by ear.
//
// It feeds the player in 100 ms blobs, in real time, the way the network does.
// Dumping the file in at once would prove nothing about a live stream.
//
//	go run ./spikes/v2_play -list
//	go run ./spikes/v2_play -wav /tmp/control.wav
//	go run ./spikes/v2_play -wav /tmp/control.wav -device "MacBook Air Speakers"
//	go run ./spikes/v2_play -wav /tmp/control.wav -volume 0.3
package main

import (
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
)

func readWAV(path string) (pcm []byte, rate, channels int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, 0, errors.New("not a RIFF/WAVE file")
	}
	pos := 12
	var bits int
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		switch id {
		case "fmt ":
			channels = int(binary.LittleEndian.Uint16(raw[body+2 : body+4]))
			rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			bits = int(binary.LittleEndian.Uint16(raw[body+14 : body+16]))
		case "data":
			if bits != 16 {
				return nil, 0, 0, fmt.Errorf("%d-bit WAV, need 16", bits)
			}
			return raw[body:min(body+size, len(raw))], rate, channels, nil
		}
		pos = body + size
		if size%2 == 1 {
			pos++
		}
	}
	return nil, 0, 0, errors.New("no data chunk")
}

func main() {
	list := flag.Bool("list", false, "list playback devices and exit")
	wav := flag.String("wav", "", "16-bit mono WAV to play")
	deviceName := flag.String("device", "", "playback device name; empty is the system default")
	volume := flag.Float64("volume", 1.0, "0.0 to 1.0")
	flag.Parse()

	devices := audio.ListOutputDevices()
	fmt.Printf("playback devices (%d):\n", len(devices))
	for _, d := range devices {
		mark := " "
		if d.IsDefault {
			mark = "*"
		}
		fmt.Printf("  %s %-40s %6.0f Hz  %dch\n", mark, d.Name, d.DefaultSampleRate, d.Channels)
	}
	if *list {
		audio.ReleaseContext()
		return
	}
	if *wav == "" {
		fmt.Fprintln(os.Stderr, "\nnothing to play: pass -wav")
		os.Exit(1)
	}

	pcm, srcRate, srcCh, err := readWAV(*wav)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wav: %v\n", err)
		os.Exit(1)
	}
	if srcCh != 1 {
		fmt.Fprintf(os.Stderr, "wav has %d channels; the model sends mono\n", srcCh)
		os.Exit(1)
	}
	fmt.Printf("\nplaying %s: %.1f s of %d Hz mono at volume %.2f\n",
		*wav, float64(len(pcm))/float64(srcRate*2), srcRate, *volume)

	player := audio.NewPlayer(*deviceName, srcRate)
	player.SetVolume(*volume)
	dev, err := player.Start()
	if err != nil {
		fmt.Fprintf(os.Stderr, "start: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("device: %s at %.0f Hz, %d ch\n\n", dev.Name, dev.DefaultSampleRate, dev.Channels)

	blob := srcRate * 2 * 100 / 1000 // 100 ms
	started := time.Now()
	maxBuffered := 0
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for i := 0; i < len(pcm); i += blob {
		<-ticker.C
		player.Write(pcm[i:min(i+blob, len(pcm))])
		if held := player.Buffered(); held > maxBuffered {
			maxBuffered = held
		}
	}

	// Let the buffer drain rather than cutting the last words off.
	for player.Buffered() > 0 && time.Since(started) < 10*time.Minute {
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)

	fmt.Printf("--- V2 result ---\n")
	fmt.Printf("  wall clock:   %v for %.1f s of audio\n",
		time.Since(started).Round(time.Millisecond), float64(len(pcm))/float64(srcRate*2))
	fmt.Printf("  max buffered: %d bytes\n", maxBuffered)
	fmt.Printf("  (the starvation and drop counts are logged by Stop)\n")

	player.Stop()
	audio.ReleaseContext()
}
