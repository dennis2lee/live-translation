package config

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
)

// isolate redirects **both** persistence locations and stubs the Keychain, the
// job tests/conftest.py does for the Python build. Without it a test run reads
// and overwrites the key and the preferences actually in use.
func isolate(t *testing.T) (dir string, keychain map[string]string) {
	t.Helper()
	dir = t.TempDir()
	t.Cleanup(SetSettingsDirForTests(dir))
	t.Cleanup(credentials.SetStoreDirForTests(dir))
	t.Cleanup(SetEnvForTests(func(string) string { return "" }))

	keychain = map[string]string{}
	t.Cleanup(SetKeychainForTests(
		func(service, user string) (string, error) {
			if v, ok := keychain[service+"/"+user]; ok {
				return v, nil
			}
			return "", errors.New("not found")
		},
		func(service, user, value string) error {
			keychain[service+"/"+user] = value
			return nil
		},
		func(service, user string) error {
			delete(keychain, service+"/"+user)
			return nil
		},
	))
	return dir, keychain
}

// --- schema ---------------------------------------------------------------

func TestClampingPullsEverythingIntoRange(t *testing.T) {
	wild := Settings{
		BgColor:            "not a colour",
		BoxColor:           "#12345",
		TextColor:          "#ffffff",
		BoxOpacity:         5,
		CornerRadius:       999,
		BoxMaxWidthPct:     10,
		BottomMarginPct:    -4,
		BoxPosition:        "sideways",
		FontSize:           4,
		MaxLines:           40,
		ClearAfterSeconds:  -1,
		LineHoldSeconds:    99,
		TranscriptDir:      "  ",
		TargetLanguageCode: "",
		CaptionPolicy:      "nonsense",
	}
	got := wild.Clamped()

	checks := []struct {
		name string
		got  any
		want any
	}{
		{"BgColor", got.BgColor, "#000000"},
		{"BoxColor", got.BoxColor, "#1B4965"},
		{"TextColor", got.TextColor, "#FFFFFF"}, // upper-cased, not replaced
		{"BoxOpacity", got.BoxOpacity, 1.0},
		{"CornerRadius", got.CornerRadius, 48},
		{"BoxMaxWidthPct", got.BoxMaxWidthPct, 60},
		{"BottomMarginPct", got.BottomMarginPct, 2},
		{"BoxPosition", got.BoxPosition, string(BoxBottom)},
		{"FontSize", got.FontSize, 24},
		{"MaxLines", got.MaxLines, 4},
		{"ClearAfterSeconds", got.ClearAfterSeconds, 0.0},
		{"LineHoldSeconds", got.LineHoldSeconds, 3.0},
		{"TargetLanguageCode", got.TargetLanguageCode, "en"},
		{"CaptionPolicy", got.CaptionPolicy, "incremental"},
	}
	for _, c := range checks {
		if !reflect.DeepEqual(c.got, c.want) {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	if got.TranscriptDir == "" {
		t.Error("TranscriptDir was left blank")
	}
}

func TestDefaultsSurviveClamping(t *testing.T) {
	if got := Default().Clamped(); got != Default() {
		t.Errorf("clamping changed the defaults:\n got %+v\nwant %+v", got, Default())
	}
}

// --- vocabulary -----------------------------------------------------------

func TestParseTerms(t *testing.T) {
	got := ParseTerms("  Gemini  \n\n영업이익률\nGemini\n  \nrevenue, margin and cost\n")
	want := []string{"Gemini", "영업이익률", "revenue, margin and cost"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseTerms = %q, want %q", got, want)
	}
}

// A phrase can be a whole clause, and clauses contain commas.
func TestParseTermsNeverSplitsOnCommas(t *testing.T) {
	got := ParseTerms("the operating margin, year on year")
	if len(got) != 1 {
		t.Errorf("ParseTerms = %q, want one whole clause", got)
	}
}

func TestParseTermsIsCapped(t *testing.T) {
	var lines []string
	for i := 0; i < MaxVocabularyTerms+50; i++ {
		lines = append(lines, "term"+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if got := len(ParseTerms(strings.Join(lines, "\n"))); got != MaxVocabularyTerms {
		t.Errorf("%d terms survived, want the %d cap", got, MaxVocabularyTerms)
	}
}

// The two lists must stay separate: a Korean name hint must never reach the
// output-side vocabulary.
func TestSourceAndCaptionTermsAreSeparate(t *testing.T) {
	s := Settings{VocabularySource: "영업이익률", VocabularyCaptions: "Pyeongwoo Lee"}
	if got := s.SourceTerms(); !reflect.DeepEqual(got, []string{"영업이익률"}) {
		t.Errorf("SourceTerms = %q", got)
	}
	if got := s.CaptionTerms(); !reflect.DeepEqual(got, []string{"Pyeongwoo Lee"}) {
		t.Errorf("CaptionTerms = %q", got)
	}
}

// --- direction ------------------------------------------------------------

func TestDirectionTargetsAreBareLanguageCodes(t *testing.T) {
	// The API rejects the region-qualified form. This is the constant that
	// cost a debugging session in spike L1.
	if got := DirectionTarget[KoreanToEnglish]; got != "en" {
		t.Errorf("KO→EN target = %q, want %q", got, "en")
	}
	if got := DirectionTarget[EnglishToKorean]; got != "ko" {
		t.Errorf("EN→KO target = %q, want %q", got, "ko")
	}
}

// Fyne renders an arrow with spaces around it as arrow-plus-tofu. Spike F2.
func TestDirectionLabelsHaveNoSpacesAroundTheArrow(t *testing.T) {
	for direction, label := range DirectionLabel {
		if strings.Contains(label, " →") || strings.Contains(label, "→ ") {
			t.Errorf("%s label %q has a space next to the arrow", direction, label)
		}
	}
	if got := DirectionLabelFor("fr"); strings.Contains(got, "→ ") {
		t.Errorf("fallback label %q has a space next to the arrow", got)
	}
}

func TestDirectionLookup(t *testing.T) {
	if d, ok := DirectionForTarget(" EN "); !ok || d != KoreanToEnglish {
		t.Errorf("DirectionForTarget(\" EN \") = %v, %v", d, ok)
	}
	if _, ok := DirectionForTarget("fr"); ok {
		t.Error("fr should not match a preset")
	}
	if got := DirectionLabelFor("fr"); got != "→FR" {
		t.Errorf("DirectionLabelFor(fr) = %q", got)
	}
	if got := DirectionLabelFor(""); got != "→?" {
		t.Errorf("DirectionLabelFor(\"\") = %q", got)
	}
}

// --- persistence ----------------------------------------------------------

func TestSaveThenLoadRoundTrips(t *testing.T) {
	isolate(t)
	want := Default()
	want.FontSize = 60
	want.CompletePhrasesOnly = true
	want.AudioDeviceName = "Sound Blaster X3"
	want.VocabularySource = "영업이익률"
	if err := Save(want); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got != want {
		t.Errorf("round trip lost something:\n got %+v\nwant %+v", got, want)
	}
}

func TestLoadWithNoFileGivesDefaults(t *testing.T) {
	isolate(t)
	if got := Load(); got != Default() {
		t.Errorf("Load with no file = %+v", got)
	}
}

// A hand-edited file must not stop the application starting.
func TestCorruptSettingsFileFallsBackToDefaults(t *testing.T) {
	dir, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got != Default() {
		t.Errorf("Load of a corrupt file = %+v, want defaults", got)
	}
}

// A file holding only some keys keeps the defaults for the rest.
func TestPartialSettingsFileKeepsDefaults(t *testing.T) {
	dir, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(`{"font_size": 72}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load()
	if got.FontSize != 72 {
		t.Errorf("FontSize = %d", got.FontSize)
	}
	if got.MaxLines != Default().MaxLines {
		t.Errorf("MaxLines = %d, want the default %d", got.MaxLines, Default().MaxLines)
	}
}

// Out-of-range values on disk are clamped on the way in.
func TestLoadClamps(t *testing.T) {
	dir, _ := isolate(t)
	if err := os.WriteFile(filepath.Join(dir, Filename), []byte(`{"font_size": 900}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load().FontSize; got != 96 {
		t.Errorf("FontSize = %d, want the 96 ceiling", got)
	}
}

func TestResetForgetsEverything(t *testing.T) {
	isolate(t)
	changed := Default()
	changed.FontSize = 90
	if err := Save(changed); err != nil {
		t.Fatal(err)
	}
	if got := Reset(); got != Default() {
		t.Errorf("Reset returned %+v", got)
	}
	if got := Load(); got != Default() {
		t.Errorf("Load after Reset = %+v", got)
	}
}

// The settings file must never hold the API key.
func TestSettingsFileNeverHoldsTheKey(t *testing.T) {
	dir, _ := isolate(t)
	if err := SaveAPIKey("AIzaSyExampleNotARealKey", false); err != nil {
		t.Fatal(err)
	}
	if err := Save(Default()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, Filename))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "AIza") {
		t.Error("the settings file contains the API key")
	}
}

// --- API key --------------------------------------------------------------

func TestAPIKeyLookupOrder(t *testing.T) {
	_, keychain := isolate(t)

	// Nothing anywhere.
	if got := LoadAPIKey(); got != "" {
		t.Errorf("LoadAPIKey with nothing stored = %q", got)
	}
	if got := APIKeySource(); got != "" {
		t.Errorf("APIKeySource with nothing stored = %q", got)
	}

	// Keychain only.
	keychain[KeyringService+"/"+KeyringUser] = "from-keychain"
	if got := LoadAPIKey(); got != "from-keychain" {
		t.Errorf("LoadAPIKey = %q, want the keychain value", got)
	}
	if got := APIKeySource(); got != "macOS Keychain" {
		t.Errorf("APIKeySource = %q", got)
	}

	// The encrypted store beats the Keychain.
	if err := SaveAPIKey("from-store", false); err != nil {
		t.Fatal(err)
	}
	if got := LoadAPIKey(); got != "from-store" {
		t.Errorf("LoadAPIKey = %q, want the stored value", got)
	}
	if got := APIKeySource(); got != "encrypted store" {
		t.Errorf("APIKeySource = %q", got)
	}

	// The environment beats both.
	defer SetEnvForTests(func(name string) string {
		if name == "GEMINI_API_KEY" {
			return "  from-env  "
		}
		return ""
	})()
	if got := LoadAPIKey(); got != "from-env" {
		t.Errorf("LoadAPIKey = %q, want the trimmed environment value", got)
	}
	if got := APIKeySource(); got != "GEMINI_API_KEY environment variable" {
		t.Errorf("APIKeySource = %q", got)
	}
}

// An unfilled template value must not be taken for a key.
func TestPlaceholdersAreIgnored(t *testing.T) {
	isolate(t)
	defer SetEnvForTests(func(name string) string {
		if name == "GEMINI_API_KEY" {
			return "your-key-here"
		}
		return ""
	})()
	if got := LoadAPIKey(); got != "" {
		t.Errorf("LoadAPIKey took a placeholder: %q", got)
	}
}

// A real key that happens to contain a marker word must still be accepted.
func TestRealKeysAreNeverMistakenForPlaceholders(t *testing.T) {
	isolate(t)
	real := "AIzaSyBhereXXXsomething"
	defer SetEnvForTests(func(name string) string {
		if name == "GEMINI_API_KEY" {
			return real
		}
		return ""
	})()
	if got := LoadAPIKey(); got != real {
		t.Errorf("LoadAPIKey rejected a real key: %q", got)
	}
}

// Cancelling Preferences must put the store back exactly as it was.
func TestRestoreStoredAPIKey(t *testing.T) {
	isolate(t)
	if err := SaveAPIKey("original", false); err != nil {
		t.Fatal(err)
	}
	before := StoredAPIKey()
	if err := SaveAPIKey("typed-by-mistake", false); err != nil {
		t.Fatal(err)
	}
	if err := RestoreStoredAPIKey(before); err != nil {
		t.Fatal(err)
	}
	if got := StoredAPIKey(); got != "original" {
		t.Errorf("StoredAPIKey = %q after restore", got)
	}

	// Restoring "no key at all" must empty the store, not write "".
	if err := RestoreStoredAPIKey(""); err != nil {
		t.Fatal(err)
	}
	if got := StoredAPIKey(); got != "" {
		t.Errorf("StoredAPIKey = %q, want nothing", got)
	}
}

func TestClearAPIKeyForgetsBothPlaces(t *testing.T) {
	_, keychain := isolate(t)
	if err := SaveAPIKey("secret", true); err != nil {
		t.Fatal(err)
	}
	if len(keychain) != 1 {
		t.Fatalf("keychain holds %d entries", len(keychain))
	}
	ClearAPIKey()
	if got := StoredAPIKey(); got != "" {
		t.Errorf("store still holds %q", got)
	}
	if len(keychain) != 0 {
		t.Errorf("keychain still holds %v", keychain)
	}
}

// --- migration ------------------------------------------------------------

// The Python build's plist must import once, and only when there is nothing of
// our own.
func TestMigrateFromPlist(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("plist migration is macOS-only")
	}
	if _, err := os.Stat("/usr/bin/plutil"); err != nil {
		t.Skip("plutil not available")
	}
	dir, _ := isolate(t)

	// QSettings writes numbers and booleans as strings on macOS, which is what
	// this fixture reproduces.
	xml := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>font_size</key><string>72</string>
  <key>max_lines</key><string>3</string>
  <key>complete_phrases_only</key><string>true</string>
  <key>fixed_box</key><string>false</string>
  <key>line_hold_seconds</key><string>1.5</string>
  <key>audio_device_name</key><string>Sound Blaster X3</string>
  <key>box_position</key><string>top</string>
  <key>vocabulary_source</key><string>영업이익률</string>
</dict>
</plist>`
	plist := filepath.Join(dir, "old.plist")
	if err := os.WriteFile(plist, []byte(xml), 0o600); err != nil {
		t.Fatal(err)
	}
	// Binary, like the real one, so the plutil path is what is exercised.
	if err := exec.Command("/usr/bin/plutil", "-convert", "binary1", plist).Run(); err != nil {
		t.Fatalf("plutil: %v", err)
	}

	raw, err := readPlistAsJSON(plist)
	if err != nil {
		t.Fatalf("readPlistAsJSON: %v", err)
	}
	got := applyPlistValues(Default(), raw).Clamped()

	if got.FontSize != 72 {
		t.Errorf("FontSize = %d", got.FontSize)
	}
	if got.MaxLines != 3 {
		t.Errorf("MaxLines = %d", got.MaxLines)
	}
	if !got.CompletePhrasesOnly {
		t.Error("CompletePhrasesOnly did not survive the string form")
	}
	if got.FixedBox {
		t.Error(`"false" was read as true`)
	}
	if got.LineHoldSeconds != 1.5 {
		t.Errorf("LineHoldSeconds = %v", got.LineHoldSeconds)
	}
	if got.AudioDeviceName != "Sound Blaster X3" {
		t.Errorf("AudioDeviceName = %q", got.AudioDeviceName)
	}
	if got.BoxPosition != string(BoxTop) {
		t.Errorf("BoxPosition = %q", got.BoxPosition)
	}
	if got.VocabularySource != "영업이익률" {
		t.Errorf("VocabularySource = %q", got.VocabularySource)
	}
	// Anything the plist did not mention keeps its default.
	if got.BoxColor != Default().BoxColor {
		t.Errorf("BoxColor = %q, want the default", got.BoxColor)
	}
}

// A settings file of our own means the migration must not run.
func TestMigrateDoesNothingWhenSettingsExist(t *testing.T) {
	isolate(t)
	if err := Save(Default()); err != nil {
		t.Fatal(err)
	}
	if MigrateFromPlist() {
		t.Error("migration ran even though this build already had settings")
	}
}

func TestPlistValueCoercion(t *testing.T) {
	cases := []struct {
		in   any
		want bool
	}{
		{true, true}, {false, false},
		{"true", true}, {"TRUE", true}, {"1", true}, {"yes", true},
		{"false", false}, {"0", false}, {"anything else", false},
		{1.0, true}, {0.0, false},
	}
	for _, c := range cases {
		if got := asBool(c.in); got != c.want {
			t.Errorf("asBool(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	if f, ok := asFloat("2.5"); !ok || f != 2.5 {
		t.Errorf("asFloat(\"2.5\") = %v, %v", f, ok)
	}
	if _, ok := asFloat("not a number"); ok {
		t.Error("asFloat accepted nonsense")
	}
}
