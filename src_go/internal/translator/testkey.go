package translator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TestAPIKey does one cheap authenticated call, so Preferences can say whether
// a key works before a session is opened.
//
// Deliberately plain HTTP rather than the SDK: listing models is free, needs no
// Live session, and returns a readable error. The key travels only in the query
// string of a TLS request to Google, and is never logged.
func TestAPIKey(key string) error {
	key = strings.TrimSpace(key)
	if key == "" {
		return errors.New("no key")
	}
	endpoint := "https://generativelanguage.googleapis.com/v1beta/models?pageSize=1&key=" +
		url.QueryEscape(key)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
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
