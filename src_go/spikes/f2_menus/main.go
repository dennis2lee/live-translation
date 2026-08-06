// Spike F2 — does Fyne give us the menu bar, the keys and the full screen?
//
// This one has to be looked at: the macOS menu bar is painted outside the
// window, so no screenshot the program can take would show it. Run it and
// check the list on screen.
//
//	go run ./spikes/f2_menus
//
// What to check, in order:
//  1. The app menu (leftmost, next to the Apple logo) says LiveCap, not
//     "f2_menus", and holds About / Preferences / Quit.
//  2. Four menus: File, Translate, Captions, Help.
//  3. Translate → "Complete phrases only" has a tick that toggles when
//     chosen, and when the 9 key toggles it.
//  4. 0, 1, 2, 9 are picked up as bare digits with no modifier.
//  5. F toggles full screen: the title bar, the menu bar and the Dock all
//     disappear, and the window covers the whole display.
//  6. The Korean caption line renders through the real OpenGL painter, not
//     just the software one F1 measured.
package main

import (
	"fmt"
	"image/color"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"
)

const captionSample = "영업이익률 up 14%. Dr. Smith agrees."

func main() {
	a := app.NewWithID("com.LiveCap.spike.f2")
	w := a.NewWindow("LiveCap — F2 menu spike")

	// --- the caption line, painted by the real GL renderer ---------------
	caption := canvas.NewText(captionSample, color.White)
	caption.TextSize = 44
	box := container.NewStack(
		canvas.NewRectangle(color.NRGBA{A: 0xcc}),
		container.NewPadded(container.NewCenter(caption)),
	)

	log := widget.NewLabel("keys pressed: (none yet)")
	log.Wrapping = fyne.TextWrapWord
	state := widget.NewLabel("")

	// --- state the menu and the keys share -------------------------------
	completePhrases := false
	translating := false
	direction := "KO → EN"
	pressed := []string{}

	var completeItem *fyne.MenuItem
	var mainMenu *fyne.MainMenu

	refresh := func() {
		completeItem.Checked = completePhrases
		mainMenu.Refresh()
		state.SetText(fmt.Sprintf(
			"translating: %v    direction: %s    complete phrases only: %v",
			translating, direction, completePhrases))
	}

	note := func(key string) {
		pressed = append(pressed, key)
		if len(pressed) > 12 {
			pressed = pressed[len(pressed)-12:]
		}
		log.SetText("keys pressed: " + fmt.Sprint(pressed))
	}

	// --- the menu bar ----------------------------------------------------
	completeItem = fyne.NewMenuItem("Complete phrases only (9)", func() {
		completePhrases = !completePhrases
		refresh()
	})
	completeItem.Checked = completePhrases

	startItem := fyne.NewMenuItem("Start translating (0)", func() {
		translating = true
		refresh()
	})
	stopItem := fyne.NewMenuItem("Stop translating (0)", func() {
		translating = false
		refresh()
	})

	mainMenu = fyne.NewMainMenu(
		fyne.NewMenu("File",
			fyne.NewMenuItem("Open transcript folder", func() { note("menu:transcripts") }),
		),
		fyne.NewMenu("Translate",
			startItem,
			stopItem,
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Korean → English (1)", func() { direction = "KO → EN"; refresh() }),
			fyne.NewMenuItem("English → Korean (2)", func() { direction = "EN → KO"; refresh() }),
		),
		fyne.NewMenu("Captions",
			completeItem,
			fyne.NewMenuItemSeparator(),
			fyne.NewMenuItem("Clear now", func() { note("menu:clear") }),
		),
		fyne.NewMenu("Help",
			fyne.NewMenuItem("About LiveCap", func() { note("menu:about") }),
		),
	)
	w.SetMainMenu(mainMenu)

	// --- bare-digit keys, with no modifier -------------------------------
	w.Canvas().SetOnTypedRune(func(r rune) {
		note(string(r))
		switch r {
		case '0':
			translating = !translating
		case '1':
			direction = "KO → EN"
		case '2':
			direction = "EN → KO"
		case '9':
			completePhrases = !completePhrases
		case 'f', 'F':
			w.SetFullScreen(!w.FullScreen())
		}
		refresh()
	})
	w.Canvas().SetOnTypedKey(func(ev *fyne.KeyEvent) {
		if ev.Name == fyne.KeyEscape && w.FullScreen() {
			w.SetFullScreen(false)
		}
	})

	checklist := widget.NewLabel(
		"1. App menu says LiveCap, with About / Preferences / Quit\n" +
			"2. Four menus: File, Translate, Captions, Help\n" +
			"3. Captions → Complete phrases only ticks and unticks\n" +
			"4. 0 1 2 9 register as bare digits below\n" +
			"5. F goes full screen — no title bar, no menu bar, no Dock\n" +
			"6. The Korean line above renders correctly through OpenGL")

	w.SetContent(container.NewVBox(box, state, checklist, log))
	w.Resize(fyne.NewSize(900, 520))
	refresh()

	// A caption that changes proves the canvas repaints under the menu.
	go func() {
		lines := []string{captionSample, "다시 한번 확인합니다. Revenue is up.", "Dr. Smith agrees."}
		for i := 0; ; i++ {
			time.Sleep(3 * time.Second)
			text := lines[i%len(lines)]
			fyne.Do(func() {
				caption.Text = text
				caption.Refresh()
			})
		}
	}()

	w.ShowAndRun()
}
