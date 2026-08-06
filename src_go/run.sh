#!/usr/bin/env bash
# Run LiveCap from source.
set -euo pipefail
cd "$(dirname "$0")"
exec go run ./cmd/livecap "$@"
