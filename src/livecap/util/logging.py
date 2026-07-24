"""로깅 설정. 오디오 콜백에서는 절대 호출하지 말 것."""

from __future__ import annotations

import logging
import os
import sys

_CONFIGURED = False


def setup(level: int | None = None) -> None:
    """루트 로거를 stderr로 한 번만 설정한다."""
    global _CONFIGURED
    if _CONFIGURED:
        return
    if level is None:
        level = logging.DEBUG if os.environ.get("LIVECAP_DEBUG") else logging.INFO
    handler = logging.StreamHandler(sys.stderr)
    handler.setFormatter(
        logging.Formatter("%(asctime)s %(levelname)-7s %(name)-22s %(message)s", "%H:%M:%S")
    )
    root = logging.getLogger()
    root.setLevel(level)
    root.addHandler(handler)
    # SDK가 매 프레임 DEBUG를 쏟아내지 않도록 눌러둔다.
    logging.getLogger("google_genai").setLevel(logging.WARNING)
    logging.getLogger("websockets").setLevel(logging.WARNING)
    _CONFIGURED = True


def get(name: str) -> logging.Logger:
    return logging.getLogger(name)
