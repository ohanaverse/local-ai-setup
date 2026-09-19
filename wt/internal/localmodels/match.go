// Package localmodels inventories local models: what is pulled or on disk
// (registered or not) and what is serving right now. Discovery is HTTP and
// filesystem only — wt never shells out for it — and running-state is a live
// probe, never modelman's per-model running flag.
package localmodels

import (
	"encoding/json"
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
// ids of the models the server is serving; nil on any failure (connection
// refused, timeout, non-2xx, non-JSON body) — nil reads as "nothing serving",
// never as "unknown".
func FetchModelIDs(client *http.Client, url string) []string {
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids
}
