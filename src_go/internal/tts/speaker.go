package tts

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Speaker turns committed caption lines into audio, one at a time, and paces
// the result into a player.
//
// Two things make this more than a `for line := range queue` loop.
//
// Pacing. Synthesis runs about seven times faster than realtime (measured:
// 0.56 s of wall clock for 3.9 s of speech, process spawn included), while
// the player's ring holds PlayRingMS of audio and drops the oldest when it
// overflows. Handing it a whole four-second utterance at once would therefore
// throw most of that utterance away. So the PCM goes out in blocks, no
// further ahead of realtime than leadMS.
//
// Backlog. If translation outruns speech — a fast speaker, a long queue — the
// right thing to discard is the oldest line, not the newest. That is playRing's
// rule and it is the same rule for the same reason: a listener wants to hear
// what is being said now, not to fall further behind catching up.
type Speaker struct {
	out   func([]byte)
	synth func(ctx context.Context, voice, text string) ([]byte, error)

	mu     sync.Mutex
	voice  string
	queue  []string
	closed bool
	wake   chan struct{}

	cancel context.CancelFunc
	done   chan struct{}

	spoken  atomic.Int64
	dropped atomic.Int64
	failed  atomic.Int64

	// gen counts Clears. A line remembers the generation it was taken in,
	// and stops being sent the moment Clear moves it on.
	gen atomic.Int64
}

const (
	// blockMS is how much audio is handed to the player at a time.
	blockMS = 100
	// leadMS is how far ahead of realtime the speaker may run. It has to stay
	// under audio.PlayRingMS (2000) or the ring drops what it is given; 800
	// leaves room for the player's own 200 ms pre-buffer and for a late block.
	leadMS = 800
	// queueDepth is how many unspoken lines are kept. Three lines is around
	// twelve seconds of speech; past that a listener is not following a
	// conversation any more, they are listening to a recording of one.
	queueDepth = 3
)

// NewSpeaker starts the worker. out receives mono PCM16 at Rate, in blocks —
// in the application it is the player's Write.
func NewSpeaker(voice string, out func([]byte)) *Speaker {
	return newSpeakerWith(voice, out, Synthesize)
}

func newSpeakerWith(voice string, out func([]byte), synth func(context.Context, string, string) ([]byte, error)) *Speaker {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Speaker{
		out:    out,
		synth:  synth,
		voice:  voice,
		wake:   make(chan struct{}, 1),
		cancel: cancel,
		done:   make(chan struct{}),
	}
	go s.run(ctx)
	return s
}

// Say queues one line. It never blocks: it is called from the UI goroutine on
// every committed caption line.
func (s *Speaker) Say(text string) {
	text = sanitize(text)
	if text == "" {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.queue = append(s.queue, text)
	for len(s.queue) > queueDepth {
		s.queue = s.queue[1:]
		s.dropped.Add(1)
	}
	s.mu.Unlock()

	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// SetVoice changes the voice for the next utterance. The one being spoken
// finishes in the old voice rather than being cut off mid-word.
func (s *Speaker) SetVoice(voice string) {
	s.mu.Lock()
	s.voice = voice
	s.mu.Unlock()
}

// Voice is the voice the next utterance will use.
func (s *Speaker) Voice() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.voice
}

// Clear drops everything queued, and abandons the line being spoken: no
// further block of it is sent, though one already past the check as Clear
// runs may still go out — the player's own flush catches that one. Used when the session stops, so a
// stale sentence does not arrive after the captions have gone, and when the
// translation is muted — where finishing the line in flight would be up to
// several seconds of speech the room was meant not to hear.
func (s *Speaker) Clear() {
	s.mu.Lock()
	s.queue = nil
	s.gen.Add(1)
	s.mu.Unlock()
}

// Stop ends the worker. Safe to call more than once; the Speaker is finished
// afterwards.
func (s *Speaker) Stop() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	s.queue = nil
	s.mu.Unlock()

	s.cancel()
	<-s.done
}

// Stats reports what the run did: lines spoken, lines dropped for backlog,
// and lines synthesis refused.
func (s *Speaker) Stats() (spoken, dropped, failed int64) {
	return s.spoken.Load(), s.dropped.Load(), s.failed.Load()
}

func (s *Speaker) run(ctx context.Context) {
	defer close(s.done)
	for {
		text, voice, gen, ok := s.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-s.wake:
				continue
			}
		}
		pcm, err := s.synth(ctx, voice, text)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// Never fatal. A voice that has been uninstalled since it was
			// chosen must not take the captions down with it.
			s.failed.Add(1)
			slog.Warn("could not speak the translation", "voice", voice, "err", err)
			continue
		}
		if s.emit(ctx, pcm, gen) {
			s.spoken.Add(1)
		}
	}
}

func (s *Speaker) next() (text, voice string, gen int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 {
		return "", "", 0, false
	}
	text, s.queue = s.queue[0], s.queue[1:]
	return text, s.voice, s.gen.Load(), true
}

// emit paces one utterance into the player and reports whether it finished.
//
// gen is the generation the line was taken in; a Clear since then ends it.
func (s *Speaker) emit(ctx context.Context, pcm []byte, gen int64) bool {
	blockBytes := blockMS * Rate / 1000 * 2
	if blockBytes <= 0 || len(pcm) == 0 {
		return false
	}
	start := time.Now()
	sent := 0
	for off := 0; off < len(pcm); off += blockBytes {
		if s.gen.Load() != gen {
			return false
		}
		end := min(off+blockBytes, len(pcm))
		s.out(pcm[off:end])
		sent += end - off

		// Sleep only when we are running further ahead of realtime than the
		// player's ring can hold.
		played := time.Duration(sent) * time.Second / time.Duration(Rate*2)
		if ahead := played - time.Since(start); ahead > leadMS*time.Millisecond {
			select {
			case <-ctx.Done():
				return false
			case <-time.After(ahead - leadMS*time.Millisecond):
			}
		}
		if ctx.Err() != nil {
			return false
		}
	}
	return true
}
