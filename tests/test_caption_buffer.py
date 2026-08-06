import pytest

from livecap.caption_buffer import CaptionBuffer, Policy


def inc(**kw) -> CaptionBuffer:
    # Silence clearing off by default, so these tests isolate commit behaviour.
    kw.setdefault("clear_after_seconds", 0.0)
    return CaptionBuffer(policy=Policy.INCREMENTAL, **kw)


def utt(**kw) -> CaptionBuffer:
    kw.setdefault("clear_after_seconds", 0.0)
    return CaptionBuffer(policy=Policy.UTTERANCE, **kw)


# --- shared ---------------------------------------------------------------


def test_starts_empty_so_the_box_is_hidden():
    buf = inc()
    assert buf.is_empty
    assert buf.display_lines() == []


def test_duplicate_consecutive_fragments_are_ignored():
    """A redelivery arrives at once — ordering is not guaranteed."""
    buf = inc()
    assert buf.feed("hello", 0.0) is True
    assert buf.feed("hello", 0.1) is False
    assert buf.pending_text == "hello"


def test_a_word_genuinely_repeated_later_is_kept():
    """Fragments arrive about a second apart, so the same text a second on is
    the speaker repeating a word, not the API sending it twice. Swallowing it
    loses what they actually said."""
    buf = inc()
    assert buf.feed(" the", 0.0) is True
    assert buf.feed(" the", 1.0) is True
    assert buf.pending_text == "the the"


def test_the_duplicate_window_is_well_under_the_fragment_interval():
    """Measured median inter-arrival is about 1000 ms (spikes/RESULTS.md)."""
    from livecap.caption_buffer import DUPLICATE_WINDOW_SECONDS

    assert DUPLICATE_WINDOW_SECONDS < 0.5


def test_empty_fragment_is_ignored():
    buf = inc()
    assert buf.feed("", 0.0) is False
    assert buf.is_empty


def test_clear_removes_everything():
    buf = inc()
    buf.feed("one two.", 0.0)
    buf.feed("three", 0.1)
    buf.clear()
    assert buf.is_empty
    assert buf.committed_lines == []


def test_clearing_the_display_still_records_what_was_said():
    """Recording is independent of the screen — see drain_new_commits. Half a
    sentence must not vanish from the transcript because the captions were
    cleared underneath it."""
    buf = inc()
    buf.feed("a finished sentence.", 0.0)
    buf.feed(" and half of another", 1.0)
    buf.clear()
    assert buf.drain_new_commits() == [
        "a finished sentence.",
        "and half of another",
    ]


def test_clearing_an_empty_buffer_records_nothing():
    buf = inc()
    buf.clear()
    assert buf.drain_new_commits() == []


def test_display_lines_are_capped_and_keep_the_newest():
    buf = inc(max_lines=2)
    for i, t in enumerate(["a.", "b.", "c."]):
        buf.feed(t, float(i))
    # Drop from the front and show the newest content.
    assert [t for t, _ in buf.display_lines()] == ["b.", "c."]


def test_set_max_lines_preserves_recent_lines():
    buf = inc(max_lines=4)
    for i, t in enumerate(["a.", "b.", "c.", "d."]):
        buf.feed(t, float(i))
    buf.set_max_lines(2)
    assert buf.committed_lines == ["c.", "d."]


# --- policy A, incremental ------------------------------------------------


def test_policy_a_accumulates_into_pending():
    buf = inc()
    buf.feed("We're ", 0.0)
    buf.feed("going ", 0.1)
    buf.feed("to look", 0.2)
    assert buf.committed_lines == []
    assert buf.pending_text == "We're going to look"
    assert buf.display_lines() == [("We're going to look", False)]


def test_policy_a_commits_on_sentence_terminator():
    buf = inc()
    buf.feed("Hello there", 0.0)
    buf.feed(".", 0.1)
    assert buf.committed_lines == ["Hello there."]
    assert buf.pending_text == ""


