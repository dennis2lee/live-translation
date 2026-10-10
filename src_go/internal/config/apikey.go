package config

import (
	"log/slog"
	"strings"

	"github.com/zalando/go-keyring"

	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
)

// APIKeyVars are the environment variables consulted for the Gemini key, in
// order.
var APIKeyVars = keySpecs[ProviderGemini].envVars

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
// Real keys start with a known prefix — "AIza" for Google, "sk-" for OpenAI —
// and a random key body could contain a marker like "here" by chance, so never
// reject one of those.
func isPlaceholder(value string) bool {
	for _, spec := range keySpecs {
		if strings.HasPrefix(value, spec.prefix) {
			return false
		}
	}
	low := strings.ToLower(value)
	for _, marker := range placeholderMarkers {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// LoadAPIKey is LoadKey for Gemini, which the spikes and older callers use.
func LoadAPIKey() string { return LoadKey(ProviderGemini) }

// APIKeySource is KeySource for Gemini.
func APIKeySource() string { return KeySource(ProviderGemini) }

// LoadKey looks up a provider's key: environment variable, encrypted store,
// then Keychain. Never stored in source or in the settings file.
func LoadKey(p Provider) string {
	spec := specFor(p)
	for _, name := range spec.envVars {
		if v := strings.TrimSpace(getenv(name)); v != "" && !isPlaceholder(v) {
			return v
		}
	}
	if stored := credentials.Get(spec.envVars[0]); stored != "" && !isPlaceholder(stored) {
		return stored
	}
	v, err := keychainGet(KeyringService, spec.keychainUser)
	if err != nil {
		// Nothing stored is the normal case, so this is not an error worth
		// raising to the user.
		slog.Debug("no key in the keychain", "provider", p, "err", err)
		return ""
	}
	return strings.TrimSpace(v)
}

// KeySource says where a provider's key in force comes from, for Preferences
// to show. It returns "" when there is no key anywhere.
func KeySource(p Provider) string {
	spec := specFor(p)
	for _, name := range spec.envVars {
		if v := strings.TrimSpace(getenv(name)); v != "" && !isPlaceholder(v) {
			return name + " environment variable"
		}
	}
	if stored := credentials.Get(spec.envVars[0]); stored != "" && !isPlaceholder(stored) {
		return "encrypted store"
	}
	if v, err := keychainGet(KeyringService, spec.keychainUser); err == nil && strings.TrimSpace(v) != "" {
		return "macOS Keychain"
	}
	return ""
}

// SaveKey stores a provider's key encrypted, and optionally in the Keychain as
// well.
func SaveKey(p Provider, key string, toKeychain bool) error {
	spec := specFor(p)
	key = strings.TrimSpace(key)
	if err := credentials.Put(spec.envVars[0], key); err != nil {
		return err
	}
	if toKeychain {
		if err := keychainSet(KeyringService, spec.keychainUser, key); err != nil {
			slog.Error("could not write key to keychain", "provider", p, "err", err)
			return err
		}
	}
	return nil
}

// StoredKey is a provider's key held in the encrypted store, ignoring the
// environment variable and the Keychain.
//
// Preferences takes this before it writes anything, so cancelling can put the
// store back exactly as it found it.
func StoredKey(p Provider) string { return credentials.Get(specFor(p).envVars[0]) }

// RestoreStoredKey undoes a write to the encrypted store, back to previous.
//
// Only the store is restored. A key that was also copied into the Keychain
// stays there — there is no way to tell a fresh copy from a pre-existing one.
func RestoreStoredKey(p Provider, previous string) error {
	name := specFor(p).envVars[0]
	if previous != "" {
		return credentials.Put(name, previous)
	}
	return credentials.Remove(name)
}

// ClearKey forgets a provider's key everywhere this application put it.
func ClearKey(p Provider) {
	spec := specFor(p)
	if err := credentials.Remove(spec.envVars[0]); err != nil {
		slog.Warn("could not clear the stored key", "provider", p, "err", err)
	}
	// Nothing stored is the normal case, so a failure here is not reported.
	_ = keychainKill(KeyringService, spec.keychainUser)
}
