package classify

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// One real fetch, opt-in: it spends ~3 MB of somebody's bandwidth.
func TestDownloadModelForReal(t *testing.T) {
	if os.Getenv("LIVECAP_NET_TESTS") == "" {
		t.Skip("set LIVECAP_NET_TESTS=1 to hit the network")
	}
	dst := filepath.Join(t.TempDir(), "models", "yamnet.tflite")
	var last int64
	err := DownloadModel(context.Background(), dst, func(done, total int64) { last = done })
	if err != nil {
		t.Fatal(err)
	}
	if last == 0 {
		t.Error("the progress callback never fired")
	}
	if _, err := LoadModel(dst); err != nil {
		t.Fatalf("the downloaded model does not load: %v", err)
	}
}
