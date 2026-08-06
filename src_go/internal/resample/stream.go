// Package resample converts audio between sample rates.
//
// Two implementations, chosen at build time. macOS links libsoxr, because that
// keeps the numeric path identical to the Python build and spike R1 measured it
// as the better filter. Everywhere else uses the pure-Go windowed sinc in
// fir.go, so the executable carries no external library — which is what makes a
// Windows installer a single file rather than a file plus a DLL.
//
// The streaming API is what matters in both. Calling a one-shot resample per
// chunk clicks at every seam, because the filter state does not carry across.
package resample

import (
	"errors"
	"fmt"
)

// errClosed is returned by every implementation after Close.
var errClosed = errors.New("resample: stream is closed")

// Stream converts mono float32 blocks from one rate to another, keeping the
// filter state across calls so block boundaries are seamless.
//
// A Stream is used from the audio callback goroutine only.
type Stream interface {
	// Process converts one block. The returned slice is only valid until the
	// next call — the caller copies what it needs.
	Process(in []float32) ([]float32, error)
	// Drain flushes the filter's remaining delay.
	Drain() ([]float32, error)
	// Close releases the converter. Safe to call twice.
	Close()
}

// newNativeStream is set by the platform that has one. Left nil, the pure-Go
// filter is used.
var (
	newNativeStream func(inRate, outRate float64) (Stream, error)
	nativeName      string
)

// NewStream opens a mono float32 converter.
func NewStream(inRate, outRate float64) (Stream, error) {
	if inRate <= 0 || outRate <= 0 {
		return nil, fmt.Errorf("resample: bad rates %v -> %v", inRate, outRate)
	}
	if newNativeStream != nil {
		return newNativeStream(inRate, outRate)
	}
	return newFIRStream(inRate, outRate), nil
}

// Version names the filter in use, for the selftest and the logs.
func Version() string {
	if nativeName != "" {
		return nativeName
	}
	return firName
}
