package audio

import (
	"encoding/binary"
	"errors"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"

	"github.com/gen2brain/malgo"

	"github.com/dennis2lee/live-translation/src_go/internal/resample"
)

const (
	// TranslatedRate is the rate the Live API returns speech at. Measured off
	// the MIME type the server stamps on every blob — "audio/pcm;rate=24000",
	// mono, 16-bit — not assumed. Guessing it wrong plays the translation at
	// the wrong pitch, which sounds like a fault in the model rather than in
	// us. See spikes/RESULTS.md and spikes/v1_voice.
	TranslatedRate = 24_000

	// PlayRingMS bounds how far behind the speaker a listener can fall.
	//
	// Measured: over 90 seconds the audio arrives with a peak backlog of
	// 570 ms — that is how much is waiting to be played at the worst moment,
	// and it is what this buffer has to hold. 500 ms, the first guess, sat
	// *below* the measurement and would have dropped mid-sentence. Two seconds
	// gives that peak room to grow over a longer session while still capping
	// how stale anything on its way out can be.
	PlayRingMS = 2000

	// PlayPrebufferMS is how much has to be waiting before playback starts, and
	// again after the buffer has run dry.
	//
	// Blobs arrive about every 100 ms and the device asks every 20, so starting
	// on the first blob leaves nothing in hand for the next one to be late
	// with: spike v2_play counted 33 starved callbacks doing exactly that. The
	// cost is 200 ms added to the start of each utterance, against a first-word
	// latency already measured at 3.3–5.2 s.
	PlayPrebufferMS = 200

	// PlayCallbackMS is the device callback period asked for.
	PlayCallbackMS = 20

	// PlayTrimAboveMS is the queue length above which silence is skipped
	// rather than played. See playRing.skipSilentHead for why the queue grows
	// at all.
	//
	// Measured by replaying a recorded six-minute run — the real audio at its
	// real arrival times — through onPlayback (replay_test.go):
	//
	//	trim off   16 cuts, 1.81 s of speech lost   queue avg 1.42 s
	//	 400 ms     0 cuts                          queue avg 0.29 s
	//	 800 ms     0 cuts, 3.0 s of pause skipped  queue avg 0.63 s, max 1.16
	//	1200 ms     0 cuts                          queue avg 0.93 s
	//
	// Every value removes the cuts; the choice is between latency and
	// cushion. The queue is also what covers the network pausing — gaps of
	// 0.87 s and 1.76 s were recorded in another run — and a gap the queue
	// cannot cover is silence, which loses nothing, where a cut loses words.
	// 800 rides out gaps up to 0.8 s and leaves 0.84 s of room under
	// PlayRingMS for a new session's opening burst (about 1 s in 0.4 s).
	PlayTrimAboveMS = 800

	// PlaySilenceDBFS is the loudest a 20 ms chunk may peak and still count as
	// silence that can be skipped.
	//
	// The model's audio has no clean gap between pause and speech: 17% of
	// chunks are exact digital zero, most speech peaks between -20 and 0
	// dBFS, and word tails and breaths fill everything in between. -60 dBFS
	// is deliberately deep in the quiet end — 21% of chunks fall under it,
	// almost all of them pause, while a soft consonant at -50 is left alone.
	// Skipping a pause is free; skipping the end of a word is the defect this
	// exists to remove.
	PlaySilenceDBFS = -60.0

	// PlayPauseKeepMS is how much of every pause is never trimmed. Pauses
	// shorter than this plus one chunk are left alone entirely — most of them
	// are gaps between words, and 83 of 180 pauses in the measured run were
	// 100 ms or less.
	PlayPauseKeepMS = 100
)

// Player speaks the translated audio the model sends back.
//
// # Threads
//
// Write runs on the translator's receive goroutine and does all the work —
// resampling, gain, interleaving — under a mutex. The device callback only
// drains the jitter buffer, so it allocates nothing and takes no lock but the
// ring's own.
//
// That split is why this holds a plain mutex where Capture needs an atomic
// pointer. Capture's converter is touched by the *device callback*, which
// cannot take a lock and which Uninit is what waits for; the ordering dance in
// Capture.Stop exists for that. Here the converter is touched by an ordinary
// goroutine, so an ordinary lock retires it safely.
// playState is everything the device callback needs, published as one
// immutable value.
//
// The callback must never read a half-updated Player: Stop retires the state
// *before* device.Stop(), which is what waits for callbacks to finish, so the
// write and the read genuinely overlap. This is bug M3's shape, and the same
// answer — the difference from Capture is only that a whole struct is swapped
// rather than a single converter.
type playState struct {
	ring       *playRing
	prebuffer  int
	blockBytes int
	// trimAbove is PlayTrimAboveMS in bytes at the device format; zero turns
	// silence trimming off.
	trimAbove int
	// silencePeak is PlaySilenceDBFS as a 16-bit sample, before gain.
	silencePeak float64
	// pauseKeep is PlayPauseKeepMS in bytes.
	pauseKeep int
}

