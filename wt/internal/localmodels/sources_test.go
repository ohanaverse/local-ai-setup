package localmodels

import (
	"context"
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
	got, err := ollamaModelNames(context.Background(), &http.Client{Timeout: time.Second}, srv.URL)
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
	if _, err := ollamaModelNames(context.Background(), client, bad.URL); err == nil {
		t.Error("500: want error")
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("nope")) }))
	defer junk.Close()
	if _, err := ollamaModelNames(context.Background(), client, junk.URL); err == nil {
		t.Error("bad json: want error")
	}
	if _, err := ollamaModelNames(context.Background(), client, "http://127.0.0.1:1/"); err == nil {
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
	// Dot-directories (.cache, .locks) are tool bookkeeping, not models.
	if err := os.Mkdir(filepath.Join(root, ".cache"), 0o755); err != nil {
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

// TestScanOmlxModelsFollowsOmlxsTwoLevelRule verifies wt finds exactly the
// models omlx itself discovers, under the ids omlx serves them by. omlx looks
// two levels deep: a top-level directory with a config.json is a model, and
// one without is an organization folder whose children with a config.json are
// models named after the child. wt used to list every top-level directory, so
// a model at mlx-community/Qwen3.6-35B-A3B-6bit showed up as a phantom model
// "mlx-community" and the real one could be neither started nor stopped.
func TestScanOmlxModelsFollowsOmlxsTwoLevelRule(t *testing.T) {
	root := t.TempDir()
	write := func(rel string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("Flat-4bit/config.json")                          // (a) a flat model
	write("mlx-community/Qwen3.6-35B-A3B-6bit/config.json") // (b) an organization folder: two models...
	write("mlx-community/Gemma-9B-4bit/config.json")
	write("mlx-community/downloading/weights.bin") // ...and a child that is not one
	write("notes/readme.txt")                      // (c) neither a model nor a folder of models
	write("my-lora/config.json")                   // (d) a LoRA adapter
	write("my-lora/adapter_config.json")
	write("mlx-community/nested-lora/config.json") // an adapter inside an organization folder
	write("mlx-community/nested-lora/adapter_config.json")
	write(".cache/hidden/config.json")                     // (e) a dot-directory
	write("mlx-community/.locks/config.json")              // a dot-directory inside an organization folder
	write("other-org/Flat-4bit/config.json")               // (g) a child whose name duplicates (a)
	write("models--Org--Cached/snapshots/abc/config.json") // an HF-hub cache entry: not listed
	linked := t.TempDir()                                  // (f) a symlink to a model directory
	if err := os.WriteFile(filepath.Join(linked, "config.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(linked, filepath.Join(root, "Linked-8bit")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := scanOmlxModels(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Flat-4bit", "Gemma-9B-4bit", "Linked-8bit", "Qwen3.6-35B-A3B-6bit"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, err := scanOmlxModels(filepath.Join(root, "missing")); got != nil || err != nil {
		t.Errorf("missing dir = %v, %v; want nil, nil (a fresh install has no model dir yet)", got, err)
	}
}

// TestScanOmlxModelsSingleModelFallback verifies omlx's last rule: when the
// model directory holds no model at any level and is itself a model (it has a
// config.json), it is the one model, named after the directory. A user who
// points omlx's model dir straight at one model otherwise sees an empty list
// in wt for a model omlx is serving. The fallback must not fire when a real
// model was found.
func TestScanOmlxModelsSingleModelFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Solo-4bit")
	mkOmlxModels(t, filepath.Dir(root), "Solo-4bit")
	if err := os.Mkdir(filepath.Join(root, "tokenizer"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := scanOmlxModels(root)
	if err != nil || !reflect.DeepEqual(got, []string{"Solo-4bit"}) {
		t.Errorf("got %v, %v; want [Solo-4bit]", got, err)
	}
	mkOmlxModels(t, root, "Inner-4bit")
	got, err = scanOmlxModels(root)
	if err != nil || !reflect.DeepEqual(got, []string{"Inner-4bit"}) {
		t.Errorf("with a model inside: got %v, %v; want [Inner-4bit] only", got, err)
	}
}
