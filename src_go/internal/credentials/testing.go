package credentials

// SetStoreDirForTests redirects the credential store at dir and returns a
// function that puts it back.
//
// Every test that writes must call this. The Python suite learned this the hard
// way: a test that touches the real store overwrites the API key actually in
// use. See the Test isolation section of CLAUDE.md.
func SetStoreDirForTests(dir string) func() {
	previous := dirOverride
	dirOverride = dir
	return func() { dirOverride = previous }
}
