package translator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// KeyTestEndpoint is the cheapest authenticated call the API offers: listing
// models is free and needs no Live session.
//
// A variable rather than a constant so a test can point it at a dead address
// and check what the resulting error says. Nothing outside a test changes it.
var KeyTestEndpoint = "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1"

// TestAPIKey does one cheap authenticated call, so Preferences can say whether
// a key works before a session is opened.
//
// Deliberately plain HTTP rather than the SDK: listing models is free, needs no
// Live session, and returns a readable error.
//
// The key goes in the x-goog-api-key header, never the query string. It used to
// ride in the URL, and that put it in the error text of every transport-level
// failure — net/http wraps those in a *url.Error whose Error() prints the whole
// URL. Preferences shows that string verbatim, so a DNS failure or a timeout
// printed the key in clear on screen:
//
//	Key rejected: Get "…/models?pageSize=1&key=AIzaSy…": dial tcp: no such host
//
// Measured 2026-08-08: the endpoint reads the header and the query string
// identically — a bad key answers API_KEY_INVALID either way — so nothing is
// given up by moving it. The key is never logged.
func TestAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("no key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, KeyTestEndpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-goog-api-key", key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	if reason := FatalReason(errors.New(string(body))); reason != "" {
		return errors.New(reason)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
