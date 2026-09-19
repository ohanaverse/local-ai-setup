package localmodels

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNameMatches pins the omlx/mtplx matching rule: lenient on the prefix (a
// server may spell the same model with a path/org prefix), strict on the
// variant tail — 4-bit and 8-bit variants differ exactly there, so a
// mismatched tail must be a different model, never a spelling variant.
func TestNameMatches(t *testing.T) {
	cases := []struct {
		served, want string
		match        bool
	}{
		{"Qwen3.8-27B-4bit", "Qwen3.8-27B-4bit", true},
		{"Qwen3.8-27B-4bit", "mlx-community/Qwen3.8-27B-4bit", true},
		{"mlx-community/Qwen3.8-27B-4bit", "Qwen3.8-27B-4bit", true},
		{"Qwen3.8-27B-4bit", "mlx-community/Qwen3.8-27B-8bit", false},
		{"other", "Qwen3.8-27B-4bit", false},
	}
	for _, tc := range cases {
		if got := NameMatches(tc.served, tc.want); got != tc.match {
			t.Errorf("NameMatches(%q, %q) = %v, want %v", tc.served, tc.want, got, tc.match)
		}
	}
}

// TestOllamaNameMatches verifies ollama matching is exact, with the ":latest"
// fallback ollama itself applies to a tagless name — so a registry entry
// "llama3" finds the pulled "llama3:latest", but "llama3:8b" never matches
// "llama3:70b".
func TestOllamaNameMatches(t *testing.T) {
	cases := []struct {
		have, want string
		match      bool
	}{
		{"gemma4:9b", "gemma4:9b", true},
		{"llama3:latest", "llama3", true},
		{"llama3:8b", "llama3", false},
		{"llama3:70b", "llama3:8b", false},
	}
	for _, tc := range cases {
		if got := OllamaNameMatches(tc.have, tc.want); got != tc.match {
			t.Errorf("OllamaNameMatches(%q, %q) = %v, want %v", tc.have, tc.want, got, tc.match)
		}
	}
}

// TestFetchModelIDs verifies the /v1/models probe returns served ids on 2xx
// and nil on every failure (bad status, bad JSON, unreachable) — nil must
// read as "nothing serving", never as an error a caller could mistake for
// "unknown".
func TestFetchModelIDs(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`))
	}))
	defer ok.Close()
	client := &http.Client{Timeout: time.Second}
	if got := FetchModelIDs(client, ok.URL); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ok = %v, want [a b]", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	if got := FetchModelIDs(client, bad.URL); got != nil {
		t.Errorf("500 = %v, want nil", got)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) }))
	defer junk.Close()
	if got := FetchModelIDs(client, junk.URL); got != nil {
		t.Errorf("junk = %v, want nil", got)
	}
	if got := FetchModelIDs(client, "http://127.0.0.1:1/"); got != nil {
		t.Errorf("unreachable = %v, want nil", got)
	}
}
