package ui

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/wordpreset"
)

// revealFolder opens a folder in the system file manager. Started, not waited
// on: explorer is known to exit 1 on success, so waiting and checking — the
// transcript folder's pattern — would report an error while the folder opens.
// A variable for the tests.
var revealFolder = func(path string) error {
	cmd := platform.RevealCommand(path)
	c := exec.Command(cmd[0], cmd[1:]...)
	if err := c.Start(); err != nil {
		return err
	}
	go func() { _ = c.Wait() }()
	return nil
}

// wordPresets is the preset row of Preferences→Words: choose, load, save,
// save as, and reveal the folder. See WORD_PRESET_PLAN.md.
//
// Everything is inline in the tab. A form dialog focuses nothing, so a name
// typed into one reached the caption window's keys — 2 flipped the
// direction, 0 stopped the translation.
type wordPresets struct {
	p *prefsDialog

	sel    *widget.Select
	load   *widget.Button
	save   *widget.Button
	folder *widget.Button
	name   *widget.Entry
	saveAs *widget.Button
	status *widget.Label

	// The loaded preset: what Save writes to, never whatever the Select
	// happens to show. stamp is the file as it was loaded or last saved, so
	// a change made in a text editor meanwhile is noticed; spoken and
	// captions are the boxes' terms at that moment, so collect can tell
	// whether the lists are still that preset.
	loaded   string
	stamp    wordpreset.Stamp
	spoken   []string
	captions []string
	comments bool
	// partial is a preset adopted whose file holds more than the boxes —
	// terms past the 200 cap, or duplicates, which applying dropped. Saving
	// over it would lose them, so the first Save asks (red team, round two:
	// one press cut a 250-term file to 200).
	partial bool

	// A second press confirms an overwrite.
	saveArmed   bool
	saveAsArmed bool
}

func newWordPresets(p *prefsDialog) *wordPresets {
	wp := &wordPresets{p: p}
	wp.sel = widget.NewSelect(nil, nil)
	wp.sel.PlaceHolder = "Choose a preset"
	wp.load = widget.NewButton("Load", wp.doLoad)
	wp.save = widget.NewButton("Save", wp.doSave)
	wp.folder = widget.NewButton("Folder", wp.doFolder)
	wp.name = widget.NewEntry()
	wp.name.SetPlaceHolder("New preset name")
	wp.name.OnChanged = func(string) { wp.disarm() }
	wp.saveAs = widget.NewButton("Save as", wp.doSaveAs)
	wp.status = widget.NewLabel("")
	// An edit to either box cancels a pending Overwrite: what was confirmed
	// is no longer what would be written.
	for _, box := range []*widget.Entry{p.vocabSource, p.vocabCaption} {
		box.OnChanged = func(string) { wp.disarm() }
	}
	// One line, cut with an ellipsis: a wrapping label reports a one-line
	// minimum and would draw three over the boxes below it.
	wp.status.Truncation = fyne.TextTruncateEllipsis
	wp.relist()
	wp.adopt(p.working.WordPreset)
	wp.syncButtons()
	return wp
}

// rows is what the tab shows above the boxes.
func (wp *wordPresets) rows() fyne.CanvasObject {
	return container.NewVBox(
		container.NewBorder(nil, nil, widget.NewLabel("Preset"),
			container.NewHBox(wp.load, wp.save, wp.folder), wp.sel),
		container.NewBorder(nil, nil, nil, wp.saveAs, wp.name),
		wp.status,
	)
}

// boxes are the two lists as a preset holds them.
func (wp *wordPresets) boxes() (spoken, captions []string) {
	return wordpreset.Lines(wp.p.vocabSource.Text), wordpreset.Lines(wp.p.vocabCaption.Text)
}

// adopt makes name the loaded preset if the file still holds what the boxes
// hold — the lists were applied from it. Otherwise nothing is loaded: the
// file changed since, and Save must not overwrite it unseen. Compared as the
// session sees the lists (config.ParseTerms): applying collapses a file's
// duplicates, and that is not a change (red team).
func (wp *wordPresets) adopt(name string) {
	wp.loaded, wp.spoken, wp.captions, wp.comments, wp.partial = "", nil, nil, false, false
	wp.saveArmed, wp.saveAsArmed = false, false
	if name == "" || wordpreset.ValidName(name) != nil {
		wp.sel.ClearSelected()
		return
	}
	r, stamp, err := wordpreset.Load(wordpreset.Dir(), name)
	spoken, captions := wp.boxes()
	if err != nil || !sameList(r.Spoken, spoken) || !sameList(r.Captions, captions) {
		if hasOption(wp.sel, name) {
			wp.sel.SetSelected(name)
		}
		if err == nil {
			wp.report(nil, []string{"Changed since it was applied — press Load to see it"}, "")
		}
		return
	}
	wp.loaded, wp.stamp, wp.spoken, wp.captions, wp.comments = name, stamp, spoken, captions, r.Comments
	wp.partial = !wordpresetEqual(r.Spoken, spoken) || !wordpresetEqual(r.Captions, captions)
	if wp.partial {
		wp.report(nil, []string{"The lists hold fewer terms than " + name + " — press Load to see them all"}, "")
	}
	if hasOption(wp.sel, name) {
		wp.sel.SetSelected(name)
	}
}

