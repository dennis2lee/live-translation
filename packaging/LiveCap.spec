# PyInstaller spec for the LiveCap .app bundle.
#
#     .venv/bin/python -m PyInstaller packaging/LiveCap.spec --noconfirm
#
# Use packaging/build_app.sh, which also regenerates the icon.

import sys
from pathlib import Path

from PyInstaller.utils.hooks import collect_data_files, collect_submodules

ROOT = Path(SPECPATH).resolve().parent  # noqa: F821  (SPECPATH is injected)

# Read the version from the package so the bundle cannot drift from the source.
sys.path.insert(0, str(ROOT / "src"))
from livecap import __version__ as VERSION  # noqa: E402

datas = []
# sounddevice ships libportaudio.dylib in a sibling data directory, which
# PyInstaller does not pick up on its own. Without it the bundle starts but
# finds no audio devices.
datas += collect_data_files("sounddevice", include_py_files=False)
datas += collect_data_files("_sounddevice_data", include_py_files=False)
datas += collect_data_files("soxr", include_py_files=False)

hiddenimports = [
    # Selected at runtime by keyring, so not discoverable by static analysis.
    "keyring.backends.macOS",
    "keyring.backends.null",
    "keyring.backends.fail",
]
hiddenimports += collect_submodules("google.genai")

a = Analysis(  # noqa: F821
    [str(ROOT / "packaging" / "entry.py")],
    pathex=[str(ROOT / "src")],
    binaries=[],
    datas=datas,
    hiddenimports=hiddenimports,
    hookspath=[],
    runtime_hooks=[],
    # Qt pulls these in and they are large; none of them is used.
    excludes=[
        "PyQt6.QtWebEngineCore",
        "PyQt6.QtWebEngineWidgets",
        "PyQt6.Qt3DCore",
        "PyQt6.QtBluetooth",
        "PyQt6.QtQuick",
        "PyQt6.QtQml",
        "PyQt6.QtMultimedia",
        "PyQt6.QtDesigner",
        "tkinter",
        "matplotlib",
        "pytest",
    ],
    noarchive=False,
)

pyz = PYZ(a.pure)  # noqa: F821

exe = EXE(  # noqa: F821
    pyz,
    a.scripts,
    [],
    exclude_binaries=True,
    name="LiveCap",
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=False,
    console=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=str(ROOT / "packaging" / "entitlements.plist"),
)

coll = COLLECT(  # noqa: F821
    exe,
    a.binaries,
    a.datas,
    strip=False,
    upx=False,
    name="LiveCap",
)

app = BUNDLE(  # noqa: F821
    coll,
    name="LiveCap.app",
    icon=str(ROOT / "packaging" / "LiveCap.icns"),
    bundle_identifier="com.livecap.LiveCap",
    info_plist={
        # Required, or macOS kills the app instead of prompting for the mic.
        "NSMicrophoneUsageDescription":
            "LiveCap listens to the selected audio input so it can caption the "
            "translation in real time.",
        "CFBundleName": "LiveCap",
        "CFBundleDisplayName": "LiveCap",
        "CFBundleShortVersionString": VERSION,
        "CFBundleVersion": VERSION,
        "LSMinimumSystemVersion": "13.0",
        "LSApplicationCategoryType": "public.app-category.productivity",
        "NSHighResolutionCapable": True,
        # The caption window is the whole UI; there is no document model.
        "LSUIElement": False,
        "NSHumanReadableCopyright": "Copyright (c) 2026 Pyeongwoo Lee. MIT licence.",
    },
)
