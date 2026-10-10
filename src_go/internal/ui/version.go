package ui

// Identity, shown in the About box, the window title and the bundle name.
//
// The port carried the name LiveCapGo while the Python build was still the one
// that shipped, because both installed as an application called LiveCap with
// the same caption window and were repeatedly mistaken for each other. At the
// cutover (PLAN §13) the Go build *is* LiveCap, so the name goes back.
const (
	AppName     = "LiveCap"
	Version     = "1.8.0"
	Author      = "Pyeongwoo Lee"
	AuthorEmail = "dennis2.lee@gmail.com"
)

// WindowTitle is what the title bar shows: the name and the version.
func WindowTitle() string { return AppName + " " + Version }