// wordpresetEqual compares two lists exactly, as a file holds them.
func wordpresetEqual(a, b []string) bool {
	return wordpreset.Join(a) == wordpreset.Join(b)
}

// label is what Settings.WordPreset becomes at Apply: the loaded preset, if
// the boxes still hold it, and "" once they have been edited.
func (wp *wordPresets) label() string {
	if wp.loaded == "" {
		return ""
	}
	spoken, captions := wp.boxes()
	if sameList(spoken, wp.spoken) && sameList(captions, wp.captions) {
		return wp.loaded
	}
	return ""
}

// relist reads the folder again: when the tab is shown, on every button, and
// when the application comes back to the front — "Folder, write a file in a
// text editor, switch back" is the path that matters.
func (wp *wordPresets) relist() (skipped int) {
	names, skipped, err := wordpreset.List(wordpreset.Dir())
	if err != nil {
		slog.Warn("could not list word presets", "err", err)
	}
	selected := wp.sel.Selected
	wp.sel.Options = names
	if selected != "" && !hasOption(wp.sel, selected) {
		wp.sel.ClearSelected()
	}
	wp.sel.Refresh()
	return skipped
}

func (wp *wordPresets) disarm() {
	wp.saveArmed, wp.saveAsArmed = false, false
	wp.syncButtons()
}

func (wp *wordPresets) syncButtons() {
	wp.save.SetText("Save")
	if wp.saveArmed {
		wp.save.SetText("Overwrite")
	}
	wp.saveAs.SetText("Save as")
	if wp.saveAsArmed {
		wp.saveAs.SetText("Overwrite")
	}
	if wp.loaded == "" {
		wp.save.Disable()
	} else {
		wp.save.Enable()
	}
}

// report puts one line on the status: an error alone; else the result with
// the first warning after it; else the warning alone. The result comes first
// so that a standing warning ("OpenAI ignores word lists") never hides
// whether a save happened (red team). Every message also goes to the log —
// names and counts, never terms.
func (wp *wordPresets) report(err error, warnings []string, info string) {
	var text string
	switch {
	case err != nil:
		text = err.Error()
		slog.Warn("word preset", "err", err)
	default:
		parts := []string{}
		if info != "" {
			parts = append(parts, info)
			slog.Info("word preset", "result", info)
		}
		if len(warnings) > 0 {
			w := warnings[0]
			if len(warnings) > 1 {
				w += fmt.Sprintf(" (+%d more)", len(warnings)-1)
			}
			parts = append(parts, w)
			for _, w := range warnings {
				slog.Info("word preset", "warning", w)
			}
		}
		text = strings.Join(parts, " · ")
	}
	wp.status.SetText(text)
}

// common warnings that hold whatever the action.
func (wp *wordPresets) contextWarnings(skipped int) []string {
	var out []string
	if !wp.p.selectedProvider().SupportsVocabulary() {
		out = append(out, "OpenAI ignores word lists")
	}
	if skipped > 0 {
		out = append(out, fmt.Sprintf("%d file(s) with unusable names not listed", skipped))
	}
	return out
}

func (wp *wordPresets) doLoad() {
	skipped := wp.relist()
	wp.disarm()
	name := wp.sel.Selected
	if name == "" {
		wp.report(errors.New("Choose a preset to load"), nil, "")
		return
	}
	r, stamp, err := wordpreset.Load(wordpreset.Dir(), name)
	if err != nil {
		wp.report(fmt.Errorf("Not loaded: %w", err), nil, "")
		return
	}
	wp.p.vocabSource.SetText(wordpreset.Join(r.Spoken))
	wp.p.vocabCaption.SetText(wordpreset.Join(r.Captions))
	wp.loaded, wp.stamp, wp.comments, wp.partial = name, stamp, r.Comments, false
	wp.spoken, wp.captions = wp.boxes()
	wp.syncButtons()

	var warnings []string
	for _, line := range r.Reserved {
		warnings = append(warnings, fmt.Sprintf("line %d: [replace] is not used by this version", line))
	}
	for _, list := range []struct {
		what  string
		terms []string
	}{{"spoken", r.Spoken}, {"caption", r.Captions}} {
		if n := distinct(list.terms); n > config.MaxVocabularyTerms {
			warnings = append(warnings, fmt.Sprintf("%d %s terms beyond %d will be ignored",
				n-config.MaxVocabularyTerms, list.what, config.MaxVocabularyTerms))
		}
	}
	warnings = append(warnings, wp.contextWarnings(skipped)...)
	wp.report(nil, warnings, fmt.Sprintf("Loaded %d spoken, %d captions — press %s",
		len(r.Spoken), len(r.Captions), wp.applyHint()))
}

