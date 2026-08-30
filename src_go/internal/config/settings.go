// Package config is the settings schema and its persistence.
//
// The API key is deliberately not part of Settings. It is looked up in order:
// environment variable, encrypted store, macOS Keychain. It is never written to
// the settings file.
package config

import (
	"reflect"
	"strconv"
	"strings"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/transcript"
)

// Direction is a translation direction preset.
//
// The Live API's TranslationConfig has no source-language field — the model
// detects it. A "direction" therefore reduces to a target language.
type Direction string

const (
	KoreanToEnglish Direction = "ko2en"
	EnglishToKorean Direction = "en2ko"
)

// BoxPosition is where the caption box sits vertically.
type BoxPosition string

const (
	BoxTop    BoxPosition = "top"
	BoxCenter BoxPosition = "center"
	BoxBottom BoxPosition = "bottom"
)

// BoxPositionMenuText labels the position choices.
var BoxPositionMenuText = map[BoxPosition]string{
	BoxTop:    "Top",
	BoxCenter: "Middle",
	BoxBottom: "Bottom",
}

// DirectionTarget maps a preset to the BCP-47 code the API wants.
//
// These are bare language codes on purpose. The API rejects the
// region-qualified form — "en-US" is refused as an invalid argument, and
// refused a second after setupComplete, so it looks like an SDK fault rather
// than a bad code. See src_go/spikes/RESULTS.md.
var DirectionTarget = map[Direction]string{
	KoreanToEnglish: "en",
	EnglishToKorean: "ko",
}

// DirectionLabel is the overlay text.
//
// No spaces around the arrow: Fyne 2.8 renders "KO → EN" as an arrow followed
// by a replacement glyph, while "KO→EN" is correct. Measured in spike F2.
var DirectionLabel = map[Direction]string{
	KoreanToEnglish: "KO→EN",
	EnglishToKorean: "EN→KO",
}

// DirectionMenuText is the menu wording.
var DirectionMenuText = map[Direction]string{
	KoreanToEnglish: "Korean→English",
	EnglishToKorean: "English→Korean",
}

// DirectionForTarget returns the preset for a target language, and whether it
// is one of them.
func DirectionForTarget(targetLanguageCode string) (Direction, bool) {
	code := strings.ToLower(strings.TrimSpace(targetLanguageCode))
	for direction, target := range DirectionTarget {
		if target == code {
			return direction, true
		}
	}
	return "", false
}

// DirectionLabelFor is the overlay label, falling back to just the target for
// languages that are not one of the presets.
func DirectionLabelFor(targetLanguageCode string) string {
	if direction, ok := DirectionForTarget(targetLanguageCode); ok {
		return DirectionLabel[direction]
	}
	code := strings.TrimSpace(targetLanguageCode)
	if code == "" {
		code = "?"
	}
	return "→" + strings.ToUpper(code)
}

// Keychain identifiers, shared with the Python build so both find the same key.
const (
	KeyringService = "LiveCap"
	KeyringUser    = "gemini-api-key"
)

