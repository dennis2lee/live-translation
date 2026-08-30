package tts

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

// The real shape of `say -v '?'`, including the two hard cases: a name with
// spaces and nested parentheses, and a language with no region.
const voiceList = `Albert              en_US    # Hello! My name is Albert.
Alice               it_IT    # Ciao! Mi chiamo Alice.
Bad News            en_US    # Hello! My name is Bad News.
Eddy (English (US)) en_US    # Hello! My name is Eddy.
Eddy (Korean (South Korea)) ko_KR    # 안녕하세요. 제 이름은 Eddy입니다.
Yuna                ko_KR    # 안녕하세요. 제 이름은 Yuna입니다.
`

func TestParsingTheVoiceList(t *testing.T) {
	got := parseVoiceList(voiceList)
	if len(got) != 6 {
		t.Fatalf("parsed %d voices, want 6: %+v", len(got), got)
	}
	// A name is not the first field: it can hold spaces and parentheses, and
	// `say -v` wants it back in full.
	want := map[string]string{
		"Albert":                      "en_US",
		"Bad News":                    "en_US",
		"Eddy (English (US))":         "en_US",
		"Eddy (Korean (South Korea))": "ko_KR",
		"Yuna":                        "ko_KR",
	}
	byName := map[string]string{}
	for _, v := range got {
		byName[v.Name] = v.Language
	}
	for name, lang := range want {
		if byName[name] != lang {
			t.Errorf("voice %q: language %q, want %q", name, byName[name], lang)
		}
	}
	for _, v := range got {
		if v.Sample == "" {
			t.Errorf("voice %q has no sample sentence — Preview would have nothing to say", v.Name)
		}
	}
}

func TestParsingIgnoresProse(t *testing.T) {
	got := parseVoiceList("some tool printed a warning here\n\nAlbert  en_US  # Hi.\n")
	if len(got) != 1 || got[0].Name != "Albert" {
		t.Errorf("parsed %+v, want just Albert", got)
	}
}

func TestVoicesAreFilteredByTargetLanguage(t *testing.T) {
	voices := parseVoiceList(voiceList)
	var en, ko int
	for _, v := range voices {
		if v.Matches("en") {
			en++
		}
		if v.Matches("ko") {
			ko++
		}
	}
	if en != 3 || ko != 2 {
		t.Errorf("en=%d ko=%d, want 3 and 2", en, ko)
	}
	// The setting is a bare code and the list a locale; a region-qualified
	// target must still match rather than filtering the list to nothing.
	if !(Voice{Language: "en_GB"}).Matches("en-US") {
		t.Error("en_GB did not match target en-US")
	}
	if (Voice{Language: "en_US"}).Matches("ko") {
		t.Error("en_US matched target ko")
	}
}

