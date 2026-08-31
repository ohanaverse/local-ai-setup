package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
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
// marking each _launch: true, while resetting the ollama provider to the
// standard local endpoint in direct mode. Other pi provider fields are preserved.
func TestPiSyncModelsAddsMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
		{ID: "claude/native", ModelName: "native", Native: true},
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
	if f.Providers.Ollama.API != "openai-completions" {
		t.Errorf("api not preserved: %+v", f.Providers.Ollama)
	}
	if f.Providers.Ollama.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q", f.Providers.Ollama.BaseURL, defaultPiOllamaBaseURL)
	}
	if f.Providers.Ollama.APIKey != "" {
		t.Errorf("apiKey = %q, want empty", f.Providers.Ollama.APIKey)
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

// syncModels in litellm mode must key entries by registry id and point pi's
// ollama provider at the LiteLLM gateway baseUrl with the configured apiKey.
func TestSyncModelsLitellm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	writeFile(t, path, emptyPiModels)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("baseUrl = %q, want %q", f.Providers.Ollama.BaseURL, "http://localhost:4000/v1")
	}
	if f.Providers.Ollama.APIKey != "sk-litellm" {
		t.Errorf("apiKey = %q, want %q", f.Providers.Ollama.APIKey, "sk-litellm")
	}
	if len(f.Providers.Ollama.Models) != 1 || f.Providers.Ollama.Models[0].ID != "ollama/qwen3.8:27b-mlx" {
		t.Errorf("unexpected models: %v", f.Providers.Ollama.Models)
	}
}

// syncModels in direct mode must revert a previously-gateway ollama provider
// config back to the local Ollama endpoint. If gateway settings were sticky,
// disabling gateway mode would keep pi routing through LiteLLM. The gateway
// config still carries the url/api_key the user flipped to "direct", so the
// revert can match the values wt itself wrote.
func TestSyncModelsDirectRevertsGatewayProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"sk-litellm","baseUrl":"http://localhost:4000/v1","models":[]}}}`)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "direct", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q", f.Providers.Ollama.BaseURL, defaultPiOllamaBaseURL)
	}
	if f.Providers.Ollama.APIKey != "" {
		t.Errorf("apiKey = %q, want empty", f.Providers.Ollama.APIKey)
	}
}

// syncModels in direct mode must still revert gateway provider settings when
// no new models are added (added==0). The early-return must not skip the write
// when the provider config still needs reverting — otherwise disabling gateway
// mode leaves pi pointed at LiteLLM forever.
func TestSyncModelsDirectRevertsWhenNoModelsAdded(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	// The model is already present (bare ModelName from an earlier direct-mode
	// sync) but the provider block still points at the gateway from a later
	// litellm-mode run.
	writeFile(t, path, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"sk-litellm","baseUrl":"http://localhost:4000/v1","models":[{"_launch":true,"id":"qwen3.8:27b-mlx"}]}}}`)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "direct", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q (gateway settings not reverted)", f.Providers.Ollama.BaseURL, defaultPiOllamaBaseURL)
	}
	if f.Providers.Ollama.APIKey != "" {
		t.Errorf("apiKey = %q, want empty", f.Providers.Ollama.APIKey)
	}
}

// syncModels in direct mode must preserve a user's own custom pi provider
// config verbatim. Only values that match the configured gateway are reverted;
// an arbitrary remote baseUrl/apiKey must survive a sync that adds a model.
func TestSyncModelsDirectPreservesCustomProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"sk-remote","baseUrl":"http://192.168.1.50:11434/v1","models":[]}}}`)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "direct"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != "http://192.168.1.50:11434/v1" {
		t.Errorf("baseUrl = %q, want custom value preserved", f.Providers.Ollama.BaseURL)
	}
	if f.Providers.Ollama.APIKey != "sk-remote" {
		t.Errorf("apiKey = %q, want custom value preserved", f.Providers.Ollama.APIKey)
	}
}

// syncModels in litellm mode must create models.json when it does not exist,
// so a fresh pi install routes through LiteLLM on the very first launch
// instead of silently bypassing the gateway.
func TestSyncModelsLitellmCreatesMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	if err := syncModels(cfg, path); err != nil {
		t.Fatalf("syncModels: %v", err)
	}
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("baseUrl = %q, want %q", f.Providers.Ollama.BaseURL, "http://localhost:4000/v1")
	}
	if f.Providers.Ollama.APIKey != "sk-litellm" {
		t.Errorf("apiKey = %q, want %q", f.Providers.Ollama.APIKey, "sk-litellm")
	}
	if len(f.Providers.Ollama.Models) != 1 || f.Providers.Ollama.Models[0].ID != "ollama/qwen3.8:27b-mlx" {
		t.Errorf("unexpected models: %v", f.Providers.Ollama.Models)
	}
}

// piDriver.Build in litellm mode must pass the registry id as --model when
// that id is present in pi's models.json.
func TestPiBuildLitellm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	piDir := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(piDir, "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)

	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := piDriver{}.Build(m, false, gw)
	// Switching between direct and litellm modes may leave both the bare
	// ModelName key and the provider-prefixed ID in pi's catalog. Both are
	// harmless: direct mode looks up ModelName, litellm mode looks up ID.
	if !slices.Equal(lc.Args, []string{"--model", "ollama/qwen3.8:27b-mlx"}) {
		t.Fatalf("expected --model with registry id, got %v", lc.Args)
	}
}

