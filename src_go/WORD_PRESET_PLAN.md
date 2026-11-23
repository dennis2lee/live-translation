# Word presets — design

Status: shipped in 1.15.0 after two design and three implementation
red-team rounds (Fable) — see the implementation notes at the end. Target
release: 1.15.0.

## The request

Preferences→Words holds the two vocabulary lists — names and jargon the model
should expect to hear (`VocabularySource`), and how names should be spelled
in the captions (`VocabularyCaptions`). The user wants them kept as **preset
files**:

1. a preset file can be written by hand in any text editor;
2. Preferences can load one, edit it, and save it;
3. what is loaded can be applied to the running translation.

A church service, a quarterly earnings call and a medical conference each need
a different list; today switching means retyping both boxes.

**Premise.** The user called it the "word translation replace" feature in a
Preferences tab. There is no literal find-and-replace anywhere in the code or
its history; the Words lists are the only candidate, and the caption list is
how a name is made to come out a given way. The format reserves a `[replace]`
section for a real substitution list later: this build skips it with a note,
so a file written for a later build still loads here.

## The file

Plain UTF-8 text, one term per line, the two lists under two headers:

```
# LiveCap word preset — one term per line.
# Lines starting with # are comments; blank lines are ignored.
# A term that starts with # [ or \ is written with a \ in front.

[spoken]
영업이익률
평우

[captions]
Pyeongwoo Lee
EBITDA
```

- **One term per line**, exactly the rule the boxes already follow
  (`config.ParseTerms`): never split on commas, since a term can be a clause.
  `term # note` is a term, not a term and a comment.
- **A header** is a whole line, trimmed, of the form `[letters]` — ASCII
  letters only. `[spoken]` and `[captions]` are known, case-insensitive. Any
  other line starting with `[` (`[A] and [B]`, `[3]`) is a term.
- **A preset is the whole Words state**: a missing or empty section loads as
  an empty list, not as "leave that box alone".
- **Comments**: a line whose first non-space character is `#`.
- **Escape**: a leading `\` is dropped only when followed by `#`, `[` or `\`;
  `\alpha` stays `\alpha`. A bare `\` is an empty line and ignored. The writer
  escapes exactly those three, so anything the boxes can hold survives a save
  and a load unchanged.
- **Tolerated**: a UTF-8 BOM (Notepad writes one), CRLF line ends, spaces
  around a term (trimmed — `strings.TrimSpace`, as `ParseTerms` does, which
  includes U+3000).
- **The reserved header `[replace]`** — its lines are skipped and the status
  line says so. **Any other unknown header is an error** with its line
  number (round two: tolerating every unknown header let an unescaped
  `[Music]` in the captions section swallow every term after it, and a typo
  like `[caption]` empty the list, with a one-line notice as the only trace).
- **Rejected, with the line number and never the line's text** (an error
  reaches the log; a term must not): a term before any header; a repeated
  known header; invalid UTF-8 — the message says "save it as UTF-8", since
  older Notepad wrote Korean as CP949 or UTF-16; a control character other
  than tab (catches UTF-16 read as bytes, which is NUL-ridden but valid
  UTF-8); a line over 1 000 characters; a file over 64 KB. A rejected file
  changes nothing in the dialog. 64 KB, not 1 MB: the text lands in a Fyne
  `MultiLineEntry` on the UI thread that also draws the captions.
- **Duplicates and the 200-term cap stay `ParseTerms`'s job at apply time.**
  Loading and saving keep the list as written; when a loaded list has more
  than `MaxVocabularyTerms` *distinct* terms, the status line says how many
  will be ignored.
- Saving from Preferences **rewrites** the file: the header comment, then
  both sections. Comments written in an editor are not kept; the status line
  says so on every save over a file that had any.

Extension `.txt`, so a double click opens the system text editor and Notepad
saves into it without renaming.

## Where presets live

`<app-support>/Word presets/` — `~/Library/Application Support/LiveCap/Word
presets/` on macOS, `%APPDATA%\LiveCap\Word presets\` on Windows, created the
first time it is needed. `wordpreset.SetDirForTests` redirects it, as
`config.SetSettingsDirForTests` does the settings, and every UI test that
opens Preferences uses it.

The list is every regular `*.txt` file there whose name passes `ValidName`,
sorted without regard to case, shown without the extension. Sub-directories,
other extensions, hidden files, the writer's temporary files and invalid names
are not listed; invalid ones are counted on the status line ("2 files with
unusable names skipped").

A file anywhere else is brought in by putting it in that folder: the
**Folder** button creates the folder if needed and reveals it. The reveal is
started, not waited on (`cmd.Start`): `explorer` is known to exit 1 on
success, so `OpenTranscriptDir`'s `Run`-and-check pattern would report an
error while the folder opens. Not verified on Windows.

## Preferences→Words

Two rows above the two boxes, and one status line:

```
Preset [ Church service              ▾ ]  [Load]  [Save]  [Folder]
       [ new name                       ]  [Save as]