def test_policy_a_commits_on_question_and_exclamation():
    for ender in "?!":
        buf = inc()
        buf.feed(f"Really{ender}", 0.0)
        assert buf.committed_lines == [f"Really{ender}"]


def test_policy_a_commits_after_idle_timeout():
    buf = inc()
    buf.feed("no terminator here", 0.0)
    assert buf.tick(0.5) is False  # not idle long enough yet
    assert buf.committed_lines == []
    assert buf.tick(1.3) is True  # past 1.2 s
    assert buf.committed_lines == ["no terminator here"]
    assert buf.pending_text == ""


def test_tick_is_noop_when_nothing_pending():
    buf = inc()
    assert buf.tick(99.0) is False


def test_policy_a_idle_timer_resets_on_each_fragment():
    buf = inc()
    buf.feed("a", 0.0)
    buf.feed("b", 1.0)
    assert buf.tick(1.5) is False  # only 0.5 s since the last fragment
    assert buf.tick(2.3) is True


# --- policy B, per utterance ----------------------------------------------


def test_policy_b_commits_immediately_with_no_pending_stage():
    buf = utt()
    buf.feed("We're going to look at the Q3 numbers", 0.0)
    assert buf.committed_lines == ["We're going to look at the Q3 numbers"]
    assert buf.pending_text == ""


def test_policy_b_keeps_previous_line_during_silence():
    buf = utt()
    buf.feed("first utterance", 0.0)
    assert buf.tick(60.0) is False  # time passing does not clear it
    assert buf.display_lines() == [("first utterance", True)]


# --- AUTO detection -------------------------------------------------------


def test_auto_resolves_to_utterance_for_long_chunks():
    buf = CaptionBuffer(policy=Policy.AUTO)
    for i in range(8):
        buf.feed(f"this is a fairly long utterance number {i}", float(i))
    assert buf.effective_policy is Policy.UTTERANCE


def test_auto_resolves_to_incremental_for_short_fragments():
    buf = CaptionBuffer(policy=Policy.AUTO)
    for i, frag in enumerate(["We ", "are ", "go", "ing ", "to ", "look ", "at ", "it "]):
        buf.feed(frag, float(i))
    assert buf.effective_policy is Policy.INCREMENTAL


def test_auto_defaults_to_incremental_before_enough_samples():
    buf = CaptionBuffer(policy=Policy.AUTO)
    buf.feed("hi ", 0.0)
    assert buf.effective_policy is Policy.INCREMENTAL


# --- reconnect ------------------------------------------------------------


def test_reconnect_continues_the_previous_line_in_policy_a():
    buf = inc()
    buf.feed("the quarterly numbers", 0.0)
    buf.tick(2.0)  # settled
    assert buf.committed_lines == ["the quarterly numbers"]

    buf.note_reconnect()
    buf.feed("were better than expected.", 3.0)
    # Continue the line rather than starting a new one.
    assert buf.committed_lines == ["the quarterly numbers were better than expected."]


def test_reconnect_continues_the_previous_line_in_policy_b():
    buf = utt()
    buf.feed("the quarterly numbers", 0.0)
    buf.note_reconnect()
    buf.feed("were better than expected", 1.0)
    assert buf.committed_lines == ["the quarterly numbers were better than expected"]


def test_reconnect_appends_to_pending_when_nothing_committed_yet():
    buf = inc()
    buf.feed("half a sen", 0.0)
    buf.note_reconnect()
    buf.feed("tence continues", 1.0)
    assert buf.pending_text == "half a sen tence continues"


def test_reconnect_allows_the_same_text_to_arrive_again():
    buf = inc()
    buf.feed("hello", 0.0)
    buf.note_reconnect()
    assert buf.feed("hello", 1.0) is True


def test_reconnect_with_empty_buffer_is_safe():
    buf = inc()
    buf.note_reconnect()
    buf.feed("first words", 0.0)
    assert buf.pending_text == "first words"


