// Package wordpreset reads and writes the Words tab's two vocabulary lists as
// plain-text preset files that can also be written by hand. See
// WORD_PRESET_PLAN.md.
//
// It follows the caption-buffer rule: no Fyne, no genai, no network.
package wordpreset

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/dennis2lee/live-translation/src_go/internal/platform"
)

// Preset is the whole Words state: what the model should expect to hear, and
// how names should come out in the captions. The two never mix.
type Preset struct {
	Spoken   []string
	Captions []string
}

// Result is a parsed file: the preset, and what a reader should be told about
// it.
type Result struct {
	Preset
	// Reserved is the line of each reserved section that was skipped.
	Reserved []int
	// Comments is whether the file had comments, which a save rewrites away.
	Comments bool
}

const (
	// MaxFileBytes bounds a preset. The text lands in a Fyne MultiLineEntry
	// on the UI thread that also draws the captions.
	MaxFileBytes = 64 << 10
	// MaxLineRunes bounds one line.
	MaxLineRunes = 1000
	// Ext is the file extension, so a double click opens a text editor.
	Ext = ".txt"

	sectionSpoken   = "spoken"
	sectionCaptions = "captions"
	// sectionReplace is reserved for a substitution list a later build may
	// add: skipped here with a note, so such a file still loads.
	sectionReplace = "replace"
)

// header is written at the top of every saved file.
const header = `# LiveCap word preset — one term per line.
# Lines starting with # are comments; blank lines are ignored.
# A term that starts with # [ or \ is written with a \ in front.
# Saving from Preferences rewrites this file; comments are not kept.
`

// ParseError is a file that cannot be loaded. It carries the line and the
// reason, never the line's text: errors reach the log, and terms must not.
type ParseError struct {
	Line   int // 1-based; 0 for the file as a whole
	Reason string
}

func (e *ParseError) Error() string {
	if e.Line == 0 {
		return e.Reason
	}
	return fmt.Sprintf("line %d: %s", e.Line, e.Reason)
}

// Parse reads a preset file. A file that cannot be read whole is refused
// whole: nothing partial comes back.
func Parse(data []byte) (Result, error) {
	var out Result
	if len(data) > MaxFileBytes {
		return Result{}, &ParseError{Reason: fmt.Sprintf("the file is over %d KB", MaxFileBytes>>10)}
	}
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	seen := map[string]bool{}
	var section string
	for i, raw := range strings.Split(string(data), "\n") {
		n := i + 1
		line := strings.TrimSuffix(raw, "\r")
		if !utf8.ValidString(line) {
			return Result{}, &ParseError{n, "not UTF-8 text — save the file as UTF-8"}
		}
		if utf8.RuneCountInString(line) > MaxLineRunes {
			return Result{}, &ParseError{n, fmt.Sprintf("longer than %d characters", MaxLineRunes)}
		}
		for _, r := range line {
			if unicode.IsControl(r) && r != '\t' {
				return Result{}, &ParseError{n, "contains a control character — save the file as UTF-8"}
			}
		}
		text := strings.TrimSpace(line)
		switch {
		case text == "":
			continue
		case strings.HasPrefix(text, "#"):
			out.Comments = true
			continue
		}
		if name, ok := headerName(text); ok {
			name = strings.ToLower(name)
			switch name {
			case sectionSpoken, sectionCaptions, sectionReplace:
			default:
				return Result{}, &ParseError{n, "unknown section — only [spoken] and [captions]"}
			}
			if seen[name] {
				return Result{}, &ParseError{n, "the section appears twice"}
			}
			seen[name] = true
			section = name
			if name == sectionReplace {
				out.Reserved = append(out.Reserved, n)
			}
			continue
		}
		term := unescape(text)
		if term == "" {
			continue
		}
		switch section {
		case "":
			return Result{}, &ParseError{n, "a term before [spoken] or [captions]"}
		case sectionSpoken:
			out.Spoken = append(out.Spoken, term)
		case sectionCaptions:
			out.Captions = append(out.Captions, term)
		}
	}
	return out, nil
}

// headerName recognises a whole line of the form [letters].
func headerName(text string) (string, bool) {
	if len(text) < 3 || text[0] != '[' || text[len(text)-1] != ']' {
		return "", false
	}
	name := text[1 : len(text)-1]
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z') {
			return "", false
		}
	}
	return name, true
}