Loaded Church service — 12 spoken, 30 captions. Apply to use it now.
```

No dialogs at all (round one: a `dialog.NewForm` focuses nothing, so typing a
name in it reached the caption window's key handlers — `2` would have flipped
the direction, `0` stopped the translation, `m` muted). Everything is inline
in the tab, and typing goes to the focused entry. With nothing focused,
typing in Preferences still reaches `0 1 2 9 m f`, as it does today; this
change does not make that better or worse.

- **The Select** chooses what **Load** reads. It is re-listed when the Words
  tab is shown, on every button press, and when the application comes back
  to the foreground — the path "Folder, write a file in the editor, switch
  back". `Lifecycle().SetOnEnteredForeground` is a single slot, unused today;
  it is registered once when the window is built and dispatches to the open
  Preferences, if any. Tests call the dispatch; the registration itself
  cannot be driven from the test driver — a known gap. It sits
  in the centre of a Border so long names are not clipped to the
  placeholder's width.
- **Load** reads the selected preset into both boxes and remembers it as the
  *loaded preset*: its name, and the file's size and modification time.
  Nothing is applied — the boxes are the dialog's working copy, as on every
  other tab.
- **Save** writes both boxes to the **loaded** preset — never to whatever the
  Select happens to show (round one: choose another preset without loading
  it, press Save, and it was overwritten). The button reads `Save` and the
  status line names the target after it saves. With nothing loaded, it is
  disabled. If the file's size or modification time moved since it was
  loaded — edited in a text editor meanwhile — the first press says so and
  the button turns into `Overwrite`; a second press writes.
- **Save as** writes to the name in the entry. If a file of that name exists
  **as the filesystem sees it** — `os.Lstat` on the target, so "church" and
  "Church" on a case-insensitive volume, or an NFD name from a Mac editor
  against NFC typed here, count as the same — the first press says so and the
  button turns into `Overwrite`; a second press writes. Editing the name or
  switching tab puts the button back. After saving, the new name becomes the
  loaded preset and is selected.
- **Folder** reveals the presets folder.
- **Apply / OK** — the dialog's existing buttons — put the boxes into force.
  Writing a file is immediate and is not part of the dialog's working copy:
  Save does not mark the dialog dirty, and **Cancel does not delete or revert
  a saved file**; it reverts the settings, as always.

### The status line

One line, truncated, so its height is fixed. It shows the most important
thing: an error; else a warning (reserved section skipped, terms over the
cap, comments that a save dropped, files skipped, the file changed on disk,
"OpenAI ignores word lists") with "(+N more)" when there are several; else
the plain result ("Loaded Church service — 12 spoken, 30 captions. Apply to
use it now."). The full text of every message also goes to the log, without
terms.

### After a save

Save and Save as re-stat the written file and keep that as the loaded
preset's stamp, so a second Save does not report the file as changed on disk.
Save as then re-lists and selects the listed entry that is the same file
(`os.SameFile`), not the typed string — on a case-insensitive volume, or with
an NFD name made by a Mac editor, the two differ — and that listed spelling is
what becomes the loaded preset's name.

### `Settings.WordPreset`

JSON `word_preset`: the name of the preset the applied lists came from, so the
Select shows it when Preferences opens. It changes only through Apply or OK:
`collect()` sets it to the loaded preset's name **if both boxes still equal
what was loaded or last saved**, and to "" otherwise — lists edited after a
load are not that preset any more. It is a label: not in
`sessionNeedsRestart`. One consequence, accepted: load, edit, Apply leaves
`WordPreset` empty, so after reopening Preferences, Save is disabled until a
preset is loaded or saved as again. `showFields` gets a `WordPreset` branch so a change made
outside the dialog (Reset) updates the Select instead of being written back by
the next Apply — the bug `adoptExternal` exists for. A name from a
hand-edited `settings.json` that fails `ValidName` is ignored.

### Applying, and OpenAI

Applying different lists while translating restarts the session, because the
terms ride in the session's setup message — a brief gap, as any vocabulary
change already costs. **Except on OpenAI**, which takes no vocabulary: there a
vocabulary change must not restart anything (round one: it cost a gap for
nothing). `sessionNeedsRestart` compares the lists only when `next.Provider`
supports vocabulary (`Provider.SupportsVocabulary()`). A provider change
already forces a restart, so OpenAI→Gemini with changed lists still picks
them up. Pinned: OpenAI→OpenAI with changed lists does not restart;
OpenAI→Gemini and Gemini→Gemini do.

### Height

Measured before this change: the Words tab is 346 px against
`prefsContentHeight` 380. Two rows and a status line need about 110, so the
boxes get a fixed, smaller number of visible rows. The status line is one
line, truncated with an ellipsis, so its height does not depend on its text —
a wrapping label reports a one-line minimum and would pass the size test while
drawing three. `TestNoTabOverflowsTheDialog` is run with the longest status
message set.

## Names

`ValidName` — trimmed, 1–64 characters; none of `/ \ : * ? " < > |` or
control characters; not starting with `.`; not ending with `.` or a space;
not a Windows device name (`CON`, `PRN`, `AUX`, `NUL`, `COM1`–`9`,
`LPT1`–`9`, with or without an extension). Same rule on every platform, so a
folder copied between a Mac and a PC keeps working. **Checked inside `Load`
and `Save` themselves**, not only at Save as, so a name from `settings.json` or
anywhere else can never reach a path other than `<dir>/<name>.txt`.