# --- joining rules --------------------------------------------------------


def test_join_respects_explicit_spacing_from_the_api():
    buf = inc()
    buf.feed("We're ", 0.0)
    buf.feed("going", 0.1)
    assert buf.pending_text == "We're going"


def test_join_does_not_insert_space_before_punctuation():
    buf = inc()
    buf.feed("Hello", 0.0)
    buf.feed(",", 0.1)
    buf.feed("world", 0.2)
    assert buf.pending_text == "Hello, world"


def test_korean_fragments_with_leading_spaces_join_correctly():
    """As measured, Korean fragments also arrive with a leading space."""
    buf = inc()
    for i, frag in enumerate([" 질문이", " 있으시면,", " 지금 말씀해 주세요."]):
        buf.feed(frag, float(i))
    assert buf.committed_lines == ["질문이 있으시면, 지금 말씀해 주세요."]


def test_no_space_is_inserted_inside_a_korean_word():
    """A fragment split mid-word must not gain a space."""
    buf = inc()
    buf.feed("늘릴 계획", 0.0)
    buf.feed("입니다.", 0.1)
    assert buf.committed_lines == ["늘릴 계획입니다."]


def test_english_still_gets_a_space_when_the_api_omits_one():
    buf = inc()
    buf.feed("hello", 0.0)
    buf.feed("world", 0.1)
    assert buf.pending_text == "hello world"


def test_mixed_korean_and_english_still_gets_a_space():
    buf = inc()
    buf.feed("연구개발", 0.0)
    buf.feed("R&D", 0.1)
    assert buf.pending_text == "연구개발 R&D"


def test_committed_lines_are_stripped():
    buf = inc()
    buf.feed("  spaced out.  ", 0.0)
    assert buf.committed_lines == ["spaced out."]


def test_whitespace_only_fragment_does_not_create_a_line():
    buf = inc()
    buf.feed("   ", 0.0)
    buf.tick(5.0)
    assert buf.committed_lines == []
    assert buf.is_empty


# --- clearing on silence --------------------------------------------------


def test_clear_after_silence_is_two_seconds_by_default():
    from livecap.caption_buffer import CLEAR_AFTER_SECONDS

    assert CLEAR_AFTER_SECONDS == 2.0
    assert CaptionBuffer().clear_after_seconds == 2.0


def test_captions_are_cleared_after_the_silence_window():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Hello there.", 0.0)
    assert buf.committed_lines == ["Hello there."]
    assert buf.tick(1.5) is False  # under two seconds
    assert not buf.is_empty
    assert buf.tick(2.0) is True
    assert buf.is_empty
    assert buf.committed_lines == []


def test_clear_also_applies_to_policy_b():
    buf = CaptionBuffer(policy=Policy.UTTERANCE, clear_after_seconds=2.0)
    buf.feed("one utterance", 0.0)
    assert buf.tick(1.9) is False
    assert buf.tick(2.1) is True
    assert buf.is_empty


def test_clear_removes_uncommitted_pending_too():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("no terminator", 0.0)
    buf.tick(1.3)  # the 1.2 s idle commit
    assert buf.committed_lines == ["no terminator"]
    # The window runs from when the line reached the screen (1.3 s), not from
    # the fragment that produced it.
    assert buf.tick(2.5) is False
    assert buf.tick(3.4) is True
    assert buf.is_empty


def test_silence_timer_resets_when_speech_resumes():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("First part.", 0.0)
    buf.feed("Second part.", 1.5)  # speech resumes before the clear
    assert buf.tick(2.5) is False  # only 1.0 s since the last fragment
    assert buf.committed_lines == ["First part.", "Second part."]
    assert buf.tick(3.6) is True
    assert buf.is_empty


def test_clear_does_not_fire_repeatedly_once_empty():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Hello.", 0.0)
    assert buf.tick(3.0) is True
    assert buf.tick(4.0) is False
    assert buf.tick(99.0) is False