// applyHint says what Apply will do. Short: the line is cut at the dialog's
// width, and the action comes before anything that may be cut.
func (wp *wordPresets) applyHint() string {
	if wp.p.win.translator != nil && wp.p.selectedProvider().SupportsVocabulary() {
		return "Apply to use them (the session restarts)"
	}
	return "Apply to use them"
}

func (wp *wordPresets) doSave() {
	skipped := wp.relist()
	if wp.loaded == "" {
		wp.report(errors.New("Load a preset first, or use Save as"), nil, "")
		return
	}
	// Changed, missing or unreadable since it was loaded or last saved: ask.
	stamp, err := wordpreset.Fingerprint(wordpreset.Dir(), wp.loaded)
	if (err != nil || stamp != wp.stamp) && !wp.saveArmed {
		wp.saveArmed = true
		wp.syncButtons()
		wp.report(nil, []string{"Changed on disk since it was loaded — press Overwrite to replace it"}, "")
		return
	}
	if wp.partial && !wp.saveArmed {
		wp.saveArmed = true
		wp.syncButtons()
		wp.report(nil, []string{"Press Overwrite to save — the file's terms beyond these lists will be lost"}, "")
		return
	}
	wp.write(wp.loaded, skipped)
}

func (wp *wordPresets) doSaveAs() {
	skipped := wp.relist()
	name := strings.TrimSpace(wp.name.Text)
	if err := wordpreset.ValidName(name); err != nil {
		wp.report(fmt.Errorf("Not saved: %w", err), nil, "")
		return
	}
	exists, err := wordpreset.Exists(wordpreset.Dir(), name)
	if err != nil {
		wp.report(fmt.Errorf("Not saved: %w", err), nil, "")
		return
	}
	// Exists as the filesystem sees it: "church" over "Church" on a
	// case-insensitive volume, or an NFD name a Mac editor made, is the same
	// file, and is asked about.
	if exists && !wp.saveAsArmed {
		wp.saveAsArmed = true
		wp.syncButtons()
		wp.report(nil, []string{"Already exists — press Overwrite to replace " + name}, "")
		return
	}
	if wp.write(name, skipped) {
		wp.name.SetText("")
	}
}

// write saves the boxes to name, makes it the loaded preset under the
// spelling the folder lists it by, and re-stamps it so the next Save is not
// mistaken for an overwrite of somebody else's change.
func (wp *wordPresets) write(name string, skipped int) bool {
	dir := wordpreset.Dir()
	spoken, captions := wp.boxes()
	preset := wordpreset.Preset{Spoken: spoken, Captions: captions}
	if err := wordpreset.Save(dir, name, preset); err != nil {
		reason := err
		if errors.Is(err, os.ErrPermission) {
			reason = errors.New("permission denied — the file may be read-only or open in another program")
		}
		wp.report(fmt.Errorf("Not saved: %w", reason), nil, "")
		return false
	}
	hadComments := wp.comments && name == wp.loaded
	wp.relist()
	if listed := wordpreset.SameFile(dir, name, wp.sel.Options); listed != "" {
		name = listed
	}
	// Re-stamped from what is on disk now, so the next Save is not mistaken
	// for an overwrite of somebody else's change.
	stamp, _ := wordpreset.Fingerprint(dir, name)
	wp.loaded, wp.stamp, wp.spoken, wp.captions, wp.comments, wp.partial = name, stamp, spoken, captions, false, false
	if hasOption(wp.sel, name) {
		wp.sel.SetSelected(name)
	}
	wp.disarm()
	var warnings []string
	if hadComments {
		warnings = append(warnings, "comments in the file were not kept")
	}
	warnings = append(warnings, wp.contextWarnings(skipped)...)
	wp.report(nil, warnings, fmt.Sprintf("Saved %d spoken, %d captions to %s", len(spoken), len(captions), name))
	return true
}

func (wp *wordPresets) doFolder() {
	wp.relist()
	dir := wordpreset.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		wp.report(fmt.Errorf("No presets folder: %w", err), nil, "")
		return
	}
	if err := revealFolder(dir); err != nil {
		wp.report(fmt.Errorf("Folder not opened: %w", err), nil, "")
		return
	}
	wp.report(nil, nil, "Save a preset there as a UTF-8 .txt file")
}

func hasOption(s *widget.Select, name string) bool {
	for _, o := range s.Options {
		if o == name {
			return true
		}
	}
	return false
}

// sameList compares two lists as a session would use them.
func sameList(a, b []string) bool {
	x := config.ParseTerms(wordpreset.Join(a))
	y := config.ParseTerms(wordpreset.Join(b))
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// distinct counts the terms ParseTerms would keep before its cap.
func distinct(terms []string) int {
	seen := map[string]bool{}
	for _, t := range terms {
		seen[t] = true
	}
	return len(seen)
}
