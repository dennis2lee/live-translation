package wordpreset

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func mustParse(t *testing.T, s string) Result {
	t.Helper()
	r, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return r
}

// The two lists come out under the headers they went in under — literal
// bytes, since a round trip alone cannot see the two being swapped.
func TestParseGolden(t *testing.T) {
	r := mustParse(t, "# comment\n\n[spoken]\n영업이익률\n  평우  \n\n[captions]\nPyeongwoo Lee\nEBITDA\n")
	if want := []string{"영업이익률", "평우"}; !reflect.DeepEqual(r.Spoken, want) {
		t.Errorf("spoken = %q, want %q", r.Spoken, want)
	}
	if want := []string{"Pyeongwoo Lee", "EBITDA"}; !reflect.DeepEqual(r.Captions, want) {
		t.Errorf("captions = %q, want %q", r.Captions, want)
	}
	if !r.Comments {
		t.Error("the comment was not noticed")
	}
}

func TestFormatGolden(t *testing.T) {
	got := string(Format(Preset{Spoken: []string{"영업이익률", "#1"}, Captions: []string{"Pyeongwoo Lee", "[x]", `\y`}}))
	want := header + "\n[spoken]\n영업이익률\n\\#1\n\n[captions]\nPyeongwoo Lee\n\\[x]\n\\\\y\n"
	if got != want {
		t.Errorf("Format:\n%q\nwant\n%q", got, want)
	}
}

func TestParseSections(t *testing.T) {
	cases := map[string]Preset{
		"[captions]\nA\n":                    {Captions: []string{"A"}},
		"[spoken]\nA\n":                      {Spoken: []string{"A"}},
		"":                                   {},
		"# only a comment\n":                 {},
		"[Spoken]\nA\n[CAPTIONS]\nB":         {Spoken: []string{"A"}, Captions: []string{"B"}},
		"\xEF\xBB\xBF[spoken]\r\nA\r\nB\r\n": {Spoken: []string{"A", "B"}},
		"[spoken]\n[A] and [B]\n[3]\n":       {Spoken: []string{"[A] and [B]", "[3]"}},
		"[spoken]\nterm # note\n":            {Spoken: []string{"term # note"}},
		"[spoken]\n\\#tag\n\\[x]\n\\\\y\n\\alpha\n\\\n":    {Spoken: []string{"#tag", "[x]", `\y`, `\alpha`}},
		"[spoken]\nA\n[replace]\nfoo=bar\n[captions]\nB\n": {Spoken: []string{"A"}, Captions: []string{"B"}},
		"  [spoken]  \n\tA\t\n":                            {Spoken: []string{"A"}},
	}
	for in, want := range cases {
		r, err := Parse([]byte(in))
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if !reflect.DeepEqual(r.Preset, want) {
			t.Errorf("%q: %+v, want %+v", in, r.Preset, want)
		}
	}
}

// Spaces inside the brackets make it a term, not a header — here a term
// before any header.
func TestABracketedPhraseIsNotAHeader(t *testing.T) {
	if _, err := Parse([]byte("[ SPOKEN ]\nA\n")); err == nil {
		t.Error("[ SPOKEN ] was read as a header")
	}
}

func TestReservedSectionIsSkippedAndReported(t *testing.T) {
	r := mustParse(t, "[spoken]\nA\n[replace]\nfoo=bar\n")
	if !reflect.DeepEqual(r.Reserved, []int{3}) {
		t.Errorf("reserved = %v, want [3]", r.Reserved)
	}
	if len(r.Spoken) != 1 || len(r.Captions) != 0 {
		t.Errorf("the reserved section's lines leaked: %+v", r.Preset)
	}
}

