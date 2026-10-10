package config

// Provider is the service a session translates with.
//
// Gemini is the default and the engine everything in this application was
// measured against. OpenAI's gpt-realtime-translate is the alternative: its
// voice follows the speaker rather than being drawn afresh at every
// reconnect, but it takes no vocabulary hints.
type Provider string

const (
	ProviderGemini Provider = "gemini"
	ProviderOpenAI Provider = "openai"
)

// Providers lists every provider, in the order Preferences shows them.
var Providers = []Provider{ProviderGemini, ProviderOpenAI}

// ProviderMenuText is what Preferences shows for each provider.
var ProviderMenuText = map[Provider]string{
	ProviderGemini: "Google Gemini",
	ProviderOpenAI: "OpenAI",
}

// Valid reports whether p is a provider this build knows.
func (p Provider) Valid() bool {
	_, ok := ProviderMenuText[p]
	return ok
}

// SupportsVocabulary reports whether the provider takes recognition hints.
// OpenAI's translation sessions accept no prompt, glossary or vocabulary.
func (p Provider) SupportsVocabulary() bool { return p != ProviderOpenAI }

// keySpec is where one provider's API key may live.
type keySpec struct {
	// envVars are consulted in order; the first is also the encrypted
	// store's entry name.
	envVars []string
	// keychainUser is the Keychain account. Gemini's is shared with the
	// Python build so both find the same key.
	keychainUser string
	// prefix marks a real key, which is never mistaken for a placeholder
	// however its random body happens to read.
	prefix string
}

var keySpecs = map[Provider]keySpec{
	ProviderGemini: {envVars: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, keychainUser: KeyringUser, prefix: "AIza"},
	ProviderOpenAI: {envVars: []string{"OPENAI_API_KEY"}, keychainUser: "openai-api-key", prefix: "sk-"},
}

// specFor never fails: an unknown provider is treated as Gemini, the same
// fallback Clamped applies to the setting.
func specFor(p Provider) keySpec {
	if s, ok := keySpecs[p]; ok {
		return s
	}
	return keySpecs[ProviderGemini]
}
