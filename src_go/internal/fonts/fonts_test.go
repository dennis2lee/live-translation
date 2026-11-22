package fonts

import (
	"bytes"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"fyne.io/fyne/v2/theme"
	"github.com/go-text/typesetting/font"
	"github.com/go-text/typesetting/fontscan"
)

// installed is the scan, or a skip on a machine with no readable fonts.
func installed(t *testing.T) []string {
	t.Helper()
	got := Families()
	if len(got) == 0 {
		t.Skip("no system fonts could be read here")
	}
	return got
}

// What the menu shows: real names, sorted, each once, and none of the
// hidden interface fonts macOS keeps behind a leading dot.
func TestFamiliesAreNamedSortedAndVisible(t *testing.T) {
	got := installed(t)
	seen := map[string]bool{}
	for _, n := range got {
		if n == "" || strings.HasPrefix(n, ".") {
			t.Errorf("listed %q", n)
		}
		if seen[strings.ToLower(n)] {
			t.Errorf("%q listed twice", n)
		}
		seen[strings.ToLower(n)] = true
		if !Has(n) {
			t.Errorf("Has(%q) = false for a listed family", n)
		}
	}
	if !sort.SliceIsSorted(got, func(i, j int) bool { return strings.ToLower(got[i]) < strings.ToLower(got[j]) }) {
		t.Error("families are not sorted")
	}
}

// Families hands out a copy: a caller appending to it must not change the
// next caller's list.
func TestFamiliesIsACopy(t *testing.T) {
	got := installed(t)
	got[0] = "changed"
	if Families()[0] == "changed" {
		t.Error("Families returned its own slice")
	}
}

// Every face Load returns is one Fyne can read — ParseTTF, which is all
// Fyne calls — including faces taken out of a collection.
func TestListedFamiliesLoadAsSingleFonts(t *testing.T) {
	got := installed(t)
	sample := []string{got[0], got[len(got)-1]}
	collection := ""
	for _, n := range got {
		if ext := strings.ToLower(filepath.Ext(byKey[font.NormalizeFamily(n)].loc.File)); ext == ".ttc" || ext == ".otc" {
			collection = n
			break
		}
	}
	if collection != "" {
		sample = append(sample, collection)
	}
	for _, n := range sample {
		data, err := Load(n)
		if err != nil {
			t.Errorf("Load(%q): %v", n, err)
			continue
		}
		if bytes.HasPrefix(data, []byte("ttcf")) {
			t.Errorf("Load(%q) returned a collection", n)
		}
		if _, err := font.ParseTTF(bytes.NewReader(data)); err != nil {
			t.Errorf("Load(%q) is not a font Fyne can read: %v", n, err)
		}
	}
	if collection == "" {
		t.Log("no collection font installed; extraction from a .ttc not exercised")
	}
}

// A face taken out of a collection is the same face: same glyph coverage
// as go-text reads from the collection itself.
func TestACollectionFaceKeepsItsGlyphs(t *testing.T) {
	installed(t)
	// In name order, not map order, so every run checks the same face.
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f := byKey[k]
		if ext := strings.ToLower(filepath.Ext(f.loc.File)); ext != ".ttc" {
			continue
		}
		data, err := Load(f.name)
		if err != nil {
			t.Fatalf("Load(%q): %v", f.name, err)
		}
		single, err := font.ParseTTF(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range []rune{'A', 'z', '0', '가', '한'} {
			_, want := single.NominalGlyph(r)
			_, inCollection := faceInCollection(t, f).NominalGlyph(r)
			if want != inCollection {
				t.Errorf("%q: glyph for %q is %v extracted, %v in the collection", f.name, r, want, inCollection)
			}
		}
		return
	}
	t.Skip("no collection font installed")
}

func faceInCollection(t *testing.T, f family) *font.Face {
	t.Helper()
	data, err := readFile(f.loc.File)
	if err != nil {
		t.Fatal(err)
	}
	faces, err := font.ParseTTC(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return faces[f.loc.Index]
}

// A plain font file is handed back as it is, not rewritten.
func TestASingleFontIsLeftAlone(t *testing.T) {
	data := theme.DefaultTextFont().Content()
	out, err := extract(data, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out, data) {
		t.Error("a single font file was rewritten")
	}
	if _, err := extract(data, 1); err == nil {
		t.Error("face 1 of a single font was accepted")
	}
}

func TestAnUnknownFamilyIsAnError(t *testing.T) {
	installed(t)
	if _, err := Load("No Such Font Family"); err == nil {
		t.Error("Load of an uninstalled family succeeded")
	}
	if Has("No Such Font Family") {
		t.Error("Has reported an uninstalled family")
	}
}

// Names are matched the way fonts match them: case and spaces do not count.
func TestFamiliesMatchLooselyByName(t *testing.T) {
	got := installed(t)
	name := got[0]
	if !Has(strings.ToUpper(name)) || !Has(strings.ReplaceAll(name, " ", "")) {
		t.Errorf("%q not found by a differently cased or spaced name", name)
	}
}

// The most regular face of a family is upright, regular weight and normal
// width. Width counts: Papyrus's first face is Condensed at a regular weight,
// and weight alone chose it (the red team).
func TestTheRegularFaceIsChosen(t *testing.T) {
	face := func(w font.Weight, st font.Style, sw font.Stretch) fontscan.Footprint {
		return fontscan.Footprint{Aspect: font.Aspect{Weight: w, Style: st, Stretch: sw}}
	}
	regular := face(font.WeightNormal, font.StyleNormal, font.StretchNormal)
	for name, other := range map[string]fontscan.Footprint{
		"condensed": face(font.WeightNormal, font.StyleNormal, font.StretchCondensed),
		"bold":      face(font.WeightBold, font.StyleNormal, font.StretchNormal),
		"italic":    face(font.WeightNormal, font.StyleItalic, font.StretchNormal),
	} {
		if regularity(other) <= regularity(regular) {
			t.Errorf("%s ranks with or ahead of regular: %d vs %d", name, regularity(other), regularity(regular))
		}
	}
	// A condensed face is further off than one weight step.
	if regularity(face(font.WeightNormal, font.StyleNormal, font.StretchCondensed)) <=
		regularity(face(font.WeightMedium, font.StyleNormal, font.StretchNormal)) {
		t.Error("a condensed face ranks ahead of a medium-weight one")
	}
}

func TestCanonicalIsTheFontsOwnName(t *testing.T) {
	for _, n := range installed(t) {
		if !strings.Contains(n, " ") {
			continue
		}
		got, ok := Canonical(strings.ToLower(strings.ReplaceAll(n, " ", "")))
		if !ok || got != n {
			t.Errorf("Canonical of the squashed %q = %q, %v", n, got, ok)
		}
		if !SameFamily(n, strings.ToUpper(n)) {
			t.Errorf("SameFamily(%q, upper case) = false", n)
		}
		return
	}
	t.Skip("no installed family has a space in its name")
}