type Player struct {
	deviceName string
	sourceRate int

	// active is nil whenever no device is running.
	active atomic.Pointer[playState]

	// gain is float64 bits, 0.0–1.0. Atomic so the Preferences slider can move
	// it without waiting on a block of audio, and so a volume change never
	// interrupts the sound the way reopening the device would.
	gain atomic.Uint64

	// primed gates the device callback. Playback waits for PlayPrebufferMS of
	// audio, and goes back to waiting whenever the buffer runs dry — which is
	// every gap between utterances, so each one starts with a cushion again.
	primed atomic.Bool

	mu        sync.Mutex
	device    *malgo.Device
	resampler resample.Stream
	running   bool
	current   OutputDevice
	devRate   int
	devChan   int

	// freeze removes the model's frozen-and-fading buzz; see freeze.go. Only
	// WriteSpeech goes through it — a test chime is a decaying sine, exactly
	// the shape it removes.
	freeze *freezeGate

	// Scratch, owned by Write once running.
	mono        []float32
	interleaved []byte
}

// NewPlayer builds a player for the named device. An empty name means the
// system default output. sourceRate is the rate of the audio that will be
// written — TranslatedRate for the Live API.
func NewPlayer(deviceName string, sourceRate int) *Player {
	if sourceRate <= 0 {
		sourceRate = TranslatedRate
	}
	p := &Player{deviceName: deviceName, sourceRate: sourceRate}
	p.SetVolume(1.0)
	return p
}

// SetVolume scales the speech, 0.0 to 1.0. Safe from any goroutine, and safe
// before Start.
func (p *Player) SetVolume(v float64) {
	if v != v { // NaN
		v = 1
	}
	p.gain.Store(math.Float64bits(max(0, min(1, v))))
}

// Volume is the gain in force.
func (p *Player) Volume() float64 { return math.Float64frombits(p.gain.Load()) }

// IsRunning reports whether the device is open.
func (p *Player) IsRunning() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

// Device is the device in use.
func (p *Player) Device() OutputDevice {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.current
}

// Start opens the output stream. It returns the device it settled on.
func (p *Player) Start() (OutputDevice, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.running {
		return OutputDevice{}, errors.New("player already started")
	}

	dev, ok := ResolveOutputDevice(p.deviceName)
	if !ok {
		return OutputDevice{}, errors.New("no audio output device found")
	}
	ctx, err := acquireContext()
	if err != nil {
		return OutputDevice{}, err
	}

	p.devRate = int(dev.DefaultSampleRate)
	if p.devRate == 0 {
		p.devRate = 48_000
	}
	// Two channels is the most this needs: the source is mono, and a device
	// reporting eight is a virtual one that miniaudio will map for us.
	p.devChan = min(max(dev.Channels, 1), 2)

	frameBytes := BytesPerSample * p.devChan
	state := &playState{
		ring:        newPlayRing(max(frameBytes, p.devRate*frameBytes*PlayRingMS/1000)),
		prebuffer:   p.devRate * frameBytes * PlayPrebufferMS / 1000,
		blockBytes:  frameBytes,
		trimAbove:   p.devRate * frameBytes * PlayTrimAboveMS / 1000,
		silencePeak: 32768 * math.Pow(10, PlaySilenceDBFS/20),
		pauseKeep:   p.devRate * frameBytes * PlayPauseKeepMS / 1000,
	}
	p.primed.Store(false)

	// The device is opened at its own rate and the audio is converted to meet
	// it. Never the other way round — the capture rule, read backwards.
	converter, err := resample.NewStream(float64(p.sourceRate), float64(p.devRate))
	if err != nil {
		return OutputDevice{}, err
	}

	p.freeze = newFreezeGate(p.sourceRate)

	// One 100 ms blob's worth, with room for the converter's expansion.
	srcFrames := p.sourceRate / 10
	p.mono = make([]float32, 0, srcFrames)
	p.interleaved = make([]byte, 0, srcFrames*frameBytes*p.devRate/p.sourceRate+frameBytes*64)

	cfg := malgo.DefaultDeviceConfig(malgo.Playback)
	cfg.Playback.Format = malgo.FormatS16
	cfg.Playback.Channels = uint32(p.devChan)
	cfg.Playback.DeviceID = dev.id.Pointer()
	cfg.SampleRate = uint32(p.devRate)
	cfg.PeriodSizeInMilliseconds = PlayCallbackMS

	// Published before the device exists, so the first callback already sees a
	// complete state.
	p.active.Store(state)

	device, err := malgo.InitDevice(ctx.Context, cfg, malgo.DeviceCallbacks{Data: p.onPlayback})
	if err != nil {
		p.active.Store(nil)
		converter.Close()
		return OutputDevice{}, err
	}
	if err := device.Start(); err != nil {
		device.Uninit()
		p.active.Store(nil)
		converter.Close()
		return OutputDevice{}, err
	}

	p.device = device
	p.resampler = converter
	p.running = true
	p.current = dev
	slog.Info("audio playback started",
		"device", dev.Name, "rate", p.devRate, "channels", p.devChan,
		"source", p.sourceRate, "volume", p.Volume())
	return dev, nil
}

