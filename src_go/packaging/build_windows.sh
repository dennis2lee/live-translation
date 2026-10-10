#!/usr/bin/env bash
# Cross-build the Windows executable and wrap it in an MSI, from macOS.
#
#   ./packaging/build_windows.sh
#
# Needs two Homebrew formulas:
#
#   brew install mingw-w64 msitools
#
# What this can and cannot do. It produces a real PE64 binary and a real MSI,
# and it checks that the binary imports nothing but Windows system DLLs. It
# cannot run either of them — there is no Windows here — so "it built" is the
# strongest claim available until someone installs it on a Windows machine.
# The Python build has the same limitation recorded in CLAUDE.md for
# PyInstaller; this is the Go equivalent, and it is at least cross-buildable.
#
# Why there is no libsoxr to ship: the Windows build uses the pure-Go filter in
# internal/resample/fir.go instead. miniaudio and GLFW compile into the binary,
# so the whole application is one file.
set -euo pipefail

cd "$(dirname "$0")/.."

APP_NAME="$(grep -o 'AppName     = "[^"]*"' internal/ui/version.go | cut -d'"' -f2)"
VERSION="$(grep -o 'Version     = "[^"]*"' internal/ui/version.go | cut -d'"' -f2)"
MANUFACTURER="$(grep -o 'Author      = "[^"]*"' internal/ui/version.go | cut -d'"' -f2)"
# A registry key cannot hold spaces comfortably.
MANUFACTURER_KEY="$(echo "$MANUFACTURER" | tr -d ' ')"

DIST="dist/windows"
STAGE="$DIST/stage"
EXE="$STAGE/$APP_NAME.exe"
MSI="$DIST/$APP_NAME-$VERSION-x64.msi"

for tool in x86_64-w64-mingw32-gcc wixl; do
  command -v "$tool" >/dev/null || {
    echo "missing $tool — run: brew install mingw-w64 msitools" >&2
    exit 1
  }
done

# The icon has to exist before the resource is compiled into the binary.
python3 packaging/make_icon.py >/dev/null

echo "building $APP_NAME $VERSION for windows/amd64"
rm -rf "$DIST"
mkdir -p "$STAGE"

# Embed the icon in the executable itself, so Windows draws it on the window,
# in the taskbar and in Explorer — the MSI's Icon element only reaches the
# shortcuts. The .syso is picked up by the Go toolchain purely by living next
# to the main package, and is removed again afterwards so it cannot leak into
# a macOS build.
SYSO="cmd/livecap/rsrc_windows_amd64.syso"
trap 'rm -f "$SYSO"' EXIT
go run github.com/akavel/rsrc@v0.10.2 \
  -ico packaging/AppIcon.ico -arch amd64 -o "$SYSO"

# -H=windowsgui keeps a console window from opening behind the app.
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 \
  CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
  go build -trimpath \
    -ldflags "-s -w -H=windowsgui -extldflags '-static'" \
    -o "$EXE" ./cmd/livecap

echo "  $(du -h "$EXE" | cut -f1)  $EXE"

# --- the check that matters: nothing but system DLLs ----------------------
# A stray libgcc or libwinpthread import means the installer would need to ship
# it too, and the first machine without it would fail at launch with a dialog
# that names a DLL rather than the application.
STRAY="$(x86_64-w64-mingw32-objdump -p "$EXE" | awk '/DLL Name:/ {print tolower($3)}' \
  | grep -vE '^(kernel32|user32|gdi32|shell32|opengl32|advapi32|ole32|oleaut32|winmm|ws2_32|imm32|dwmapi|comdlg32|version|api-ms-win-crt-.*)\.dll$' || true)"
if [[ -n "$STRAY" ]]; then
  echo "FAIL: the binary imports non-system DLLs that are not being shipped:" >&2
  echo "$STRAY" >&2
  exit 1
fi
echo "  imports: Windows system DLLs only"

# --- the MSI ---------------------------------------------------------------
sed -e "s/@APP_NAME@/$APP_NAME/g" \
    -e "s#@ICON@#$PWD/packaging/AppIcon.ico#g" \
    -e "s/@VERSION@/$VERSION/g" \
    -e "s/@MANUFACTURER@/$MANUFACTURER/g" \
    -e "s/@MANUFACTURER_KEY@/$MANUFACTURER_KEY/g" \
    -e "s#@STAGE@#$STAGE#g" \
    packaging/livecap.wxs.in > "$DIST/livecap.wxs"

wixl -v -a x64 -o "$MSI" "$DIST/livecap.wxs"

echo
echo "  $(du -h "$MSI" | cut -f1)  $MSI"
echo
echo "Not yet run on Windows since 1.0. Install it there and check, in this order:"
echo "  1. it installs and the Start menu entry launches it"
echo "  2. the caption window opens and Korean renders"
echo "  3. Settings > Audio Input lists the capture devices"
echo "  4. full screen covers the taskbar  <- the requirement that chose Fyne"
echo "  5. 0 starts translating and Windows asks for the microphone"
