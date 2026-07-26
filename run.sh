#!/usr/bin/env bash
# Development launcher.
set -euo pipefail
cd "$(dirname "$0")"

PY=.venv/bin/python

if [[ ! -x "$PY" ]]; then
    echo "==> No .venv found. Creating one." >&2
    python3 -m venv .venv
fi

# Install the package in editable mode if it is not importable yet.
if ! "$PY" -c "import livecap" >/dev/null 2>&1; then
    echo "==> Installing dependencies (first run only, may take a minute or two)" >&2
    "$PY" -m pip install -q --upgrade pip
    "$PY" -m pip install -q -e '.[dev]'
fi

exec "$PY" -m livecap.app "$@"