// Stop closes the stream. Safe to call when nothing is running.
func (p *Player) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return
	}

	device, converter := p.device, p.resampler
	p.device, p.resampler, p.running = nil, nil, false
	p.current = OutputDevice{}
	p.primed.Store(false)

	// Retire the state first, so a callback already in flight plays silence
	// rather than reading a ring that is about to go. Then stop the device —
	// Uninit is what waits for the callback to return.
	//
	// Write cannot be part-way through the converter here, because Write holds
	// this same lock; that is the whole reason a mutex is enough where Capture
	// needed an atomic.
	state := p.active.Swap(nil)
	if err := device.Stop(); err != nil {
		slog.Warn("error stopping the audio output device", "err", err)
	}
	device.Uninit()
	if converter != nil {
		converter.Close()
	}

	if state != nil {
		starved, starvedBytes, dropped := state.ring.Stats()
		slog.Info("audio playback stopped",
			"starved_callbacks", starved, "silence_ms", p.msOf(starvedBytes),
			"dropped_ms", p.msOf(dropped), "trimmed_ms", p.msOf(state.ring.Trimmed()),
			"freezes_removed", len(p.freeze.runStarts),
			"freeze_muted_ms", p.freeze.Muted()*1000/int64(max(p.sourceRate, 1)))
	}
}

func (p *Player) msOf(bytes int64) int64 {
	frameBytes := int64(BytesPerSample * max(p.devChan, 1))
	if p.devRate <= 0 {
		return 0
	}
	return bytes * 1000 / (frameBytes * int64(p.devRate))
}

// Write queues one blob of mono PCM16 at the source rate.
//
// It is called from the translator's receive goroutine. Writing to a stopped
// player is not an error — a session can outlive the device, and speech with
// nowhere to go is simply dropped.
func (p *Player) Write(pcm []byte) { p.write(pcm, false) }

// WriteSpeech queues the model's translated speech. It is Write with the
// freeze gate in the path, which removes the buzz the far end's synthesis
// makes as a session opens; it holds audio back only during an opening. Everything that
// is not the model's audio — a chosen local voice, the test chime — goes
// through Write and is never touched.
func (p *Player) WriteSpeech(pcm []byte) { p.write(pcm, true) }

// ResetSpeech drops the model audio the freeze gate is holding, and ends any
// opening it is watching. Call it when the model's stream is about to be
// replaced — a translator restart, a switch between a local voice and the
// model — or audio held from the old stream plays in front of the new one.
//
// It covers what has reached the gate. A blob the old session's receive loop
// is already delivering when the translator stops can still arrive after it:
// Translator.Stop closes sessions without waiting for those loops, and
// guarding against one stray blob is not worth making Stop block on them.
func (p *Player) ResetSpeech() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.freeze != nil {
		p.freeze.Reset()
	}
}

