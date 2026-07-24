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
    # .env를 먼저 읽어야 LIVECAP_DEBUG 같은 값이 로깅 설정에 반영된다.
    loaded = config.apply_dotenv_to_environ()
    log.setup()
    if loaded:
        # 키 '이름'만 남긴다. 값은 절대 로그에 찍지 않는다.
        log.get(__name__).info("loaded from .env: %s", ", ".join(sorted(loaded)))

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