// unescape drops a leading backslash before # [ or \ — and nothing else, so
// "\alpha" stays as typed.
func unescape(text string) string {
	if len(text) >= 2 && text[0] == '\\' && strings.ContainsRune(`#[\`, rune(text[1])) {
		return text[1:]
	}
	if text == `\` {
		return ""
	}
	return text
}

// escape is unescape's inverse for a term.
func escape(term string) string {
	if term != "" && strings.ContainsRune(`#[\`, rune(term[0])) {
		return `\` + term
	}
	return term
}

// Validate reports why a preset could not be loaded back once saved — a term
// over the line limit (counting the escape a leading # [ or \ costs), a
// control character, text that is not UTF-8, or a file over the size limit.
// Save refuses such a preset rather than writing a file Load will refuse,
// over a good one. The error names the list and the term's position, never
// its text.
func Validate(p Preset) error {
	for _, list := range []struct {
		what  string
		terms []string
	}{{"spoken", p.Spoken}, {"captions", p.Captions}} {
		for i, term := range list.terms {
			if err := termProblem(term); err != "" {
				return &ParseError{Reason: fmt.Sprintf("%s term %d %s", list.what, i+1, err)}
			}
		}
	}
	if n := len(Format(p)); n > MaxFileBytes {
		return &ParseError{Reason: fmt.Sprintf("the lists come to %d KB; a preset holds at most %d KB", (n+1023)>>10, MaxFileBytes>>10)}
	}
	return nil
}

// termProblem is what is wrong with a term as a line of a file, or "".
func termProblem(term string) string {
	switch {
	case !utf8.ValidString(term):
		return "is not valid text"
	case utf8.RuneCountInString(escape(term)) > MaxLineRunes:
		return fmt.Sprintf("is longer than %d characters", MaxLineRunes)
	case strings.ContainsAny(term, "\n"):
		return "contains a line break"
	}
	for _, r := range term {
		if unicode.IsControl(r) && r != '\t' {
			return "contains a control character"
		}
	}
	return ""
}

// Format writes a preset: the header comment, then both sections.
func Format(p Preset) []byte {
	var b strings.Builder
	b.WriteString(header)
	write := func(name string, terms []string) {
		b.WriteString("\n[" + name + "]\n")
		for _, t := range terms {
			b.WriteString(escape(t))
			b.WriteByte('\n')
		}
	}
	write(sectionSpoken, p.Spoken)
	write(sectionCaptions, p.Captions)
	return []byte(b.String())
}

// Lines splits a box's text into terms the way a preset holds them: one per
// line, trimmed, blank lines dropped. Duplicates and the cap are left to
// config.ParseTerms at apply time, so loading and saving never change a list.
func Lines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if t := strings.TrimSpace(strings.TrimSuffix(line, "\r")); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// Join is Lines' inverse, for putting a list back in a box.
func Join(terms []string) string { return strings.Join(terms, "\n") }

// --- names ----------------------------------------------------------------

// MaxNameRunes bounds a preset's name.
const MaxNameRunes = 64

var reservedDevice = map[string]bool{"CON": true, "PRN": true, "AUX": true, "NUL": true}

// ValidName reports whether name can be a preset's name. The rule is the same
// on every platform, so a presets folder copied between a Mac and a PC keeps
// working, and nothing but <dir>/<name>.txt can ever come of it.
func ValidName(name string) error {
	switch {
	case name != strings.TrimSpace(name):
		return errors.New("the name starts or ends with a space")
	case name == "":
		return errors.New("the name is empty")
	case utf8.RuneCountInString(name) > MaxNameRunes:
		return fmt.Errorf("the name is longer than %d characters", MaxNameRunes)
	case !utf8.ValidString(name):
		return errors.New("the name is not valid text")
	case strings.HasPrefix(name, "."):
		return errors.New("the name starts with a dot")
	case strings.HasSuffix(name, "."):
		return errors.New("the name ends with a dot")
	}
	for _, r := range name {
		if unicode.IsControl(r) || strings.ContainsRune(`/\:*?"<>|`, r) {
			return errors.New(`the name contains one of / \ : * ? " < > |`)
		}
	}
	stem := strings.ToUpper(name)
	if i := strings.IndexByte(stem, '.'); i >= 0 {
		stem = stem[:i]
	}
	stem = strings.TrimSpace(stem)
	if reservedDevice[stem] || len(stem) == 4 && (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) &&
		stem[3] >= '1' && stem[3] <= '9' {
		return errors.New("the name is reserved by Windows")
	}
	return nil
}

// --- the folder -----------------------------------------------------------

var (
	dirMu       sync.Mutex
	dirOverride string
)

