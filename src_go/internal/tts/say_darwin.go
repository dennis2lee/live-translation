//go:build darwin

package tts

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Available reports whether local synthesis can run here.
func Available() bool {
	_, err := exec.LookPath("say")
	return err == nil
}

// Voices lists what is installed, sorted by name. The list is stable for the
// life of a login session in practice, so callers cache it; it costs a
// subprocess.
func Voices(ctx context.Context) ([]Voice, error) {
	out, err := exec.CommandContext(ctx, "say", "-v", "?").Output()
	if err != nil {
		return nil, fmt.Errorf("tts: listing voices: %w", err)
	}
	voices := parseVoiceList(string(out))
	sort.Slice(voices, func(a, b int) bool {
		if voices[a].Name != voices[b].Name {
			return voices[a].Name < voices[b].Name
		}
		return voices[a].Language < voices[b].Language
	})
	return voices, nil
}

// Synthesize renders text in the named voice and returns mono PCM16 at Rate.
//
// It goes through a temporary file rather than a pipe because `say` writes a
// WAVE header, and a WAVE header is not writable to a stream — it carries
// sizes it has to seek back and fill in. The file is 24 kB per second and is
// removed before the audio is handed back.
func Synthesize(ctx context.Context, voice, text string) ([]byte, error) {
	text = sanitize(text)
	if text == "" {
		return nil, nil
	}
	if voice == "" {
		return nil, errors.New("tts: no voice selected")
	}

	dir, err := os.MkdirTemp("", "livecap-tts-")
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "u.wav")

	// The text is an argument, never a shell word: exec.Command does not go
	// through a shell, and sanitize has already removed say's own [[…]]
	// command syntax. "--" stops a translation that begins with a dash from
	// being read as a flag.
	cmd := exec.CommandContext(ctx, "say",
		"-v", voice,
		"--data-format=LEI16@24000",
		"--file-format=WAVE",
		"-o", path,
		"--", text)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tts: say: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("tts: %w", err)
	}
	pcm, rate, err := pcmFromWAV(raw)
	if err != nil {
		return nil, err
	}
	if rate != Rate {
		return nil, fmt.Errorf("tts: say produced %d Hz, asked for %d", rate, Rate)
	}
	return pcm, nil
}
