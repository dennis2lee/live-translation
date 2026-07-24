#!/usr/bin/env bash
# 개발용 실행 스크립트
set -euo pipefail
cd "$(dirname "$0")"

if [[ ! -d .venv ]]; then
    echo "no .venv — creating one" >&2
    python3 -m venv .venv
    .venv/bin/python -m pip install -q -e '.[dev]'
fi

exec .venv/bin/python -m livecap.app "$@"
