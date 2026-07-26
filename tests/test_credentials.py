"""The encrypted credential store.

The passphrase is a constant in the source, so this is obfuscation rather than
secrecy — but it must still round-trip correctly, keep plaintext off disk, and
survive a corrupt file without taking the app down with it.
"""

from __future__ import annotations

import json

import pytest

from livecap import credentials

KEY = "GEMINI_API_KEY"
VALUE = "AIzaSyD-not-a-real-key-000111222"


def test_round_trip():
    assert credentials.put(KEY, VALUE) is True
    assert credentials.get(KEY) == VALUE


def test_nothing_stored_reads_as_none():
    assert credentials.get(KEY) is None


def test_the_file_holds_no_plaintext():
    credentials.put(KEY, VALUE)
    blob = credentials.store_path().read_bytes()
    assert VALUE.encode() not in blob
    assert KEY.encode() not in blob


def test_the_file_is_readable_only_by_its_owner():
    credentials.put(KEY, VALUE)
    mode = credentials.store_path().stat().st_mode & 0o777
    assert mode == 0o600, f"expected 0600, found {oct(mode)}"


def test_value_is_stripped():
    credentials.put(KEY, "  AIzaPadded  ")
    assert credentials.get(KEY) == "AIzaPadded"


def test_an_empty_value_reads_as_none():
    credentials.put(KEY, "   ")
    assert credentials.get(KEY) is None


def test_a_second_entry_does_not_displace_the_first():
    credentials.put(KEY, VALUE)
    credentials.put("OTHER", "second")
    assert credentials.get(KEY) == VALUE
    assert credentials.get("OTHER") == "second"


def test_remove_deletes_the_file_when_it_empties():
    credentials.put(KEY, VALUE)
    assert credentials.remove(KEY) is True
    assert credentials.get(KEY) is None
    assert not credentials.store_path().exists()


def test_remove_keeps_other_entries():
    credentials.put(KEY, VALUE)
    credentials.put("OTHER", "second")
    credentials.remove(KEY)
    assert credentials.get("OTHER") == "second"
    assert credentials.store_path().exists()


def test_removing_something_absent_is_not_an_error():
    assert credentials.remove("NEVER_STORED") is True


# --- robustness -----------------------------------------------------------


def test_a_corrupt_file_reads_as_empty_rather_than_raising():
    """A truncated or tampered store must not stop the app from starting."""
    path = credentials.store_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(b"this is not a Fernet token")
    assert credentials.load_all() == {}
    assert credentials.get(KEY) is None


def test_a_file_encrypted_with_another_passphrase_reads_as_empty(monkeypatch):
    """Someone else's store must not be silently half-read."""
    credentials.put(KEY, VALUE)
    monkeypatch.setattr(credentials, "_PASSPHRASE", b"a-different-passphrase")
    assert credentials.load_all() == {}


def test_valid_ciphertext_holding_non_json_reads_as_empty():
    path = credentials.store_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(credentials._cipher().encrypt(b"not json at all"))
    assert credentials.load_all() == {}


def test_valid_json_that_is_not_an_object_reads_as_empty():
    path = credentials.store_path()
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_bytes(credentials._cipher().encrypt(json.dumps(["a", "list"]).encode()))
    assert credentials.load_all() == {}


def test_an_unwritable_directory_is_reported_not_raised(monkeypatch, tmp_path):
    blocker = tmp_path / "blocked"
    blocker.write_text("i am a file, not a directory")
    monkeypatch.setattr(credentials, "store_path", lambda: blocker / "sub" / "credentials.enc")
    assert credentials.put(KEY, VALUE) is False


def test_a_write_leaves_no_temporary_file_behind():
    credentials.put(KEY, VALUE)
    leftovers = list(credentials.store_path().parent.glob("*.tmp"))
    assert leftovers == []


@pytest.mark.parametrize("value", ["AIza" + "x" * 200, "키-with-unicode-값", "with spaces"])
def test_awkward_values_survive_the_round_trip(value):
    credentials.put(KEY, value)
    assert credentials.get(KEY) == value