// say acts on [[…]] rather than reading it aloud — measured. The text being
// spoken is a translation from a remote model, so it is data, and data must
// not be able to retune the voice the user chose.
func TestEmbeddedCommandsAreStrippedBeforeSpeaking(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"[[pbas 20]] hello", "hello"},
		{"before [[rate 400]] after", "before   after"},
		{"unterminated [[pbas 20", "unterminated"},
		{"[[volm 0]]", ""},
		{"ordinary text", "ordinary text"},
	} {
		if got := sanitize(tc.in); got != tc.want {
			t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadingBackAWAV(t *testing.T) {
	body := []byte{1, 0, 2, 0, 3, 0}
	var w []byte
	w = append(w, "RIFF"...)
	w = binary.LittleEndian.AppendUint32(w, uint32(36+len(body)))
	w = append(w, "WAVEfmt "...)
	w = binary.LittleEndian.AppendUint32(w, 16)
	w = binary.LittleEndian.AppendUint16(w, 1)
	w = binary.LittleEndian.AppendUint16(w, 1)
	w = binary.LittleEndian.AppendUint32(w, Rate)
	w = binary.LittleEndian.AppendUint32(w, Rate*2)
	w = binary.LittleEndian.AppendUint16(w, 2)
	w = binary.LittleEndian.AppendUint16(w, 16)
	w = append(w, "data"...)
	w = binary.LittleEndian.AppendUint32(w, uint32(len(body)))
	w = append(w, body...)

	pcm, rate, err := pcmFromWAV(w)
	if err != nil {
		t.Fatal(err)
	}
	if rate != Rate {
		t.Errorf("rate %d, want %d", rate, Rate)
	}
	if string(pcm) != string(body) {
		t.Errorf("pcm %v, want %v", pcm, body)
	}
	if _, _, err := pcmFromWAV([]byte("not a wav at all")); err == nil {
		t.Error("garbage parsed as a WAV")
	}
}

// --- the speaker -----------------------------------------------------------

// fakeSynth returns n milliseconds of audio per call and records what it was
// asked to say.
func fakeSynth(ms int, said *[]string, mu *sync.Mutex, fail error) func(context.Context, string, string) ([]byte, error) {
	return func(_ context.Context, voice, text string) ([]byte, error) {
		mu.Lock()
		*said = append(*said, voice+"|"+text)
		mu.Unlock()
		if fail != nil {
			return nil, fail
		}
		return make([]byte, ms*Rate/1000*2), nil
	}
}

func TestTheSpeakerSpeaksQueuedLinesInOrder(t *testing.T) {
	var mu sync.Mutex
	var said []string
	var got []byte
	s := newSpeakerWith("Yuna", func(b []byte) {
		mu.Lock()
		got = append(got, b...)
		mu.Unlock()
	}, fakeSynth(50, &said, &mu, nil))
	defer s.Stop()

	s.Say("one")
	s.Say("two")
	waitFor(t, func() bool {
		spoken, _, _ := s.Stats()
		return spoken == 2
	}, "two lines to be spoken")

	mu.Lock()
	defer mu.Unlock()
	if len(said) != 2 || said[0] != "Yuna|one" || said[1] != "Yuna|two" {
		t.Errorf("synthesized %v, want one then two in the chosen voice", said)
	}
	if want := 2 * 50 * Rate / 1000 * 2; len(got) != want {
		t.Errorf("emitted %d bytes, want %d", len(got), want)
	}
}

// Falling behind must cost the oldest line, not the newest. A listener wants
// what is being said now; playRing decides the same way for the same reason.
func TestABacklogDropsTheOldestLine(t *testing.T) {
	var mu sync.Mutex
	var said []string
	release := make(chan struct{})
	s := newSpeakerWith("V", func([]byte) {}, func(ctx context.Context, voice, text string) ([]byte, error) {
		mu.Lock()
		said = append(said, text)
		mu.Unlock()
		<-release // hold the worker inside the first utterance
		return nil, nil
	})
	defer func() { close(release); s.Stop() }()

	s.Say("first")
	waitFor(t, func() bool { mu.Lock(); defer mu.Unlock(); return len(said) == 1 }, "the worker to start")

	for _, line := range []string{"a", "b", "c", "d", "e"} {
		s.Say(line)
	}
	if _, dropped, _ := s.Stats(); dropped != 2 {
		t.Errorf("dropped %d lines, want 2 (queue holds %d)", dropped, queueDepth)
	}
	s.mu.Lock()
	queued := append([]string(nil), s.queue...)
	s.mu.Unlock()
	if len(queued) != queueDepth || queued[0] != "c" || queued[queueDepth-1] != "e" {
		t.Errorf("queue holds %v, want the newest %d lines", queued, queueDepth)
	}
}

// A voice uninstalled after it was chosen, or any other synthesis failure,
// must not take the captions down. Captions are the product.
func TestSynthesisFailureIsNotFatal(t *testing.T) {
	var mu sync.Mutex
	var said []string
	s := newSpeakerWith("Gone", func([]byte) {}, fakeSynth(10, &said, &mu, errors.New("no such voice")))
	defer s.Stop()

	s.Say("hello")
	waitFor(t, func() bool {
		_, _, failed := s.Stats()
		return failed == 1
	}, "the failure to be counted")

	// And the speaker is still alive to try the next line.
	s.Say("again")
	waitFor(t, func() bool {
		_, _, failed := s.Stats()
		return failed == 2
	}, "the speaker to keep going after a failure")
}

func TestSetVoiceAppliesToTheNextLine(t *testing.T) {
	var mu sync.Mutex
	var said []string
	s := newSpeakerWith("Old", func([]byte) {}, fakeSynth(10, &said, &mu, nil))
	defer s.Stop()

	s.Say("one")
	waitFor(t, func() bool {
		spoken, _, _ := s.Stats()
		return spoken == 1
	}, "the first line")
	s.SetVoice("New")
	s.Say("two")
	waitFor(t, func() bool {
		spoken, _, _ := s.Stats()
		return spoken == 2
	}, "the second line")

	mu.Lock()
	defer mu.Unlock()
	if said[0] != "Old|one" || said[1] != "New|two" {
		t.Errorf("synthesized %v, want the voice to change on the second line", said)
	}
}

func TestStopIsIdempotentAndSilencesTheQueue(t *testing.T) {
	var mu sync.Mutex
	var said []string
	s := newSpeakerWith("V", func([]byte) {}, fakeSynth(10, &said, &mu, nil))
	s.Stop()
	s.Stop() // must not panic or block
	s.Say("after stop")
	time.Sleep(20 * time.Millisecond)
	if spoken, _, _ := s.Stats(); spoken != 0 {
		t.Errorf("spoke %d lines after Stop", spoken)
	}
}

// The player's ring holds PlayRingMS; handing it a whole utterance at once
// would overflow it and drop most of the words. The speaker must never run
// more than leadMS ahead of realtime.
func TestTheSpeakerPacesItselfToTheRing(t *testing.T) {
	var mu sync.Mutex
	var said []string
	type mark struct {
		at    time.Duration
		bytes int
	}
	start := time.Now()
	var marks []mark
	total := 0
	s := newSpeakerWith("V", func(b []byte) {
		mu.Lock()
		total += len(b)
		marks = append(marks, mark{time.Since(start), total})
		mu.Unlock()
	}, fakeSynth(3000, &said, &mu, nil)) // three seconds of audio at once
	defer s.Stop()

	s.Say("a long sentence")
	waitFor(t, func() bool {
		spoken, _, _ := s.Stats()
		return spoken == 1
	}, "the utterance to finish")

	mu.Lock()
	defer mu.Unlock()
	worst := time.Duration(0)
	for _, m := range marks {
		played := time.Duration(m.bytes) * time.Second / time.Duration(Rate*2)
		if ahead := played - m.at; ahead > worst {
			worst = ahead
		}
	}
	t.Logf("peak lead over realtime: %v (limit %dms)", worst.Round(time.Millisecond), leadMS)
	if worst > (leadMS+250)*time.Millisecond {
		t.Errorf("ran %v ahead of realtime — the player's ring is %d ms and would drop audio",
			worst, 2000)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// The one test that touches the real synthesizer, skipped where it is absent.
func TestTheRealSynthesizerProducesTheRateThePlayerExpects(t *testing.T) {
	if !Available() {
		t.Skip("no `say` on this machine")
	}
	voices, err := Voices(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(voices) == 0 {
		t.Skip("no voices installed")
	}
	var voice string
	for _, v := range voices {
		if v.Matches("en") {
			voice = v.Name
			break
		}
	}
	if voice == "" {
		t.Skip("no English voice installed")
	}
	pcm, err := Synthesize(context.Background(), voice, "The quick brown fox.")
	if err != nil {
		t.Fatal(err)
	}
	if len(pcm) < Rate/2 {
		t.Errorf("%q produced %d bytes — under a quarter second of audio", voice, len(pcm))
	}
	if len(pcm)%2 != 0 {
		t.Error("odd byte count — that is not PCM16")
	}
	if strings.Contains(voice, "|") {
		t.Skip()
	}
}
