package ui

import (
	"strings"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

func selectProvider(p *prefsDialog, pr config.Provider) {
	p.provider.SetSelected(config.ProviderMenuText[pr])
}

// Choosing OpenAI and applying makes it the provider, and the key typed goes
// to OpenAI's store — never Gemini's.
func TestChoosingOpenAIStoresItsKeySeparately(t *testing.T) {
	w := newTestWindow(t)
	if err := config.SaveKey(config.ProviderGemini, "AIza-gemini", false); err != nil {
		t.Fatal(err)
	}
	p := openPrefs(t, w)
	selectProvider(p, config.ProviderOpenAI)
	if !strings.Contains(p.keyHeading.Text, "OpenAI") {
		t.Errorf("heading = %q after choosing OpenAI", p.keyHeading.Text)
	}
	p.apiKey.SetText("sk-proj-typed")
	p.apply()

	if w.settings.Provider != config.ProviderOpenAI {
		t.Errorf("Provider = %q after Apply", w.settings.Provider)
	}
	if got := config.StoredKey(config.ProviderOpenAI); got != "sk-proj-typed" {
		t.Errorf("OpenAI's store holds %q", got)
	}
	if got := config.StoredKey(config.ProviderGemini); got != "AIza-gemini" {
		t.Errorf("Gemini's store holds %q — the OpenAI key went to the wrong place", got)
	}
}

// A key typed and then followed by a change of provider is dropped, not saved
// as the newly selected provider's.
func TestSwitchingProviderDropsTheTypedKey(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	p.apiKey.SetText("AIza-typed-for-gemini")
	selectProvider(p, config.ProviderOpenAI)
	if p.apiKey.Text != "" {
		t.Fatalf("the key field still holds %q after switching provider", p.apiKey.Text)
	}
	p.apply()
	if got := config.StoredKey(config.ProviderOpenAI); got != "" {
		t.Errorf("a Gemini key was saved as OpenAI's: %q", got)
	}
}

// Cancel puts back every provider's key and the provider itself.
func TestCancelRestoresEveryProvidersKey(t *testing.T) {
	w := newTestWindow(t)
	if err := config.SaveKey(config.ProviderOpenAI, "sk-original", false); err != nil {
		t.Fatal(err)
	}
	p := openPrefs(t, w)
	selectProvider(p, config.ProviderOpenAI)
	p.forgetKey()
	p.apiKey.SetText("sk-typed-by-mistake")
	p.apply()
	p.cancel()
	if got := config.StoredKey(config.ProviderOpenAI); got != "sk-original" {
		t.Errorf("OpenAI's key after Cancel = %q", got)
	}
	if w.settings.Provider != config.ProviderGemini {
		t.Errorf("Provider after Cancel = %q", w.settings.Provider)
	}
}

// The dialog opens on the provider in force, showing that provider's key.
func TestTheDialogOpensOnTheProviderInForce(t *testing.T) {
	w := newTestWindow(t)
	w.settings.Provider = config.ProviderOpenAI
	p := openPrefs(t, w)
	if p.selectedProvider() != config.ProviderOpenAI || !strings.Contains(p.keyHeading.Text, "OpenAI") {
		t.Errorf("opened on %q, heading %q", p.selectedProvider(), p.keyHeading.Text)
	}
}

// Starting with OpenAI chosen needs OpenAI's key; Gemini's does not count, and
// the message says which key is missing.
func TestStartingWithOpenAINeedsItsOwnKey(t *testing.T) {
	w := newTestWindow(t)
	if err := config.SaveKey(config.ProviderGemini, "AIza-gemini", false); err != nil {
		t.Fatal(err)
	}
	w.settings.Provider = config.ProviderOpenAI
	w.StartTranslation()
	if w.state != translator.StateError || !strings.Contains(w.errorDetail, "OpenAI API key") {
		t.Errorf("state %v, detail %q", w.state, w.errorDetail)
	}
}

// The production translator is told which provider to use.
func TestTheTranslatorIsBuiltForTheChosenProvider(t *testing.T) {
	w := newTestWindow(t)
	w.settings.Provider = config.ProviderOpenAI
	if got := w.translatorOptions(audio.NewRing(4096), "sk-x").Provider; got != translator.ProviderOpenAI {
		t.Errorf("Options.Provider = %q", got)
	}
}

// Warm handover is on by default, reaches the translator, and is switched off
// from Behaviour.
func TestWarmHandoverIsOnAndCanBeSwitchedOff(t *testing.T) {
	w := newTestWindow(t)
	if !w.settings.WarmHandover {
		t.Fatal("warm handover is off by default")
	}
	if !w.translatorOptions(audio.NewRing(4096), "AIza-x").WarmHandover {
		t.Error("the translator is not told to hand over warm")
	}
	p := openPrefs(t, w)
	p.warmHandover.SetChecked(false)
	p.apply()
	if w.settings.WarmHandover {
		t.Error("unchecking warm handover did not apply")
	}
	if w.translatorOptions(audio.NewRing(4096), "AIza-x").WarmHandover {
		t.Error("the translator still hands over warm after it was switched off")
	}
}