// Flush drops everything queued and not yet heard: the playback ring and the
// audio the freeze gate is holding. The volume is applied as audio is
// written, so up to PlayRingMS of speech already queued would otherwise go on
// playing at the old volume after a mute — measured by the red team, 1.5 s of
// 1.5 s. Muting flushes so the room hears nothing more; unmuting flushes so
// what was queued silently while muted does not delay what comes next.
func (p *Player) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.freeze != nil {
		// Reset ends the watch over a session's opening. If one was being
		// watched it starts again from here, or an unmute in the first
		// second of a session let its buzz through.
		watching := p.freeze.active
		p.freeze.Reset()
		if watching {
			p.freeze.MarkSessionStart()
		}
	}
	// The converter keeps a few tens of milliseconds in its filter, which
	// the next write would push out ahead of whatever comes next. A fresh one
	// holds nothing.
	if p.resampler != nil {
		if fresh, err := resample.NewStream(float64(p.sourceRate), float64(p.devRate)); err == nil {
			p.resampler.Close()
			p.resampler = fresh
		} else {
			slog.Warn("could not reset the output converter", "err", err)
		}
	}
	if state := p.active.Load(); state != nil {
		state.ring.Clear()
	}
}

// MarkSessionStart tells the freeze gate that the next WriteSpeech is a new
// session's first audio. See freezeGate.MarkSessionStart.
func (p *Player) MarkSessionStart() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.freeze != nil {
		p.freeze.MarkSessionStart()
	}
}

func (p *Player) write(pcm []byte, gated bool) {
	if len(pcm) < BytesPerSample {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	state := p.active.Load()
	if !p.running || p.resampler == nil || state == nil {
		return
	}

	gain := float32(math.Float64frombits(p.gain.Load()))

	// Decoded at unity gain: the freeze gate's level thresholds are about
	// the model's audio, not about where the volume slider sits.
	p.mono = p.mono[:0]
	for i := 0; i+1 < len(pcm); i += BytesPerSample {
		s := int16(binary.LittleEndian.Uint16(pcm[i : i+2]))
		p.mono = append(p.mono, float32(s)/32768.0)
	}
	samples := p.mono
	if gated && p.freeze != nil {
		samples = p.freeze.Process(samples)
	}
	if len(samples) == 0 {
		return
	}
	for i := range samples {
		samples[i] *= gain
	}

	converted, err := p.resampler.Process(samples)
	if err != nil {
		// Nobody to raise this to, and dropping a blob is survivable where
		// stalling the receive loop is not.
		slog.Warn("could not resample translated audio", "err", err)
		return
	}
	if len(converted) == 0 {
		return
	}

	p.interleaved = p.interleaved[:0]
	for _, v := range converted {
		s := uint16(clampToInt16(v))
		for range p.devChan {
			p.interleaved = binary.LittleEndian.AppendUint16(p.interleaved, s)
		}
	}
	state.ring.Write(p.interleaved)
}

// Buffered is how much audio is waiting, in bytes. For tests and the log.
func (p *Player) Buffered() int {
	state := p.active.Load()
	if state == nil {
		return 0
	}
	return state.ring.Available()
}

// --- device callback thread ----------------------------------------------
// No UI, no allocation, no lock but the ring's own.

func (p *Player) onPlayback(out, _ []byte, frames uint32) {
	if len(out) == 0 {
		return
	}
	state := p.active.Load()
	if state == nil {
		clear(out)
		return
	}

	want := int(frames) * state.blockBytes
	if want <= 0 || want > len(out) {
		want = len(out)
	}
	block := out[:want]

	// Wait for a cushion before the first sound, and again after running dry.
	// Playing the instant one blob lands leaves nothing in hand for the next
	// one to be late with.
	if !p.primed.Load() {
		if state.ring.Available() < state.prebuffer {
			clear(block)
			return
		}
		p.primed.Store(true)
	}

	// Keep the queue short by skipping silence, never speech. The threshold
	// follows the volume, because Write applies the gain before the ring: at
	// a low volume the same pause sits lower, and at zero — muted — only
	// exact silence qualifies, which is what muted audio is.
	if state.trimAbove > 0 {
		gain := math.Float64frombits(p.gain.Load())
		state.ring.skipSilentHead(len(block), state.trimAbove, state.pauseKeep,
			int32(state.silencePeak*gain))
	}

	if n := state.ring.Read(block); n < len(block) {
		// Ran dry. Re-arm the cushion so the next utterance starts with one.
		p.primed.Store(false)
	}
	if want < len(out) {
		clear(out[want:])
	}
}
