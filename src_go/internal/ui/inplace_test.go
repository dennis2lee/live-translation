package ui

import (
	"image/color"
	"os"
	"slices"
	"strings"
	"testing"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/test"

	"github.com/dennis2lee/live-translation/src_go/internal/captions"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

// spoken is a stretch of a real session, fragment by fragment, as the server
// sent it (spikes/s3_sync, 2026-11-22).
var spoken = []string{
	"Hello.", " Today's quarterly", " earnings meeting", " will now begin.",
	" First,", " let's review", " the third-quarter", " sales.",
	" Total", " revenue is", " higher than the same", " period last year", " by 14%.",
	" However,", " the domestic", " market alone", " shows a slight", " slowdown in growth.",
	" Overseas", " sales accounted", " for more than half", " for the first time.",
	" This is", " particularly significant.",
}

// drawn is one caption row as it is painted.
type drawn struct {
	text   string
	colour color.Color
	pos    fyne.Position
}

// viewOf puts a caption view on a test window, already faded in.
func viewOf(t *testing.T, s config.Settings) (*CaptionView, *captionRenderer) {
	t.Helper()
	v := NewCaptionView(s)
	v.boxAlpha = 1
	win := test.NewWindow(v)
	t.Cleanup(win.Close)
	win.SetPadded(false)
	win.Resize(fyne.NewSize(900, 400))
	return v, test.WidgetRenderer(v).(*captionRenderer)
}

func drawnRows(r *captionRenderer) []drawn {
	var out []drawn
	for _, tx := range shownTexts(r) {
		out = append(out, drawn{tx.Text, tx.Color, tx.Position()})
	}
	return out
}

// Text in progress is drawn at the end of the paragraph, in the text colour.
func TestTextInProgressIsDrawnInPlace(t *testing.T) {
	v, r := viewOf(t, config.Default())
	v.SetLines([]captions.Line{
		{Text: "One settled.", Settled: true},
		{Text: " and one being said ", Settled: false},
	})
	rows := drawnRows(r)
	if len(rows) != 1 || rows[0].text != "One settled. and one being said" {
		t.Fatalf("rows = %+v, want the two on one row", rows)
	}
	if _, _, _, a := rows[0].colour.RGBA(); a != 0xFFFF {
		t.Errorf("drawn at alpha %#x, want the full text colour", a)
	}
}

// With the setting on it is drawn the old way: dimmed, on a row of its own.
func TestDimmingDrawsTextInProgressApart(t *testing.T) {
	s := config.Default()
	s.DimDraft = true
	v, r := viewOf(t, s)
	v.SetLines([]captions.Line{
		{Text: "A settled sentence.", Settled: true},
		{Text: "and one being said", Settled: false},
	})
	rows := drawnRows(r)
	if len(rows) != 2 || rows[0].text != "A settled sentence." || rows[1].text != "and one being said" {
		t.Fatalf("rows = %+v, want one each", rows)
	}
	_, _, _, settled := rows[0].colour.RGBA()
	_, _, _, draft := rows[1].colour.RGBA()
	if settled != 0xFFFF || draft >= settled*6/10 || draft <= settled*5/10 {
		t.Errorf("alpha settled %#x, draft %#x — want the draft at 55%%", settled, draft)
	}
}

// A sentence settling changes nothing on screen: not the words, not the
// colour, not where a single row sits.
//
// The complaint the dimmed draft drew was that every caption was written
// twice. Settled by the idle timer here, so the settling is the only thing
// that happens between the two pictures.
func TestSettlingChangesNothingOnScreen(t *testing.T) {
	s := config.Default()
	s.MaxLines = 3
	v, r := viewOf(t, s)
	b := captions.New(captions.Options{
		MaxLines: 3, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	b.Feed("A first sentence, long enough that the second has to wrap.", 0)
	b.Feed(" And a second one that the speaker", 1)
	b.Feed(" has not finished and may never finish", 2)
	if b.PendingText() == "" {
		t.Fatal("setup: nothing is in progress")
	}
	v.SetLines(b.DisplayLines())
	before := drawnRows(r)

	b.Tick(2+captions.IdleCommitSeconds+0.1, captions.SpeechActive)
	if b.PendingText() != "" {
		t.Fatal("setup: the idle timer did not settle the sentence")
	}
	v.SetLines(b.DisplayLines())
	after := drawnRows(r)

	if len(before) < 2 {
		t.Fatalf("setup: only %d row, the wrap is not exercised", len(before))
	}
	if !slices.Equal(before, after) {
		t.Errorf("settling redrew the caption:\n  before %+v\n  after  %+v", before, after)
	}
}

// As words arrive, every row but the last stays exactly as it was — a word
// is added to the last row or starts a new one, and nothing above moves.
func TestEarlierRowsStayPutAsWordsArrive(t *testing.T) {
	b := captions.New(captions.Options{
		MaxLines: 4, Policy: captions.PolicyIncremental, DraftInPlace: true,
	})
	// Narrow, so the paragraph runs to many rows; uncapped, so a row
	// scrolling off the top is not mistaken for one changing.
	const width, noCap = 500, 1000
	var prev []row
	for i, fragment := range spoken {
		now := float64(i)
		b.Feed(fragment, now)
		b.Tick(now+0.1, captions.SpeechActive)
		rows := reflowRows(inPlace(b.DisplayLines()), 44, width, noCap)

		for _, rw := range rows {
			if !rw.settled {
				t.Fatalf("after %q a row is still drawn as a draft: %+v", fragment, rw)
			}
		}
		if len(rows) < len(prev) {
			t.Fatalf("after %q the caption lost a row: %q -> %q", fragment, rowTexts(prev), rowTexts(rows))
		}
		for j := 0; j+1 < len(prev); j++ {
			if rows[j] != prev[j] {
				t.Fatalf("after %q row %d changed: %q -> %q", fragment, j, prev[j].text, rows[j].text)
			}
		}
		prev = rows
	}
	if len(prev) < 6 {
		t.Fatalf("setup: only %d rows, the wrap is not exercised", len(prev))
	}
	want := strings.Join(strings.Fields(strings.Join(spoken, "")), " ")
	if got := strings.Join(rowTexts(prev), " "); got != want {
		t.Errorf("the rows read\n  %q\nwant\n  %q", got, want)
	}
}

// The window draws each fragment the moment it arrives, without waiting for
// its sentence — which is what keeps the captions with the voice.
func TestTheWindowDrawsWordsAsTheyArrive(t *testing.T) {
	w := newTestWindow(t)
	if w.settings.CompletePhrasesOnly || w.settings.DimDraft {
		t.Fatal("setup: the defaults are not word by word, in place")
	}
	if !w.buffer.DraftInPlace() {
		t.Fatal("the buffer was not told the draft is drawn in place")
	}
	w.view.boxAlpha = 1
	r := test.WidgetRenderer(w.view).(*captionRenderer)

	for i, fragment := range spoken[:8] {
		w.onTargetText(fragment)
		want := strings.Join(strings.Fields(strings.Join(spoken[:i+1], "")), " ")
		var got []string
		for _, d := range drawnRows(r) {
			got = append(got, d.text)
			if _, _, _, a := d.colour.RGBA(); a != 0xFFFF {
				t.Errorf("after %q a row is dimmed: %q", fragment, d.text)
			}
		}
		if strings.Join(got, " ") != want {
			t.Fatalf("after %q the screen reads %q, want %q", fragment, got, want)
		}
	}
}

// The setting reaches the buffer when it changes, so the hold comes back
// with the dimmed draft and goes with it.
func TestTheBufferFollowsTheDimSetting(t *testing.T) {
	w := newTestWindow(t)
	w.update(func(s *config.Settings) { s.DimDraft = true })
	if w.buffer.DraftInPlace() {
		t.Error("dimming is on and the buffer still treats the draft as in place")
	}
	w.update(func(s *config.Settings) { s.DimDraft = false })
	if !w.buffer.DraftInPlace() {
		t.Error("dimming is off and the buffer was not told")
	}
}

// Preferences reads the check back, and shows a change made elsewhere.
func TestPreferencesChoosesHowTextInProgressIsDrawn(t *testing.T) {
	w := newTestWindow(t)
	p := openLivePrefs(t, w)
	if p.dimDraft.Checked {
		t.Fatal("setup: the check starts on")
	}
	p.dimDraft.SetChecked(true)
	if !p.collect().DimDraft {
		t.Error("the check is not read back into the settings")
	}
	p.apply()
	if !w.settings.DimDraft || w.buffer.DraftInPlace() {
		t.Error("Apply did not turn dimming on")
	}

	w.update(func(s *config.Settings) { s.DimDraft = false })
	if p.dimDraft.Checked {
		t.Error("the check did not follow a change made outside the dialog")
	}
}

// An older settings file has no dim_draft and reads as off; a choice to dim
// survives being saved and loaded.
func TestTheDimSettingIsKeptInTheSettingsFile(t *testing.T) {
	dir := t.TempDir()
	t.Cleanup(config.SetSettingsDirForTests(dir))
	older := `{"complete_phrases_only": true, "max_lines": 4, "font_size": 44}`
	if err := os.WriteFile(config.Path(), []byte(older), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := config.Load()
	if loaded.DimDraft {
		t.Error("an older settings file reads as dimmed")
	}
	if !loaded.CompletePhrasesOnly || loaded.MaxLines != 4 {
		t.Fatalf("setup: the file was not read: %+v", loaded)
	}

	loaded.DimDraft = true
	if err := config.Save(loaded); err != nil {
		t.Fatal(err)
	}
	if !config.Load().DimDraft {
		t.Error("the choice to dim did not survive a save and a load")
	}
	raw, _ := os.ReadFile(config.Path())
	if !strings.Contains(string(raw), `"dim_draft": true`) {
		t.Errorf("the settings file does not carry dim_draft: %s", raw)
	}
}

// Started with dimming already chosen, the buffer is told, and Preferences
// shows it.
func TestAWindowStartedDimmedStaysDimmed(t *testing.T) {
	w := newTestWindowWith(t, func(s *config.Settings) { s.DimDraft = true })
	if w.buffer.DraftInPlace() {
		t.Error("dimming was on at start and the buffer treats the draft as in place")
	}
	p := openPrefs(t, w)
	if !p.dimDraft.Checked {
		t.Error("Preferences opened with the check off")
	}
	if !p.collect().DimDraft {
		t.Error("an untouched dialog would turn dimming off")
	}
}

// OpenAI sends pieces of words and Gemini whole ones; the buffer is told
// which, at start and when the service is changed.
func TestTheBufferKnowsWhichServiceIsSpeaking(t *testing.T) {
	w := newTestWindowWith(t, func(s *config.Settings) { s.Provider = config.ProviderOpenAI })
	if !w.buffer.WordPieces() {
		t.Error("started on OpenAI, and the buffer joins fragments as whole words")
	}
	w.update(func(s *config.Settings) { s.Provider = config.ProviderGemini })
	if w.buffer.WordPieces() {
		t.Error("changed to Gemini, and the buffer still joins pieces")
	}
	w.update(func(s *config.Settings) { s.Provider = config.ProviderOpenAI })
	if !w.buffer.WordPieces() {
		t.Error("changed back to OpenAI, and the buffer was not told")
	}

	// And it shows: the pieces of a word come out as the word.
	w.view.boxAlpha = 1
	for _, piece := range []string{"He", " gre", "ets", " me", "."} {
		w.onTargetText(piece)
	}
	r := test.WidgetRenderer(w.view).(*captionRenderer)
	if rows := drawnRows(r); len(rows) != 1 || rows[0].text != "He greets me." {
		t.Errorf("the screen reads %+v", rows)
	}
}

// The line hold does nothing when words are drawn in place as they arrive,
// and Preferences greys it then.
func TestTheLineHoldIsGreyedWhereItDoesNothing(t *testing.T) {
	w := newTestWindow(t)
	p := openPrefs(t, w)
	if !p.lineHold.Disabled() {
		t.Error("words in place, and the line hold is offered")
	}
	p.completeOnly.SetChecked(true)
	if p.lineHold.Disabled() {
		t.Error("whole sentences, and the line hold is greyed")
	}
	p.completeOnly.SetChecked(false)
	p.dimDraft.SetChecked(true)
	if p.lineHold.Disabled() {
		t.Error("dimmed draft, and the line hold is greyed")
	}
	p.dimDraft.SetChecked(false)
	if !p.lineHold.Disabled() {
		t.Error("back to words in place, and the line hold is offered again")
	}
	// Greyed, it still keeps its value.
	if got := p.collect().LineHoldSeconds; got != w.settings.LineHoldSeconds {
		t.Errorf("a greyed slider changed the setting: %v", got)
	}
}

// Lines that arrive whole — the utterance policy — are paced, so the slider
// is offered; and a reset back to words in place greys it, though neither
// check in the dialog was touched.
func TestTheLineHoldFollowsThePolicy(t *testing.T) {
	w := newTestWindowWith(t, func(s *config.Settings) {
		s.CaptionPolicy = string(captions.PolicyUtterance)
	})
	p := openLivePrefs(t, w)
	if p.lineHold.Disabled() {
		t.Fatal("whole utterances, and the line hold is greyed")
	}
	w.ResetSettings()
	if w.settings.Policy() != captions.PolicyIncremental {
		t.Fatalf("setup: reset left the policy at %q", w.settings.CaptionPolicy)
	}
	if !p.lineHold.Disabled() {
		t.Error("reset to words in place, and the line hold is still offered")
	}
}
