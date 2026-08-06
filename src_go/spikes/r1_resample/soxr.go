// cgo binding to libsoxr's streaming API — the Go equivalent of
// soxr.ResampleStream(x, last=False), which is what the Python build uses.
//
// Homebrew's libsoxr ships a pkgconfig file but is not on the default
// PKG_CONFIG_PATH, so the paths are given explicitly. If R1 picks soxr, the
// .app build script bundles libsoxr.0.dylib and fixes the rpath (PLAN §10 G6).
package main

/*
#cgo darwin CFLAGS: -I/opt/homebrew/opt/libsoxr/include
#cgo darwin LDFLAGS: -L/opt/homebrew/opt/libsoxr/lib -lsoxr
#include <soxr.h>
#include <stdlib.h>
*/
import "C"

import (
	"errors"
	"unsafe"
)

// SoxrStream converts int16 mono blocks from one rate to another, keeping the
// filter state across calls so block boundaries are seamless.
type SoxrStream struct {
	handle C.soxr_t
	ratio  float64
}

// NewSoxrStream opens a mono int16 → int16 converter at the given quality.
// quality is one of the SOXR_* recipe constants; HQ mirrors python-soxr.
func NewSoxrStream(inRate, outRate float64, quality uint32) (*SoxrStream, error) {
	var cerr C.soxr_error_t
	ioSpec := C.soxr_io_spec(C.SOXR_INT16_I, C.SOXR_INT16_I)
	qSpec := C.soxr_quality_spec(C.ulong(quality), 0)

	h := C.soxr_create(
		C.double(inRate), C.double(outRate), 1,
		&cerr, &ioSpec, &qSpec, nil,
	)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	return &SoxrStream{handle: h, ratio: outRate / inRate}, nil
}

// Process converts one block. The returned slice is freshly allocated, as the
// real capture path will hand it straight to the sender goroutine.
func (s *SoxrStream) Process(in []int16) ([]int16, error) {
	// Headroom: the ratio plus a few samples for filter delay flushing.
	out := make([]int16, int(float64(len(in))*s.ratio)+64)

	var idone, odone C.size_t
	var inPtr unsafe.Pointer
	if len(in) > 0 {
		inPtr = unsafe.Pointer(&in[0])
	}
	cerr := C.soxr_process(
		s.handle,
		C.soxr_in_t(inPtr), C.size_t(len(in)), &idone,
		C.soxr_out_t(unsafe.Pointer(&out[0])), C.size_t(len(out)), &odone,
	)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	if int(idone) != len(in) {
		return nil, errors.New("soxr did not consume the whole block")
	}
	return out[:odone], nil
}

// Drain flushes the filter's remaining delay, the last=True call.
func (s *SoxrStream) Drain() ([]int16, error) {
	out := make([]int16, 4096)
	var odone C.size_t
	cerr := C.soxr_process(
		s.handle,
		nil, 0, nil,
		C.soxr_out_t(unsafe.Pointer(&out[0])), C.size_t(len(out)), &odone,
	)
	if cerr != nil {
		return nil, errors.New(C.GoString(cerr))
	}
	return out[:odone], nil
}

func (s *SoxrStream) Close() {
	if s.handle != nil {
		C.soxr_delete(s.handle)
		s.handle = nil
	}
}

// Quality recipes, from soxr.h.
const (
	SoxrQQ  = uint32(C.SOXR_QQ)
	SoxrLQ  = uint32(C.SOXR_LQ)
	SoxrMQ  = uint32(C.SOXR_MQ)
	SoxrHQ  = uint32(C.SOXR_HQ)
	SoxrVHQ = uint32(C.SOXR_VHQ)
)

func soxrVersion() string { return C.GoString(C.soxr_version()) }
