#!/usr/bin/env bash
# Assemble LiveCap.app from the Go binary.
#
#   ./packaging/build_app.sh              build the bundle
#   ./packaging/build_app.sh --sign ID    build and codesign with a Developer ID
#
# Two things are load-bearing and easy to get wrong:
#
#   * NSMicrophoneUsageDescription. Without it macOS *kills* the process on the
#     first capture rather than prompting for permission.
#   * CFBundleName. The application menu takes its title from the bundle, so an
#     unbundled binary cannot show the right name at all — the same trap that
#     once made the Python build's menu read "Python".
#
# libsoxr is a dynamic library from Homebrew, so it is copied in and the binary
# is repointed at the copy. Without that the bundle only runs on a machine that
# has Homebrew's libsoxr installed at the same path.
set -euo pipefail

cd "$(dirname "$0")/.."

APP_NAME="LiveCap"
BUNDLE_ID="com.LiveCap.LiveCap.go"
VERSION="$(grep -o 'Version     = "[^"]*"' internal/ui/version.go | cut -d'"' -f2)"
DIST="dist"
APP="$DIST/$APP_NAME.app"
MACOS="$APP/Contents/MacOS"
RESOURCES="$APP/Contents/Resources"
FRAMEWORKS="$APP/Contents/Frameworks"

SIGN_ID=""
if [[ "${1:-}" == "--sign" ]]; then
  SIGN_ID="${2:?--sign needs a Developer ID}"
fi

echo "building $APP_NAME $VERSION"
rm -rf "$APP"
mkdir -p "$MACOS" "$RESOURCES" "$FRAMEWORKS"

CGO_ENABLED=1 go build -trimpath -ldflags "-s -w" -o "$MACOS/$APP_NAME" ./cmd/livecap

# --- Info.plist ------------------------------------------------------------
sed -e "s/@APP_NAME@/$APP_NAME/g" \
    -e "s/@BUNDLE_ID@/$BUNDLE_ID/g" \
    -e "s/@VERSION@/$VERSION/g" \
    packaging/Info.plist.tmpl > "$APP/Contents/Info.plist"

if [[ -f packaging/AppIcon.icns ]]; then
  cp packaging/AppIcon.icns "$RESOURCES/AppIcon.icns"
else
  echo "  note: packaging/AppIcon.icns is missing — the bundle will use the default icon"
fi

# --- bundle libsoxr --------------------------------------------------------
SOXR_PATH="$(otool -L "$MACOS/$APP_NAME" | awk '/libsoxr/ {print $1; exit}')"
if [[ -n "$SOXR_PATH" && -f "$SOXR_PATH" ]]; then
  SOXR_LIB="$(basename "$SOXR_PATH")"
  cp "$SOXR_PATH" "$FRAMEWORKS/$SOXR_LIB"
  chmod u+w "$FRAMEWORKS/$SOXR_LIB"
  install_name_tool -id "@rpath/$SOXR_LIB" "$FRAMEWORKS/$SOXR_LIB"
  install_name_tool -change "$SOXR_PATH" "@rpath/$SOXR_LIB" "$MACOS/$APP_NAME"
  install_name_tool -add_rpath "@executable_path/../Frameworks" "$MACOS/$APP_NAME"
  echo "  bundled $SOXR_LIB"
else
  echo "  note: no dynamic libsoxr found in the binary — nothing to bundle"
fi

# --- sign ------------------------------------------------------------------
if [[ -n "$SIGN_ID" ]]; then
  echo "signing with $SIGN_ID"
  # The library is signed before the bundle that contains it.
  find "$FRAMEWORKS" -name '*.dylib' -exec \
    codesign --force --timestamp --options runtime --sign "$SIGN_ID" {} \;
  codesign --force --timestamp --options runtime \
    --entitlements packaging/entitlements.plist \
    --sign "$SIGN_ID" "$APP"
  codesign --verify --deep --strict --verbose=2 "$APP"
else
  # An ad-hoc signature is enough for the bundle to run locally, and it stops
  # macOS re-asking for microphone permission after every rebuild.
  #
  # The nested library has to be signed first and explicitly. install_name_tool
  # invalidates whatever signature the copied dylib arrived with, and on Apple
  # silicon dyld refuses to load an invalidly-signed library — the process is
  # killed outright rather than failing with a message.
  find "$FRAMEWORKS" -name '*.dylib' -exec codesign --force --sign - {} \;
  codesign --force --sign - "$APP"
fi

echo
du -sh "$APP"
echo "running the selftest from inside the bundle:"
"$MACOS/$APP_NAME" --selftest