def test_zero_disables_clearing():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Stays put.", 0.0)
    assert buf.tick(600.0) is False
    assert buf.committed_lines == ["Stays put."]


def test_text_arriving_after_a_clear_starts_a_fresh_line():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Old sentence.", 0.0)
    buf.tick(2.5)
    buf.feed("New sentence.", 5.0)
    assert buf.committed_lines == ["New sentence."]


def test_clear_setting_can_be_changed_at_runtime():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Hello.", 0.0)
    assert buf.tick(10.0) is False
    buf.clear_after_seconds = 2.0
    assert buf.tick(11.0) is True
    assert buf.is_empty


# --- pacing a burst of settled lines --------------------------------------
#
# The model translates a whole utterance at a time, so several sentences can
# settle inside one 100 ms display tick. Qt coalesces the repaints, so without
# pacing the reader only ever sees the last pair and the rest flash past.

BURST = [
    "One sentence.",
    "Two sentences.",
    "Three sentences.",
    "Four sentences.",
    "Five sentences.",
]


def burst_into(buf: CaptionBuffer, start: float = 0.0, step: float = 0.02) -> float:
    """Feed BURST far faster than one repaint. Returns the last timestamp."""
    now = start
    for text in BURST:
        now += step
        buf.feed(text, now)
    return now


def display_states(buf: CaptionBuffer, start: float, ticks: int = 40) -> list[list[str]]:
    """Every distinct display state the 100 ms tick produces."""
    states = [buf.committed_lines]
    for i in range(1, ticks):
        buf.tick(start + i * 0.1)
        if buf.committed_lines and buf.committed_lines != states[-1]:
            states.append(buf.committed_lines)
    return states


def test_a_burst_no_longer_flashes_past_within_one_repaint():
    """The measured regression: 5 sentences at once used to show only 2.

    Qt coalesces repaints, so before pacing the reader saw the final pair and
    the other three never appeared on screen at all.
    """
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    now = burst_into(buf)
    shown = {line for state in display_states(buf, now) for line in state}
    assert len(shown) == 4, "was 2 of 5 before the release was paced"


def test_the_display_advances_one_line_at_a_time():
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    now = burst_into(buf)
    states = display_states(buf, now)
    assert len(states) > 1
    for before, after in zip(states, states[1:], strict=False):
        assert len(set(after) - set(before)) == 1, f"{before} jumped to {after}"


def test_no_line_is_replaced_before_its_hold_elapses():
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    now = burst_into(buf)
    changed_at = [now]
    previous = buf.committed_lines
    for i in range(1, 40):
        t = now + i * 0.1
        buf.tick(t)
        if buf.committed_lines != previous:
            changed_at.append(t)
            previous = buf.committed_lines
    gaps = [b - a for a, b in zip(changed_at, changed_at[1:], strict=False)]
    assert gaps, "the queue never drained"
    assert min(gaps) >= 0.7 - 1e-9


def test_a_single_sentence_is_never_delayed():
    """Pacing must not add latency to the ordinary one-line case."""
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    buf.feed("Just the one.", 0.0)
    assert buf.committed_lines == ["Just the one."]
    assert buf.queued_lines == []
    assert buf.output_complete is True


def test_a_line_is_not_replaced_before_its_hold_elapses():
    buf = inc(max_lines=1, line_hold_seconds=0.7)
    buf.feed("First.", 0.0)
    buf.feed("Second.", 0.1)
    assert buf.committed_lines == ["First."]  # Second is queued
    assert buf.tick(0.5) is False
    assert buf.committed_lines == ["First."]
    assert buf.tick(0.9) is True
    assert buf.committed_lines == ["Second."]


