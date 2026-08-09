package platform

// There is no caption font stack here, and that is deliberate.
//
// The Python build named font *families* per platform, because that is what
// QFont takes. Fyne does not have that API at all: a theme supplies a font
// resource — an actual .ttf — and canvas.Text takes a FontSource. A list of
// family names is not something any part of Fyne can be handed, so the ported
// function decided nothing and was called by nothing.
//
// Spike F1 settled the question it existed to answer: Fyne 2.8 renders Hangul
// correctly with its bundled theme font, with no font loading, no custom theme
// and no embedded face. Latin and Hangul come from different faces, so their
// metrics differ slightly — F1 recorded a full stop sitting differently after
// Hangul than after Latin — and embedding one face (Noto Sans KR, ~5 MB) is
// filed there as a deliberate choice for cross-platform determinism, not as a
// fix for a defect. If that is ever taken up, it belongs in a Fyne theme with
// the .ttf in the bundle, not in a list of names.

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
