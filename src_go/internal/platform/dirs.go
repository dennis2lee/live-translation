// Package platform is the only package that branches on the operating system.
//
// It inherits platform_support.py's contract verbatim: every platform
// difference lives here, and the Windows and Linux branches are pinned by
// tests that fake the platform, so they stay honest without those machines.
package platform

import (
	"os"
	"path/filepath"
)

// AppName is the directory the application data lives under.
const AppName = "LiveCap"

// AppSupportDir is the per-user directory for application data — the encrypted
// credential store, settings, transcripts. Mirrors
// platform_support.app_support_dir().
func AppSupportDir() string {
	return appSupportDirFor(runtimeGOOS(), os.Getenv, userHomeDir)
}

// appSupportDirFor is the testable core: the OS and the environment are
// parameters, so the Windows and Linux branches can be exercised from a Mac.
func appSupportDirFor(goos string, getenv func(string) string, home func() string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home(), "Library", "Application Support", AppName)
	case "windows":
		if appdata := getenv("APPDATA"); appdata != "" {
			return filepath.Join(appdata, AppName)
		}
		return filepath.Join(home(), "AppData", "Roaming", AppName)
	default:
		if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
			return filepath.Join(xdg, AppName)
		}
		return filepath.Join(home(), ".config", AppName)
	}
}

func userHomeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}
