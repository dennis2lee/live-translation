package config

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/dennis2lee/live-translation/src_go/internal/platform"
)

// Filename of the settings file inside the application-support directory.
//
// The Python build used a QSettings plist. JSON in our own directory is
// deliberate: it keeps the format ours rather than CFPreferences', and it is
// readable when something goes wrong. MigrateFromPlist imports the old one.
const Filename = "settings.json"

// dirOverride lets tests redirect persistence. Never set outside tests — a test
// that writes to the real path overwrites the preferences actually in use.
var dirOverride string

func settingsDir() string {
	if dirOverride != "" {
		return dirOverride
	}
	return platform.AppSupportDir()
}

// Path is the settings file.
func Path() string { return filepath.Join(settingsDir(), Filename) }

// Load reads the settings. Anything missing or malformed falls back to the
// default and is clamped, so a hand-edited file cannot stop the app starting.
func Load() Settings {
	settings := Default()
	data, err := os.ReadFile(Path())
	if err != nil {
		return settings.Clamped()
	}
	// Unmarshalling into the defaults leaves absent keys at their default.
	if err := json.Unmarshal(data, &settings); err != nil {
		slog.Error("settings file is not valid JSON — using defaults", "path", Path(), "err", err)
		return Default().Clamped()
	}
	return settings.Clamped()
}

// Save writes the settings, clamped.
func Save(s Settings) error {
	dir := settingsDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s.Clamped(), "", "  ")
	if err != nil {
		return err
	}
	// Write then rename, so an interrupted write cannot truncate the file.
	temp := Path() + ".tmp"
	if err := os.WriteFile(temp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, Path())
}

// Reset forgets every setting and returns the defaults.
func Reset() Settings {
	if err := os.Remove(Path()); err != nil && !os.IsNotExist(err) {
		slog.Warn("could not remove the settings file", "err", err)
	}
	return Default()
}
