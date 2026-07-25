"""Entry point — QApplication bootstrap."""

from __future__ import annotations

import signal
import sys

from PyQt6.QtCore import QTimer
from PyQt6.QtWidgets import QApplication

from . import config
from .ui.main_window import CaptionWindow
from .util import logging as log


def selftest() -> int:
    """Check that a packaged build can actually reach everything it needs.

    Run with --selftest. Exercises the parts that a bundle silently breaks:
    the audio backend, the resampler extension, the Keychain backend and the
    API client. Never opens a window and never calls the paid API.
    """
    ok = True

    def check(label: str, fn) -> None:
        nonlocal ok
        try:
            detail = fn()
        except Exception as exc:  # noqa: BLE001 — report, never raise
            ok = False
            print(f"  FAIL  {label}: {type(exc).__name__}: {exc}")
        else:
            print(f"  ok    {label}{f' — {detail}' if detail else ''}")

    print("LiveCap self-test")
    print(f"  frozen: {getattr(sys, 'frozen', False)}  executable: {sys.executable}")

    def audio_backend() -> str:
        from .audio import list_input_devices

        devices = list_input_devices()
        if not devices:
            raise RuntimeError("no input devices — is libportaudio bundled?")
        return f"{len(devices)} input device(s), first {devices[0].name!r}"

    def resampler() -> str:
        import numpy as np
        import soxr

        stream = soxr.ResampleStream(48000, 16000, 1, dtype="float32", quality="HQ")
        out = stream.resample_chunk(np.zeros(960, dtype=np.float32))
        return f"soxr {soxr.__version__}, {out.size} samples from a 20 ms block"

    def qt() -> str:
        from PyQt6.QtCore import QT_VERSION_STR

        return f"Qt {QT_VERSION_STR}"

    def sdk() -> str:
        from google import genai  # noqa: F401
        from google.genai import types

        types.TranslationConfig(target_language_code="en")
        return "google-genai types resolve"

    def keychain() -> str:
        import keyring

        return f"backend {keyring.get_keyring().__class__.__name__}"

    def key() -> str:
        found = config.load_api_key()
        return "API key found" if found else "no API key (set one in Preferences)"

    check("audio backend", audio_backend)
    check("resampler", resampler)
    check("Qt", qt)
    check("Gemini SDK", sdk)
    check("Keychain", keychain)
    check("credentials", key)

    print("PASS" if ok else "FAIL")
    return 0 if ok else 1


def main() -> int:
    if "--selftest" in sys.argv:
        config.apply_dotenv_to_environ()
        log.setup()
        return selftest()

    # Load .env first so values like LIVECAP_DEBUG reach the logging setup.
    loaded = config.apply_dotenv_to_environ()
    log.setup()
    if loaded:
        # Log the variable names only. Never log a key's value.
        log.get(__name__).info("loaded from .env: %s", ", ".join(sorted(loaded)))

    app = QApplication(sys.argv)
    app.setApplicationName("LiveCap")
    app.setOrganizationName("LiveCap")
    app.setApplicationDisplayName("LiveCap")

    window = CaptionWindow(config.load())

    # Give Python a chance to run so Ctrl-C is not swallowed by the Qt event loop.
    signal.signal(signal.SIGINT, lambda *_: app.quit())
    idle = QTimer()
    idle.start(200)
    idle.timeout.connect(lambda: None)

    exit_code = app.exec()
    window.stop_translation()
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