// Every refusal carries its line and never the line's text: errors reach the
// log, and terms must not.
func TestParseErrors(t *testing.T) {
	secret := "SECRETTERM"
	cases := []struct {
		in   string
		line int
	}{
		{secret + "\n[spoken]\n", 1},
		{"[spoken]\nA\n[caption]\n" + secret, 3},
		{"[spoken]\nA\n[Music]\n" + secret, 3},
		{"[spoken]\nA\n[spoken]\n" + secret, 3},
		{"[spoken]\n" + secret + "\xff\n", 2},
		{"[spoken]\n" + secret + "\x00x\n", 2},
		{"[spoken]\n" + secret + "\x1bx\n", 2},
		{"[spoken]\n" + secret + "\rx\n", 2},
		{"[spoken]\n" + secret + "\u0085x\n", 2},
		{"[spoken]\n" + secret + strings.Repeat("x", MaxLineRunes) + "\n", 2},
	}
	for _, c := range cases {
		_, err := Parse([]byte(c.in))
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("%q: err = %v, want a ParseError", c.in, err)
			continue
		}
		if pe.Line != c.line {
			t.Errorf("%q: line %d, want %d (%v)", c.in, pe.Line, c.line, err)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "Music") {
			t.Errorf("the error carries the line's text: %v", err)
		}
	}
	if _, err := Parse([]byte("[spoken]\n" + strings.Repeat("a\n", MaxFileBytes))); err == nil {
		t.Error("an oversize file was read")
	}
	// UTF-16 read as bytes: valid UTF-8, full of NULs.
	utf16 := []byte{'[', 0, 's', 0, 'p', 0, ']', 0, '\n', 0}
	if _, err := Parse(utf16); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("UTF-16 text: %v, want a UTF-8 hint", err)
	}
}

