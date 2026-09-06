package tts

// The portable half of the Windows backend.
//
// Windows has no `say`. What it has is SAPI5, reachable from PowerShell
// through System.Speech — no cgo, no bundled runtime, the same reasoning that
// made `say` the macOS backend and keeps the resampler pure Go.
//
// Everything here is pure and tested on any platform; say_windows.go holds
// nothing but the two exec calls, exactly as say_darwin.go does.
//
// Two things about driving PowerShell are load-bearing.
//
// The script is passed base64-encoded. `powershell.exe -Command` re-parses its
// argument, and Go's own Windows argument quoting and PowerShell's parser do
// not agree about quotes; -EncodedCommand takes UTF-16LE base64 and parses
// nothing, so there is no quoting to get wrong.
//
// The data is passed in the environment, never in the script. The text being
// spoken is a translation from a remote model — it is data, the same data the
// `[[…]]` rule in sanitize exists for — and pasting data into a script is
// handing that model a shell. `$env:X` is a variable read: its content is
// never parsed as code, whatever it contains.

import (
	"encoding/base64"
	"fmt"
	"strings"
	"unicode/utf16"
)

// The variables the scripts read their arguments from.
const (
	envVoice = "LIVECAP_TTS_VOICE"
	envText  = "LIVECAP_TTS_TEXT"
	envWAV   = "LIVECAP_TTS_WAV"
)

// listScript prints one "name<TAB>culture" line per installed voice.
//
// [Console]::Out rather than Write-Output, and UTF-8 rather than the console's
// code page, because a voice name is not guaranteed to be ASCII and the
// console's default encoding on a Korean Windows install is not.
const listScript = `
$ErrorActionPreference='Stop'
try{
[Console]::OutputEncoding=[System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Speech
$s=New-Object System.Speech.Synthesis.SpeechSynthesizer
foreach($v in $s.GetInstalledVoices()){
if(-not $v.Enabled){continue}
$i=$v.VoiceInfo
[Console]::Out.WriteLine($i.Name+"` + "`t" + `"+$i.Culture.Name)
}
$s.Dispose()
}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}
`

// synthScript renders $env:LIVECAP_TTS_TEXT to the WAV file named by
// $env:LIVECAP_TTS_WAV, in the voice named by $env:LIVECAP_TTS_VOICE.
//
// The format is asked for explicitly so SAPI resamples to Rate on its way
// out, rather than handing back whatever the voice happens to run at — the
// player's converter is then set up identically whichever voice speaks, which
// is the whole point of asking `say` for LEI16@24000 too.
func synthScript() string {
	return fmt.Sprintf(`
$ErrorActionPreference='Stop'
try{
Add-Type -AssemblyName System.Speech
$s=New-Object System.Speech.Synthesis.SpeechSynthesizer
$s.SelectVoice($env:%s)
$f=New-Object System.Speech.AudioFormat.SpeechAudioFormatInfo(%d,[System.Speech.AudioFormat.AudioBitsPerSample]::Sixteen,[System.Speech.AudioFormat.AudioChannel]::Mono)
$s.SetOutputToWaveFile($env:%s,$f)
$s.Speak($env:%s)
$s.SetOutputToNull()
$s.Dispose()
}catch{[Console]::Error.WriteLine($_.Exception.Message);exit 1}
`, envVoice, Rate, envWAV, envText)
}

// encodePowerShell renders a script for -EncodedCommand: UTF-16LE, base64.
func encodePowerShell(script string) string {
	units := utf16.Encode([]rune(script))
	raw := make([]byte, 0, len(units)*2)
	for _, u := range units {
		raw = append(raw, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// parseSAPIVoiceList reads what listScript prints:
//
//	Microsoft Zira Desktop	en-US
//	Microsoft Heami Desktop	ko-KR
//
// Tab-separated because a SAPI voice name holds spaces and the culture is the
// only other field; there is no comment column and no sample sentence, so the
// sample comes from sampleFor.
func parseSAPIVoiceList(out string) []Voice {
	var voices []Voice
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		name, culture, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		name = strings.TrimSpace(name)
		culture = strings.TrimSpace(culture)
		if name == "" || !looksLikeLocale(culture) {
			continue
		}
		voices = append(voices, Voice{
			Name:     name,
			Language: culture,
			Sample:   sampleFor(culture),
		})
	}
	return voices
}

// sampleFor is the sentence Preview speaks. macOS ships one per voice in its
// own language; SAPI ships none, so we carry them.
//
// These are not UI strings — they are the material a voice is judged on, and
// a Korean voice reading an English sentence tells the listener nothing about
// the voice. Same standing as the Korean fixture scripts under spikes/.
func sampleFor(language string) string {
	lang := strings.ToLower(strings.TrimSpace(language))
	if i := strings.IndexAny(lang, "-_"); i > 0 {
		lang = lang[:i]
	}
	switch lang {
	case "ko":
		return "이 목소리가 번역을 읽어 드립니다."
	case "ja":
		return "この声が翻訳を読み上げます。"
	case "zh":
		return "这个声音将朗读翻译。"
	default:
		return "This is the voice that will speak the translation."
	}
}

// redactedEnv is the environment a synthesis subprocess inherits: this
// process's, less anything that looks like a credential.
//
// PowerShell needs a working environment — SystemRoot, PATH, TEMP — so this
// copies rather than builds one. But the API key is in this process's
// environment on the documented path, and a child process is one more place
// it can be read out of; nothing under System.Speech has any business seeing
// it. The rule is a shape, not a list, so it keeps holding when a variable is
// added somewhere else.
func redactedEnv(environ []string) []string {
	kept := make([]string, 0, len(environ))
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		if looksSecret(name) {
			continue
		}
		kept = append(kept, kv)
	}
	return kept
}

func looksSecret(name string) bool {
	upper := strings.ToUpper(name)
	for _, mark := range []string{"API_KEY", "APIKEY", "SECRET", "TOKEN", "PASSWORD", "CREDENTIAL"} {
		if strings.Contains(upper, mark) {
			return true
		}
	}
	return false
}
