package logging

// SetDirForTests redirects the log file at dir and returns a function that puts
// it back.
//
// Same rule as config.SetSettingsDirForTests: a test that writes to the real
// path scribbles on the log the user is actually collecting.
func SetDirForTests(dir string) func() {
	previous := dirOverride
	dirOverride = dir
	return func() { dirOverride = previous }
}
