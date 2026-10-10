// Command livecap shows real-time speech translation captions.
//
// Korean → English by default, English → Korean on demand. The engine is the
// Google Gemini Live API.
package main

import (
	"flag"

	"fmt"
	"fyne.io/fyne/v2"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"fyne.io/fyne/v2/app"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/credentials"
	"github.com/dennis2lee/live-translation/src_go/internal/logging"
	"github.com/dennis2lee/live-translation/src_go/internal/netcheck"
	"github.com/dennis2lee/live-translation/src_go/internal/platform"
	"github.com/dennis2lee/live-translation/src_go/internal/resample"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
	"github.com/dennis2lee/live-translation/src_go/internal/ui"
)

// AppID is deliberately the identifier the Python build used. At the cutover
// this application *replaces* that one, so sharing the identifier means an
// existing install upgrades in place: the settings directory, the Keychain
// entry and — the one that would otherwise be a fresh annoyance — the
// microphone permission all carry over untouched.
const AppID = "com.livecap.LiveCap"

// shutdownDeadline bounds how long a signal waits for the UI goroutine before
// exiting anyway. Long enough for a transcript footer, short enough that a
// wedged process still dies on Ctrl-C.
const shutdownDeadline = 5 * time.Second

func main() {
	selftest := flag.Bool("selftest", false, "check the environment and exit")
	version := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	if *version {
		fmt.Printf("%s %s\n", ui.AppName, ui.Version)
		return
	}

	settings := config.Load()
	if path := logging.Setup(settings.DebugLogging); path != "" {
		slog.Info("logging to file", "path", path, "debug", settings.DebugLogging)
	}

	if *selftest {
		os.Exit(runSelftest())
	}

	// A first run after the Python build imports its preferences, once.
	if config.MigrateFromPlist() {
		settings = config.Load()
	}

	application := app.NewWithID(AppID)
	window := ui.NewWindow(application, settings)

	// SIGINT and SIGTERM reach the same single exit path as closing the window
	// and quitting, so a live session is always closed and the transcript
	// always finished with its footer.
	//
	// Shutdown is queued onto the UI goroutine rather than called here.
	// Calling it from the signal goroutine crashed with an NSException inside
	// cgo the first time it was tried mid-session: Shutdown tears down window
	// state, and AppKit aborts when that happens off the main thread. The same
	// rule the audio callback lives by applies to the signal handler — it is
	// just another foreign thread.
	//
	// fyne.Do, not DoAndWait: the process must not hang if the UI goroutine is
	// wedged, so the wait has a deadline and a complaint.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-signals
		slog.Info("signal received — shutting down", "signal", sig)
		done := make(chan struct{})
		fyne.Do(func() {
			window.Shutdown()
			close(done)
		})
		select {
		case <-done:
		case <-time.After(shutdownDeadline):
			slog.Error("shutdown did not finish in time", "deadline", shutdownDeadline)
		}
		os.Exit(0)
	}()

	window.Show()
	// ShowAndRun returns when the last window closes; Shutdown is idempotent,
	// so calling it here covers the routes that bypass the close intercept.
	window.Shutdown()
}

// runSelftest reports on everything the application needs, without opening a
// window. It is what a packaged bundle is checked with.
func runSelftest() int {
	ok := true
	report := func(name string, good bool, detail string) {
		mark := "ok  "
		if !good {
			mark = "FAIL"
			ok = false
		}
		fmt.Printf("  %s  %-24s %s\n", mark, name, detail)
	}

	fmt.Printf("%s %s selftest\n\n", ui.AppName, ui.Version)

	report("settings", true, config.Path())
	report("credential store", true, credentials.StorePath())
	report("app support dir", true, platform.AppSupportDir())
	if path := logging.Path(); path != "" {
		report("log file", true, path)
	} else {
		report("log file", false, "could not be opened — stderr only")
	}

	provider := config.Load().Provider
	report("provider", true, config.ProviderMenuText[provider])
	if source := config.KeySource(provider); source != "" {
		report("api key", true, "from "+source)
	} else {
		report("api key", false, "not found — enter one in Preferences")
	}

	report("resampler", resample.Version() != "", resample.Version())

	devices := audio.ListInputDevices()
	audio.ReleaseContext()
	if len(devices) == 0 {
		report("audio input", false, "no capture device found")
	} else {
		names := ""
		for i, d := range devices {
			if i > 0 {
				names += ", "
			}
			names += d.Name
		}
		report("audio input", true, fmt.Sprintf("%d device(s): %s", len(devices), names))
	}

	host := netcheck.LiveAPIHost
	if provider == config.ProviderOpenAI {
		host = netcheck.OpenAIHost
	}
	latency := netcheck.MeasureLatency(host, netcheck.LiveAPIPort, 3, 0)
	if latency.OK() {
		report("live api reachable", true,
			fmt.Sprintf("%s, %.0f ms median", latency.Host, latency.MedianMS()))
	} else {
		report("live api reachable", false, latency.Host+" — "+latency.Err)
	}

	if provider == config.ProviderOpenAI {
		report("model", true, translator.OpenAIModel)
	} else {
		report("model", true, translator.Model)
	}

	fmt.Println()
	if !ok {
		fmt.Println("selftest failed")
		return 1
	}
	fmt.Println("selftest passed")
	return 0
}
