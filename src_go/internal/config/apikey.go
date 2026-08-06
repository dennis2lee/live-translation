package config

import (
	"log/slog"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
)

// APIKeyVars are the environment variables consulted, in order.
var APIKeyVars = []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}

var placeholderMarkers = []string{"your", "xxx", "<", "here"}

// getenv and keychain are indirected so tests can drive them without touching
// the real environment or the real Keychain.
var (
	getenv       = defaultGetenv
	keychainGet  = keyring.Get
	keychainSet  = keyring.Set
	keychainKill = keyring.Delete
)

// isPlaceholder spots an unfilled template value.
//
// Real Google API keys start with "AIza"; a random key body could contain a
// marker like "here" by chance, so never reject one of those.
func isPlaceholder(value string) bool {
	if strings.HasPrefix(value, "AIza") {
		return false
	}
	low := strings.ToLower(value)
	for _, marker := range placeholderMarkers {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// LoadAPIKey looks up the key: environment variable, encrypted store, then
// Keychain. Never stored in source or in the settings file.
func LoadAPIKey() string {
	for _, name := range APIKeyVars {
		if v := strings.TrimSpace(getenv(name)); v != "" && !isPlaceholder(v) {
			return v
		}
	}
	if stored := credentials.Get(APIKeyVars[0]); stored != "" && !isPlaceholder(stored) {
		return stored
	}
	v, err := keychainGet(KeyringService, KeyringUser)
	if err != nil {
		// Nothing stored is the normal case, so this is not an error worth
		// raising to the user.
		slog.Debug("no key in the keychain", "err", err)
		return ""
	}
	return strings.TrimSpace(v)
}

// APIKeySource says where the key in force comes from, for Preferences to show.
// It returns "" when there is no key anywhere.
func APIKeySource() string {
	for _, name := range APIKeyVars {
		if v := strings.TrimSpace(getenv(name)); v != "" && !isPlaceholder(v) {
			return name + " environment variable"
		}
	}
	if stored := credentials.Get(APIKeyVars[0]); stored != "" && !isPlaceholder(stored) {
		return "encrypted store"
	}
	if v, err := keychainGet(KeyringService, KeyringUser); err == nil && strings.TrimSpace(v) != "" {
		return "macOS Keychain"
	}
	return ""
}

// SaveAPIKey stores the key encrypted, and optionally in the Keychain as well.
func SaveAPIKey(key string, toKeychain bool) error {
	key = strings.TrimSpace(key)
	if err := credentials.Put(APIKeyVars[0], key); err != nil {
		return err
	}
	if toKeychain {
		if err := keychainSet(KeyringService, KeyringUser, key); err != nil {
			slog.Error("could not write key to keychain", "err", err)
			return err
		}
	}
	return nil
}

// StoredAPIKey is the key held in the encrypted store, ignoring the environment
// variable and the Keychain.
//
// Preferences takes this before it writes anything, so cancelling can put the
// store back exactly as it found it.
func StoredAPIKey() string { return credentials.Get(APIKeyVars[0]) }

// RestoreStoredAPIKey undoes a write to the encrypted store, back to previous.
//
// Only the store is restored. A key that was also copied into the Keychain
// stays there — there is no way to tell a fresh copy from a pre-existing one.
func RestoreStoredAPIKey(previous string) error {
	if previous != "" {
		return credentials.Put(APIKeyVars[0], previous)
	}
	return credentials.Remove(APIKeyVars[0])
}

// ClearAPIKey forgets the key everywhere this application put it.
func ClearAPIKey() {
	if err := credentials.Remove(APIKeyVars[0]); err != nil {
		slog.Warn("could not clear the stored key", "err", err)
	}
	// Nothing stored is the normal case, so a failure here is not reported.
	_ = keychainKill(KeyringService, KeyringUser)
}
