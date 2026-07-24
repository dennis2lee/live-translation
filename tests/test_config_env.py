"""API 키 조회 경로 검증 — 환경변수 → .env → 키체인."""

from __future__ import annotations

import os

os.environ.setdefault("QT_QPA_PLATFORM", "offscreen")

import pytest  # noqa: E402

from livecap import config  # noqa: E402


@pytest.fixture(autouse=True)
def clean_env(monkeypatch, tmp_path):
    for var in (*config.API_KEY_VARS, "LIVECAP_ENV_FILE"):
        monkeypatch.delenv(var, raising=False)
    # 실제 키체인을 건드리지 않는다.
    monkeypatch.setattr(config, "load_api_key", config.load_api_key)
    yield


def write_env(tmp_path, monkeypatch, body: str):
    path = tmp_path / ".env"
    path.write_text(body, encoding="utf-8")
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(path))
    return path


def no_keychain(monkeypatch):
    """키체인 조회가 결과에 섞이지 않도록 막는다."""
    import sys
    import types

    fake = types.ModuleType("keyring")
    fake.get_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    fake.set_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    fake.delete_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)


# --- 파서 -----------------------------------------------------------------


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
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=abc # 발급받은 키\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "abc"


def test_missing_file_is_not_an_error(tmp_path, monkeypatch):
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(tmp_path / "nope.env"))
    assert config.read_dotenv() == {}


def test_value_containing_equals_is_preserved(tmp_path, monkeypatch):
    path = write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=abc=def==\n")
    assert config.read_dotenv(path)["GEMINI_API_KEY"] == "abc=def=="


# --- 우선순위 --------------------------------------------------------------


def test_env_var_wins_over_dotenv(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=from-dotenv\n")
    monkeypatch.setenv("GEMINI_API_KEY", "from-environ")
    assert config.load_api_key() == "from-environ"


def test_dotenv_is_used_when_no_env_var(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=from-dotenv\n")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "from-dotenv"


def test_google_api_key_is_accepted_as_a_fallback(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GOOGLE_API_KEY=from-google-var\n")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "from-google-var"


def test_keychain_is_used_when_dotenv_has_no_key(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "LIVECAP_DEBUG=1\n")
    import sys
    import types

    fake = types.ModuleType("keyring")
    fake.get_password = lambda *_a, **_k: "from-keychain"  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)
    assert config.load_api_key() == "from-keychain"


def test_unedited_placeholder_is_treated_as_missing(tmp_path, monkeypatch):
    """.env.example copied but never filled in."""
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=your-key-here\n")
    no_keychain(monkeypatch)
    assert config.load_api_key() is None


def test_empty_value_is_treated_as_missing(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=\n")
    no_keychain(monkeypatch)
    assert config.load_api_key() is None


def test_whitespace_around_the_key_is_stripped(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=   AIzaTEST   \n")
    no_keychain(monkeypatch)
    assert config.load_api_key() == "AIzaTEST"


# --- environ 주입 ----------------------------------------------------------


def test_apply_dotenv_fills_environ_without_overwriting(tmp_path, monkeypatch):
    write_env(tmp_path, monkeypatch, "GEMINI_API_KEY=from-dotenv\nLIVECAP_DEBUG=1\n")
    monkeypatch.setenv("GEMINI_API_KEY", "already-set")
    applied = config.apply_dotenv_to_environ()
    assert "LIVECAP_DEBUG" in applied
    assert "GEMINI_API_KEY" not in applied  # 기존 환경변수를 덮어쓰지 않는다
    assert os.environ["GEMINI_API_KEY"] == "already-set"
    assert os.environ["LIVECAP_DEBUG"] == "1"


def test_api_key_is_never_written_to_qsettings():
    """Settings 스키마에 키가 들어갈 자리가 없어야 한다."""
    from dataclasses import fields

    names = {f.name for f in fields(config.Settings)}
    assert not any("key" in n.lower() for n in names)
