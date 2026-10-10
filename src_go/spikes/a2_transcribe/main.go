// Spike A2 — is this stretch of the model's audio a word, or an artifact?
//
// Transcribes WAV clips with a general Gemini model, so a question like "does
// removing this 200 ms lose a word" can be answered by a machine instead of by
// someone listening. Not the live model: this is offline measurement.
//
//	go run ./spikes/a2_transcribe -list
//	go run ./spikes/a2_transcribe -model <name> clip1.wav clip2.wav ...
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/genai"

	"github.com/dennis2lee/live-translation/src_go/internal/config"
)

func main() {
	list := flag.Bool("list", false, "list models that can take audio")
	model := flag.String("model", "", "model to transcribe with")
	flag.Parse()

	key := config.LoadAPIKey()
	if key == "" {
		fmt.Fprintln(os.Stderr, "no API key")
		os.Exit(1)
	}
	ctx := context.Background()
	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: key, Backend: genai.BackendGeminiAPI})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if *list {
		for m, err := range client.Models.All(ctx) {
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			if strings.Contains(m.Name, "flash") || strings.Contains(m.Name, "pro") {
				fmt.Println(m.Name, m.SupportedActions)
			}
		}
		return
	}
	temp := float32(0)
	prompt := "Transcribe the English speech in this audio exactly, word for word. " +
		"Include partial words at the edges if they are intelligible. " +
		"Output only the transcript on one line. If there is no speech, output [none]."
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		resp, err := client.Models.GenerateContent(ctx, *model, []*genai.Content{{
			Role: "user",
			Parts: []*genai.Part{
				{InlineData: &genai.Blob{MIMEType: "audio/wav", Data: data}},
				{Text: prompt},
			},
		}}, &genai.GenerateContentConfig{Temperature: &temp})
		if err != nil {
			fmt.Printf("%s\tERROR %v\n", filepath.Base(path), err)
			continue
		}
		fmt.Printf("%s\t%s\n", filepath.Base(path), strings.TrimSpace(resp.Text()))
	}
}
