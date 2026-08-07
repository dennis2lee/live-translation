package platform

// CaptionFontFamilies is the font stack for the captions, Latin first then a
// CJK face.
//
// Korean output needs an explicit Hangul family. Without one the toolkit falls
// back per glyph and the weight and spacing drift away from the Latin text —
// visible in spike F1, where the default stack put a differently-shaped full
// stop after Hangul than after Latin.
func CaptionFontFamilies() []string {
	return captionFontFamiliesFor(runtimeGOOS())
}

func captionFontFamiliesFor(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"SF Pro Display", "Apple SD Gothic Neo", "Helvetica Neue", "Helvetica"}
	case "windows":
		return []string{"Segoe UI", "Malgun Gothic", "Arial"}
	default:
		return []string{"Noto Sans", "Noto Sans CJK KR", "DejaVu Sans"}
	}
}

// RevealCommand is the command that opens a directory in the system file
// manager.
func RevealCommand(path string) []string {
	return revealCommandFor(runtimeGOOS(), path)
}

func revealCommandFor(goos, path string) []string {
	switch goos {
	case "darwin":
		return []string{"open", path}
	case "windows":
		return []string{"explorer", path}
	default:
		return []string{"xdg-open", path}
	}
}

// MenuIsDrawnInWindow reports whether the toolkit paints the menu inside the
// window rather than in a system menu bar.
//
// It decides one real behaviour: going full screen. macOS puts the menu in the
// system bar, which full screen hides by itself. Windows and Linux draw it as
// an overlay on the window, so full screen keeps it on screen — measured on
// Windows on 2026-08-06, where the strip stayed over the captions.
func MenuIsDrawnInWindow() bool { return runtimeGOOS() != "darwin" }
