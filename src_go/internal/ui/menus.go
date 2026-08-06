package ui

import (
	"fmt"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/driver/desktop"

	"github.com/dennis2lee/live-translation/src_go/internal/audio"
	"github.com/dennis2lee/live-translation/src_go/internal/config"
	"github.com/dennis2lee/live-translation/src_go/internal/translator"
)

// menuBar owns the native menu and the items whose state has to be kept in
// step with the settings.
//
// Preferences lives in the Settings menu rather than the application menu:
// spike F2 measured that Fyne 2.8 builds the app menu itself — About, Services,
// Hide, Show All, Quit — and offers no hook for the conventional ⌘, slot.
type menuBar struct {
	win  *Window
	menu *fyne.MainMenu

	start          *fyne.MenuItem
	stop           *fyne.MenuItem
	status         *fyne.MenuItem
	saveTranscript *fyne.MenuItem
	directionHint  *fyne.MenuItem
	completeOnly   *fyne.MenuItem
	fixedBox       *fyne.MenuItem
	sourceText     *fyne.MenuItem
	fullscreen     *fyne.MenuItem

	directions map[config.Direction]*fyne.MenuItem
	positions  map[config.BoxPosition]*fyne.MenuItem
	fontSizes  map[int]*fyne.MenuItem
	lineCounts map[int]*fyne.MenuItem

	deviceMenu *fyne.Menu
}

func newMenuBar(w *Window) *menuBar {
	m := &menuBar{
		win:        w,
		directions: map[config.Direction]*fyne.MenuItem{},
		positions:  map[config.BoxPosition]*fyne.MenuItem{},
		fontSizes:  map[int]*fyne.MenuItem{},
		lineCounts: map[int]*fyne.MenuItem{},
	}

	// --- Translate ---
	m.start = fyne.NewMenuItem("Start Translating", w.StartTranslation)
	m.start.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyR, Modifier: fyne.KeyModifierShortcutDefault}
	// Two separate items rather than one toggle, so a mis-click cannot stop a
	// live session. The 0 key toggles for people who want that.
	m.stop = fyne.NewMenuItem("Stop Translating", w.StopTranslation)
	clear := fyne.NewMenuItem("Clear Captions", w.ClearCaptions)
	toggle := fyne.NewMenuItem("Toggle Translation (0)", w.ToggleTranslation)

	m.saveTranscript = fyne.NewMenuItem("Save Transcript", func() {
		w.update(func(s *config.Settings) { s.SaveTranscript = !s.SaveTranscript })
	})
	openDir := fyne.NewMenuItem("Open Transcript Folder", w.OpenTranscriptDir)

	m.status = fyne.NewMenuItem("Status: Idle", nil)
	m.status.Disabled = true

	translate := fyne.NewMenu("Translate",
		m.start, m.stop,
		fyne.NewMenuItemSeparator(),
		clear, toggle,
		fyne.NewMenuItemSeparator(),
		m.saveTranscript, openDir,
		fyne.NewMenuItemSeparator(),
		m.status,
	)

	// --- Settings ---
	var directionItems []*fyne.MenuItem
	for i, direction := range []config.Direction{config.KoreanToEnglish, config.EnglishToKorean} {
		d := direction
		item := fyne.NewMenuItem(fmt.Sprintf("%s (%d)", config.DirectionMenuText[d], i+1), func() {
			w.SetDirection(d)
		})
		m.directions[d] = item
		directionItems = append(directionItems, item)
	}
	directionMenu := fyne.NewMenuItem("Direction", nil)
	directionMenu.ChildMenu = fyne.NewMenu("", directionItems...)

	m.directionHint = fyne.NewMenuItem("Show Direction", func() {
		w.update(func(s *config.Settings) { s.ShowDirectionHint = !s.ShowDirectionHint })
	})

	var sizeItems []*fyne.MenuItem
	for _, size := range FontSizeChoices {
		size := size
		item := fyne.NewMenuItem(fmt.Sprint(size), func() {
			w.update(func(s *config.Settings) { s.FontSize = size })
		})
		m.fontSizes[size] = item
		sizeItems = append(sizeItems, item)
	}
	sizeMenu := fyne.NewMenuItem("Font Size", nil)
	sizeMenu.ChildMenu = fyne.NewMenu("", sizeItems...)

	// The lower part of the screen is often covered by something else, so the
	// box can be moved rather than only resized.
	var positionItems []*fyne.MenuItem
	for _, position := range []config.BoxPosition{config.BoxTop, config.BoxCenter, config.BoxBottom} {
		p := position
		item := fyne.NewMenuItem(config.BoxPositionMenuText[p], func() {
			w.update(func(s *config.Settings) { s.BoxPosition = string(p) })
		})
		m.positions[p] = item
		positionItems = append(positionItems, item)
	}
	positionMenu := fyne.NewMenuItem("Caption Position", nil)
	positionMenu.ChildMenu = fyne.NewMenu("", positionItems...)

	var lineItems []*fyne.MenuItem
	for _, n := range LineCountChoices {
		n := n
		item := fyne.NewMenuItem(fmt.Sprint(n), func() {
			w.update(func(s *config.Settings) { s.MaxLines = n })
		})
		m.lineCounts[n] = item
		lineItems = append(lineItems, item)
	}
	linesMenu := fyne.NewMenuItem("Caption Lines", nil)
	linesMenu.ChildMenu = fyne.NewMenu("", lineItems...)

	// A USB interface can be plugged or unplugged at any time, so the device
	// list is rebuilt whenever the menu is opened.
	m.deviceMenu = fyne.NewMenu("")
	deviceMenu := fyne.NewMenuItem("Audio Input", nil)
	deviceMenu.ChildMenu = m.deviceMenu

	refreshDevices := fyne.NewMenuItem("Refresh Device List", func() {
		m.populateDevices()
		m.menu.Refresh()
	})

	m.completeOnly = fyne.NewMenuItem("Complete Phrases Only (9)", func() {
		w.update(func(s *config.Settings) { s.CompletePhrasesOnly = !s.CompletePhrasesOnly })
	})
	m.fixedBox = fyne.NewMenuItem("Always Show Caption Box", func() {
		w.update(func(s *config.Settings) { s.FixedBox = !s.FixedBox })
	})
	m.sourceText = fyne.NewMenuItem("Show Source Text", func() {
		w.update(func(s *config.Settings) { s.ShowSourceText = !s.ShowSourceText })
	})
	m.fullscreen = fyne.NewMenuItem("Full Screen", func() {
		w.setFullScreen(!w.settings.Fullscreen)
	})

	prefs := fyne.NewMenuItem("Preferences…", w.OpenPreferences)
	prefs.Shortcut = &desktop.CustomShortcut{KeyName: fyne.KeyComma, Modifier: fyne.KeyModifierShortcutDefault}
	reset := fyne.NewMenuItem("Reset to Defaults", w.ResetSettings)

	settings := fyne.NewMenu("Settings",
		prefs,
		fyne.NewMenuItemSeparator(),
		directionMenu, m.directionHint,
		fyne.NewMenuItemSeparator(),
		sizeMenu, positionMenu, linesMenu,
		fyne.NewMenuItemSeparator(),
		deviceMenu, refreshDevices,
		fyne.NewMenuItemSeparator(),
		m.completeOnly, m.fixedBox, m.sourceText, m.fullscreen,
		fyne.NewMenuItemSeparator(),
		reset,
	)

	help := fyne.NewMenu("Help", fyne.NewMenuItem("About LiveCap", w.ShowAbout))

	m.menu = fyne.NewMainMenu(translate, settings, help)
	m.populateDevices()
	return m
}

