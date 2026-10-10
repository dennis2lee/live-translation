package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A settings file from before providers existed has no "provider" key, and
// must keep translating with Gemini rather than arrive at an empty provider.
func TestAnOlderSettingsFileKeepsGemini(t *testing.T) {
	dir, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(`{"font_size": 40}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load().Provider; got != ProviderGemini {
		t.Errorf("Provider = %q, want gemini", got)
	}
}

func TestAnUnknownProviderFallsBackToGemini(t *testing.T) {
	s := Default()
	s.Provider = "anthropic-someday"
	if got := s.Clamped().Provider; got != ProviderGemini {
		t.Errorf("Clamped Provider = %q, want gemini", got)
	}
	s.Provider = ProviderOpenAI
	if got := s.Clamped().Provider; got != ProviderOpenAI {
		t.Errorf("Clamped dropped a valid provider: %q", got)
	}
}

func TestTheProviderIsSaved(t *testing.T) {
	isolate(t)
	s := Default()
	s.Provider = ProviderOpenAI
	if err := Save(s); err != nil {
		t.Fatal(err)
	}
	if got := Load().Provider; got != ProviderOpenAI {
		t.Errorf("Provider after a round trip = %q", got)
	}
	raw, _ := json.Marshal(s)
	if !json.Valid(raw) {
		t.Fatal("settings do not marshal")
	}
}

// Each provider's key lives in its own place, and one never answers for the
// other: a Gemini key sent to OpenAI is a rejected session and a leaked key.
func TestKeysAreKeptPerProvider(t *testing.T) {
	_, keychain := isolate(t)
	if err := SaveKey(ProviderGemini, "AIza-gemini", true); err != nil {
		t.Fatal(err)
	}
	if got := LoadKey(ProviderOpenAI); got != "" {
		t.Errorf("OpenAI found the Gemini key: %q", got)
	}
	if err := SaveKey(ProviderOpenAI, "sk-openai", true); err != nil {
		t.Fatal(err)
	}
	if g, o := LoadKey(ProviderGemini), LoadKey(ProviderOpenAI); g != "AIza-gemini" || o != "sk-openai" {
		t.Errorf("Gemini %q, OpenAI %q", g, o)
	}
	if keychain[KeyringService+"/openai-api-key"] != "sk-openai" ||
		keychain[KeyringService+"/"+KeyringUser] != "AIza-gemini" {
		t.Errorf("Keychain = %v", keychain)
	}

	ClearKey(ProviderOpenAI)
	if got := LoadKey(ProviderOpenAI); got != "" {
		t.Errorf("OpenAI key survived ClearKey: %q", got)
	}
	if got := LoadKey(ProviderGemini); got != "AIza-gemini" {
		t.Errorf("clearing OpenAI's key took Gemini's: %q", got)
	}
}

func TestTheOpenAIKeyComesFromItsOwnVariable(t *testing.T) {
	isolate(t)
	t.Cleanup(SetEnvForTests(func(name string) string {
		if name == "OPENAI_API_KEY" {
			return "sk-proj-from-env"
		}
		return ""
	}))
	if got := LoadKey(ProviderOpenAI); got != "sk-proj-from-env" {
		t.Errorf("LoadKey = %q", got)
	}
	if got := KeySource(ProviderOpenAI); got != "OPENAI_API_KEY environment variable" {
		t.Errorf("KeySource = %q", got)
	}
	if got := LoadKey(ProviderGemini); got != "" {
		t.Errorf("Gemini read OPENAI_API_KEY: %q", got)
	}
}

// An OpenAI key's random body can contain "here" or "your" like any other;
// its prefix says it is real.
func TestARealOpenAIKeyIsNotAPlaceholder(t *testing.T) {
	if isPlaceholder("sk-proj-abcHEREyourXXX") {
		t.Error("a real-looking OpenAI key was taken for a placeholder")
	}
	if !isPlaceholder("your-openai-key-here") {
		t.Error("a template value was taken for a key")
	}
}

// An older settings file has no warm_handover key and gets the default.
func TestAnOlderSettingsFileGetsWarmHandover(t *testing.T) {
	dir, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(`{"font_size": 40}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Load().WarmHandover {
		t.Error("an older settings file came back with warm handover off")
	}
}

// An unknown provider's key is looked for where Gemini's is, the same
// fallback Clamped applies to the setting — never in OpenAI's places.
func TestAnUnknownProvidersKeyIsGeminis(t *testing.T) {
	isolate(t)
	if err := SaveKey(ProviderGemini, "AIza-gemini", false); err != nil {
		t.Fatal(err)
	}
	if got := LoadKey("someday"); got != "AIza-gemini" {
		t.Errorf("LoadKey(unknown) = %q", got)
	}
}
