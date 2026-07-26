"""Files the documentation tells people to use must actually exist.

`.env.example` went missing once while README, INSTALL and run.sh all told the
user to copy it. Nothing failed — the instruction simply did not work.
"""

from __future__ import annotations

from pathlib import Path

import pytest

from livecap import config

ROOT = Path(__file__).resolve().parents[1]


def test_env_example_exists():
    assert (ROOT / ".env.example").is_file()


def test_env_example_contains_no_real_key():
    """This file is committed, so a real key here would be published."""
    parsed = config.read_dotenv(ROOT / ".env.example")
    for var in config.API_KEY_VARS:
        value = parsed.get(var, "")
        assert not value.startswith("AIza"), f"{var} looks like a real key"


def test_env_example_placeholder_counts_as_no_key(monkeypatch):
    """Copying the example without editing it must not look like a configured key."""
    monkeypatch.setenv("LIVECAP_ENV_FILE", str(ROOT / ".env.example"))
    for var in config.API_KEY_VARS:
        monkeypatch.delenv(var, raising=False)

    import sys
    import types

    fake = types.ModuleType("keyring")
    fake.get_password = lambda *_a, **_k: None  # type: ignore[attr-defined]
    monkeypatch.setitem(sys.modules, "keyring", fake)

    assert config.load_api_key() is None


def test_real_env_is_not_tracked_by_git():
    import subprocess

    tracked = subprocess.run(
        ["git", "ls-files", ".env"], cwd=ROOT, capture_output=True, text=True
    ).stdout
    assert tracked.strip() == ""


@pytest.mark.parametrize("name", ["README.md", "INSTALL.md", "LICENSE", "requirements.txt"])
def test_documented_files_exist(name):
    assert (ROOT / name).is_file()


def test_run_script_is_executable():
    run = ROOT / "run.sh"
    assert run.is_file()
    assert run.stat().st_mode & 0o111, "run.sh must be executable"


def test_version_is_consistent_between_package_and_pyproject():
    import tomllib

    from livecap import __version__

    with (ROOT / "pyproject.toml").open("rb") as fh:
        declared = tomllib.load(fh)["project"]["version"]
    assert declared == __version__


def test_author_is_consistent_across_package_license_and_pyproject():
    """One name in three places; they used to be able to drift apart."""
    import tomllib

    from livecap import __author__, __email__

    assert __author__ in (ROOT / "LICENSE").read_text(encoding="utf-8")
    assert __author__ in (ROOT / "README.md").read_text(encoding="utf-8")
    assert __email__ in (ROOT / "README.md").read_text(encoding="utf-8")

    with (ROOT / "pyproject.toml").open("rb") as fh:
        authors = tomllib.load(fh)["project"]["authors"]
    assert authors[0]["name"] == __author__
    assert authors[0]["email"] == __email__


def test_bundle_copyright_names_the_author():
    from livecap import __author__

    spec = (ROOT / "packaging" / "LiveCap.spec").read_text(encoding="utf-8")
    assert __author__ in spec
