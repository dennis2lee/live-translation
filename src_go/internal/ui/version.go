package ui

// Identity, shown in the About box, the window title and the bundle name.
//
// AppName is deliberately *not* "LiveCap". The Python build is still the one
// that ships, and both install as an application called LiveCap with the same
// caption window — during the port they were repeatedly mistaken for each
// other, once badly enough to credit the Python build's behaviour to this one.
// The name goes back to LiveCap at the cutover (PLAN §12/§13).
const (
	AppName     = "LiveCapGo"
	Version     = "1.0.0"
	Author      = "Pyeongwoo Lee"
	AuthorEmail = "dennis2.lee@gmail.com"
)