// Settings is everything the user can change. Field names match the Python
// dataclass, so a plist written by that build imports one-for-one.
type Settings struct {
	// colours (hex)
	BgColor    string  `json:"bg_color"`
	BoxColor   string  `json:"box_color"`
	TextColor  string  `json:"text_color"`
	BoxOpacity float64 `json:"box_opacity"` // 0.6 ~ 1.0

	// layout
	CornerRadius   int `json:"corner_radius"`     // 0 ~ 48
	BoxMaxWidthPct int `json:"box_max_width_pct"` // 60 ~ 100

	// BottomMarginPct is the distance from the edge the box is anchored to, as
	// a percentage of the window height. Named for the original bottom-only
	// layout; it is the top margin when BoxPosition is "top", and ignored when
	// centred.
	BottomMarginPct int `json:"bottom_margin_pct"` // 2 ~ 20

	// BoxPosition is where the box sits vertically. Top is useful when the
	// lower part of the screen is covered by something else.
	BoxPosition string `json:"box_position"`

	FontSize int `json:"font_size"` // 24 ~ 96
	MaxLines int `json:"max_lines"` // 1 ~ 4

	// FixedBox keeps the caption box at a fixed size and position, even when
	// empty. Turning it off makes it hug its content and disappear when empty.
	FixedBox bool `json:"fixed_box"`

	// ClearAfterSeconds clears captions this long after the backlog has drained
	// and the input has gone quiet. 0 keeps them on screen.
	ClearAfterSeconds float64 `json:"clear_after_seconds"` // 0 ~ 30

	// LineHoldSeconds is the minimum time a settled line holds the screen
	// before the next replaces it. Stops a burst of sentences scrolling past
	// inside a single repaint.
	LineHoldSeconds float64 `json:"line_hold_seconds"` // 0 ~ 3

	// CompletePhrasesOnly shows only settled text, never a phrase the model is
	// still revising. Trades a little latency for captions that stop moving
	// under the reader.
	CompletePhrasesOnly bool `json:"complete_phrases_only"`

	// Transcript recording. The filename is the session start timestamp.
	SaveTranscript bool   `json:"save_transcript"`
	TranscriptDir  string `json:"transcript_dir"`

	// ShowDirectionHint briefly shows KO→EN below the caption box on start and
	// on direction change.
	ShowDirectionHint bool `json:"show_direction_hint"`

	// behaviour
	AudioDeviceName string `json:"audio_device_name"` // empty means the system default input
	DebugLogging    bool   `json:"debug_logging"`

	// SpeakTranslation plays the model's translated speech aloud. The audio
	// has always been arriving — it is generated whether or not anyone listens,
	// so this costs nothing extra to switch on.
	//
	// Off by default, and that is not caution. Measured: with speakers rather
	// than headphones the translation returns through the microphone and is
	// translated again, and echo_target_language does not prevent it
	// (spikes/RESULTS.md).
	SpeakTranslation bool `json:"speak_translation"`

	// OutputDeviceName is empty for the system default output. Stored by name,
	// never index, for the same reason as AudioDeviceName.
	OutputDeviceName string `json:"output_device_name"`

	// SpeechVolume scales the spoken translation, 0.0 to 1.0. Its own control
	// because the translation competes with a live speaker in the same room,
	// and the system slider moves everything else along with it.
	SpeechVolume float64 `json:"speech_volume"`

	ShowSourceText bool `json:"show_source_text"`
	Fullscreen     bool `json:"fullscreen"`
	SilenceGate    bool `json:"silence_gate"`

	// SpeechVoices is the voice that speaks the translation, per target
	// language. Absent or empty means the model's own audio, whose voice
	// changes at every reconnect and cannot be pinned — measured 2026-08-30,
	// three sessions all requesting "Charon" came back at 228.6, 154.8 and
	// 233.0 Hz median f0. A name here is a voice installed on this machine,
	// and it never changes.
	//
	// Per language rather than one name, because the direction keys switch
	// the target between "en" and "ko" while the app runs, and an English
	// voice reading Korean is not a voice the user chose either.
	SpeechVoices map[string]string `json:"speech_voices"`

	// MusicGate skips sending audio the local YAMNet classifier calls music,
	// so songs are not translated. Off by default: it needs a one-time model
	// download, and a gate nobody asked for that eats a sentence would be a
	// defect with no explanation.
	MusicGate bool `json:"music_gate"`
	// MusicGateThreshold is the music-family score at which a frame counts as
	// musical. 0.5 is measured, not guessed: Korean TTS speech never pushed
	// the music family past 0.004, real music medians 0.67–0.89, and 0.5 is
	// the crossover at which speech twice as loud as backing music still
	// reads as speech (spikes/y3_gatecal).
	MusicGateThreshold float64 `json:"music_gate_threshold"`
	// MusicGateHold is how many seconds of continuous music close the gate.
	MusicGateHold float64 `json:"music_gate_hold"`
	// MusicGateRelease is how many seconds of continuous non-music reopen it.
	// 1.5 is measured: the classifier dips below the threshold for a single
	// frame constantly inside real music, and 1.5 s is where the leak stops
	// falling — 19.5 s of music leaked at 1.0 s, 5.5 s at 1.5 s, 5.0 s at
	// 2.0 s (spike y5_flap). The pre-roll makes the wait cost no speech.
	MusicGateRelease float64 `json:"music_gate_release"`

	// Terms to bias speech recognition towards, one per line. Two lists,
	// because they are genuinely different: what the model should hear in the
	// source language, and how names should come out in the captions. They must
	// stay separate — a Korean name hint must never reach the output side.
	VocabularySource   string `json:"vocabulary_source"`
	VocabularyCaptions string `json:"vocabulary_captions"`

	// API
	TargetLanguageCode string `json:"target_language_code"`
	EchoTargetLanguage bool   `json:"echo_target_language"`

	// CaptionPolicy is fixed to A by measurement (spikes/RESULTS.md):
	// fragments arrive incrementally, median 14 chars, about once per second.
	CaptionPolicy string `json:"caption_policy"`
}

