// Spike F1 — can Fyne render Korean captions?
//
// Renders the caption sample at caption size through Fyne's software painter
// and writes a PNG, so the glyphs can be inspected without a display. Three
// font sources are compared: Fyne's bundled theme font, Apple's Korean system
// faces loaded at runtime, and a mixed Korean/Latin line — the shape a real
// caption takes when a fragment splits mid-sentence.
//
//	go run ./spikes/f1_text
package main

import (
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/test"
)

// The line that has to work: Korean, Latin, a number and a full stop, all on
// one line, at the size the caption box actually paints.
const (
	sampleKO    = "영업이익률이 십사 퍼센트 올랐습니다."
	sampleMixed = "영업이익률 up 14%. Dr. Smith agrees."
	sampleEN    = "Revenue is up fourteen percent."
	captionSize = 44
)

// Candidate faces, in the order the plan prefers them.
var faces = []struct {
	label string
	path  string
}{
	{"theme default (Fyne bundled)", ""},
	{"AppleSDGothicNeo.ttc", "/System/Library/Fonts/AppleSDGothicNeo.ttc"},
	{"AppleGothic.ttf", "/System/Library/Fonts/Supplemental/AppleGothic.ttf"},
}

func loadFont(path string) (fyne.Resource, error) {
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return fyne.NewStaticResource(filepath.Base(path), data), nil
}

func line(text string, src fyne.Resource) *canvas.Text {
	t := canvas.NewText(text, color.White)
	t.TextSize = captionSize
	t.FontSource = src
	return t
}

func label(text string) *canvas.Text {
	t := canvas.NewText(text, color.NRGBA{R: 0x88, G: 0xcc, B: 0xff, A: 0xff})
	t.TextSize = 18
	return t
}

func main() {
	test.NewApp()

	rows := []fyne.CanvasObject{}
	for _, f := range faces {
		src, err := loadFont(f.path)
		if err != nil {
			fmt.Printf("  %-30s SKIPPED (%v)\n", f.label, err)
			continue
		}
		fmt.Printf("  %-30s loaded\n", f.label)
		rows = append(rows,
			label(f.label),
			line(sampleKO, src),
			line(sampleMixed, src),
			line(sampleEN, src),
		)
	}

	content := container.NewStack(
		canvas.NewRectangle(color.NRGBA{A: 0xff}),
		container.NewPadded(container.NewVBox(rows...)),
	)

	size := content.MinSize()
	fmt.Printf("content min size: %v\n", size)

	w := test.NewWindow(content)
	w.Resize(size)
	c := w.Canvas()

	out := "spikes/f1_text/out.png"
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		panic(err)
	}
	fh, err := os.Create(out)
	if err != nil {
		panic(err)
	}
	defer fh.Close()
	img := c.Capture()
	if err := png.Encode(fh, img); err != nil {
		panic(err)
	}
	fmt.Printf("wrote %s (%v)\n", out, img.Bounds().Size())
}
