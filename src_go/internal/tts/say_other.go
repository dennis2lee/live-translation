//go:build !darwin

package tts

import "context"

// Only macOS ships, and `say` is macOS's. The Windows build compiles this
// file so the UI can ask and be told no, rather than the feature being a
// compile error off the shipping platform.
func Available() bool { return false }

func Voices(context.Context) ([]Voice, error) { return nil, ErrUnsupported }

func Synthesize(context.Context, string, string) ([]byte, error) { return nil, ErrUnsupported }
