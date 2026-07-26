"""Encrypted credential storage.

Replaces the old `.env` file. The API key is entered in Preferences and written
here, encrypted, under the application-support directory.

**On the strength of this.** The passphrase is a constant in this source file,
so anyone holding the application can derive the key and read the file. That
makes this obfuscation, not secrecy: it stops a key being read by accident —
over someone's shoulder, in a backup, in a screen share — but it does not stop
anyone who wants it. The macOS Keychain, which is protected by the login
password and by the OS, is still supported and is the stronger option; see
`config.load_api_key` for the order they are consulted in.

The file is written 0600 and never logged.
"""

from __future__ import annotations

import base64
import contextlib
import hashlib
import json
import os
from pathlib import Path

from cryptography.fernet import Fernet, InvalidToken

from . import platform_support
from .util import logging as log

_log = log.get(__name__)

FILENAME = "credentials.enc"

# Deliberately fixed, and deliberately in the source — see the module docstring.
_PASSPHRASE = b"praykepc"
_SALT = b"livecap-credential-store-v1"
_ITERATIONS = 200_000


def store_path() -> Path:
    return platform_support.app_support_dir() / FILENAME


def _cipher() -> Fernet:
    """Fernet key derived from the passphrase. AES-128-CBC with an HMAC."""
    derived = hashlib.pbkdf2_hmac("sha256", _PASSPHRASE, _SALT, _ITERATIONS)
    return Fernet(base64.urlsafe_b64encode(derived))


def load_all() -> dict[str, str]:
    """Every stored value. An unreadable or tampered file reads as empty."""
    path = store_path()
    try:
        blob = path.read_bytes()
    except OSError:
        return {}
    try:
        raw = _cipher().decrypt(blob)
    except InvalidToken:
        _log.error("credential store could not be decrypted — ignoring %s", path)
        return {}
    try:
        values = json.loads(raw.decode("utf-8"))
    except (UnicodeDecodeError, json.JSONDecodeError):
        _log.error("credential store is not valid JSON — ignoring %s", path)
        return {}
    return {str(k): str(v) for k, v in values.items()} if isinstance(values, dict) else {}


def save_all(values: dict[str, str]) -> bool:
    """Write the whole store. Returns False on failure rather than raising."""
    path = store_path()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        blob = _cipher().encrypt(json.dumps(values).encode("utf-8"))
        # Write then replace, so an interrupted write cannot truncate the store.
        temp = path.with_suffix(".tmp")
        temp.write_bytes(blob)
        os.chmod(temp, 0o600)
        temp.replace(path)
    except OSError as exc:
        _log.error("could not write the credential store: %s", exc)
        return False
    _log.info("credential store updated (%d entries)", len(values))
    return True


def get(name: str) -> str | None:
    value = load_all().get(name, "").strip()
    return value or None


def put(name: str, value: str) -> bool:
    values = load_all()
    values[name] = value.strip()
    return save_all(values)


def remove(name: str) -> bool:
    values = load_all()
    if name not in values:
        return True
    del values[name]
    if not values:
        # An empty store is just a missing file.
        with contextlib.suppress(OSError):
            store_path().unlink()
        return True
    return save_all(values)
