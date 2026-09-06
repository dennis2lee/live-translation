package ui

import (
	_ "embed"

	"fyne.io/fyne/v2"
)

// The window icon, drawn by packaging/make_icon.py from the same 32x32 grid
// as the .icns and the .ico, so the title bar, the taskbar and the desktop all
// show one picture.
//
// It has to be set explicitly. GLFW only picks an icon out of the executable
// when it finds a resource *named* GLFW_ICON, and rsrc writes the .ico under a
// numeric id — so on Windows GLFW fell through to the generic default while
// Explorer read the real icon straight out of the binary. The desktop was
// right and the title bar was not, and nothing in the packaging could fix it
// because the packaging was already correct.
//
//go:embed appicon.png
var appIconPNG []byte

// AppIcon is the embedded icon as a Fyne resource.
func AppIcon() fyne.Resource { return fyne.NewStaticResource("appicon.png", appIconPNG) }
