from livecap.caption_buffer import CaptionBuffer, Policy


def inc(**kw) -> CaptionBuffer:
    # 무음 후 지우기는 기본 끔 — 커밋 동작만 따로 보기 위해서.
    kw.setdefault("clear_after_seconds", 0.0)
    return CaptionBuffer(policy=Policy.INCREMENTAL, **kw)


def utt(**kw) -> CaptionBuffer:
    kw.setdefault("clear_after_seconds", 0.0)
    return CaptionBuffer(policy=Policy.UTTERANCE, **kw)


# --- 공통 ------------------------------------------------------------------


def test_starts_empty_so_the_box_is_hidden():
    buf = inc()
    assert buf.is_empty
    assert buf.display_lines() == []


def test_duplicate_consecutive_fragments_are_ignored():
    buf = inc()
    assert buf.feed("hello", 0.0) is True
    assert buf.feed("hello", 0.1) is False
    assert buf.pending_text == "hello"


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


def test_display_lines_are_capped_and_keep_the_newest():
    buf = inc(max_lines=2)
    for i, t in enumerate(["a.", "b.", "c."]):
        buf.feed(t, float(i))
    # 앞부분을 잘라내고 최신 내용을 보여준다.
    assert [t for t, _ in buf.display_lines()] == ["b.", "c."]


def test_set_max_lines_preserves_recent_lines():
    buf = inc(max_lines=4)
    for i, t in enumerate(["a.", "b.", "c.", "d."]):
        buf.feed(t, float(i))
    buf.set_max_lines(2)
    assert buf.committed_lines == ["c.", "d."]


# --- 정책 A (점진) ---------------------------------------------------------


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
    assert buf.tick(0.5) is False  # 아직 유휴 시간 미달
    assert buf.committed_lines == []
    assert buf.tick(1.3) is True  # 1.2초 경과
    assert buf.committed_lines == ["no terminator here"]
    assert buf.pending_text == ""


def test_tick_is_noop_when_nothing_pending():
    buf = inc()
    assert buf.tick(99.0) is False


def test_policy_a_idle_timer_resets_on_each_fragment():
    buf = inc()
    buf.feed("a", 0.0)
    buf.feed("b", 1.0)
    assert buf.tick(1.5) is False  # 마지막 조각으로부터 0.5초뿐
    assert buf.tick(2.3) is True


# --- 정책 B (발화 단위) ----------------------------------------------------


def test_policy_b_commits_immediately_with_no_pending_stage():
    buf = utt()
    buf.feed("We're going to look at the Q3 numbers", 0.0)
    assert buf.committed_lines == ["We're going to look at the Q3 numbers"]
    assert buf.pending_text == ""


def test_policy_b_keeps_previous_line_during_silence():
    buf = utt()
    buf.feed("first utterance", 0.0)
    assert buf.tick(60.0) is False  # 시간이 흘러도 지우지 않는다
    assert buf.display_lines() == [("first utterance", True)]


# --- AUTO 판별 -------------------------------------------------------------


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


# --- 재연결 ---------------------------------------------------------------


def test_reconnect_continues_the_previous_line_in_policy_a():
    buf = inc()
    buf.feed("the quarterly numbers", 0.0)
    buf.tick(2.0)  # 확정됨
    assert buf.committed_lines == ["the quarterly numbers"]

    buf.note_reconnect()
    buf.feed("were better than expected.", 3.0)
    # 새 줄을 시작하지 않고 이어 붙인다.
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


# --- 이어 붙이기 규칙 ------------------------------------------------------


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
    """실측대로 한국어 조각도 선행 공백을 달고 온다."""
    buf = inc()
    for i, frag in enumerate([" 질문이", " 있으시면,", " 지금 말씀해 주세요."]):
        buf.feed(frag, float(i))
    assert buf.committed_lines == ["질문이 있으시면, 지금 말씀해 주세요."]


def test_no_space_is_inserted_inside_a_korean_word():
    """어절 중간에서 잘린 조각에 공백을 넣으면 안 된다."""
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


# --- 무음 후 지우기 --------------------------------------------------------


def test_clear_after_silence_is_two_seconds_by_default():
    from livecap.caption_buffer import CLEAR_AFTER_SECONDS

    assert CLEAR_AFTER_SECONDS == 3.0
    assert CaptionBuffer().clear_after_seconds == 3.0


def test_captions_are_cleared_after_the_silence_window():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("Hello there.", 0.0)
    assert buf.committed_lines == ["Hello there."]
    assert buf.tick(1.5) is False  # 아직 2초 안 됨
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
    buf.tick(1.3)  # 1.2초 유휴 커밋
    assert buf.committed_lines == ["no terminator"]
    buf.tick(2.5)
    assert buf.is_empty


def test_silence_timer_resets_when_speech_resumes():
    buf = CaptionBuffer(policy=Policy.INCREMENTAL, clear_after_seconds=2.0)
    buf.feed("First part.", 0.0)
    buf.feed("Second part.", 1.5)  # 지우기 전에 말이 이어짐
    assert buf.tick(2.5) is False  # 마지막 조각으로부터 1.0초뿐
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
