//go:build !darwin && !windows

package tts

import "context"

// macOS speaks through `say` and Windows through SAPI5; this file is what a
// third platform compiles, so the UI can ask and be told no rather than the
// feature being a compile error off the two that ship.
func Available() bool { return false }

func Voices(context.Context) ([]Voice, error) { return nil, ErrUnsupported }

func Synthesize(context.Context, string, string) ([]byte, error) { return nil, ErrUnsupported }
