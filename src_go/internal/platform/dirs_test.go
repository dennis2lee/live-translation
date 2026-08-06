package platform

import (
	"path/filepath"
	"testing"
)

// The Windows and Linux branches are pinned here, without those machines —
// the same job tests/test_platform_support.py does for the Python build.
func TestAppSupportDirPerPlatform(t *testing.T) {
	home := func() string { return filepath.Join("/home", "dennis") }

	cases := []struct {
		name string
		goos string
		env  map[string]string
		want string
	}{
		{
			name: "macOS",
			goos: "darwin",
			want: "/home/dennis/Library/Application Support/LiveCap",
		},
		{
			name: "Windows with APPDATA",
			goos: "windows",
			env:  map[string]string{"APPDATA": filepath.Join("C:", "Users", "d", "AppData", "Roaming")},
			want: filepath.Join("C:", "Users", "d", "AppData", "Roaming", "LiveCap"),
		},
		{
			name: "Windows without APPDATA",
			goos: "windows",
			want: "/home/dennis/AppData/Roaming/LiveCap",
		},
		{
			name: "Linux with XDG_CONFIG_HOME",
			goos: "linux",
			env:  map[string]string{"XDG_CONFIG_HOME": "/home/dennis/.cfg"},
			want: "/home/dennis/.cfg/LiveCap",
		},
		{
			name: "Linux without XDG_CONFIG_HOME",
			goos: "linux",
			want: "/home/dennis/.config/LiveCap",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			getenv := func(k string) string { return tc.env[k] }
			if got := appSupportDirFor(tc.goos, getenv, home); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// An empty XDG_CONFIG_HOME must fall through, not produce "/LiveCap".
func TestEmptyEnvFallsThrough(t *testing.T) {
	home := func() string { return "/home/d" }
	getenv := func(string) string { return "" }
	if got := appSupportDirFor("linux", getenv, home); got != "/home/d/.config/LiveCap" {
		t.Errorf("got %q", got)
	}
}
