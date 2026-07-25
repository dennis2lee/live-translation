package captionbuffer

import (
	"reflect"
	"testing"
)

// These mirror tests/test_caption_buffer.py so the Go port is provably
// equivalent to the Python original rather than merely similar.

func inc() *Buffer { return New(2, PolicyIncremental, 0) }
func utt() *Buffer { return New(2, PolicyUtterance, 0) }

func eq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func texts(lines []Line) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.Text)
	}
	return out
}

// --- shared ---------------------------------------------------------------

func TestStartsEmptySoTheBoxIsHidden(t *testing.T) {
	b := inc()
	if !b.IsEmpty() || len(b.DisplayLines()) != 0 {
		t.Fatal("expected an empty buffer")
	}
}

func TestDuplicateConsecutiveFragmentsAreIgnored(t *testing.T) {
	b := inc()
	if !b.Feed("hello", 0) {
		t.Fatal("first feed should change the display")
	}
	if b.Feed("hello", 0.1) {
		t.Fatal("repeat should be ignored")
	}
	if b.PendingText() != "hello" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestEmptyFragmentIsIgnored(t *testing.T) {
	b := inc()
	if b.Feed("", 0) || !b.IsEmpty() {
		t.Fatal("empty fragment must be ignored")
	}
}

func TestClearRemovesEverything(t *testing.T) {
	b := inc()
	b.Feed("one two.", 0)
	b.Feed("three", 0.1)
	b.Clear()
	if !b.IsEmpty() {
		t.Fatal("expected empty after Clear")
	}
	eq(t, b.CommittedLines(), nil)
}

func TestDisplayLinesAreCappedAndKeepTheNewest(t *testing.T) {
	b := New(2, PolicyIncremental, 0)
	for i, s := range []string{"a.", "b.", "c."} {
		b.Feed(s, float64(i))
	}
	// Drop from the front and show the newest content.
	eq(t, texts(b.DisplayLines()), []string{"b.", "c."})
}

func TestSetMaxLinesPreservesRecentLines(t *testing.T) {
	b := New(4, PolicyIncremental, 0)
	for i, s := range []string{"a.", "b.", "c.", "d."} {
		b.Feed(s, float64(i))
	}
	b.SetMaxLines(2)
	eq(t, b.CommittedLines(), []string{"c.", "d."})
}

// --- policy A, incremental ------------------------------------------------

func TestPolicyAAccumulatesIntoPending(t *testing.T) {
	b := inc()
	b.Feed("We're ", 0)
	b.Feed("going ", 0.1)
	b.Feed("to look", 0.2)
	eq(t, b.CommittedLines(), nil)
	if b.PendingText() != "We're going to look" {
		t.Fatalf("pending = %q", b.PendingText())
	}
	lines := b.DisplayLines()
	if len(lines) != 1 || lines[0].Settled {
		t.Fatalf("expected one unsettled line, got %+v", lines)
	}
}

func TestPolicyACommitsOnSentenceTerminator(t *testing.T) {
	b := inc()
	b.Feed("Hello there", 0)
	b.Feed(".", 0.1)
	eq(t, b.CommittedLines(), []string{"Hello there."})
	if b.PendingText() != "" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestPolicyACommitsOnQuestionAndExclamation(t *testing.T) {
	for _, ender := range []string{"?", "!"} {
		b := inc()
		b.Feed("Really"+ender, 0)
		eq(t, b.CommittedLines(), []string{"Really" + ender})
	}
}

func TestPolicyACommitsAfterIdleTimeout(t *testing.T) {
	b := inc()
	b.Feed("no terminator here", 0)
	if b.Tick(0.5) { // not idle long enough yet
		t.Fatal("committed too early")
	}
	eq(t, b.CommittedLines(), nil)
	if !b.Tick(1.3) { // past 1.2 s
		t.Fatal("should have committed")
	}
	eq(t, b.CommittedLines(), []string{"no terminator here"})
}

func TestTickIsNoopWhenNothingPending(t *testing.T) {
	if inc().Tick(99) {
		t.Fatal("nothing to do")
	}
}

func TestPolicyAIdleTimerResetsOnEachFragment(t *testing.T) {
	b := inc()
	b.Feed("a", 0)
	b.Feed("b", 1.0)
	if b.Tick(1.5) { // only 0.5 s since the last fragment
		t.Fatal("timer did not reset")
	}
	if !b.Tick(2.3) {
		t.Fatal("should have committed")
	}
}

// --- policy B, per utterance ----------------------------------------------

func TestPolicyBCommitsImmediately(t *testing.T) {
	b := utt()
	b.Feed("We're going to look at the Q3 numbers", 0)
	eq(t, b.CommittedLines(), []string{"We're going to look at the Q3 numbers"})
	if b.PendingText() != "" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestPolicyBKeepsPreviousLineDuringSilence(t *testing.T) {
	b := utt()
	b.Feed("first utterance", 0)
	if b.Tick(60) { // time passing does not clear it
		t.Fatal("should not clear with clearing disabled")
	}
	eq(t, texts(b.DisplayLines()), []string{"first utterance"})
}

// --- AUTO detection -------------------------------------------------------

func TestAutoResolvesToUtteranceForLongChunks(t *testing.T) {
	b := New(2, PolicyAuto, 0)
	for i := 0; i < 8; i++ {
		b.Feed("this is a fairly long utterance number x", float64(i))
		b.NoteReconnect() // defeat duplicate suppression
	}
	if b.EffectivePolicy() != PolicyUtterance {
		t.Fatalf("policy = %s", b.EffectivePolicy())
	}
}

func TestAutoResolvesToIncrementalForShortFragments(t *testing.T) {
	b := New(2, PolicyAuto, 0)
	for i, frag := range []string{"We ", "are ", "go", "ing ", "to ", "look ", "at ", "it "} {
		b.Feed(frag, float64(i))
	}
	if b.EffectivePolicy() != PolicyIncremental {
		t.Fatalf("policy = %s", b.EffectivePolicy())
	}
}

func TestAutoDefaultsToIncrementalBeforeEnoughSamples(t *testing.T) {
	b := New(2, PolicyAuto, 0)
	b.Feed("hi ", 0)
	if b.EffectivePolicy() != PolicyIncremental {
		t.Fatalf("policy = %s", b.EffectivePolicy())
	}
}

// --- reconnect ------------------------------------------------------------

func TestReconnectContinuesThePreviousLineInPolicyA(t *testing.T) {
	b := inc()
	b.Feed("the quarterly numbers", 0)
	b.Tick(2.0) // settled
	eq(t, b.CommittedLines(), []string{"the quarterly numbers"})

	b.NoteReconnect()
	b.Feed("were better than expected.", 3.0)
	// Continue the line rather than starting a new one.
	eq(t, b.CommittedLines(), []string{"the quarterly numbers were better than expected."})
}

func TestReconnectContinuesThePreviousLineInPolicyB(t *testing.T) {
	b := utt()
	b.Feed("the quarterly numbers", 0)
	b.NoteReconnect()
	b.Feed("were better than expected", 1.0)
	eq(t, b.CommittedLines(), []string{"the quarterly numbers were better than expected"})
}

func TestReconnectAppendsToPendingWhenNothingCommitted(t *testing.T) {
	b := inc()
	b.Feed("half a sen", 0)
	b.NoteReconnect()
	b.Feed("tence continues", 1.0)
	if b.PendingText() != "half a sen tence continues" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestReconnectAllowsTheSameTextAgain(t *testing.T) {
	b := inc()
	b.Feed("hello", 0)
	b.NoteReconnect()
	if !b.Feed("hello", 1.0) {
		t.Fatal("duplicate suppression should reset on reconnect")
	}
}

func TestReconnectWithEmptyBufferIsSafe(t *testing.T) {
	b := inc()
	b.NoteReconnect()
	b.Feed("first words", 0)
	if b.PendingText() != "first words" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

// --- joining rules --------------------------------------------------------

func TestJoinRespectsExplicitSpacingFromTheAPI(t *testing.T) {
	b := inc()
	b.Feed("We're ", 0)
	b.Feed("going", 0.1)
	if b.PendingText() != "We're going" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestJoinDoesNotInsertSpaceBeforePunctuation(t *testing.T) {
	b := inc()
	b.Feed("Hello", 0)
	b.Feed(",", 0.1)
	b.Feed("world", 0.2)
	if b.PendingText() != "Hello, world" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestKoreanFragmentsWithLeadingSpacesJoinCorrectly(t *testing.T) {
	// As measured, Korean fragments also arrive with a leading space.
	b := inc()
	for i, frag := range []string{" 질문이", " 있으시면,", " 지금 말씀해 주세요."} {
		b.Feed(frag, float64(i))
	}
	eq(t, b.CommittedLines(), []string{"질문이 있으시면, 지금 말씀해 주세요."})
}

func TestNoSpaceIsInsertedInsideAKoreanWord(t *testing.T) {
	// A fragment split mid-word must not gain a space.
	b := inc()
	b.Feed("늘릴 계획", 0)
	b.Feed("입니다.", 0.1)
	eq(t, b.CommittedLines(), []string{"늘릴 계획입니다."})
}

func TestEnglishStillGetsASpaceWhenTheAPIOmitsOne(t *testing.T) {
	b := inc()
	b.Feed("hello", 0)
	b.Feed("world", 0.1)
	if b.PendingText() != "hello world" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestMixedKoreanAndEnglishStillGetsASpace(t *testing.T) {
	b := inc()
	b.Feed("연구개발", 0)
	b.Feed("R&D", 0.1)
	if b.PendingText() != "연구개발 R&D" {
		t.Fatalf("pending = %q", b.PendingText())
	}
}

func TestCommittedLinesAreStripped(t *testing.T) {
	b := inc()
	b.Feed("  spaced out.  ", 0)
	eq(t, b.CommittedLines(), []string{"spaced out."})
}

func TestWhitespaceOnlyFragmentCreatesNoLine(t *testing.T) {
	b := inc()
	b.Feed("   ", 0)
	b.Tick(5.0)
	eq(t, b.CommittedLines(), nil)
	if !b.IsEmpty() {
		t.Fatal("expected empty")
	}
}

// --- clearing on silence --------------------------------------------------

func TestClearAfterSilenceIsThreeSecondsByDefault(t *testing.T) {
	if ClearAfterSeconds != 3.0 {
		t.Fatalf("ClearAfterSeconds = %v", ClearAfterSeconds)
	}
}

func TestCaptionsAreClearedAfterTheSilenceWindow(t *testing.T) {
	b := New(2, PolicyIncremental, 2.0)
	b.Feed("Hello there.", 0)
	eq(t, b.CommittedLines(), []string{"Hello there."})
	if b.Tick(1.5) { // under two seconds
		t.Fatal("cleared too early")
	}
	if !b.Tick(2.0) {
		t.Fatal("should have cleared")
	}
	if !b.IsEmpty() {
		t.Fatal("expected empty")
	}
}

func TestClearAlsoAppliesToPolicyB(t *testing.T) {
	b := New(2, PolicyUtterance, 2.0)
	b.Feed("one utterance", 0)
	if b.Tick(1.9) {
		t.Fatal("cleared too early")
	}
	if !b.Tick(2.1) || !b.IsEmpty() {
		t.Fatal("should have cleared")
	}
}

func TestSilenceTimerResetsWhenSpeechResumes(t *testing.T) {
	b := New(2, PolicyIncremental, 2.0)
	b.Feed("First part.", 0)
	b.Feed("Second part.", 1.5) // speech resumes before the clear
	if b.Tick(2.5) {            // only 1.0 s since the last fragment
		t.Fatal("cleared too early")
	}
	eq(t, b.CommittedLines(), []string{"First part.", "Second part."})
	if !b.Tick(3.6) || !b.IsEmpty() {
		t.Fatal("should have cleared")
	}
}

func TestClearDoesNotFireRepeatedlyOnceEmpty(t *testing.T) {
	b := New(2, PolicyIncremental, 2.0)
	b.Feed("Hello.", 0)
	if !b.Tick(3.0) {
		t.Fatal("should have cleared")
	}
	if b.Tick(4.0) || b.Tick(99.0) {
		t.Fatal("should stay quiet once empty")
	}
}

func TestZeroDisablesClearing(t *testing.T) {
	b := New(2, PolicyIncremental, 0)
	b.Feed("Stays put.", 0)
	if b.Tick(600) {
		t.Fatal("clearing should be disabled")
	}
	eq(t, b.CommittedLines(), []string{"Stays put."})
}

func TestTextAfterAClearStartsAFreshLine(t *testing.T) {
	b := New(2, PolicyIncremental, 2.0)
	b.Feed("Old sentence.", 0)
	b.Tick(2.5)
	b.Feed("New sentence.", 5.0)
	eq(t, b.CommittedLines(), []string{"New sentence."})
}

// --- transcript handover --------------------------------------------------

func TestBufferHandsOverEachCommittedLineOnce(t *testing.T) {
	b := New(2, PolicyUtterance, 0)
	b.Feed("first line", 0)
	b.Feed("second line", 1.0)
	eq(t, b.DrainNewCommits(), []string{"first line", "second line"})
	eq(t, b.DrainNewCommits(), nil)
}

func TestClearingTheDisplayDoesNotDropUndrainedLines(t *testing.T) {
	b := New(2, PolicyIncremental, 2.0)
	b.Feed("kept for the record.", 0)
	b.Tick(3.0) // the display clears
	if !b.IsEmpty() {
		t.Fatal("expected empty display")
	}
	eq(t, b.DrainNewCommits(), []string{"kept for the record."})
}

func TestShortClearWindowStillRecordsPendingText(t *testing.T) {
	// Recording survives ClearAfterSeconds shorter than IdleCommitSeconds.
	b := New(2, PolicyIncremental, 0.5)
	b.Feed("no terminator here", 0)
	b.Tick(0.6)
	if !b.IsEmpty() {
		t.Fatal("expected empty display")
	}
	eq(t, b.DrainNewCommits(), []string{"no terminator here"})
}

func TestReconnectDoesNotDuplicateAnAlreadyRecordedLine(t *testing.T) {
	// Resuming an already-written line emits only the continuation.
	b := New(2, PolicyIncremental, 0)
	b.Feed("Revenue is up 14%.", 0)
	eq(t, b.DrainNewCommits(), []string{"Revenue is up 14%."}) // written to the file

	b.NoteReconnect()
	b.Feed(" We will continue.", 1.0)
	eq(t, b.DrainNewCommits(), []string{"We will continue."})
	// The display keeps the merged line.
	eq(t, b.CommittedLines(), []string{"Revenue is up 14%. We will continue."})
}

func TestReconnectBeforeDrainEmitsTheMergedLineOnce(t *testing.T) {
	b := New(2, PolicyIncremental, 0)
	b.Feed("Revenue is up 14%.", 0)
	b.NoteReconnect()
	b.Feed(" We will continue.", 1.0)
	eq(t, b.DrainNewCommits(), []string{"Revenue is up 14%. We will continue."})
}

func TestFlushCommitsTheTrailingSentence(t *testing.T) {
	b := New(2, PolicyIncremental, 0)
	b.Feed("unfinished thought", 0)
	if !b.Flush() {
		t.Fatal("flush should commit")
	}
	eq(t, b.DrainNewCommits(), []string{"unfinished thought"})
	if b.Flush() {
		t.Fatal("second flush should be a no-op")
	}
}
