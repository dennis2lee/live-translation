package platform

import "runtime"

// runtimeGOOS is a variable in disguise so tests can pin the other platforms.
var goosOverride string

func runtimeGOOS() string {
	if goosOverride != "" {
		return goosOverride
	}
	return runtime.GOOS
}
