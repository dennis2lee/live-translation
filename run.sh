#!/usr/bin/env bash
# 개발용 실행 스크립트
set -euo pipefail
cd "$(dirname "$0")"

PY=.venv/bin/python

if [[ ! -x "$PY" ]]; then
    echo "==> .venv가 없습니다. 만듭니다." >&2
    python3 -m venv .venv
fi

# 패키지가 아직 설치되지 않았으면 editable로 설치한다.
if ! "$PY" -c "import livecap" >/dev/null 2>&1; then
    echo "==> 의존성을 설치합니다 (최초 1회, 1~2분 걸릴 수 있습니다)" >&2
    "$PY" -m pip install -q --upgrade pip
    "$PY" -m pip install -q -e '.[dev]'
fi

if [[ ! -f .env ]]; then
    echo "==> .env가 없습니다. .env.example을 복사해 키를 채우십시오:" >&2
    echo "        cp .env.example .env" >&2
fi

exec "$PY" -m livecap.app "$@"
