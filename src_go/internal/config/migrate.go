package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// PlistName is the file QSettings("LiveCap", "LiveCap") writes on macOS.
const PlistName = "com.LiveCap.LiveCap.plist"

// PlistPath is where the Python build's preferences live.
func PlistPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Preferences", PlistName)
}

// MigrateFromPlist imports the Python build's preferences once, when this build
// has no settings file of its own. It reports whether anything was imported.
//
// The plist is binary, so it is read through plutil rather than parsed here —
// this runs at most once per install, on macOS only, and a hand-rolled binary
// plist reader would be a lot of code for that.
//
// Values that fail to convert are left at their default rather than blocking
// the import: a partial migration is better than none, and everything is
// clamped afterwards anyway.
func MigrateFromPlist() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	if _, err := os.Stat(Path()); err == nil {
		return false // this build already has settings of its own
	}
	path := PlistPath()
	if path == "" {
		return false
	}
	if _, err := os.Stat(path); err != nil {
		return false
	}

	raw, err := readPlistAsJSON(path)
	if err != nil {
		slog.Warn("could not read the old preferences", "path", path, "err", err)
		return false
	}
	if len(raw) == 0 {
		return false
	}

	settings := applyPlistValues(Default(), raw)
	if err := Save(settings); err != nil {
		slog.Error("could not write the migrated settings", "err", err)
		return false
	}
	slog.Info("imported preferences from the Python build", "from", path, "to", Path())
	return true
}

func readPlistAsJSON(path string) (map[string]any, error) {
	out, err := exec.Command("/usr/bin/plutil", "-convert", "json", "-o", "-", path).Output()
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(out, &values); err != nil {
		return nil, err
	}
	return values, nil
}

// applyPlistValues copies whatever it recognises onto the settings. QSettings
// writes numbers and booleans as strings on macOS, so every field is parsed
// from its string form as well as its native one.
func applyPlistValues(s Settings, raw map[string]any) Settings {
	str := func(key string, dst *string) {
		if v, ok := raw[key]; ok {
			*dst = asString(v)
		}
	}
	num := func(key string, dst *float64) {
		if v, ok := raw[key]; ok {
			if f, ok := asFloat(v); ok {
				*dst = f
			}
		}
	}
	integer := func(key string, dst *int) {
		if v, ok := raw[key]; ok {
			if f, ok := asFloat(v); ok {
				*dst = int(f)
			}
		}
	}
	boolean := func(key string, dst *bool) {
		if v, ok := raw[key]; ok {
			*dst = asBool(v)
		}
	}

	str("bg_color", &s.BgColor)
	str("box_color", &s.BoxColor)
	str("text_color", &s.TextColor)
	num("box_opacity", &s.BoxOpacity)
	integer("corner_radius", &s.CornerRadius)
	integer("box_max_width_pct", &s.BoxMaxWidthPct)
	integer("bottom_margin_pct", &s.BottomMarginPct)
	str("box_position", &s.BoxPosition)
	integer("font_size", &s.FontSize)
	integer("max_lines", &s.MaxLines)
	boolean("fixed_box", &s.FixedBox)
	num("clear_after_seconds", &s.ClearAfterSeconds)
	num("line_hold_seconds", &s.LineHoldSeconds)
	boolean("complete_phrases_only", &s.CompletePhrasesOnly)
	boolean("save_transcript", &s.SaveTranscript)
	str("transcript_dir", &s.TranscriptDir)
	boolean("show_direction_hint", &s.ShowDirectionHint)
	str("audio_device_name", &s.AudioDeviceName)
	boolean("debug_logging", &s.DebugLogging)
	boolean("show_source_text", &s.ShowSourceText)
	boolean("fullscreen", &s.Fullscreen)
	boolean("silence_gate", &s.SilenceGate)
	boolean("music_gate", &s.MusicGate)
	num("music_gate_threshold", &s.MusicGateThreshold)
	num("music_gate_hold", &s.MusicGateHold)
	str("vocabulary_source", &s.VocabularySource)
	str("vocabulary_captions", &s.VocabularyCaptions)
	str("target_language_code", &s.TargetLanguageCode)
	boolean("echo_target_language", &s.EchoTargetLanguage)
	str("caption_policy", &s.CaptionPolicy)
	return s
}

func asString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return ""
	}
}

func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

// asBool follows the Python loader: anything that is not "true", "1" or "yes"
// is false.
func asBool(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "1", "yes":
			return true
		}
		return false
	default:
		return false
	}
}
