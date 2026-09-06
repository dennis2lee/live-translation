package tts

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

const sapiList = "Microsoft David Desktop\ten-US\r\n" +
	"Microsoft Heami Desktop\tko-KR\r\n" +
	"Microsoft Zira Desktop\ten-US\r\n"

func TestSAPIVoicesParse(t *testing.T) {
	got := parseSAPIVoiceList(sapiList)
	if len(got) != 3 {
		t.Fatalf("parsed %d voices, want 3: %+v", len(got), got)
	}
	if got[1].Name != "Microsoft Heami Desktop" || got[1].Language != "ko-KR" {
		t.Errorf("second voice = %+v, want the Korean one with its name intact", got[1])
	}
	// A SAPI name holds spaces, so the tab is the only field separator there
	// is; splitting on whitespace would keep "Microsoft" and drop the rest.
	if strings.Contains(got[0].Name, "\t") || got[0].Name != "Microsoft David Desktop" {
		t.Errorf("first voice name = %q, want the whole name", got[0].Name)
	}
}

// PowerShell prints an error to stdout in some configurations, and a blank
// trailing line always. Neither is a voice.
func TestProseIsNotAVoice(t *testing.T) {
	out := "\r\nAdd-Type : could not do the thing\r\nMicrosoft Zira Desktop\ten-US\r\n\r\n"
	got := parseSAPIVoiceList(out)
	if len(got) != 1 || got[0].Name != "Microsoft Zira Desktop" {
		t.Fatalf("parsed %+v, want only the real voice", got)
	}
}

// The hyphenated culture SAPI prints has to survive the locale check and the
// language match — `say` prints en_US and this one prints en-US.
func TestAHyphenatedCultureIsALocale(t *testing.T) {
	for _, s := range []string{"en-US", "en_US", "ko-KR", "ko", "zh-Hans"} {
		if !looksLikeLocale(s) {
			t.Errorf("looksLikeLocale(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "-US", "english-US", "e", "1a-US", "en-U"} {
		if looksLikeLocale(s) {
			t.Errorf("looksLikeLocale(%q) = true, want false", s)
		}
	}
}

func TestASAPIVoiceMatchesItsTargetLanguage(t *testing.T) {
	v := parseSAPIVoiceList("Microsoft Heami Desktop\tko-KR\n")[0]
	if !v.Matches("ko") {
		t.Error("a ko-KR voice does not match target ko — the picker would be empty")
	}
	if v.Matches("en") {
		t.Error("a ko-KR voice matched target en")
	}
}

// Preview judges a voice, so it has to speak the voice's own language: a
// Korean voice reading English says nothing about the Korean voice.
func TestEachLanguageGetsItsOwnSample(t *testing.T) {
	ko := sampleFor("ko-KR")
	en := sampleFor("en-US")
	if ko == en {
		t.Fatal("the Korean sample is the English one")
	}
	if !strings.ContainsAny(ko, "가나다라마바사아자차카타파하목소리번역") {
		t.Errorf("the ko sample is not Korean: %q", ko)
	}
	if sampleFor("qq-ZZ") != en {
		t.Error("an unknown language should fall back to the English sample")
	}
	for _, lang := range []string{"ko", "KO", "ko_KR", " ko-kr "} {
		if sampleFor(lang) != ko {
			t.Errorf("sampleFor(%q) did not normalise to the Korean sample", lang)
		}
	}
}

// -EncodedCommand takes UTF-16LE base64. Getting the encoding wrong is not a
// parse error at our end — PowerShell simply runs something else.
func TestTheScriptIsEncodedAsPowerShellWantsIt(t *testing.T) {
	got := encodePowerShell("Ab")
	if want := "QQBiAA=="; got != want {
		t.Fatalf("encodePowerShell(%q) = %q, want %q", "Ab", got, want)
	}
	// A voice name or a sample sentence can be outside the BMP; the encoder
	// has to surrogate-pair rather than truncate.
	round := decodeUTF16LE(t, encodePowerShell("한국어 🎵"))
	if round != "한국어 🎵" {
		t.Errorf("round trip = %q, want the original", round)
	}
}

// The whole point of the environment is that the model's text never reaches
// PowerShell's parser. If a script ever interpolated it, this catches it.
func TestNoScriptCarriesItsOwnData(t *testing.T) {
	for name, body := range map[string]string{"list": listScript, "synth": synthScript()} {
		if !strings.Contains(body, "System.Speech") {
			t.Errorf("%s script does not load System.Speech", name)
		}
	}
	synth := synthScript()
	for _, env := range []string{envVoice, envText, envWAV} {
		if !strings.Contains(synth, "$env:"+env) {
			t.Errorf("the synth script does not read $env:%s — data would have to be interpolated", env)
		}
	}
	// The rate is asked for explicitly, so the player's converter is set up
	// the same way whichever voice speaks.
	if !strings.Contains(synth, "24000") {
		t.Error("the synth script does not pin the sample rate")
	}
}

// The API key is in this process's environment on the documented path. A
// speech synthesizer has no business inheriting it.
func TestTheSynthesizerNeverInheritsTheKey(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"SystemRoot=C:\\Windows",
		"GEMINI_API_KEY=sk-do-not-leak",
		"GOOGLE_API_KEY=sk-do-not-leak-either",
		"LIVECAP_SECRET=nope",
		"GITHUB_TOKEN=nope",
		"TEMP=C:\\Temp",
	}
	got := strings.Join(redactedEnv(in), "\n")
	if strings.Contains(got, "do-not-leak") || strings.Contains(got, "nope") {
		t.Fatalf("a credential survived redaction:\n%s", got)
	}
	// PowerShell needs a working environment; redaction must not empty it.
	for _, need := range []string{"PATH=", "SystemRoot=", "TEMP="} {
		if !strings.Contains(got, need) {
			t.Errorf("redactedEnv dropped %s, which PowerShell needs", need)
		}
	}
}

func decodeUTF16LE(t *testing.T, encoded string) string {
	t.Helper()
	raw := decodeBase64(t, encoded)
	if len(raw)%2 != 0 {
		t.Fatalf("odd number of bytes: %d", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	return string(utf16.Decode(units))
}

func decodeBase64(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("not base64: %v", err)
	}
	return raw
}