// Whatever Validate lets through survives a save and a load unchanged —
// so Save, which refuses what Validate refuses, never writes a file Load will
// refuse (red team: it used to, over a good preset).
func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []string{"영업이익률", "#1", "[x]", `\y`, "a, b", "x\u3000y", "[spoken]", `\`, "\\#", "a\x00b", "\xff", "a\rb", "a\u0085b", "a\tb"} {
		f.Add(seed, seed+"2")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		in := Preset{Spoken: Lines(a), Captions: Lines(b)}
		if Validate(in) != nil {
			return
		}
		r, err := Parse(Format(in))
		if err != nil {
			t.Fatalf("Validate passed %q but Parse refused it: %v", in, err)
		}
		if !reflect.DeepEqual(nilEmpty(r.Preset), nilEmpty(in)) {
			t.Fatalf("round trip:\n in %q\nout %q", in, r.Preset)
		}
	})
}

// Validate refuses exactly what would not come back: a term over the line
// limit once escaped, a control character, a file over the size limit.
func TestValidateRefusesWhatLoadWouldRefuse(t *testing.T) {
	long := strings.Repeat("a", MaxLineRunes)
	cases := []struct {
		p  Preset
		ok bool
	}{
		{Preset{Spoken: []string{long}}, true},
		{Preset{Spoken: []string{long + "a"}}, false},
		{Preset{Spoken: []string{"#" + long[1:]}}, false}, // 1000, but the escape makes 1001
		{Preset{Captions: []string{"a\rb"}}, false},
		{Preset{Captions: []string{"a\u0085b"}}, false},
		{Preset{Captions: []string{"a\x1bb"}}, false},
		{Preset{Captions: []string{"a\tb"}}, true},
		{Preset{Spoken: []string{"\xff"}}, false},
	}
	for _, c := range cases {
		err := Validate(c.p)
		if (err == nil) != c.ok {
			t.Errorf("Validate(%.20q…) = %v, want ok=%v", c.p, err, c.ok)
		}
		if err == nil {
			if _, perr := Parse(Format(c.p)); perr != nil {
				t.Errorf("valid, but Parse refused it: %v", perr)
			}
		}
	}
	var big Preset
	for i := 0; i < 80; i++ {
		big.Spoken = append(big.Spoken, strings.Repeat("b", 900))
	}
	if Validate(big) == nil {
		t.Error("a preset over 64 KB passed")
	}
	if err := Validate(Preset{Captions: []string{"SECRET\x01"}}); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the error carries the term, or there is none: %v", err)
	}
}

// Save refuses what Validate refuses, and the good file it would have
// replaced is untouched.
func TestSaveRefusesAPresetLoadWouldRefuse(t *testing.T) {
	dir := t.TempDir()
	good := Preset{Spoken: []string{"good"}}
	if err := Save(dir, "Good", good); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, "Good", Preset{Spoken: []string{strings.Repeat("x", MaxLineRunes+1)}}); err == nil {
		t.Fatal("an unloadable preset was saved")
	}
	r, _, err := Load(dir, "Good")
	if err != nil || !reflect.DeepEqual(r.Preset, good) {
		t.Errorf("the good preset was disturbed: %+v %v", r.Preset, err)
	}
}

// The line limit's boundary: exactly MaxLineRunes is a line, one more is not.
func TestTheLineLimitBoundary(t *testing.T) {
	if _, err := Parse([]byte("[spoken]\n" + strings.Repeat("가", MaxLineRunes))); err != nil {
		t.Errorf("a line of exactly %d characters: %v", MaxLineRunes, err)
	}
	if _, err := Parse([]byte("[spoken]\n" + strings.Repeat("가", MaxLineRunes+1))); err == nil {
		t.Error("a line one over the limit was read")
	}
}

// Only a regular file is read: a link to /dev/zero named like a preset hung
// the UI thread.
func TestLoadReadsOnlyARegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "zero.txt")); err != nil {
		t.Skip("no symlinks here")
	}
	done := make(chan error, 1)
	go func() { _, _, err := Load(dir, "zero"); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Error("a link to /dev/zero was loaded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Load hung on a link to /dev/zero")
	}
	if _, err := Fingerprint(dir, "zero"); err == nil {
		t.Error("a link to /dev/zero was fingerprinted")
	}
}

// Exists asks the filesystem without following links: a dangling link is a
// file Save as would replace, so it exists.
func TestExistsSeesADanglingLink(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "nowhere"), filepath.Join(dir, "dangling.txt")); err != nil {
		t.Skip("no symlinks here")
	}
	if ok, err := Exists(dir, "dangling"); err != nil || !ok {
		t.Errorf("Exists(dangling) = %v %v, want true", ok, err)
	}
}

func nilEmpty(p Preset) Preset {
	if len(p.Spoken) == 0 {
		p.Spoken = nil
	}
	if len(p.Captions) == 0 {
		p.Captions = nil
	}
	return p
}

// Terms that look like the file's own syntax survive a save and a load.
func TestSyntaxLookalikesRoundTrip(t *testing.T) {
	in := Preset{
		Spoken:   []string{"[spoken]", "[captions]", "#hash", `\back`, `\#`, "[A] and [B]", "x　y"},
		Captions: []string{"[replace]", "# not a comment", `\`, "a, b, c"},
	}
	r, err := Parse(Format(in))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Preset, in) {
		t.Errorf("round trip:\n in %q\nout %q", in, r.Preset)
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"Church service", "교회 예배", "Q3 earnings", "a", "x.y", strings.Repeat("가", MaxNameRunes)} {
		if err := ValidName(ok); err != nil {
			t.Errorf("%q: %v", ok, err)
		}
	}
	for _, bad := range []string{"", " lead", "trail ", ".hidden", "end.", "a/b", `a\b`, "a:b", "a*b", "a?b",
		`a"b`, "a<b", "a>b", "a|b", "a\x01b", "../x", "CON", "con", "nul.txt", "COM1", "lpt9", "Aux.x",
		strings.Repeat("a", MaxNameRunes+1), "\xff"} {
		if ValidName(bad) == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
	for _, fine := range []string{"COM", "COM0", "LPT10", "CONSOLE"} {
		if err := ValidName(fine); err != nil {
			t.Errorf("%q: %v", fine, err)
		}
	}
}

// Load and Save check the name themselves, so nothing reaches a path other
// than <dir>/<name>.txt — whatever the caller.
func TestLoadAndSaveRefuseBadNames(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(filepath.Dir(dir), "x.txt")
	if err := Save(dir, "../x", Preset{}); err == nil {
		t.Error("Save accepted ../x")
	}
	if _, err := os.Stat(outside); err == nil {
		os.Remove(outside)
		t.Error("Save wrote outside the folder")
	}
	if _, _, err := Load(dir, "../x"); err == nil {
		t.Error("Load accepted ../x")
	}
	if _, err := Exists(dir, "a/b"); err == nil {
		t.Error("Exists accepted a/b")
	}
}

func TestListShowsOnlyPresets(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("[spoken]\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("beta.txt")
	write("Alpha.TXT")
	write("gamma.md")
	write(".hidden.txt")
	write("CON.txt")
	write(" spaced.txt")
	write(".wordpreset-123.partial")
	if err := os.Mkdir(filepath.Join(dir, "sub.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	names, skipped, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"Alpha", "beta"}; !reflect.DeepEqual(names, want) {
		t.Errorf("names = %q, want %q", names, want)
	}
	if skipped != 2 {
		t.Errorf("skipped = %d, want 2 (CON, a leading space)", skipped)
	}
	if names, _, err := List(filepath.Join(dir, "missing")); err != nil || names != nil {
		t.Errorf("a missing folder: %v %v", names, err)
	}
}

// A crash's leftover temporary file goes once it is old; a fresh one — a save
// in progress — stays.
func TestListSweepsStalePartials(t *testing.T) {
	dir := t.TempDir()
	stale := filepath.Join(dir, ".wordpreset-1.partial")
	fresh := filepath.Join(dir, ".wordpreset-2.partial")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * staleAfter)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	if _, _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("the stale temporary file was kept")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a fresh temporary file was removed")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "made on demand")
	p := Preset{Spoken: []string{"영업이익률"}, Captions: []string{"Pyeongwoo Lee"}}
	if err := Save(dir, "교회", p); err != nil {
		t.Fatal(err)
	}
	r, stamp, err := Load(dir, "교회")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.Preset, p) {
		t.Errorf("loaded %+v", r.Preset)
	}
	info, err := os.Stat(filepath.Join(dir, "교회.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("mode %v, want 0644", info.Mode().Perm())
	}
	if fp, err := Fingerprint(dir, "교회"); err != nil || fp != stamp {
		t.Errorf("the stamp does not match the file: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left behind: %v", entries)
	}
}

// A save that fails — here the target is a directory — leaves no temporary
// file and does not disturb what is there.
func TestAFailedSaveLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "taken.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "taken.txt", "inside"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, "taken", Preset{Spoken: []string{"a"}}); err == nil {
		t.Fatal("saving over a directory succeeded")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".partial") {
			t.Errorf("temporary file left: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "taken.txt", "inside")); err != nil {
		t.Error("the existing directory was disturbed")
	}
}

// On a case-insensitive volume, "church" exists when "Church" does, and the
// listed spelling is found for it.
func TestExistsAndSameFileSeeTheFilesystem(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, "Church", Preset{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "church.txt")); err != nil {
		t.Skip("this volume is case-sensitive")
	}
	if ok, err := Exists(dir, "church"); err != nil || !ok {
		t.Errorf("Exists(church) = %v %v, want found", ok, err)
	}
	names, _, _ := List(dir)
	if got := SameFile(dir, "church", names); got != "Church" {
		t.Errorf("SameFile = %q, want the listed spelling Church", got)
	}
	if ok, _ := Exists(dir, "Synagogue"); ok {
		t.Error("a missing preset was found")
	}
}

