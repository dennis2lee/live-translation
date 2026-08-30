// Package tts speaks text with a voice installed on this machine.
//
// It exists because the translation model will not hold a voice. Measured
// again on 2026-08-30 (spike v3_voiceid): three consecutive sessions, every
// one of them opened with speech_config voice "Charon", came back with median
// f0 of 228.6 Hz, 154.8 Hz and 233.0 Hz — fingerprint distances of 3.97 and
// 8.35 at the two swaps, against a 3.0 threshold. There is no field that asks
// the model for a voice and is honoured, and a session swap every nine
// minutes is not optional, so a voice the user picks and keeps has to be
// synthesized here.
//
// Speaking locally also takes the model's generated audio out of the output
// path entirely, which is why it is the answer to more than one complaint.
//
// Like classify and captions, this package holds no Fyne and no genai
// dependency and is unit-tested. Synthesis shells out to `say`: no cgo, the
// same reason the resampler is pure Go. Everything except the two exec calls
// is portable and tested on any platform.
package tts

import (
	"encoding/binary"
	"errors"
	"strings"
)

// Rate is the sample rate synthesis is asked for. It matches the rate the
// translation model sends, so the player's converter is set up the same way
// whichever voice is speaking.
const Rate = 24000

// ErrUnsupported is returned by the platform calls where `say` does not exist.
var ErrUnsupported = errors.New("tts: local speech synthesis is macOS-only")

// Voice is one voice installed on the machine.
type Voice struct {
	// Name is what `say -v` takes, parentheses and all — "Eddy (English (US))"
	// is a real and valid voice name.
	Name string
	// Language is the BCP-ish locale `say` prints, e.g. "en_US".
	Language string
	// Sample is the demo sentence the voice list ships with, in that voice's
	// language. It is what Preview speaks.
	Sample string
}

// Matches reports whether the voice speaks the given target language code.
// The setting is a bare code ("en"), the voice list a locale ("en_US"), so
// the comparison is on the prefix — and it has to be a whole first component,
// or "en" would match "eng" and target "ko" would match nothing sensible.
func (v Voice) Matches(languageCode string) bool {
	code := strings.ToLower(strings.TrimSpace(languageCode))
	if code == "" {
		return true
	}
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	lang := strings.ToLower(v.Language)
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	return lang == code
}

// parseVoiceList reads the table `say -v '?'` prints:
//
//	Albert              en_US    # Hello! My name is Albert.
//	Eddy (English (US)) en_US    # Hello! My name is Eddy.
//
// The name can hold spaces and nested parentheses, so it is not the first
// field; the locale is the last field before the comment, and the name is
// everything to its left.
func parseVoiceList(out string) []Voice {
	var voices []Voice
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		head, sample, _ := strings.Cut(line, "#")
		fields := strings.Fields(head)
		if len(fields) < 2 {
			continue
		}
		locale := fields[len(fields)-1]
		name := strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(head, " \t"), locale))
		if name == "" || !looksLikeLocale(locale) {
			continue
		}
		voices = append(voices, Voice{
			Name:     name,
			Language: locale,
			Sample:   strings.TrimSpace(sample),
		})
	}
	return voices
}

// looksLikeLocale keeps a stray line of prose from being read as a voice.
func looksLikeLocale(s string) bool {
	lang, region, hasRegion := strings.Cut(s, "_")
	if len(lang) < 2 || len(lang) > 3 || !allLetters(lang) {
		return false
	}
	if hasRegion && (len(region) < 2 || !allLetters(region)) {
		return false
	}
	return true
}

func allLetters(s string) bool {
	for _, r := range s {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') {
			return false
		}
	}
	return len(s) > 0
}

// sanitize strips `say`'s embedded command syntax from text before speaking
// it. The text is a translation produced by a remote model: it is data, and
// `[[pbas 20]]` in data must not be allowed to retune the voice the user
// chose. Measured — say really does act on it rather than reading it aloud.
func sanitize(text string) string {
	for {
		open := strings.Index(text, "[[")
		if open < 0 {
			break
		}
		closeAt := strings.Index(text[open:], "]]")
		if closeAt < 0 {
			text = text[:open]
			break
		}
		text = text[:open] + " " + text[open+closeAt+2:]
	}
	return strings.TrimSpace(text)
}

// pcmFromWAV returns the sample data of a mono 16-bit WAV, and its rate.
// Written rather than reused because the one in the spikes returns float32
// and the player wants the bytes.
func pcmFromWAV(raw []byte) ([]byte, int, error) {
	if len(raw) < 12 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errors.New("tts: not a WAV")
	}
	rate := 0
	pos := 12
	for pos+8 <= len(raw) {
		id := string(raw[pos : pos+4])
		size := int(binary.LittleEndian.Uint32(raw[pos+4 : pos+8]))
		body := pos + 8
		if body+size > len(raw) || size < 0 {
			size = len(raw) - body
		}
		switch id {
		case "fmt ":
			if size >= 8 {
				rate = int(binary.LittleEndian.Uint32(raw[body+4 : body+8]))
			}
		case "data":
			return raw[body : body+size], rate, nil
		}
		pos = body + size + size%2
	}
	return nil, rate, errors.New("tts: no data chunk")
}
