package localmodels

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestOllamaModelNames verifies decoding of ollama's tags/ps body and that
// cloud entries (non-empty remote_host) are excluded — they are hosted at
// ollama.com, not local models, and would otherwise show up as local
// artifacts in the selector.
func TestOllamaModelNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen3.8:27b-mlx"},
			{"name":"kimi-k3:cloud","remote_host":"https://ollama.com"},
			{"name":"medgemma:27b","remote_host":""}]}`))
	}))
	defer srv.Close()
	got, err := ollamaModelNames(&http.Client{Timeout: time.Second}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"qwen3.8:27b-mlx", "medgemma:27b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestOllamaModelNamesErrors verifies a non-2xx status, bad JSON and an
// unreachable daemon are all errors, so the inventory can report the ollama
// provider as unreachable instead of silently listing nothing.
func TestOllamaModelNamesErrors(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	if _, err := ollamaModelNames(client, bad.URL); err == nil {
		t.Error("500: want error")
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("nope")) }))
	defer junk.Close()
	if _, err := ollamaModelNames(client, junk.URL); err == nil {
		t.Error("bad json: want error")
	}
	if _, err := ollamaModelNames(client, "http://127.0.0.1:1/"); err == nil {
		t.Error("unreachable: want error")
	}
}

// TestScanModelDirs verifies only directories count as models (loose files
// like .DS_Store do not), a symlink to a directory counts (users symlink
// model dirs into the cache), and a missing directory is "no models", not
// an error — a fresh install has no ~/.omlx/models yet.
func TestScanModelDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Qwen3.8-27B-4bit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	got, err := scanModelDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"Qwen3.8-27B-4bit", "linked"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, err := scanModelDirs(filepath.Join(root, "missing")); got != nil || err != nil {
		t.Errorf("missing dir = %v, %v; want nil, nil", got, err)
	}
}

// TestMtplxRepoID verifies mtplx's "<org>--<model>" directory names map back
// to "org/model" — including every "--" — since the registry stores the
// slash form and matching/ids depend on it.
func TestMtplxRepoID(t *testing.T) {
	if got := mtplxRepoID("Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"); got != "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" {
		t.Errorf("got %q", got)
	}
	if got := mtplxRepoID("org--sub--model"); got != "org/sub/model" {
		t.Errorf("got %q", got)
	}
	if got := mtplxRepoID("plain"); got != "plain" {
		t.Errorf("got %q", got)
	}
}
