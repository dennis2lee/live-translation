"""Logging setup. Never call this from the audio callback."""

from __future__ import annotations

import logging
import os
import sys

_CONFIGURED = False


def setup(level: int | None = None) -> None:
    """Configure the root logger to write to stderr, once."""
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
    # Keep the SDK from emitting a DEBUG line per frame.
    logging.getLogger("google_genai").setLevel(logging.WARNING)
    logging.getLogger("websockets").setLevel(logging.WARNING)
    _CONFIGURED = True


def get(name: str) -> logging.Logger:
    return logging.getLogger(name)
