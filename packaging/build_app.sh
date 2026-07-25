#!/usr/bin/env bash
# Build dist/LiveCap.app.
#
#     packaging/build_app.sh              # build and self-test
#     packaging/build_app.sh --sign       # also codesign with Developer ID
#
# Signing needs a "Developer ID Application" certificate in the login keychain
# and LIVECAP_SIGN_IDENTITY set to its name. Notarisation is a separate step —
# see the instructions printed at the end.
set -euo pipefail
cd "$(dirname "$0")/.."

PY=.venv/bin/python
APP=dist/LiveCap.app

if [[ ! -x "$PY" ]]; then
    echo "no .venv — run the install steps in INSTALL.md first" >&2
    exit 1
fi

if ! "$PY" -c "import PyInstaller" >/dev/null 2>&1; then
    echo "==> installing PyInstaller"
    "$PY" -m pip install -q pyinstaller
fi

echo "==> generating the icon"
"$PY" packaging/make_icon.py

echo "==> building the bundle"
rm -rf build dist
"$PY" -m PyInstaller packaging/LiveCap.spec --noconfirm --distpath dist --workpath build \
    --log-level WARN

echo "==> self-test inside the bundle"
# Run from a directory with no .env, so the check reflects a fresh machine.
(cd /tmp && "$OLDPWD/$APP/Contents/MacOS/LiveCap" --selftest) || {
    echo "self-test failed — the bundle is not shippable" >&2
    exit 1
}

if [[ "${1:-}" == "--sign" ]]; then
    : "${LIVECAP_SIGN_IDENTITY:?set LIVECAP_SIGN_IDENTITY to your Developer ID Application name}"
    echo "==> codesigning"
    codesign --force --deep --options runtime --timestamp \
        --entitlements packaging/entitlements.plist \
        --sign "$LIVECAP_SIGN_IDENTITY" "$APP"
    codesign --verify --strict --verbose=2 "$APP"
    echo
    echo "Signed. To notarise:"
    echo "  ditto -c -k --keepParent $APP dist/LiveCap.zip"
    echo "  xcrun notarytool submit dist/LiveCap.zip --keychain-profile <profile> --wait"
    echo "  xcrun stapler staple $APP"
else
    echo
    echo "Unsigned build. It runs on this Mac, but Gatekeeper will block it on"
    echo "another one. Re-run with --sign once you have a Developer ID."
fi

echo
du -sh "$APP"
echo "built $APP"
