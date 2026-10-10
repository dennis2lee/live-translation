package netcheck

import "testing"

// The round trip that matters is to the server the chosen provider's
// sessions connect to.
func TestHostForFollowsTheProvider(t *testing.T) {
	if HostFor("openai") != OpenAIHost || HostFor("gemini") != LiveAPIHost || HostFor("") != LiveAPIHost {
		t.Errorf("openai %q, gemini %q, empty %q", HostFor("openai"), HostFor("gemini"), HostFor(""))
	}
}
