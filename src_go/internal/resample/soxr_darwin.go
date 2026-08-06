//go:build darwin

// libsoxr, used on macOS. Chosen over the pure-Go filter by spike R1
// (src_go/spikes/RESULTS.md), and it keeps the numeric path identical to the
// Python build, so transcripts from the two can be compared at the parity gate
// without the resampler as a variable.
//
// Windows and Linux use the pure-Go filter in fir.go instead: cross-building
// and shipping a libsoxr DLL would cost more than the difference is worth, and
// a single self-contained executable is a far better installer.
package resample

/*
// Homebrew on Apple silicon. An Intel Mac installs under /usr/local, and a
// Linux build finds the library on the default path; both are handled by
// setting CGO_CFLAGS/CGO_LDFLAGS rather than by listing a directory here that
// does not exist, which makes the linker warn on every build.
#cgo darwin CFLAGS: -I/opt/homebrew/opt/libsoxr/include
#cgo darwin LDFLAGS: -L/opt/homebrew/opt/libsoxr/lib -lsoxr
#cgo !darwin LDFLAGS: -lsoxr
#include <soxr.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

func init() {
	newNativeStream = newSoxrStream
	nativeName = "libsoxr " + C.GoString(C.soxr_version())
}

// soxrQuality is the recipe used. SOXR_HQ is what python-soxr defaults to and
// what the Python build runs with.
const soxrQuality = C.SOXR_HQ

// soxrStream converts mono float32 blocks from one rate to another, keeping the
// filter state across calls so block boundaries are seamless.
type soxrStream struct {
	handle C.soxr_t
	ratio  float64
	out    []float32 // reused across calls; the callback must not allocate
}

func newSoxrStream(inRate, outRate float64) (Stream, error) {
	var cerr C.soxr_error_t
	ioSpec := C.soxr_io_spec(C.SOXR_FLOAT32_I, C.SOXR_FLOAT32_I)
	qSpec := C.soxr_quality_spec(C.ulong(soxrQuality), 0)

	h := C.soxr_create(C.double(inRate), C.double(outRate), 1, &cerr, &ioSpec, &qSpec, nil)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	return &soxrStream{handle: h, ratio: outRate / inRate}, nil
}

// Process converts one block. The returned slice is only valid until the next
// call — the caller copies what it needs, which the capture path does when it
// converts to PCM16.
func (s *soxrStream) Process(in []float32) ([]float32, error) {
	if s.handle == nil {
		return nil, errClosed
	}
	// Headroom for the ratio plus the filter's own delay.
	want := int(float64(len(in))*s.ratio) + 64
	if cap(s.out) < want {
		s.out = make([]float32, want)
	}
	s.out = s.out[:want]

	var idone, odone C.size_t
	var inPtr unsafe.Pointer
	if len(in) > 0 {
		inPtr = unsafe.Pointer(&in[0])
	}
	cerr := C.soxr_process(
		s.handle,
		C.soxr_in_t(inPtr), C.size_t(len(in)), &idone,
		C.soxr_out_t(unsafe.Pointer(&s.out[0])), C.size_t(len(s.out)), &odone,
	)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	if int(idone) != len(in) {
		return nil, fmt.Errorf("resample: consumed %d of %d samples", int(idone), len(in))
	}
	return s.out[:odone], nil
}

// Drain flushes the filter's remaining delay — the last=True call.
func (s *soxrStream) Drain() ([]float32, error) {
	if s.handle == nil {
		return nil, errClosed
	}
	out := make([]float32, 4096)
	var odone C.size_t
	cerr := C.soxr_process(s.handle, nil, 0, nil,
		C.soxr_out_t(unsafe.Pointer(&out[0])), C.size_t(len(out)), &odone)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	return out[:odone], nil
}

// Close releases the converter. Safe to call twice.
func (s *soxrStream) Close() {
	if s.handle != nil {
		C.soxr_delete(s.handle)
		s.handle = nil
	}
}
