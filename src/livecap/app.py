"""진입점 — QApplication 부트스트랩."""

from __future__ import annotations

import signal
import sys

from PyQt6.QtCore import QTimer
from PyQt6.QtWidgets import QApplication

from . import config
from .ui.main_window import CaptionWindow
from .util import logging as log


def main() -> int:
    log.setup()

    app = QApplication(sys.argv)
    app.setApplicationName("LiveCap")
    app.setOrganizationName("LiveCap")
    app.setApplicationDisplayName("LiveCap")

    window = CaptionWindow(config.load())

    # Ctrl-C가 Qt 이벤트 루프에 먹히지 않도록 주기적으로 파이썬에 제어를 넘긴다.
    signal.signal(signal.SIGINT, lambda *_: app.quit())
    idle = QTimer()
    idle.start(200)
    idle.timeout.connect(lambda: None)

    exit_code = app.exec()
    window.stop_translation()
    return exit_code


if __name__ == "__main__":
    raise SystemExit(main())
