// Package config is the settings schema and its persistence.
//
// The API key is deliberately not part of Settings. It is looked up in order:
// environment variable, encrypted store, macOS Keychain. It is never written to
// the settings file.
package config

import (
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
	ShowSourceText  bool   `json:"show_source_text"`
	Fullscreen      bool   `json:"fullscreen"`
	SilenceGate     bool   `json:"silence_gate"`

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
		TargetLanguageCode: "en",
		CaptionPolicy:      string(captions.PolicyIncremental),
	}
}

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