def test_pacing_never_lets_the_display_fall_further_behind():
    """More than max_lines waiting means catching up, not queueing for ever.

    This is the bound that keeps the captions near the audio: the display can
    trail by at most max_lines * line_hold_seconds, 1.4 s at the defaults. A
    burst longer than that loses its oldest lines from the screen, which is why
    the transcript is written from a separate path.
    """
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    for i in range(20):
        buf.feed(f"Sentence {i}.", i * 0.02)
    assert len(buf.queued_lines) <= buf.max_lines


def test_zero_hold_shows_everything_at_once():
    buf = inc(max_lines=2, line_hold_seconds=0.0)
    burst_into(buf)
    assert buf.queued_lines == []
    assert buf.committed_lines == ["Four sentences.", "Five sentences."]


def test_the_transcript_keeps_every_line_of_a_burst():
    """Pacing is a display concern; recording must not lose anything."""
    buf = inc(max_lines=2)
    burst_into(buf)
    assert buf.drain_new_commits() == BURST


def test_unsettled_text_is_withheld_while_the_queue_drains():
    """Showing live text under lines still catching up would jumble the order."""
    buf = inc(max_lines=2)
    burst_into(buf)
    buf.feed(" still being said", 1.0)
    assert buf.pending_text == "still being said"
    assert all(settled for _t, settled in buf.display_lines())
    assert buf.output_complete is False


def test_output_complete_once_the_queue_has_drained():
    buf = inc(max_lines=2, line_hold_seconds=0.7)
    now = burst_into(buf)
    assert buf.output_complete is False
    for i in range(1, 40):
        buf.tick(now + i * 0.1)
    assert buf.output_complete is True
    assert buf.queued_lines == []


def test_a_reconnect_continues_a_line_that_is_still_queued():
    """The line to continue may be waiting its turn rather than on screen."""
    buf = inc(max_lines=1)
    buf.feed("the quarterly numbers.", 0.0)
    buf.feed("A second line.", 0.1)  # queued, not displayed
    assert buf.queued_lines == ["A second line."]

    buf.note_reconnect()
    buf.feed(" continued from there.", 1.0)
    # Merged rather than started afresh, and settled again by the full stop.
    assert buf.committed_lines == ["A second line. continued from there."]
    assert buf.drain_new_commits() == [
        "the quarterly numbers.",
        "A second line. continued from there.",
    ]


# --- clearing: drained output plus a quiet input ---------------------------


def test_the_clear_window_starts_when_the_last_line_reaches_the_screen():
    """Not when the fragment arrived — a queued burst is still being read."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0, max_lines=2)
    now = burst_into(buf)

    drained_at = cleared_at = None
    for i in range(1, 80):
        t = now + i * 0.1
        buf.tick(t)
        if drained_at is None and buf.output_complete and not buf.is_empty:
            drained_at = t
        if buf.is_empty:
            cleared_at = t
            break

    assert drained_at is not None, "the queue never drained"
    assert cleared_at is not None, "the captions never cleared"
    # Two seconds after the backlog finished, not two seconds after the burst.
    assert cleared_at - drained_at >= 2.0 - 1e-9
    assert cleared_at - now > 3.0


def test_speech_holds_the_captions_even_with_no_fragments():
    """The model can stop sending mid-sentence; captions must not vanish then."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Half of what was said.", 0.0)
    for i in range(1, 60):  # six seconds of somebody talking
        assert buf.tick(i * 0.1, speaking=True) is False
    assert buf.committed_lines == ["Half of what was said."]


def test_captions_clear_two_seconds_after_the_room_goes_quiet():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("All of what was said.", 0.0)
    for i in range(1, 50):  # still talking for five seconds
        buf.tick(i * 0.1, speaking=True)
    assert not buf.is_empty

    buf.tick(5.0, speaking=False)  # quiet starts here
    assert buf.tick(6.9, speaking=False) is False
    assert buf.tick(7.1, speaking=False) is True
    assert buf.is_empty


def test_an_unknown_input_level_falls_back_to_fragment_timing():
    """With no capture open there is no level to go on, so behave as before."""
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Hello there.", 0.0)
    assert buf.tick(1.5, speaking=None) is False
    assert buf.tick(2.1, speaking=None) is True


