"""Shared fixtures.

Both of the places LiveCap persists things are outside the repository: the
encrypted credential store in Application Support, and the preferences plist
under ~/Library/Preferences. Without isolation a test run would read — and
overwrite — the key and the settings the user actually uses. Every test gets its
own empty copy of both instead.
"""

from __future__ import annotations

import pytest

from livecap import credentials


@pytest.fixture(autouse=True)
def isolated_credential_store(tmp_path, monkeypatch):
    """Point the encrypted store at a per-test file. Applied to every test."""
    monkeypatch.setattr(
        credentials, "store_path", lambda: tmp_path / "credentials.enc"
    )
    yield tmp_path / "credentials.enc"


@pytest.fixture(autouse=True)
def isolated_qsettings(tmp_path, monkeypatch):
    """Point QSettings at a per-test file. Applied to every test.

    `config.reset()` calls `QSettings.clear()`, which without this would wipe the
    developer's own preferences for real.
    """
    from PyQt6.QtCore import QSettings

    from livecap import config

    path = str(tmp_path / "preferences.ini")
    monkeypatch.setattr(
        config, "_qsettings", lambda: QSettings(path, QSettings.Format.IniFormat)
    )
    yield path


@pytest.fixture(autouse=True)
def no_api_key_env(monkeypatch):
    """A key in the developer's environment must not change any test's result."""
    for name in ("GEMINI_API_KEY", "GOOGLE_API_KEY"):
        monkeypatch.delenv(name, raising=False)


@pytest.fixture(autouse=True)
def stub_keychain(monkeypatch):
    """Keep the real macOS Keychain out of the test run.

    Without this a test could read the developer's own key, and
    `clear_api_key()` would delete it for real. Tests that need particular
    Keychain behaviour install their own fake over this one.
    """
    import sys
    import types

    fake = types.ModuleType("keyring")
    stored: dict[tuple[str, str], str] = {}
    fake.get_password = lambda s, u: stored.get((s, u))  # type: ignore[attr-defined]
    fake.set_password = lambda s, u, v: stored.__setitem__((s, u), v)  # type: ignore[attr-defined]
    fake.delete_password = lambda s, u: stored.pop((s, u), None)  # type: ignore[attr-defined]
    fake.get_keyring = lambda: types.SimpleNamespace()  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)