func TestLinesAndJoin(t *testing.T) {
	if got := Lines(" a \r\n\n b\n  \n"); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("Lines = %q", got)
	}
	if got := Lines("dup\ndup"); len(got) != 2 {
		t.Errorf("duplicates were collapsed: %q — that is ParseTerms' job at apply time", got)
	}
	if Join([]string{"a", "b"}) != "a\nb" {
		t.Error("Join")
	}
}

func TestSetDirForTests(t *testing.T) {
	restore := SetDirForTests("/tmp/x")
	if Dir() != "/tmp/x" {
		t.Errorf("Dir = %q", Dir())
	}
	restore()
	if !strings.HasSuffix(Dir(), FolderName) {
		t.Errorf("Dir after restore = %q", Dir())
	}
}

// The read is bounded: a huge regular file — sparse, so it costs no disk — is
// refused after reading at most the limit, not after reading all of it.
func TestTheReadIsBounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("NTFS files are not sparse by default; this would write 4 GB")
	}
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "huge.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(4 << 30); err != nil {
		f.Close()
		t.Skip("no sparse files here:", err)
	}
	f.Close()
	start := time.Now()
	if _, _, err := Load(dir, "huge"); err == nil {
		t.Fatal("a 4 GB file was loaded")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("refusing a 4 GB file took %v — it was read, not bounded", d)
	}
}
