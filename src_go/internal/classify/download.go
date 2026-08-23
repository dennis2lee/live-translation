package classify

// Fetching the model. Once, on first enable, ~3 MB compressed; never at
// inference time.
//
// The canonical source is Kaggle's public model API. Facts measured in spike
// Y1 (2026-08-23), all from this machine, anonymous:
//
//   - The slug is case-sensitive: the lowercase form below answers 200 and
//     redirects to a signed Google Storage URL; the "tfLite" casing shown in
//     Kaggle's own UI answers 404.
//   - No authentication, no cookies. The response is a tar.gz of 3 240 957
//     bytes holding exactly one file, 1.tflite — byte-identical across
//     downloads, so the inner file's SHA-256 is pinned (ModelSHA256).
//   - TF Hub, the model's original home, is dead: both the tfhub.dev URL and
//     the storage.googleapis.com/tfhub-lite-models bucket answer 403/404 now.
//     Do not "fix" the URL back to those.
//
// The archive hash is deliberately not pinned — a re-tar on Kaggle's side
// would change it without changing the model. The inner file hash is the
// contract, and LoadModel checks it again on every load anyway.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// ModelURL is the anonymous download endpoint. See the measurement notes
	// above before changing anything about it, including the casing.
	ModelURL = "https://www.kaggle.com/api/v1/models/google/yamnet/tflite/classification-tflite/1/download"

	// downloadCap bounds both the archive and the extracted file. The real
	// sizes are ~3.2 MB and ~4.1 MB; anything past this is not the model.
	downloadCap = 32 << 20

	// downloadTimeout covers the whole fetch. The archive is ~3 MB, so even a
	// slow link finishes in seconds; a minute means something is wrong, and
	// the settings window must not hang longer than that on a click.
	downloadTimeout = 60 * time.Second

	// userAgent, because CDNs 403 anonymous Go clients — the lesson netcheck
	// learned from Cloudflare, applied before it hurts here.
	userAgent = "LiveCap-model-fetch"
)

// DownloadModel fetches, verifies and installs the model at dst, atomically.
// progress, when non-nil, is called with bytes received so far and the total
// (or -1 when the server did not say).
func DownloadModel(ctx context.Context, dst string, progress func(done, total int64)) error {
	ctx, cancel := context.WithTimeout(ctx, downloadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ModelURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("model download answered %s", resp.Status)
	}

	body := io.Reader(io.LimitReader(resp.Body, downloadCap))
	if progress != nil {
		body = &progressReader{r: body, total: resp.ContentLength, report: progress}
	}

	pcm, err := extractModel(body)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(pcm)
	if got := hex.EncodeToString(sum[:]); got != ModelSHA256 {
		return fmt.Errorf("downloaded model does not match the pinned hash (got %s) — refusing to install it", got)
	}

	// Atomic install: a crash mid-write must not leave a torn file that every
	// later load rejects with a confusing hash error.
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	if err := os.WriteFile(tmp, pcm, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

// extractModel pulls the single .tflite out of the tar.gz bundle.
func extractModel(r io.Reader) ([]byte, error) {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("the download is not the expected archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, fmt.Errorf("no .tflite file in the archive")
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".tflite") {
			continue
		}
		return io.ReadAll(io.LimitReader(tr, downloadCap))
	}
}

type progressReader struct {
	r      io.Reader
	done   int64
	total  int64
	report func(done, total int64)
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.done += int64(n)
	total := p.total
	if total <= 0 {
		total = -1
	}
	p.report(p.done, total)
	return n, err
}

// ModelPath is where the application keeps its copy: beside the log and the
// settings, in the per-user application-support directory.
func ModelPath(appSupportDir string) string {
	return filepath.Join(appSupportDir, "models", "yamnet.tflite")
}
