"""API key lookup: environment variable, encrypted store, then the Keychain.

`.env` is no longer read at run time; it survives only as a one-time
migration into the encrypted store.
"""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402

from livecap import config, credentials  # noqa: E402


@pytest.fixture(autouse=True)
def clean_env(monkeypatch, tmp_path):
    for var in (*config.API_KEY_VARS, "LIVECAP_ENV_FILE"):
        monkeypatch.delenv(var, raising=False)
    # Never touch the real Keychain.
    monkeypatch.setattr(config, "load_api_key", config.load_api_key)
    yield


def write_env(tmp_path, monkeypatch, body: str):
    path = tmp_path / ".env"
    path.write_text(body, encoding="utf-8")
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(path))
    return path


def no_keychain(monkeypatch):
    """Stop the Keychain from contributing to the result."""
    import sys
    import types

    fake = types.ModuleType("keyring")
    fake.get_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    fake.set_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    fake.delete_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)


# --- parser ---------------------------------------------------------------


def test_parses_simple_assignment(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=AIzaTEST123\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "AIzaTEST123"


def test_ignores_comments_and_blank_lines(tmp_path, monkeypatch):
    path = write_env(
        tmp_path,
        monkeypatch,
        "# a comment\n\n  \nGEMINI_API_KEY=abc\n# GOOGLE_API_KEY=should-not-load\n",
    )
    parsed = config.read_dotenv(path)
    assert parsed == {"GEMINI_API_KEY": "abc"}


def test_strips_quotes(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY='quoted'\nOTHER=\"double\"\n")
    parsed = config.read_dotenv(path)
    assert parsed["GEMINI_API_KEY"] == "quoted"
    assert parsed["OTHER"] == "double"


def test_accepts_export_prefix(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "export GEMINI_API_KEY=abc\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "abc"


def test_strips_trailing_inline_comment(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=abc # the issued key\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "abc"


def test_quoted_value_keeps_a_hash_inside_the_quotes(tmp_path, monkeypatch):
    """` #` ends a comment only outside quotes, or a key containing it is truncated."""
    path = write_env(tmp_path, monkeypatch, 'GEMINI_API_KEY="AIza-with #-inside"\n')
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "AIza-with #-inside"


def test_quoted_value_still_drops_a_comment_after_the_closing_quote(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, 'GEMINI_API_KEY="AIzaABC"  # issued 2026-07\n')
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "AIzaABC"


def test_unterminated_quote_still_yields_the_value(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, 'GEMINI_API_KEY="AIzaABC\n')
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "AIzaABC"


def test_missing_file_is_not_an_error(tmp_path, monkeypatch):
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(tmp_path / "nope.env"))
    assert config.read_dotenv() == {}


def test_value_containing_equals_is_preserved(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=abc=def==\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "abc=def=="


# --- where .env is looked for ---------------------------------------------


def test_source_checkout_looks_in_the_repository_root(monkeypatch):
    monkeypatch.setattr(config.sys, "frozen", False, raising=False)
    paths = config.dotenv_candidates()
    assert paths[-1] == config.Path(config.__file__).resolve().parents[2] / ".env"


def test_a_bundle_falls_back_to_application_support(monkeypatch, tmp_path):
    """The repository root does not exist inside a .app, so it must not be used."""
    fake_exe = tmp_path / "LiveCap.app" / "Contents" / "MacOS" / "LiveCap"
    fake_exe.parent.mkdir(parents=True)
    fake_exe.touch()
    monkeypatch.setattr(config.sys, "frozen", True, raising=False)
    monkeypatch.setattr(config.sys, "executable", str(fake_exe))

    paths = config.dotenv_candidates()
    assert config.APP_SUPPORT_DIR / ".env" in paths
    assert tmp_path / ".env" in paths  # beside the .app
    assert not any("src" in p.parts for p in paths)


def test_env_file_override_wins_everywhere(monkeypatch, tmp_path):
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(tmp_path / "custom.env"))
    assert config.dotenv_candidates() == [tmp_path / "custom.env"]


def test_dotenv_path_returns_the_first_existing_candidate(monkeypatch, tmp_path):
    monkeypatch.chdir(tmp_path)
    (tmp_path / ".env").write_text("GEMINI_API_KEY=cwd-wins\n", encoding="utf-8")
    monkeypatch.setattr(config.sys, "frozen", False, raising=False)
    assert config.dotenv_path() == tmp_path / ".env"


# --- precedence -----------------------------------------------------------
#
# `.env` is no longer a source. The order is environment variable, then the
# encrypted store, then the Keychain.


def test_env_var_wins_over_the_encrypted_store(monkeypatch):
    credentials.put("GEMINI_API_KEY", "AIzaFromStore")
    monkeypatch.setenv("GEMINI_API_KEY", "AIzaFromEnviron")
    assert config.load_api_key() == "AIzaFromEnviron"


def test_the_encrypted_store_is_used_when_no_env_var(monkeypatch):
    credentials.put("GEMINI_API_KEY", "AIzaFromStore")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "AIzaFromStore"


def test_google_api_key_variable_is_accepted_as_a_fallback(monkeypatch):
    monkeypatch.setenv("GOOGLE_API_KEY", "AIzaFromGoogleVar")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "AIzaFromGoogleVar"


def test_dotenv_is_no_longer_read_at_run_time(tmp_path, monkeypatch):
    """A leftover .env must not quietly supply a key any more."""
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=AIzaFromDotenv\n")
    no_keychain(monkeypatch)
    assert config.load_api_key() is None


def test_keychain_is_used_when_the_store_is_empty(monkeypatch):
    import sys
    import types

    fake = types.ModuleType("keyring")
    fake.get_password = lambda *_a, **_k: "from-keychain"  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)
    assert config.load_api_key() == "from-keychain"


def test_unedited_placeholder_is_treated_as_missing(monkeypatch):
    """.env.example copied into the store without being filled in."""
    credentials.put("GEMINI_API_KEY", "your-key-here")
    no_keychain(monkeypatch)
    assert config.load_api_key() is None


def test_a_real_key_containing_a_placeholder_word_is_still_accepted(monkeypatch):
    """Key bodies are random; one may contain "here" or "xxx" by chance."""
    credentials.put("GEMINI_API_KEY", "AIzaSyDxxxHEREabc123")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "AIzaSyDxxxHEREabc123"


def test_empty_value_is_treated_as_missing(monkeypatch):
    credentials.put("GEMINI_API_KEY", "")
    no_keychain(monkeypatch)
    assert config.load_api_key() is None


def test_whitespace_around_the_key_is_stripped(monkeypatch):
    credentials.put("GEMINI_API_KEY", "   AIzaTEST   ")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "AIzaTEST"


# --- reporting where the key came from ------------------------------------


def test_api_key_source_names_the_environment(monkeypatch):
    monkeypatch.setenv("GEMINI_API_KEY", "AIzaFromEnviron")
    assert "environment variable" in config.api_key_source()


def test_api_key_source_names_the_store(monkeypatch):
    credentials.put("GEMINI_API_KEY", "AIzaFromStore")
    no_keychain(monkeypatch)
    assert config.api_key_source() == "encrypted store"


def test_api_key_source_is_blank_when_nothing_is_stored(monkeypatch):
    no_keychain(monkeypatch)
    assert config.api_key_source() == ""


# --- one-time migration off .env ------------------------------------------


def test_migration_imports_a_key_from_a_leftover_dotenv(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=AIzaLegacyKey\n")
    no_keychain(monkeypatch)
    assert config.migrate_dotenv_once() == "GEMINI_API_KEY"
    assert config.load_api_key() == "AIzaLegacyKey"


def test_migration_does_not_overwrite_a_stored_key(tmp_path, monkeypatch):
    credentials.put("GEMINI_API_KEY", "AIzaAlreadyStored")
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=AIzaLegacyKey\n")
    no_keychain(monkeypatch)
    assert config.migrate_dotenv_once() is None
    assert config.load_api_key() == "AIzaAlreadyStored"


def test_migration_ignores_an_unedited_placeholder(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=your-key-here\n")
    no_keychain(monkeypatch)
    assert config.migrate_dotenv_once() is None
    assert config.load_api_key() is None


def test_migration_is_a_noop_without_a_dotenv(tmp_path, monkeypatch):
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(tmp_path / "absent.env"))
    no_keychain(monkeypatch)
    assert config.migrate_dotenv_once() is None


# --- saving ---------------------------------------------------------------


def test_saving_writes_the_encrypted_store(monkeypatch):
    no_keychain(monkeypatch)
    assert config.save_api_key("AIzaTypedIn") is True
    assert config.load_api_key() == "AIzaTypedIn"


def test_saving_does_not_write_plaintext_to_disk(monkeypatch):
    no_keychain(monkeypatch)
    config.save_api_key("AIzaSecretValue")
    blob = credentials.store_path().read_bytes()
    assert b"AIzaSecretValue" not in blob


def test_clearing_removes_the_stored_key(monkeypatch):
    no_keychain(monkeypatch)
    config.save_api_key("AIzaTypedIn")
    config.clear_api_key()
    assert config.load_api_key() is None


def test_stored_api_key_ignores_the_environment(monkeypatch):
    """Preferences needs the value it can put back, not the one in force."""
    no_keychain(monkeypatch)
    monkeypatch.setenv("GEMINI_API_KEY", "AIzaFromTheEnvironment")
    credentials.put("GEMINI_API_KEY", "AIzaFromTheStore")
    assert config.load_api_key() == "AIzaFromTheEnvironment"
    assert config.stored_api_key() == "AIzaFromTheStore"


def test_restoring_puts_the_previous_key_back(monkeypatch):
    no_keychain(monkeypatch)
    config.save_api_key("AIzaTheOriginal")
    config.save_api_key("AIzaTypedByMistake")
    config.restore_stored_api_key("AIzaTheOriginal")
    assert config.load_api_key() == "AIzaTheOriginal"


def test_restoring_nothing_removes_the_key(monkeypatch):
    no_keychain(monkeypatch)
    assert config.stored_api_key() is None
    config.save_api_key("AIzaTypedByMistake")
    config.restore_stored_api_key(None)
    assert config.stored_api_key() is None


# --- vocabulary hints -----------------------------------------------------


def test_terms_are_split_one_per_line():
    assert config.parse_terms("first\nsecond\nthird") == ["first", "second", "third"]


def test_a_term_may_contain_a_comma():
    """Splitting on commas would cut an adaptation phrase in half."""
    assert config.parse_terms("revenue is up, year over year") == [
        "revenue is up, year over year"
    ]


def test_blank_lines_and_surrounding_space_are_dropped():
    assert config.parse_terms("\n  one  \n\n\t\ntwo\n") == ["one", "two"]


def test_duplicates_are_dropped_keeping_the_first_order():
    assert config.parse_terms("b\na\nb\nc\na") == ["b", "a", "c"]


def test_no_terms_is_an_empty_list():
    assert config.parse_terms("") == []
    assert config.parse_terms("   \n \n") == []


def test_the_term_count_is_capped():
    """They ride in the setup message of every reconnect."""
    many = "\n".join(f"term{i}" for i in range(config.MAX_VOCABULARY_TERMS + 50))
    assert len(config.parse_terms(many)) == config.MAX_VOCABULARY_TERMS


def test_clamping_stores_exactly_what_will_be_sent():
    from dataclasses import replace

    messy = replace(config.Settings(), vocabulary_source="  a  \n\nb\na\n")
    assert messy.clamped().vocabulary_source == "a\nb"


def test_vocabulary_round_trips_through_qsettings():
    """Newlines have to survive the plist, or every term merges into one."""
    from dataclasses import replace

    saved = replace(
        config.Settings(),
        vocabulary_source="영업이익률\n평우",
        vocabulary_captions="operating margin",
    )
    config.save(saved)
    try:
        loaded = config.load()
        assert loaded.source_terms == ["영업이익률", "평우"]
        assert loaded.caption_terms == ["operating margin"]
    finally:
        config.reset()


def test_the_two_lists_do_not_share_state():
    from dataclasses import replace

    s = replace(config.Settings(), vocabulary_source="only source")
    assert s.source_terms == ["only source"]
    assert s.caption_terms == []


# --- the caption timing settings ------------------------------------------


def test_the_new_timing_settings_round_trip_through_qsettings():
    from dataclasses import replace

    saved = replace(config.Settings(), clear_after_seconds=1.5, line_hold_seconds=1.2)
    config.save(saved)
    try:
        loaded = config.load()
        assert loaded.clear_after_seconds == 1.5
        assert loaded.line_hold_seconds == 1.2
    finally:
        config.reset()


def test_the_timing_settings_are_clamped_to_their_documented_ranges():
    from dataclasses import replace

    wild = replace(config.Settings(), clear_after_seconds=999.0, line_hold_seconds=-4.0)
    clamped = wild.clamped()
    assert clamped.clear_after_seconds == 30.0
    assert clamped.line_hold_seconds == 0.0


def test_the_clear_default_matches_the_buffer_default():
    from livecap.caption_buffer import CLEAR_AFTER_SECONDS, LINE_HOLD_SECONDS

    assert config.Settings().clear_after_seconds == CLEAR_AFTER_SECONDS
    assert config.Settings().line_hold_seconds == LINE_HOLD_SECONDS


def test_api_key_is_never_written_to_qsettings():
    """The Settings schema must have nowhere to put a key."""
    from dataclasses import fields

    names = {f.name for f in fields(config.Settings)}
    assert not any("key" in n.lower() for n in names)