// FolderName is the presets folder's name inside the app-support directory.
const FolderName = "Word presets"

// Dir is where presets live.
func Dir() string {
	dirMu.Lock()
	defer dirMu.Unlock()
	if dirOverride != "" {
		return dirOverride
	}
	return filepath.Join(platform.AppSupportDir(), FolderName)
}

// SetDirForTests redirects Dir and returns the restore.
func SetDirForTests(dir string) func() {
	dirMu.Lock()
	old := dirOverride
	dirOverride = dir
	dirMu.Unlock()
	return func() {
		dirMu.Lock()
		dirOverride = old
		dirMu.Unlock()
	}
}

const partialPattern = ".wordpreset-*.partial"

// staleAfter is how old a temporary file has to be before List treats it as a
// crash's leftover.
const staleAfter = time.Hour

// List is every preset in dir, sorted without regard to case, and how many
// .txt files were skipped for an unusable name. A missing folder is an empty
// list.
func List(dir string) (names []string, skipped int, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".wordpreset-") && strings.HasSuffix(name, ".partial") {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > staleAfter {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		if !e.Type().IsRegular() || strings.HasPrefix(name, ".") ||
			!strings.EqualFold(filepath.Ext(name), Ext) {
			continue
		}
		stem := name[:len(name)-len(Ext)]
		if ValidName(stem) != nil {
			skipped++
			continue
		}
		names = append(names, stem)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := strings.ToLower(names[i]), strings.ToLower(names[j])
		if a != b {
			return a < b
		}
		return names[i] < names[j]
	})
	return names, skipped, nil
}

// path is the one place a name becomes a path, and only a valid one does.
func path(dir, name string) (string, error) {
	if err := ValidName(name); err != nil {
		return "", err
	}
	return filepath.Join(dir, name+Ext), nil
}

// Stamp is what tells whether a file changed behind our back: a hash of its
// content. Not its size and modification time — a same-size edit on a volume
// with coarse times (FAT, exFAT, SMB) changes neither, and was overwritten
// unasked (red team). A preset is at most 64 KB, so hashing it is free.
type Stamp [sha256.Size]byte

// Exists reports whether a preset of that name exists as the filesystem sees
// it — os.Lstat, so on a case-insensitive volume "church" finds "Church", and
// a dangling link counts.
func Exists(dir, name string) (bool, error) {
	p, err := path(dir, name)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// read returns a preset file's bytes. Only a regular file, read through a
// limit: a FIFO or a link to /dev/zero named like a preset hung the UI
// thread (red team).
func read(dir, name string) ([]byte, error) {
	p, err := path(dir, name)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(p)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, &ParseError{Reason: "not a regular file"}
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxFileBytes {
		return nil, &ParseError{Reason: fmt.Sprintf("the file is over %d KB", MaxFileBytes>>10)}
	}
	return data, nil
}

// Fingerprint is the stamp of a preset as it is on disk now.
func Fingerprint(dir, name string) (Stamp, error) {
	data, err := read(dir, name)
	if err != nil {
		return Stamp{}, err
	}
	return sha256.Sum256(data), nil
}

// Load reads a preset and its stamp.
func Load(dir, name string) (Result, Stamp, error) {
	data, err := read(dir, name)
	if err != nil {
		return Result{}, Stamp{}, err
	}
	r, err := Parse(data)
	if err != nil {
		return Result{}, Stamp{}, err
	}
	return r, sha256.Sum256(data), nil
}

// Save writes a preset atomically: a temporary file in the same folder,
// synced, renamed over the target. Any failure leaves the old file as it was
// and no temporary file behind.
func Save(dir, name string, preset Preset) error {
	p, err := path(dir, name)
	if err != nil {
		return err
	}
	if err := Validate(preset); err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, partialPattern)
	if err != nil {
		return err
	}
	done := false
	defer func() {
		if !done {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err := tmp.Write(Format(preset)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return err
	}
	done = true
	return nil
}

// SameFile finds, among names, the one that is the same file as name — the
// listed spelling of a preset saved under a name that differs from it only in
// case or Unicode normalisation. "" if none is.
func SameFile(dir, name string, names []string) string {
	p, err := path(dir, name)
	if err != nil {
		return ""
	}
	want, err := os.Stat(p)
	if err != nil {
		return ""
	}
	for _, n := range names {
		if q, err := path(dir, n); err == nil {
			if info, err := os.Stat(q); err == nil && os.SameFile(want, info) {
				return n
			}
		}
	}
	return ""
}
