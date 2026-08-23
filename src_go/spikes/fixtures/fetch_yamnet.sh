#!/usr/bin/env bash
# Fetch the pinned YAMNet model for the classify tests and spikes.
#
# The application downloads its own copy at runtime (Preferences → Filter);
# this one only feeds the golden tests, which skip when it is absent. The
# model itself is Apache 2.0; the file is gitignored because a 4 MB binary
# does not belong in the history.
set -euo pipefail
cd "$(dirname "$0")"

URL="https://www.kaggle.com/api/v1/models/google/yamnet/tflite/classification-tflite/1/download"
WANT="10c95ea3eb9a7bb4cb8bddf6feb023250381008177ac162ce169694d05c317de"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
curl -sL -A "LiveCap-model-fetch" -o "$tmp/bundle.tar.gz" "$URL"
tar xzf "$tmp/bundle.tar.gz" -C "$tmp"
got="$(shasum -a 256 "$tmp/1.tflite" | cut -d' ' -f1)"
if [ "$got" != "$WANT" ]; then
  echo "hash mismatch: got $got" >&2
  exit 1
fi
mv "$tmp/1.tflite" yamnet.tflite
echo "ok: yamnet.tflite ($(wc -c < yamnet.tflite | tr -d ' ') bytes)"
