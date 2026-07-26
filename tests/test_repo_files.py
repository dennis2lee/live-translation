"""Files the documentation tells people to use must actually exist.

`.env.example` went missing once while README, INSTALL and run.sh all told the
user to copy it. Nothing failed — the instruction simply did not work.
"""

from __future__ import annotations

from pathlib import Path

import pytest

ROOT = Path(__file__).resolve().parents[1]


def test_no_dotenv_example_is_shipped():
    """The key is entered in Preferences now; an example file would mislead."""
    assert not (ROOT / ".env.example").exists()


def test_nothing_still_tells_the_user_to_copy_a_dotenv():
    for name in ("README.md", "INSTALL.md", "run.sh"):
        text = (ROOT / name).read_text(encoding="utf-8")
        assert "cp .env.example" not in text, f"{name} still points at .env.example"


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


def _claims_mit(text: str) -> bool:
    """"MIT" as a licence name, not the MIT inside "LIMITED"."""
    import re

    return re.search(r"\bMIT\b", text) is not None


def test_the_licence_is_proprietary_not_mit():
    """The project is not open source; nothing may claim otherwise."""
    licence = (ROOT / "LICENSE").read_text(encoding="utf-8")
    assert "All rights reserved" in licence
    assert not _claims_mit(licence)

    for name in ("README.md", "pyproject.toml", "packaging/LiveCap.spec"):
        text = (ROOT / name).read_text(encoding="utf-8")
        assert not _claims_mit(text), f"{name} still claims MIT"


def test_the_app_does_not_claim_an_open_source_licence():
    source = (ROOT / "src" / "livecap" / "ui" / "main_window.py").read_text(encoding="utf-8")
    assert not _claims_mit(source)
    assert "All rights reserved" in source
