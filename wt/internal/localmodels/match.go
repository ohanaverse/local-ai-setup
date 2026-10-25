// Package localmodels inventories local models: what is pulled or on disk
// (registered or not) and what is serving right now. Discovery is HTTP and
// filesystem only — wt never shells out for it — and running-state is a live
// probe, never a stored flag.
package localmodels

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// NameMatches reports whether a served/on-disk model name names the same model
// as want. Lenient on prefix (a server may report a path- or org-prefixed
// spelling), strict on the variant tail — 4-bit and 8-bit variants differ
// exactly there, so a mismatched tail is a different model.
func NameMatches(served, want string) bool {
	return served == want || strings.HasSuffix(served, "/"+want) || strings.HasSuffix(want, "/"+served)
}

// OllamaNameMatches reports whether ollama's name have satisfies want: exact,
// or — when want carries no ":" tag — have is want with ollama's implicit
// ":latest" tag.
func OllamaNameMatches(have, want string) bool {
	if have == want {
		return true
	}
	return !strings.Contains(want, ":") && have == want+":latest"
}

// FetchModelIDs GETs an OpenAI-compatible /v1/models endpoint and returns the
// ids the server is serving, or nil on any failure. Because nil cannot
// distinguish "nothing is serving" from "could not ask", callers that need
// that distinction must use FetchModelIDsErr instead.
func FetchModelIDs(client *http.Client, url string) []string {
	ids, err := FetchModelIDsErr(client, url)
	if err != nil {
		return nil
	}
	return ids
}

// FetchModelIDsErr is FetchModelIDs with the failure mode preserved. A non-nil
// error means the server gave no usable answer (refused connection, timeout,
// non-2xx, undecodable body), so an empty id list must not be read as "nothing
// is serving". A nil error with zero ids means the server answered and is
// serving nothing.
func FetchModelIDsErr(client *http.Client, url string) ([]string, error) {
	return FetchModelIDsKeyed(client, url, "")
}

// ErrKeyRefused is the error a probe wraps when the server answered 401 or
// 403: it is up and understood the request, and turns it away for want of a
// valid API key. A caller tells it apart with errors.Is — it is never "down".
var ErrKeyRefused = errors.New("refused for want of a valid API key")

// FetchModelIDsKeyed is FetchModelIDsErr with key, when there is one, sent as
// a Bearer token. With key "" the request carries no Authorization header at
// all: a provider with no secret_ref is asked exactly as it always was. A 401
// or 403 answer is an error wrapping ErrKeyRefused.
func FetchModelIDsKeyed(client *http.Client, url, key string) ([]string, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, fmt.Errorf("GET %s: status %d: %w", url, resp.StatusCode, ErrKeyRefused)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("GET %s: %w", url, err)
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids, nil
}