// Default returns the settings a fresh install starts with.
func Default() Settings {
	return Settings{
		BgColor:            "#000000",
		BoxColor:           "#1B4965",
		TextColor:          "#FFFFFF",
		BoxOpacity:         1.0,
		CornerRadius:       24,
		BoxMaxWidthPct:     90,
		BottomMarginPct:    8,
		BoxPosition:        string(BoxBottom),
		FontSize:           44,
		MaxLines:           2,
		FixedBox:           true,
		ClearAfterSeconds:  2.0,
		LineHoldSeconds:    0.7,
		SaveTranscript:     true,
		TranscriptDir:      transcript.DefaultDir,
		ShowDirectionHint:  true,
		SpeechVolume:       1.0,
		MusicGateThreshold: 0.5,
		MusicGateHold:      2.0,
		MusicGateRelease:   1.5,
		TargetLanguageCode: "en",
		CaptionPolicy:      string(captions.PolicyIncremental),
	}
}

// Equal compares two Settings.
//
// It exists because SpeechVoices made the struct uncomparable with ==, and the
// alternative — one voice for every direction — would put an English voice on
// Korean captions. reflect rather than a field-by-field list: this runs once
// when a dialog closes, never in a loop, and a hand-written comparison would
// silently stop covering the next field somebody adds.
func (s Settings) Equal(other Settings) bool {
	return reflect.DeepEqual(s, other)
}

// VoiceFor is the chosen voice for a target language, or "" for the model's.
func (s Settings) VoiceFor(languageCode string) string {
	return s.SpeechVoices[voiceKey(languageCode)]
}

// WithVoiceFor returns a copy with one language's voice set; an empty name
// removes the entry rather than storing a blank.
func (s Settings) WithVoiceFor(languageCode, voice string) Settings {
	out := s
	out.SpeechVoices = map[string]string{}
	for k, v := range s.SpeechVoices {
		out.SpeechVoices[k] = v
	}
	key := voiceKey(languageCode)
	if strings.TrimSpace(voice) == "" {
		delete(out.SpeechVoices, key)
	} else {
		out.SpeechVoices[key] = strings.TrimSpace(voice)
	}
	if len(out.SpeechVoices) == 0 {
		out.SpeechVoices = nil
	}
	return out
}

// voiceKey normalises a language code to the bare lowercase language, so
// "en", "EN" and "en-US" all name the same voice.
func voiceKey(languageCode string) string {
	code := strings.ToLower(strings.TrimSpace(languageCode))
	if i := strings.IndexAny(code, "-_"); i > 0 {
		code = code[:i]
	}
	return code
}

// maxSpeechVoices bounds a hand-edited settings file. There are only ever a
// couple of directions in play; anything past this is a corrupt file, not a
// preference.
const maxSpeechVoices = 32

