"""Entry point — QApplication bootstrap."""

from __future__ import annotations

import signal
import sys

from PyQt6.QtCore import QTimer
from PyQt6.QtWidgets import QApplication

from . import config
from .ui.main_window import CaptionWindow
from .util import logging as log


def main() -> int:
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
