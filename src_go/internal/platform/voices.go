package platform

// VoiceInstallHint is where the user adds a speech voice on this platform.
//
// It is shown in Preferences when the target language has no voice installed,
// and it has to name the actual screen: "install a voice" without a path is a
// dead end, and the path differs on every platform.
func VoiceInstallHint() string { return voiceInstallHintFor(runtimeGOOS()) }

func voiceInstallHintFor(goos string) string {
	switch goos {
	case "darwin":
		return "Add one in System Settings → Accessibility → Spoken Content → System Voice → Manage Voices."
	case "windows":
		// Settings adds the voice; only the ones registered for SAPI5 are
		// visible to System.Speech, which is what LiveCap reads. The natural
		// voices added under Narrator are not, and saying so here is cheaper
		// than the user installing one and finding it missing.
		return "Add one in Settings → Time & language → Speech → Manage voices. " +
			"Voices added under Narrator's \"natural voices\" are not usable here."
	default:
		return "Install a speech voice for this language and reopen Preferences."
	}
}
