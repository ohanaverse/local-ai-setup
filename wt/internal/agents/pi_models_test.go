package agents

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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
	ol := f.Providers[piOllamaProviderID]
	if len(ol.Models) != 1 {
		t.Fatalf("models = %d, want 1 (native skipped)", len(ol.Models))
	}
	m := ol.Models[0]
	if m.ID != "deepseek-v4-pro:cloud" || !m.Launch {
		t.Errorf("model = %+v, want id deepseek-v4-pro:cloud with _launch true", m)
	}
	if ol.API != "openai-completions" {
		t.Errorf("api not preserved: %+v", ol)
	}
	if ol.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q", ol.BaseURL, defaultPiOllamaBaseURL)
	}
	if ol.APIKey != defaultPiOllamaAPIKey {
		t.Errorf("apiKey = %q, want %q (pi placeholder)", ol.APIKey, defaultPiOllamaAPIKey)
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
	if len(f.Providers[piOllamaProviderID].Models) != 1 {
		t.Fatalf("models = %d, want 1 after two syncs", len(f.Providers[piOllamaProviderID].Models))
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
	if f.Providers[piOllamaProviderID].Models[0].ID != "deepseek-v4-pro:cloud" {
		t.Errorf("id = %q, want %q (bare ModelName)", f.Providers[piOllamaProviderID].Models[0].ID, "deepseek-v4-pro:cloud")
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
	ol := f.Providers[piOllamaProviderID]
	if len(ol.Models) != 2 {
		t.Fatalf("models = %d, want 2 (existing + added)", len(ol.Models))
	}
	if ol.Models[0].ID != "manual-model" || ol.Models[0].Launch {
		t.Errorf("existing entry changed: %+v", ol.Models[0])
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

// isLaunchable must return true only when the model is present under the
// named provider AND marked _launch: true. A model present but _launch: false
// must not be launched; an entry under the wrong provider must not match.
func TestIsLaunchable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"good"},{"_launch":false,"id":"orphan"}]},"litellm":{"models":[{"_launch":true,"id":"ollama/good"}]}}}`)
	if !isLaunchable("ollama", "good", path) {
		t.Error("isLaunchable(ollama, good) = false, want true")
	}
	if isLaunchable("ollama", "orphan", path) {
		t.Error("isLaunchable(ollama, orphan) = true, want false (_launch false)")
	}
	if isLaunchable("ollama", "missing", path) {
		t.Error("isLaunchable(ollama, missing) = true, want false")
	}
	if !isLaunchable("litellm", "ollama/good", path) {
		t.Error("isLaunchable(litellm, ollama/good) = false, want true")
	}
	if isLaunchable("ollama", "ollama/good", path) {
		t.Error("isLaunchable(ollama, ollama/good) = true, want false (wrong provider)")
	}
}

// isLaunchable must return false (not panic) when models.json is missing or
// unparseable, so the caller falls back to pi's default model.
func TestIsLaunchableMissingOrCorrupt(t *testing.T) {
	if isLaunchable("ollama", "x", filepath.Join(t.TempDir(), "nope.json")) {
		t.Error("isLaunchable on missing file = true, want false")
	}
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, `{not valid json`)
	if isLaunchable("ollama", "x", path) {
		t.Error("isLaunchable on corrupt file = true, want false")
	}
}

// syncModels in litellm mode must put gateway models under a dedicated
// "litellm" provider keyed by full registry id, and leave pi's ollama provider
// alone. Under the "ollama" provider such entries are unreachable: pi splits
// --model on the first slash, so "ollama/<id>" would always resolve to the
// bare <id> entry and the bare name would be sent to LiteLLM (400).
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
	lw := f.Providers[piLitellmProviderID]
	if lw.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("litellm baseUrl = %q, want %q", lw.BaseURL, "http://localhost:4000/v1")
	}
	if lw.APIKey != "sk-litellm" {
		t.Errorf("litellm apiKey = %q, want %q", lw.APIKey, "sk-litellm")
	}
	if len(lw.Models) != 1 || lw.Models[0].ID != "ollama/qwen3.8:27b-mlx" || !lw.Models[0].Launch {
		t.Errorf("unexpected litellm models: %v", lw.Models)
	}
	ol := f.Providers[piOllamaProviderID]
	if ol.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("ollama baseUrl = %q, want local direct preserved", ol.BaseURL)
	}
	if len(ol.Models) != 0 {
		t.Errorf("ollama models = %v, want none added in litellm mode", ol.Models)
	}
}

// Switching to litellm mode must migrate the state an older wt sync left
// behind: pi's ollama provider restored from the gateway to the local endpoint
// (its bare entries 400 through LiteLLM), the un-launchable "ollama/…" entries
// pruned from it, and gateway entries created under the litellm provider
// instead. A second sync must be a no-op. Otherwise pi keeps sending bare
// model names to LiteLLM and every launch fails with "Invalid model name".
func TestSyncModelsLitellmDedicatedProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	writeFile(t, path, `{"providers":{"ollama":{"api":"openai-completions","apiKey":"sk-litellm","baseUrl":"http://localhost:4000/v1","models":[{"_launch":true,"id":"qwen3.8:27b-mlx"},{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"},{"_launch":false,"id":"user-model"}]}}}`)
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	for run := 1; run <= 2; run++ {
		if err := syncModels(cfg, path); err != nil {
			t.Fatalf("syncModels run %d: %v", run, err)
		}
	}
	f := readPiModels(t, path)
	ol := f.Providers[piOllamaProviderID]
	if ol.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("ollama baseUrl = %q, want %q (gateway redirect not reverted)", ol.BaseURL, defaultPiOllamaBaseURL)
	}
	if ol.APIKey != defaultPiOllamaAPIKey {
		t.Errorf("ollama apiKey = %q, want %q (pi placeholder; empty fails pi schema)", ol.APIKey, defaultPiOllamaAPIKey)
	}
	ids := []string{}
	for _, m := range ol.Models {
		ids = append(ids, m.ID)
	}
	if !slices.Equal(ids, []string{"qwen3.8:27b-mlx", "user-model"}) {
		t.Errorf("ollama models = %v, want bare entry + user model (prefixed artifact pruned)", ids)
	}
	lw := f.Providers[piLitellmProviderID]
	if len(lw.Models) != 1 || lw.Models[0].ID != "ollama/qwen3.8:27b-mlx" || !lw.Models[0].Launch {
		t.Errorf("unexpected litellm models: %v", lw.Models)
	}
}

// syncModels in direct mode must leave the litellm provider block alone: it
// is either wt-created (harmless stale routing the user can flip back) or a
// user's own provider config that must never be dropped.
func TestSyncModelsDirectPreservesLitellmProvider(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	writeFile(t, path, `{"providers":{"litellm":{"api":"openai-completions","apiKey":"sk-x","baseUrl":"http://localhost:4000/v1","models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]},"ollama":{"api":"openai-completions","apiKey":"ollama","baseUrl":"http://localhost:11434/v1","models":[]}}}`)
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
	lw := f.Providers[piLitellmProviderID]
	if len(lw.Models) != 1 || lw.BaseURL != "http://localhost:4000/v1" || lw.APIKey != "sk-x" {
		t.Errorf("litellm provider not preserved: %+v", lw)
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
	ol := f.Providers[piOllamaProviderID]
	if ol.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q", ol.BaseURL, defaultPiOllamaBaseURL)
	}
	if ol.APIKey != defaultPiOllamaAPIKey {
		t.Errorf("apiKey = %q, want %q (pi placeholder)", ol.APIKey, defaultPiOllamaAPIKey)
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
	ol := f.Providers[piOllamaProviderID]
	if ol.BaseURL != defaultPiOllamaBaseURL {
		t.Errorf("baseUrl = %q, want %q (gateway settings not reverted)", ol.BaseURL, defaultPiOllamaBaseURL)
	}
	if ol.APIKey != defaultPiOllamaAPIKey {
		t.Errorf("apiKey = %q, want %q (pi placeholder)", ol.APIKey, defaultPiOllamaAPIKey)
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
	ol := f.Providers[piOllamaProviderID]
	if ol.BaseURL != "http://192.168.1.50:11434/v1" {
		t.Errorf("baseUrl = %q, want custom value preserved", ol.BaseURL)
	}
	if ol.APIKey != "sk-remote" {
		t.Errorf("apiKey = %q, want custom value preserved", ol.APIKey)
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
	lw := f.Providers[piLitellmProviderID]
	if lw.BaseURL != "http://localhost:4000/v1" {
		t.Errorf("baseUrl = %q, want %q", lw.BaseURL, "http://localhost:4000/v1")
	}
	if lw.APIKey != "sk-litellm" {
		t.Errorf("apiKey = %q, want %q", lw.APIKey, "sk-litellm")
	}
	if len(lw.Models) != 1 || lw.Models[0].ID != "ollama/qwen3.8:27b-mlx" {
		t.Errorf("unexpected models: %v", lw.Models)
	}
}

// piDriver.Build in litellm mode must pass the registry id routed through the
// wt-created provider as --model ("litellm/<registry-id>"). pi splits --model
// on the first slash, so "litellm" must carry the entry while the full id —
// which itself contains a slash — lands verbatim in the request body that
// LiteLLM dispatches on. Passing the bare name or a bare "<id>" resolves to
// the wrong entry and 400s at the gateway.
func TestPiBuildLitellm(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	piDir := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(piDir, "models.json")
	writeFile(t, path, `{"providers":{"litellm":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)

	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := piDriver{}.Build(m, false, gw)
	if !slices.Equal(lc.Args, []string{"--model", "litellm/ollama/qwen3.8:27b-mlx"}) {
		t.Fatalf("expected --model litellm/<registry id>, got %v", lc.Args)
	}
}

// piDriver.Build in litellm mode must fall back to pi's default model with a
// warning when the gateway entry is not present/launchable in models.json —
// for example before the first sync or when the user disabled the entry.
func TestPiBuildLitellmNotConfigured(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	piDir := filepath.Join(dir, ".pi", "agent")
	if err := os.MkdirAll(piDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(piDir, "models.json"), `{"providers":{"ollama":{"models":[{"_launch":true,"id":"ollama/qwen3.8:27b-mlx"}]}}}`)

	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := piDriver{}.Build(m, false, gw)
	if len(lc.Args) != 0 {
		t.Fatalf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when falling back")
	}
}