// populateDevices fills the submenu with the detected input devices.
func (m *menuBar) populateDevices() {
	w := m.win
	items := []*fyne.MenuItem{
		fyne.NewMenuItem("System Default Input", func() { w.chooseDevice("") }),
	}
	items[0].Checked = w.settings.AudioDeviceName == ""

	devices := audio.ListInputDevices()
	if len(devices) == 0 {
		none := fyne.NewMenuItem("(no input device found)", nil)
		none.Disabled = true
		items = append(items, fyne.NewMenuItemSeparator(), none)
	} else {
		items = append(items, fyne.NewMenuItemSeparator())
		for _, dev := range devices {
			name := dev.Name
			item := fyne.NewMenuItem(deviceLabel(dev), func() { w.chooseDevice(name) })
			item.Checked = name == w.settings.AudioDeviceName
			items = append(items, item)
		}
	}
	m.deviceMenu.Items = items
}

// sync brings every checkable item and label into line with the settings.
func (m *menuBar) sync() {
	w := m.win
	s := w.settings

	// Keyed off the session objects, not the state: a start that failed leaves
	// ERROR behind with nothing running, and Start must stay usable.
	running := w.translator != nil || w.capture != nil
	m.start.Disabled = running
	m.stop.Disabled = !running && w.state != translator.StateError

	label := statusText[w.state]
	if label == "" {
		label = "Idle"
	}
	if w.errorDetail != "" {
		label += " (" + w.errorDetail + ")"
	}
	m.status.Label = "Status: " + label

	m.saveTranscript.Checked = s.SaveTranscript
	m.directionHint.Checked = s.ShowDirectionHint
	m.completeOnly.Checked = s.CompletePhrasesOnly
	m.fixedBox.Checked = s.FixedBox
	m.sourceText.Checked = s.ShowSourceText
	m.fullscreen.Checked = s.Fullscreen

	current, known := config.DirectionForTarget(s.TargetLanguageCode)
	for direction, item := range m.directions {
		item.Checked = known && direction == current
	}
	for position, item := range m.positions {
		item.Checked = position == s.Position()
	}
	for size, item := range m.fontSizes {
		item.Checked = size == s.FontSize
	}
	for n, item := range m.lineCounts {
		item.Checked = n == s.MaxLines
	}
	m.menu.Refresh()
}

// syncActions is the Window-side name, so the call sites read like the Python
// build's.
func (w *Window) syncActions() {
	if w.menus != nil {
		w.menus.sync()
	}
}

// chooseDevice stores devices by name, not index — indexes change on USB
// reconnect.
func (w *Window) chooseDevice(name string) {
	if name == w.settings.AudioDeviceName {
		return
	}
	w.update(func(s *config.Settings) { s.AudioDeviceName = name })
	w.menus.populateDevices()
	w.menus.menu.Refresh()
}
