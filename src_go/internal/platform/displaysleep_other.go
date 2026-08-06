//go:build !darwin

package platform

// DisplaySleepBlocker is the non-macOS stub.
//
// Windows would use SetThreadExecutionState and Linux an inhibit call over
// D-Bus. Neither ships, so neither is guessed at here — Acquire returns false
// rather than pretending, which is the same contract platform_support.py has.
type DisplaySleepBlocker struct{}

// Held always reports false: nothing is ever taken.
func (b *DisplaySleepBlocker) Held() bool { return false }

// Acquire refuses, honestly.
func (b *DisplaySleepBlocker) Acquire(reason string) bool { return false }

// Release is a no-op.
func (b *DisplaySleepBlocker) Release() {}