def test_captions_never_linger_past_the_ceiling():
    """A noisy room must not pin stale captions on screen for ever."""
    from livecap.caption_buffer import MAX_CLEAR_WAIT_SECONDS

    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Said once, long ago.", 0.0)
    assert buf.tick(MAX_CLEAR_WAIT_SECONDS - 0.1, speaking=True) is False
    assert buf.tick(MAX_CLEAR_WAIT_SECONDS + 0.1, speaking=True) is True
    assert buf.is_empty


def test_the_ceiling_does_not_override_a_disabled_clear():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Stays put.", 0.0)
    assert buf.tick(600.0, speaking=False) is False
    assert buf.committed_lines == ["Stays put."]


# --- complete phrases only ------------------------------------------------
#
# The model streams its translation a fragment at a time and rewrites as it
# goes. Drawing every fragment is what makes the captions visibly churn.


def phrases(**kw) -> CaptionBuffer:
    kw.setdefault("clear_after_seconds", 0.0)
    return CaptionBuffer(policy=Policy.INCREMENTAL, complete_phrases_only=True, **kw)


def test_unsettled_text_is_never_drawn():
    buf = phrases()
    buf.feed("The quarterly", 0.0)
    buf.feed(" numbers are", 1.0)
    assert buf.pending_text  # it is buffered...
    assert buf.display_lines() == []  # ...but nothing half-written is shown


def test_the_default_still_shows_unsettled_text_dimmed():
    """The mode is opt-in; the old behaviour must survive untouched."""
    buf = inc()
    buf.feed("The quarterly", 0.0)
    assert buf.display_lines() == [("The quarterly", False)]


def test_a_finished_sentence_appears():
    buf = phrases()
    buf.feed("Revenue is up fourteen percent.", 0.0)
    assert buf.display_lines() == [("Revenue is up fourteen percent.", True)]


def test_an_abbreviation_does_not_end_a_sentence():
    """"Dr." would otherwise settle a two-word fragment as a whole sentence."""
    buf = phrases()
    buf.feed("Dr.", 0.0)
    assert buf.committed_lines == []
    assert buf.pending_text == "Dr."


def test_the_abbreviation_guard_only_applies_to_this_mode():
    buf = inc()
    buf.feed("Dr.", 0.0)
    assert buf.committed_lines == ["Dr."]


def test_a_short_sentence_still_settles_on_the_idle_timer():
    """The length guard delays a short phrase, it must not strand it."""
    buf = phrases()
    buf.feed("Yes.", 0.0)
    assert buf.committed_lines == []
    assert buf.tick(1.3) is True
    assert buf.committed_lines == ["Yes."]


def test_turn_complete_settles_whatever_is_buffered():
    """The one boundary the API reports reliably, per spikes/RESULTS.md."""
    buf = phrases()
    buf.feed("A phrase with no full stop", 0.0)
    assert buf.display_lines() == []
    assert buf.note_turn_complete(1.0) is True
    assert buf.display_lines() == [("A phrase with no full stop", True)]


def test_turn_complete_with_nothing_buffered_changes_nothing():
    buf = phrases()
    assert buf.note_turn_complete(1.0) is False
    assert buf.is_empty


def test_turn_complete_beats_the_abbreviation_guard():
    """A genuinely short utterance must not wait for the idle timer."""
    buf = phrases()
    buf.feed("Yes.", 0.0)
    assert buf.committed_lines == []
    buf.note_turn_complete(0.2)
    assert buf.committed_lines == ["Yes."]


def test_the_transcript_gets_the_text_either_way():
    """Withholding is a display concern; recording must not change."""
    buf = phrases()
    buf.feed("A phrase with no full stop", 0.0)
    buf.note_turn_complete(1.0)
    assert buf.drain_new_commits() == ["A phrase with no full stop"]


