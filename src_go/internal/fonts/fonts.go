// Package fonts lists the installed font families and loads one for the
// captions.
//
// Fyne draws a canvas.Text in any font handed to it as FontSource, but it reads
// that font with go-text's ParseTTF, which takes a single font file and nothing
// else. Much of what a Mac ships is collections — Apple SD Gothic Neo,
// Helvetica and Avenir are .ttc — so Load extracts the one face it wants into a
// standalone file first. A glyph the chosen font does not have, such as Hangul
// in an English-only font, is still drawn: Fyne falls back to an installed font
// that has it.
//
// The listing uses go-text's index of the system fonts, the same one Fyne
// itself builds for that fallback and cached in the same place, so it costs
// tens of milliseconds rather than a scan of every font file.
//
// No UI and no Fyne here: the package hands back bytes and names, and the
// caller wraps them.
package fonts

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/go-text/typesetting/font"
	ot "github.com/go-text/typesetting/font/opentype"
	"github.com/go-text/typesetting/fontscan"
)

// family is one installed family, by the face Load would use for it.
type family struct {
	name string      // as the font names itself, for the menu
	loc  font.FontID // its most regular face
}

var (
	scanOnce sync.Once
	byKey    map[string]family // keyed by font.NormalizeFamily(name)
	names    []string
	scanErr  error
)

// scanFor is the scan, a variable so the tests can run on fixtures.
var scanFor = func() ([]fontscan.Footprint, error) { return fontscan.SystemFonts(quiet{}, "") }

type quiet struct{}

func (quiet) Printf(string, ...any) {}

// Families is every installed family that can show English or Korean, by the
// name the font gives itself, sorted. Empty if the system fonts could not be
// read. Scanned once per process.
func Families() []string {
	scan()
	return append([]string(nil), names...)
}

// Has reports whether a family is installed.
func Has(name string) bool {
	_, ok := Canonical(name)
	return ok
}

// Canonical is the installed family's own name for name, which may differ in
// case or spacing — "helveticaneue" is "Helvetica Neue". Fonts match names
// that way; a menu comparing them as strings showed a font that was in use as
// the default, and the next Apply dropped it (the red team).
func Canonical(name string) (string, bool) {
	scan()
	f, ok := byKey[font.NormalizeFamily(name)]
	return f.name, ok
}

// SameFamily reports whether two names are the same family by the rule fonts
// use: case and spaces do not count.
func SameFamily(a, b string) bool { return font.NormalizeFamily(a) == font.NormalizeFamily(b) }

// Load returns the family's most regular face as a standalone font file.
func Load(name string) ([]byte, error) {
	scan()
	if scanErr != nil {
		return nil, scanErr
	}
	f, ok := byKey[font.NormalizeFamily(name)]
	if !ok {
		return nil, fmt.Errorf("font %q is not installed", name)
	}
	data, err := readFile(f.loc.File)
	if err != nil {
		return nil, err
	}
	out, err := extract(data, int(f.loc.Index))
	if err != nil {
		return nil, fmt.Errorf("font %q: %w", name, err)
	}
	// The check Fyne will make, made here so a font it cannot read is an
	// error now rather than a silent fallback to the default when drawn.
	if _, err := font.ParseTTF(bytes.NewReader(out)); err != nil {
		return nil, fmt.Errorf("font %q: %w", name, err)
	}
	return out, nil
}

// extract returns face index of a font file — a single font, a collection or
// a Mac dfont — as a single TrueType/OpenType file. A single font comes back
// as it is.
func extract(data []byte, index int) ([]byte, error) {
	lds, err := ot.NewLoaders(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if index < 0 || index >= len(lds) {
		return nil, fmt.Errorf("face %d of %d", index, len(lds))
	}
	if len(lds) == 1 && !bytes.HasPrefix(data, []byte("ttcf")) && looksLikeSfnt(data) {
		return data, nil
	}
	ld := lds[index]
	var tables []ot.Table
	for _, tag := range ld.Tables() {
		raw, err := ld.RawTable(tag)
		if err != nil {
			return nil, err
		}
		tables = append(tables, ot.Table{Tag: tag, Content: raw})
	}
	if len(tables) == 0 {
		return nil, errors.New("no tables")
	}
	return ot.WriteTTF(tables), nil
}

// readFile is os.ReadFile, named so the tests read fonts the same way.
var readFile = os.ReadFile

// looksLikeSfnt reports a plain TrueType or OpenType file.
func looksLikeSfnt(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	switch string(data[:4]) {
	case "\x00\x01\x00\x00", "OTTO", "true":
		return true
	}
	return false
}

// fileTypes are the font files offered. Anything else the index holds —
// bitmap and Type 1 fonts on some systems — Fyne cannot draw.
var fileTypes = map[string]bool{".ttf": true, ".otf": true, ".ttc": true, ".otc": true, ".dfont": true}

func scan() {
	scanOnce.Do(func() {
		byKey = map[string]family{}
		fps, err := scanFor()
		if err != nil {
			scanErr = err
			return
		}
		// One face per family: the closest to upright, regular weight.
		best := map[string]fontscan.Footprint{}
		for _, fp := range fps {
			if !fileTypes[strings.ToLower(filepath.Ext(fp.Location.File))] {
				continue
			}
			// Captions are English or Korean. A font with neither — a symbol
			// or a CJK-only font — is not a caption font.
			if !fp.Runes.Contains('A') && !fp.Runes.Contains('가') {
				continue
			}
			if cur, ok := best[fp.Family]; !ok || regularity(fp) < regularity(cur) {
				best[fp.Family] = fp
			}
		}
		for key, fp := range best {
			name := displayName(fp)
			// macOS hides its interface fonts behind a leading dot; they
			// are not offered in any font menu.
			if name == "" || strings.HasPrefix(name, ".") {
				continue
			}
			byKey[key] = family{name: name, loc: fp.Location}
			names = append(names, name)
		}
		sort.Slice(names, func(i, j int) bool { return strings.ToLower(names[i]) < strings.ToLower(names[j]) })
	})
}

// regularity is how far a face is from upright regular; lower is closer.
// Width counts as well as weight: Papyrus's first face is its Condensed one, at
// a regular weight, and weight alone picked it (the red team). A quarter off
// normal width weighs more than one step of weight. A named instance of a
// variable font comes after a static face, since Fyne draws a variable font at
// its default instance whichever one is named.
func regularity(fp fontscan.Footprint) int {
	d := int(fp.Aspect.Weight) - int(font.WeightNormal)
	if d < 0 {
		d = -d
	}
	if w := float64(fp.Aspect.Stretch - font.StretchNormal); w != 0 {
		d += int(math.Abs(w) * 1000)
	}
	if fp.Aspect.Style != font.StyleNormal {
		d += 1000
	}
	if fp.Location.Instance != 0 {
		d += 1
	}
	return d
}

// displayName reads the family name from the font itself. The index keeps
// only a normalised form — lower case, no spaces — which no menu should show.
func displayName(fp fontscan.Footprint) string {
	f, err := os.Open(fp.Location.File)
	if err != nil {
		return ""
	}
	defer f.Close()
	lds, err := ot.NewLoaders(f)
	if err != nil || int(fp.Location.Index) >= len(lds) {
		return ""
	}
	d, _ := font.Describe(lds[fp.Location.Index], nil)
	return strings.TrimSpace(d.Family)
}
