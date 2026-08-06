//go:build darwin

package platform

/*
#cgo LDFLAGS: -framework IOKit -framework CoreFoundation
#include <IOKit/pwr_mgt/IOPMLib.h>
#include <CoreFoundation/CoreFoundation.h>
#include <stdlib.h>

// Wrapped in C so the Go side never has to build a CFString by hand.
static IOReturn livecap_block_display_sleep(const char *reason, IOPMAssertionID *id) {
    CFStringRef type = CFSTR("NoDisplaySleepAssertion");
    CFStringRef name = CFStringCreateWithCString(NULL, reason, kCFStringEncodingUTF8);
    IOReturn rc = IOPMAssertionCreateWithName(type, kIOPMAssertionLevelOn, name, id);
    if (name) CFRelease(name);
    return rc;
}
*/
import "C"

import (
	"log/slog"
	"sync"
	"unsafe"
)

// DisplaySleepBlocker holds a power assertion so the screen stays lit while
// captions are live.
//
// Captions are useless if the display blanks mid-session, and the user is
// watching rather than typing, so the idle timer fires. Acquiring is
// best-effort: if the assertion cannot be taken the app carries on and only
// logs it, because translating matters more than the screen staying on.
//
// kIOPMAssertionTypeNoDisplaySleep stops the idle timer that blanks the screen,
// on battery as well as on mains — the same assertion a video player holds. It
// does not stop the lid switch or a manual lock.
type DisplaySleepBlocker struct {
	mu   sync.Mutex
	id   C.IOPMAssertionID
	held bool
}

// Held reports whether the assertion is currently taken.
func (b *DisplaySleepBlocker) Held() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.held
}

// Acquire takes the assertion. It reports whether the display is now pinned
// awake. Idempotent.
func (b *DisplaySleepBlocker) Acquire(reason string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.held {
		return true
	}
	if reason == "" {
		reason = DefaultSleepReason
	}
	cReason := C.CString(reason)
	defer C.free(unsafe.Pointer(cReason))

	var id C.IOPMAssertionID
	if rc := C.livecap_block_display_sleep(cReason, &id); rc != 0 {
		slog.Warn("display sleep assertion refused", "status", int(rc))
		return false
	}
	b.id = id
	b.held = true
	slog.Info("display sleep blocked while translating")
	return true
}

// Release gives the assertion back. Safe to call when nothing is held.
func (b *DisplaySleepBlocker) Release() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.held {
		return
	}
	if rc := C.IOPMAssertionRelease(b.id); rc != 0 {
		slog.Warn("could not release the display sleep assertion", "status", int(rc))
	} else {
		slog.Info("display sleep no longer blocked")
	}
	b.held = false
}
