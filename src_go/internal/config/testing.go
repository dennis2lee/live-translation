package config

// SetSettingsDirForTests redirects the settings file at dir and returns a
// function that puts it back.
//
// Both persistence locations have to be redirected, not just one: the settings
// file here and the credential store in internal/credentials. A test run that
// misses either reads and overwrites what the user actually has.
func SetSettingsDirForTests(dir string) func() {
	previous := dirOverride
	dirOverride = dir
	return func() { dirOverride = previous }
}

// SetEnvForTests replaces the environment lookup used by the API-key functions.
func SetEnvForTests(lookup func(string) string) func() {
	previous := getenv
	getenv = lookup
	return func() { getenv = previous }
}

// SetKeychainForTests replaces the Keychain calls, so no test ever reaches the
// real one.
func SetKeychainForTests(get func(service, user string) (string, error),
	set func(service, user, value string) error,
	remove func(service, user string) error) func() {
	pg, ps, pr := keychainGet, keychainSet, keychainKill
	keychainGet, keychainSet, keychainKill = get, set, remove
	return func() { keychainGet, keychainSet, keychainKill = pg, ps, pr }
}
