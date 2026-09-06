//go:build windows

package tts

// The Windows backend: SAPI5 through PowerShell's System.Speech.
//
// This file holds the two exec calls and nothing else — every rule they obey
// (the encoding, the environment, the parsing, the sample sentences) lives in
// sapi.go, where it is tested on any platform. Same split as say_darwin.go.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// createNoWindow keeps the console PowerShell would otherwise open from
// flashing over the captions. The application is linked -H=windowsgui and so
// has no console of its own to inherit; without this, every spoken line
// blinks a black rectangle onto the screen.
const createNoWindow = 0x08000000

// Available reports whether local synthesis can run here.
func Available() bool {
	_, err := powershellPath()
	return err == nil
}

func powershellPath() (string, error) {
	if path, err := exec.LookPath("powershell.exe"); err == nil {
		return path, nil
	}
	// A machine with PowerShell 7 and Windows PowerShell removed still has
	// System.Speech, through the Windows compatibility assemblies.
	if path, err := exec.LookPath("pwsh.exe"); err == nil {
		return path, nil
	}
	return "", ErrUnsupported
}

// script builds the subprocess. The script is base64 UTF-16LE so nothing in
// it passes through a quoting layer, and the environment is redacted so a
// speech synthesizer never sees the API key.
func script(ctx context.Context, body string, env ...string) (*exec.Cmd, error) {
	path, err := powershellPath()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, path,
		"-NoProfile", "-NonInteractive", "-EncodedCommand", encodePowerShell(body))
	cmd.Env = append(redactedEnv(os.Environ()), env...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd, nil
}

// Voices lists the SAPI5 voices registered on this machine, sorted by name.
//
// SAPI5 is what System.Speech can see, which is not everything Windows can
// speak with: the "natural" voices added through Narrator are registered
// under Speech_OneCore and are invisible here. Voices installed with a
// language pack do appear. This is a property of the API, not of the parsing.
func Voices(ctx context.Context) ([]Voice, error) {
	cmd, err := script(ctx, listScript)
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("tts: listing voices: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	voices := parseSAPIVoiceList(string(out))
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
// Through a temporary file for the same reason the macOS path is: SAPI writes
// a WAVE header, and a WAVE header carries sizes it has to seek back and fill
// in, so it is not writable to a pipe.
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

	// The voice name and the translated text go in the environment, never in
	// the script: $env:X is read, not parsed. See sapi.go.
	cmd, err := script(ctx, synthScript(),
		envVoice+"="+voice,
		envText+"="+text,
		envWAV+"="+path)
	if err != nil {
		return nil, err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("tts: sapi: %w: %s", err, strings.TrimSpace(stderr.String()))
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
		return nil, fmt.Errorf("tts: sapi produced %d Hz, asked for %d", rate, Rate)
	}
	return pcm, nil
}