## Writing

Atomic: `os.CreateTemp` in the same folder with a name `List` never shows
(`.wordpreset-*.partial` — hidden, not `.txt`), written, `Sync`, `Chmod` 0644
(CreateTemp makes 0600), `Close`, rename over the target. Any failure removes
the temporary file and leaves the old one untouched. `List` also removes
`.partial` files older than an hour, left by a crash.

On Windows a rename over a file an editor holds open can fail with a sharing
violation; that is reported ("could not save — is the file open in another
program?") and the old file is intact. Not verified on Windows.

## Package

`internal/wordpreset`, following the caption-buffer rule: no Fyne, no genai,
no network; unit-tested.

```go
type Preset struct{ Spoken, Captions []string }
type Result struct {
	Preset
	Unknown  []Section // skipped sections, by name and line
	Comments bool      // the file had comments, which a save drops
}

func Parse(data []byte) (Result, error) // *ParseError: line + reason, never text
func Format(p Preset) []byte
func ValidName(name string) error
func Dir() string
func SetDirForTests(dir string) (restore func())
func List(dir string) (names []string, skipped int, err error)
func Load(dir, name string) (Result, Stamp, error)
func Save(dir, name string, p Preset) error
func Stat(dir, name string) (Stamp, bool, error) // Lstat: exists as the FS sees it
```

`Stamp` is size plus modification time. The boxes' text becomes a `Preset`
by splitting on newlines, trimming and dropping blank lines — nothing else —
and back by joining with newlines.

## Rules this must keep

- The two lists stay separate end to end; tested both ways with golden bytes,
  since a round trip alone cannot see a symmetric swap.
- Nothing about a preset touches the key, the log or a URL. The log records a
  preset's name and term counts, never its terms; `ParseError` never carries
  line text.
- `Settings.Equal` keeps covering the new field (reflect).
- UI strings are English; any arrow is written `A→B`.

## Tests

- `Parse`, against literal golden bytes: both sections, either missing, both
  missing; header case and spaces; comments; every escape case (`\#`, `\[`,
  `\\`, `\alpha`, bare `\`); `[A] and [B]` as a term; BOM; CRLF; an unknown
  section skipped and reported; a term before a header, a repeated header,
  invalid UTF-8, NUL, a long line, an oversize file — each with its line
  number, and none with the line's text in `Error()`.
- `Format`, against golden bytes, with terms under the right headers.
- Round trip on a defined domain — terms that are trimmed, non-empty, no
  newline, no control character — including `#`, `[`, `\` starts, Korean,
  commas, U+3000 inside: a fuzz test, which also checks that a term outside
  the domain is rejected or normalised by the box splitter rather than
  silently changing on the way through.
- `ValidName`: every rejected class; ordinary names in both scripts; `Load`
  and `Save` refuse `../x` themselves.
- `List`: sub-directories, other extensions, hidden files, `.partial` files
  and invalid names are not listed; skipped count; sort without case; a
  missing folder is an empty list.
- `Save`: a real failure (the target is a directory) leaves no temporary
  file; mode is 0644; `Stat` finds "Church" when "church" exists on this
  (case-insensitive) volume — skipped where the volume is case-sensitive.
- Preferences, through the widgets and the real `AppTabs`, with the presets
  folder redirected:
  - Load fills both boxes, applies nothing; Apply then puts both lists into
    the settings and restarts a running Gemini translator, not an OpenAI one;
  - Cancel after Load→Apply restores the old lists (non-empty and different
    from the preset's), and the saved file survives Cancel;
  - Save writes exactly the boxes to the loaded preset even when the Select
    shows another; Save with the file changed on disk asks first; two
    consecutive Saves do not;
  - Save as over an existing name asks first, including a case-only
    difference;
  - a bad file leaves the boxes as they were and shows the line;
  - a file written behind the dialog's back is listed after the tab is shown
    again through `AppTabs`, and after a foreground event;
  - typing a name into the Save as entry reaches the entry, not the caption
    window's key handlers (through the canvas handlers, per the Keys rule);
  - loading a preset whose lists equal the current ones, formatted
    differently (CRLF, duplicates), restarts nothing;
  - the "N ignored" count with duplicates in a file of more than 200 lines;
  - `WordPreset`: set by Apply after Load, "" after an edit, unchanged by
    Cancel, and updated by a Reset made with the dialog open.
- Size: every tab fits with the longest status message set.

## Not in scope

Import/export dialogs, per-direction presets (switching presets when `1`/`2`
changes the direction), a literal find-and-replace list, and applying a
vocabulary change without a session restart — that last one could ride the
warm handover, and is separate work.

## Implementation round one (Fable)

Changed from the design above:

- **Stamp is a content hash** (SHA-256 of the file, at most 64 KB), not size
  and modification time: a same-size edit with an unchanged time — FAT,
  exFAT, SMB — was overwritten without asking. A preset gone since it was
  loaded also asks.
- **`Save` validates first** (`Validate`): it wrote terms over the line limit
  (counting the escape), control characters and >64 KB lists as "Saved", over
  a good preset, and `Load` then refused the file. The fuzz now asserts
  "Validate passes ⇒ the lists come back unchanged".
- **Reads go through `Lstat`, a regular-file check and a `LimitReader`**: a
  FIFO or a link to `/dev/zero` named `x.txt` hung `Load` on the UI thread.
  `Stat` became `Exists` (Lstat, existence only) and `Fingerprint`.
- **`adopt` and the label compare lists through `config.ParseTerms`**: a file
  with a duplicate was never adopted again after Apply collapsed it.
- **Status: result first, then the first warning**, and every message puts
  its action before anything a long name could push past the cut.
- **A pending Overwrite is dropped** by an edit to either box, by `adopt`,
  and after the write it confirmed.

## Implementation rounds two and three (Fable)

- **A partial preset asks before Save.** Comparing through `ParseTerms` let a
  reopened 250-term preset be adopted with 200 terms in the boxes, and one
  press of Save cut the file to 200. Adoption still compares as the session
  sees the lists; but when the file's own lists differ from the boxes, the
  preset is `partial`, reopening says so, and the first Save arms Overwrite
  with "the file's terms beyond these lists will be lost".
- Pinned: `Parse`'s control-character check (`\x1b`, mid-line `\r`, U+0085),
  and the bounded read (a sparse 4 GB file refused within a second; skipped
  on Windows, where it would not be sparse).
- Round three: SHIP. The guard could not be bypassed by a box edit, a tab
  change, Apply, the `9` key with the dialog open, or Save as onto the same
  file.