def test_the_mode_can_be_switched_at_runtime():
    """The 9 key flips it mid-session, so it cannot be construction-only."""
    buf = inc()
    buf.feed("Half a thought", 0.0)
    assert buf.display_lines() == [("Half a thought", False)]
    buf.complete_phrases_only = True
    assert buf.display_lines() == []
    buf.complete_phrases_only = False
    assert buf.display_lines() == [("Half a thought", False)]


# --- invariants under a randomised session --------------------------------


@pytest.mark.parametrize("complete_phrases", [False, True])
def test_the_invariants_survive_a_long_randomised_session(complete_phrases):
    """Fuzz every entry point together and check the invariants at each step.

    The interesting bugs in this module have all come from two operations
    meeting — a reconnect during a paced drain, a clear with text still pending,
    max_lines shrinking with a queue. Driving them at random covers the
    combinations no hand-written case thinks to try.
    """
    import random

    fragments = [
        " Good morning.", " Revenue", " is up", " fourteen percent.", " Dr.",
        " Smith agrees.", " 영업이익률", " 입니다.", " the", " the", "",
    ]
    rng = random.Random(20260805)
    buf = CaptionBuffer(
        policy=Policy.INCREMENTAL,
        clear_after_seconds=2.0,
        max_lines=2,
        complete_phrases_only=complete_phrases,
    )
    now = 0.0
    for step in range(4000):
        now += rng.choice([0.02, 0.1, 0.4, 1.0, 3.0])
        roll = rng.random()
        if roll < 0.55:
            buf.feed(rng.choice(fragments), now)
        elif roll < 0.60:
            buf.note_turn_complete(now)
        elif roll < 0.63:
            buf.note_reconnect()
        elif roll < 0.65:
            buf.clear()
        elif roll < 0.67:
            buf.set_max_lines(rng.choice([1, 2, 3, 4]))
        else:
            buf.tick(now, speaking=rng.choice([True, False, None]))
        buf.drain_new_commits()

        assert len(buf.committed_lines) <= buf.max_lines, step
        assert len(buf.display_lines()) <= buf.max_lines, step
        assert not (buf.is_empty and buf.display_lines()), step
        if complete_phrases:
            assert all(settled for _t, settled in buf.display_lines()), step
        # Shrinking max_lines leaves the queue briefly over the bound; the tick
        # that always follows in the application is what brings it back.
        buf.tick(now)
        assert len(buf.queued_lines) <= buf.max_lines, step


def test_nothing_accumulates_when_the_commits_are_never_drained():
    """The caller may have no transcript open, but the buffer still has to be
    bounded — it outlives a session."""
    buf = inc()
    for i in range(500):
        buf.feed(f"Sentence {i} here.", i * 0.5)
        buf.tick(i * 0.5)
    assert len(buf.committed_lines) <= buf.max_lines
    assert len(buf.queued_lines) <= buf.max_lines
    # _new_commits is the one thing that legitimately grows until drained —
    # which is exactly why the window drains it whether or not it records.
    assert len(buf.drain_new_commits()) == 500


# --- switching policy at runtime ------------------------------------------


def test_policy_can_be_changed_at_runtime():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("no terminator", 0.0)
    assert buf.committed_lines == []  # incremental waits for punctuation or idle
    buf.set_policy(Policy.UTTERANCE)
    buf.feed(" and more", 1.0)
    assert buf.committed_lines == ["no terminator and more"]


def test_switching_to_auto_restarts_detection():
    buf = CaptionBuffer(policy=Policy.UTTERANCE, clear_after_seconds=0.0)
    assert buf.effective_policy is Policy.UTTERANCE
    buf.set_policy(Policy.AUTO)
    assert buf.effective_policy is Policy.INCREMENTAL  # until enough samples


def test_setting_the_same_policy_is_a_noop():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=0.0)
    buf.feed("Kept.", 0.0)
    buf.set_policy(Policy.INCREMENTAL)
    assert buf.committed_lines == ["Kept."]
