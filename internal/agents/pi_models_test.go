package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// emptyPiModels is a minimal valid models.json with no models but pi's own
// provider config (api/apiKey/baseUrl) that a sync must preserve verbatim.
const emptyPiModels = `{"providers":{"ollama":{"api":"openai-completions","apiKey":"ollama","baseUrl":"http://127.0.0.1:11434/v1","models":[]}}}`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readPiModels(t *testing.T, path string) piModelsFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// syncModels must add non-native models that are missing from models.json,
// marking each _launch: true, while preserving pi's provider config. If a
// rotation-selected model is not synced, pi falls back to its own default.
func TestPiSyncModelsAddsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
		{ID: "claude/native", ModelName: "native"},
	}}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if len(f.Providers.Ollama.Models) != 1 {
		t.Fatalf("models = %d, want 1 (native skipped)", len(f.Providers.Ollama.Models))
	}
	m := f.Providers.Ollama.Models[0]
	if m.ID != "deepseek-v4-pro:cloud" || !m.Launch {
		t.Errorf("model = %+v, want id deepseek-v4-pro:cloud with _launch true", m)
	}
	if f.Providers.Ollama.API != "openai-completions" || f.Providers.Ollama.BaseURL != "http://127.0.0.1:11434/v1" {
		t.Errorf("provider config not preserved: %+v", f.Providers.Ollama)
	}
}

// syncModels must be idempotent: running it twice must not duplicate entries.
// A duplicate would grow models.json on every launch.
func TestPiSyncModelsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("first syncModels: %v", err)
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("second syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if len(f.Providers.Ollama.Models) != 1 {
		t.Fatalf("models = %d, want 1 after two syncs", len(f.Providers.Ollama.Models))
	}
}

// syncModels must use ModelName (bare name) as the models.json id, not the
// provider-prefixed ID. pi's catalog keys on the bare name; using the prefixed
// ID would create an entry pi never matches.
func TestPiSyncModelsUsesModelName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.Models[0].ID != "deepseek-v4-pro:cloud" {
		t.Errorf("id = %q, want %q (bare ModelName)", f.Providers.Ollama.Models[0].ID, "deepseek-v4-pro:cloud")
	}
}

// syncModels must leave existing entries untouched, including ones marked
// _launch: false. Removing or flipping them would change pi's own catalog.
func TestPiSyncModelsPreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"ollama","baseUrl":"http://127.0.0.1:11434/v1","models":[{"_launch":false,"contextWindow":1000,"id":"manual-model","input":["text"],"reasoning":false}]}}}`)
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if len(f.Providers.Ollama.Models) != 2 {
		t.Fatalf("models = %d, want 2 (existing + added)", len(f.Providers.Ollama.Models))
	}
	if f.Providers.Ollama.Models[0].ID != "manual-model" || f.Providers.Ollama.Models[0].Launch {
		t.Errorf("existing entry changed: %+v", f.Providers.Ollama.Models[0])
	}
}

// syncModels must return nil (not an error) when models.json does not exist.
// A missing catalog is not a failure — there is simply nothing to sync.
func TestPiSyncModelsMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels on missing file = %v, want nil", err)
	}
}

// isLaunchable must return true only when the model is present AND marked
// _launch: true. A model present but _launch: false must not be launched.
func TestIsLaunchable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"good"},{"_launch":false,"id":"orphan"}]}}}`)
	if !isLaunchable("good", path) {
		t.Error("isLaunchable(good) = false, want true")
	}
	if isLaunchable("orphan", path) {
		t.Error("isLaunchable(orphan) = true, want false (_launch false)")
	}
	if isLaunchable("missing", path) {
		t.Error("isLaunchable(missing) = true, want false")
	}
}

// isLaunchable must return false (not panic) when models.json is missing or
// unparseable, so the caller falls back to pi's default model.
func TestIsLaunchableMissingOrCorrupt(t *testing.T) {
	if isLaunchable("x", filepath.Join(t.TempDir(), "nope.json")) {
		t.Error("isLaunchable on missing file = true, want false")
	}
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{not valid json`)
	if isLaunchable("x", path) {
		t.Error("isLaunchable on corrupt file = true, want false")
	}
}
