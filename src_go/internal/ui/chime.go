package ui

import (
	"encoding/binary"
	"math"
)

// The sound the output test makes.
//
// It used to be a 440 Hz sine wave. That is the right sound for measuring a
// device and the wrong one for a person: it is indistinguishable from the tone
// an appliance makes when something has gone wrong, and it gives no sense of
// what speech will sound like through this device — a sine wave is a single
// frequency, and a voice is not.
//
// This is a short mallet-struck major arpeggio instead: C5, E5, G5, C6, each
// note decaying like something that was hit rather than switched on, with a
// silence after it so the pattern reads as a chime repeating rather than a
// continuous alarm. It covers 523 Hz to 3 kHz once the harmonics are counted,
// which is most of where speech lives, so a device that muffles the chime will
// muffle the translation too.
//
// Still synthesised, not a recorded sample. The reasons that ruled out an
// asset for the sine wave hold here: nothing to bundle, nothing to keep in step
// with the packaging on two platforms, and no third party's audio inside a
// binary that gets published. A spoken sample would say more, but every source
// of one carries a redistribution question that a C major triad does not.
var chimeNotes = []struct {
	at    float64 // seconds into the pattern
	freq  float64
	decay float64 // exponential time constant
}{
	{0.00, 523.25, 0.45},  // C5
	{0.16, 659.25, 0.45},  // E5
	{0.32, 783.99, 0.45},  // G5
	{0.48, 1046.50, 0.45}, // C6
}

const (
	// chimePeriod includes the silence, and the silence is not padding — it is
	// what makes the sound read as a chime repeating rather than an alarm
	// sounding. It is also load-bearing: a loop that restarts while the last
	// note is still ringing steps from that level back to zero, and a step is a
	// click. 2.2 s was tried first and left 0.036 on the table, which
	// TestChimeLoopsWithoutAClick caught. At 2.8 s the tail is under 0.006.
	chimePeriod = 2.8
	// chimeAmplitude leaves headroom for the four notes ringing together and
	// for a device already near clipping. Measured peak at this setting is
	// checked by TestChimeDoesNotClip.
	chimeAmplitude = 0.20
)

// There is deliberately no attack envelope.
//
// One was written first — a 4 ms rise on each note, on the usual reasoning that
// a note switched on at full amplitude clicks. Measured over a whole pattern at
// 24 kHz it changed the peak from 0.449 to 0.442 and the largest step between
// consecutive samples from 0.72324 to 0.72323, and removing it broke no test,
// because the premise was wrong: each note is a sine starting at dt = 0, so it
// already begins at zero. The waveform's own first quarter-cycle rises faster
// than any 4 ms envelope could hold it back. Do not add one back without a
// measurement that shows it doing something.

// chimeAt is the waveform at t seconds into the pattern, before gain.
//
// Kept pure and separate from the buffer filling so the shape can be measured
// without an audio device: the peak, the value at the loop point, and whether
// anything is there at all are all properties of this function.
func chimeAt(t float64) float64 {
	var v float64
	for _, n := range chimeNotes {
		dt := t - n.at
		if dt < 0 {
			continue
		}
		// Struck, not switched on. The decay is the whole character: a note
		// held at constant amplitude is the appliance beep this replaced.
		env := math.Exp(-dt / n.decay)
		w := 2 * math.Pi * n.freq * dt
		// Two quiet harmonics give the note a body. A bare sine at these
		// frequencies is the appliance beep again, one octave up.
		v += env * (math.Sin(w) + 0.30*math.Sin(2*w) + 0.10*math.Sin(3*w))
	}
	return v
}

// fillChime writes the next block of the test sound, continuing from pos and
// returning where the following block starts.
//
// Position rather than phase: the notes overlap, so there is no single phase to
// carry between blocks — and a pattern that restarts cleanly needs an absolute
// index to take the modulus of.
func fillChime(pcm []byte, rate int, pos int64) int64 {
	period := int64(chimePeriod * float64(rate))
	for i := 0; i+1 < len(pcm); i += 2 {
		t := float64(pos%period) / float64(rate)
		v := chimeAt(t) * chimeAmplitude
		// The clamp is a backstop, not the level control. If it is ever doing
		// work, chimeAmplitude is wrong.
		v = math.Max(-1, math.Min(1, v))
		binary.LittleEndian.PutUint16(pcm[i:i+2], uint16(int16(v*32767)))
		pos++
	}
	return pos
}