// Clamped returns a copy with every value forced back into its documented
// range. A settings file edited by hand cannot break the application.
func (s Settings) Clamped() Settings {
	out := s
	out.BgColor = hexColour(s.BgColor, "#000000")
	out.BoxColor = hexColour(s.BoxColor, "#1B4965")
	out.TextColor = hexColour(s.TextColor, "#FFFFFF")
	out.BoxOpacity = clamp(s.BoxOpacity, 0.6, 1.0)
	out.CornerRadius = int(clamp(float64(s.CornerRadius), 0, 48))
	out.BoxMaxWidthPct = int(clamp(float64(s.BoxMaxWidthPct), 60, 100))
	out.BottomMarginPct = int(clamp(float64(s.BottomMarginPct), 2, 20))
	switch BoxPosition(s.BoxPosition) {
	case BoxTop, BoxCenter, BoxBottom:
	default:
		out.BoxPosition = string(BoxBottom)
	}
	out.FontSize = int(clamp(float64(s.FontSize), 24, 96))
	out.MaxLines = int(clamp(float64(s.MaxLines), 1, 4))
	out.ClearAfterSeconds = clamp(s.ClearAfterSeconds, 0, 30)
	out.LineHoldSeconds = clamp(s.LineHoldSeconds, 0, 3)
	// 0 is a legal volume — muted — so this cannot treat it as "unset". An
	// upgraded settings file has no speech_volume key at all, and Load starts
	// from Default() and unmarshals into it, so the absent key keeps 1.0
	// rather than arriving here as a silent zero. See store.go.
	out.SpeechVolume = clamp(s.SpeechVolume, 0, 1)
	// Unlike SpeechVolume, zero here can only mean "the key was absent from
	// an older settings file" — a threshold of 0 would gate on everything and
	// a hold of 0 would close on a single frame, neither of which is a
	// setting anyone can choose (the sliders stop well short).
	if s.MusicGateThreshold == 0 {
		out.MusicGateThreshold = 0.5
	} else {
		out.MusicGateThreshold = clamp(s.MusicGateThreshold, 0.1, 0.9)
	}
	if s.MusicGateHold == 0 {
		out.MusicGateHold = 2.0
	} else {
		out.MusicGateHold = clamp(s.MusicGateHold, 0.5, 10)
	}
	out.SpeechVoices = nil
	if len(s.SpeechVoices) > 0 {
		out.SpeechVoices = map[string]string{}
		for lang, voice := range s.SpeechVoices {
			key, name := voiceKey(lang), strings.TrimSpace(voice)
			if key == "" || name == "" || len(out.SpeechVoices) >= maxSpeechVoices {
				continue
			}
			out.SpeechVoices[key] = name
		}
		if len(out.SpeechVoices) == 0 {
			out.SpeechVoices = nil
		}
	}
	if s.MusicGateRelease == 0 {
		out.MusicGateRelease = 1.5
	} else {
		out.MusicGateRelease = clamp(s.MusicGateRelease, 0.5, 5)
	}
	if strings.TrimSpace(s.TranscriptDir) == "" {
		out.TranscriptDir = transcript.DefaultDir
	}
	out.VocabularySource = normaliseTerms(s.VocabularySource)
	out.VocabularyCaptions = normaliseTerms(s.VocabularyCaptions)
	if strings.TrimSpace(s.TargetLanguageCode) == "" {
		out.TargetLanguageCode = "en"
	}
	switch captions.Policy(s.CaptionPolicy) {
	case captions.PolicyAuto, captions.PolicyIncremental, captions.PolicyUtterance:
	default:
		out.CaptionPolicy = string(captions.PolicyIncremental)
	}
	return out
}

// Policy is the caption commit policy.
func (s Settings) Policy() captions.Policy { return captions.Policy(s.CaptionPolicy) }

// Position is where the caption box sits.
func (s Settings) Position() BoxPosition { return BoxPosition(s.BoxPosition) }

// SourceTerms are vocabulary hints for the language being spoken.
func (s Settings) SourceTerms() []string { return ParseTerms(s.VocabularySource) }

// CaptionTerms are vocabulary hints for the language on screen.
func (s Settings) CaptionTerms() []string { return ParseTerms(s.VocabularyCaptions) }

// MaxVocabularyTerms is our own ceiling, not the API's: the terms ride in the
// setup message of every session, including each reconnect, so an unbounded
// list would slow every swap.
const MaxVocabularyTerms = 200

// ParseTerms reads one term per line. Blank lines and surrounding spaces are
// dropped, duplicates collapse, and the first MaxVocabularyTerms survive.
//
// Split on newlines only, never commas — a phrase can be a whole clause, and
// clauses contain commas.
func ParseTerms(text string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, line := range strings.Split(text, "\n") {
		term := strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if term == "" || seen[term] {
			continue
		}
		seen[term] = true
		out = append(out, term)
		if len(out) == MaxVocabularyTerms {
			break
		}
	}
	return out
}

// normaliseTerms stores exactly what will be sent: stripped, de-duplicated,
// capped.
func normaliseTerms(text string) string {
	return strings.Join(ParseTerms(text), "\n")
}

func clamp(v, lo, hi float64) float64 {
	if v != v { // NaN
		return lo
	}
	return max(lo, min(hi, v))
}

func hexColour(v, fallback string) string {
	v = strings.TrimSpace(v)
	if len(v) == 7 && strings.HasPrefix(v, "#") {
		if _, err := strconv.ParseUint(v[1:], 16, 32); err == nil {
			return strings.ToUpper(v)
		}
	}
	return fallback
}
