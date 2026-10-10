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

// OpenAIKeyTestEndpoint asks for the translation model itself, which is free
// and answers both questions at once: is the key good, and can it see the
// model. It cannot say whether the account has credit — nothing short of a
// session can, and that is billed.
var OpenAIKeyTestEndpoint = "https://api.openai.com/v1/models/" + OpenAIModel

// TestKeyFor tests a key against the provider it is for.
func TestKeyFor(provider, key string) error {
	if provider == ProviderOpenAI {
		return testKey(OpenAIKeyTestEndpoint, key, func(r *http.Request, k string) {
			r.Header.Set("Authorization", "Bearer "+k)
		})
	}
	return TestAPIKey(key)
}

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
	return testKey(KeyTestEndpoint, key, func(r *http.Request, k string) {
		r.Header.Set("x-goog-api-key", k)
	})
}

// testKey makes one GET with the key in whatever header the provider reads.
func testKey(endpoint, key string, authorise func(*http.Request, string)) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("no key")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	authorise(req, key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 400))
	// FatalReason's messages never quote the body, which matters here:
	// OpenAI's echoes the key that was sent.
	if reason := FatalReason(errors.New(string(body))); reason != "" {
		return errors.New(reason)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
