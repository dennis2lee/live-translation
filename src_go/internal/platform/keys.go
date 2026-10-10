package platform

// ScanCode is the hardware code GLFW reports for a letter key on the US
// physical layout, or -1 where it is not known.
//
// The application binds m and f by key, not by character, so that an input
// method holding the character back cannot swallow them. Fyne names a letter
// key by asking the current keyboard layout (glfw.GetKeyName), and under a
// Korean input source that answer is not guaranteed to be "m"; the scan code
// does not depend on the layout. Values: macOS virtual key codes
// (kVK_ANSI_M = 46, kVK_ANSI_F = 3), Windows set-1 scan codes (0x32, 0x21),
// X11 key codes (58, 41; Wayland would report evdev codes, 50 and 33, but
// Linux does not ship).
func ScanCode(letter rune) int { return scanCodeFor(runtimeGOOS(), letter) }

func scanCodeFor(goos string, letter rune) int {
	codes := map[string]map[rune]int{
		"darwin":  {'M': 46, 'F': 3},
		"windows": {'M': 0x32, 'F': 0x21},
		"linux":   {'M': 58, 'F': 41},
	}
	if c, ok := codes[goos][letter]; ok {
		return c
	}
	return -1
}
