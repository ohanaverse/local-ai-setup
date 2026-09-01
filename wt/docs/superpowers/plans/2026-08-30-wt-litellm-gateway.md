# Route wt agent traffic through the LiteLLM proxy — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use `superpowers:subagent-driven-development` (recommended) or `superpowers:executing-plans` to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `[gateway]` section to `wt` config so non-native agents route through the LiteLLM proxy at `:4000`, and filter the wt model catalog by `modelman`'s `litellm_exposed` flag.

**Architecture:** A new `Gateway` type is threaded through `internal/agents`. Drivers that currently hardcode `localhost:11434` now accept the gateway URL/key and pass the registry model id (`m.ID`) as the client-visible model name. The config layer reads `modelman.toml` and applies the exposure filter inside `EligibleModels`, so both TUI and CLI paths honor it automatically. pi's `models.json` sync is updated to point its ollama provider block at the gateway when enabled.

**Tech Stack:** Go, TOML, JSON, LiteLLM proxy, Postgres, modelman.

---

## File map

| File | Responsibility |
|---|---|
| `internal/config/config.go` | Add `GatewayConfig` struct, parse `[gateway]` from config.toml, read `modelman.toml` into an `exposed` set, add `IsExposed`, apply filter in `EligibleModels`. |
| `internal/config/config_test.go` | Tests for gateway parsing, modelman.toml loading, and exposure filtering. |
| `internal/agents/agents.go` | Define `Gateway` type, update `Driver.Build` signature to receive `Gateway`, thread gateway through `BuildLaunchCmd`. |
| `internal/agents/agents_test.go` | Update test stubs and helpers for the new `Build` signature. |
| `internal/agents/claude.go` | Gateway-mode env construction (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`), use `m.ID` as model name when gateway is litellm. |
| `internal/agents/claude_test.go` | Gateway vs direct mode assertions. |
| `internal/agents/codex.go` | Gateway-mode inline provider `base_url` and model name. |
| `internal/agents/codex_test.go` | Gateway vs direct mode assertions. |
| `internal/agents/copilot.go` | Gateway-mode `COPILOT_PROVIDER_BASE_URL` and `COPILOT_MODEL`. |
| `internal/agents/copilot_test.go` | Gateway vs direct mode assertions. |
| `internal/agents/opencode.go` | **Out of scope for gateway mode** — OpenCode's only supported provider block is ollama-native; LiteLLM's `/v1` is OpenAI-compatible. Keep direct-to-Ollama behavior; update signature only. |
| `internal/agents/opencode_test.go` | Signature update only. |
| `internal/agents/pi.go` / `pi_models.go` | Update `SyncModels` to write gateway `baseUrl`/`apiKey` and use `m.ID` in gateway mode; `isLaunchable` checks `m.ID`. |
| `internal/agents/pi_models_test.go` | Gateway vs direct sync assertions. |
| `internal/agents/agy.go` / `shell.go` | Signature update only. |
| `cmd/wt/launch.go` / `internal/tui/app.go` | No changes needed if filtering lives in `EligibleModels`; verify after config changes. |
| `docs/guides/` in `local-ai-setup` | Update config map, agent/model guide, and usage/spend guide. |

---

## Task 1: Gateway config + exposure filter in `internal/config`

**Files:**
- Modify: `internal/config/config.go`
- Create: `internal/config/modelman.go`
- Test: `internal/config/config_test.go`

### Step 1: Add `GatewayConfig` and `ModelmanState` types

Modify `internal/config/config.go`:

```go
// GatewayConfig is wt-owned gateway routing config.
type GatewayConfig struct {
	Mode    string `toml:"mode"`    // "direct" | "litellm"
	URL     string `toml:"url"`
	APIKey  string `toml:"api_key"`
}

// IsDirect reports whether the gateway is disabled/absent.
func (g GatewayConfig) IsDirect() bool {
	return g.Mode == "" || g.Mode == "direct"
}

// IsLitellm reports whether the gateway routes through the LiteLLM proxy.
func (g GatewayConfig) IsLitellm() bool {
	return g.Mode == "litellm"
}
```

Add to `Config` struct (between `DefaultTag` and `Providers`):

```go
type Config struct {
	DefaultTag string         `toml:"default_tag"`
	Gateway    GatewayConfig  `toml:"gateway"`
	Providers  []Provider     `toml:"providers"`
	Models     []Model        `toml:"models"`
	Agents     []Agent        `toml:"agents"`
	exposed    map[string]bool `toml:"-"` // from modelman.toml
}
```

### Step 2: Read `modelman.toml`

Create `internal/config/modelman.go`:

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// modelmanState mirrors the subset of ~/.config/local-ai/modelman.toml that
// wt needs read-only access to. The full file is owned by modelman.
type modelmanState struct {
	ModelState map[string]struct {
		LitellmExposed bool `toml:"litellm_exposed"`
	} `toml:"model_state"`
}

// loadModelmanState reads modelman.toml and returns a set of exposed model ids.
// A missing file returns an empty set (every non-native model is unexposed).
func loadModelmanState() (map[string]bool, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(home, ".config", "local-ai", "modelman.toml")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modelman.toml: %w", err)
	}
	var s modelmanState
	if err := toml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("parse modelman.toml: %w", err)
	}
	out := make(map[string]bool, len(s.ModelState))
	for id, st := range s.ModelState {
		if st.LitellmExposed {
			out[id] = true
		}
	}
	return out, nil
}
```

### Step 3: Load state into Config

Modify the end of `Load()` in `internal/config/config.go` (after `deriveNative`):

```go
exposed, err := loadModelmanState()
if err != nil {
	return nil, err
}
cfg.exposed = exposed
return cfg, nil
```

### Step 4: Add `IsExposed` and filter `EligibleModels`

Add to `internal/config/config.go`:

```go
// IsExposed reports whether m should appear in wt's model catalog.
// Native models are always exposed (they cannot route through LiteLLM).
func (c *Config) IsExposed(m Model) bool {
	if m.Native {
		return true
	}
	return c.exposed[m.ID]
}
```

Modify `EligibleModels` to add the exposure filter at the end of the per-model loop (after the family check):

```go
for _, m := range ms {
	// ... existing tag and family filters ...
	if !c.IsExposed(m) {
		continue
	}
	out = append(out, m)
}
```

### Step 5: Write failing tests

Append to `internal/config/config_test.go`:

```go
func TestGatewayConfigDirectByDefault(t *testing.T) {
	cfg := &Config{}
	if !cfg.Gateway.IsDirect() {
		t.Fatalf("expected default gateway mode to be direct")
	}
	if cfg.Gateway.IsLitellm() {
		t.Fatalf("expected default gateway mode not to be litellm")
	}
}

func TestIsExposedNativeAlways(t *testing.T) {
	cfg := &Config{exposed: map[string]bool{}}
	m := Model{ID: "claude/native", ProviderID: "claude", Native: true}
	if !cfg.IsExposed(m) {
		t.Fatalf("native model must be exposed")
	}
}

func TestIsExposedNonNativeRequiresFlag(t *testing.T) {
	cfg := &Config{exposed: map[string]bool{"ollama/qwen3.8:27b-mlx": true}}
	exposed := Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama"}
	unexposed := Model{ID: "ollama/gemma4:9b", ProviderID: "ollama"}
	if !cfg.IsExposed(exposed) {
		t.Fatalf("expected exposed model to be exposed")
	}
	if cfg.IsExposed(unexposed) {
		t.Fatalf("expected unexposed model to be unexposed")
	}
}
```

### Step 6: Run tests

```bash
cd /Users/keith/github/ohanaverse/agent-worktree
go test ./internal/config -run 'TestGatewayConfigDirectByDefault|TestIsExposed' -v
```

Expected: PASS.

### Step 7: Commit

```bash
git add internal/config/config.go internal/config/modelman.go internal/config/config_test.go
git commit -m "feat(config): gateway config and litellm_exposed filter"
```

---

## Task 2: Thread `Gateway` through `internal/agents`

**Files:**
- Modify: `internal/agents/agents.go`, all driver files
- Test: `internal/agents/agents_test.go`

### Step 1: Define `Gateway` type and update `Driver` interface

Modify `internal/agents/agents.go`:

```go
// Gateway carries the routing target for non-native models.
type Gateway struct {
	Mode    string // "direct" | "litellm"
	URL     string // base URL, no /v1 suffix
	APIKey  string
}

// IsDirect reports whether the gateway is disabled.
func (g Gateway) IsDirect() bool { return g.Mode == "" || g.Mode == "direct" }

// IsLitellm reports whether the gateway routes through LiteLLM.
func (g Gateway) IsLitellm() bool { return g.Mode == "litellm" }
```

Change the `Driver` interface:

```go
type Driver interface {
	Build(m config.Model, yolo bool, gw Gateway) LaunchCmd
	YoloFlag() string
}
```

### Step 2: Update `BuildLaunchCmd` to build the gateway and pass it

Modify the start of `BuildLaunchCmd` in `internal/agents/agents.go`:

```go
func BuildLaunchCmd(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config, extraArgs []string) (*exec.Cmd, error) {
	d := ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	gw := Gateway{}
	if cfg != nil {
		gw = Gateway{
			Mode:   cfg.Gateway.Mode,
			URL:    cfg.Gateway.URL,
			APIKey: cfg.Gateway.APIKey,
		}
	}
	// ... existing Syncer check ...
	cmd, err := Command(d, m, yolo, gw, worktreePath)
	// ... rest unchanged ...
}
```

Update `Command` signature and call:

```go
func Command(d Driver, m config.Model, yolo bool, gw Gateway, workdir string) (*exec.Cmd, error) {
	lc := d.Build(m, yolo, gw)
	// ... rest unchanged ...
}
```

### Step 3: Mechanical signature update across all drivers

Update `Build` signatures in:

- `internal/agents/claude.go`
- `internal/agents/codex.go`
- `internal/agents/copilot.go`
- `internal/agents/opencode.go`
- `internal/agents/pi.go`
- `internal/agents/agy.go`
- `internal/agents/shell.go`

Example for `agy.go` and `shell.go`:

```go
func (agyDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	// existing body unchanged
}
```

### Step 4: Add a test helper and fix call sites

Add to `internal/agents/agents_test.go`:

```go
func directGateway() Gateway { return Gateway{Mode: "direct"} }
```

Use `sed` to update all direct `Build(m, yolo)` calls in `internal/agents/*_test.go`:

```bash
sed -i '' 's/\.Build(m, yolo)/.Build(m, yolo, directGateway())/g' internal/agents/*_test.go
```

### Step 5: Run tests

```bash
go test ./internal/agents -run 'Test' -count=1
```

Expected: compile and pass.

### Step 6: Commit

```bash
git add internal/agents/agents.go internal/agents/agents_test.go internal/agents/*_test.go internal/agents/*.go
git commit -m "refactor(agents): pass Gateway to every Driver.Build"
```

---

## Task 3: Claude gateway routing

**Files:**
- Modify: `internal/agents/claude.go`
- Test: `internal/agents/claude_test.go`

### Step 1: Implement gateway mode

Replace the non-native branch in `claude.go` with:

```go
if m.Native {
	lc.ClearEnv = []string{"ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL"}
	if m.ModelName != "native" {
		lc.Args = append(lc.Args, "--model", m.ModelName)
	}
	return lc
}

if gw.IsLitellm() {
	lc.Env = append(lc.Env,
		"ANTHROPIC_AUTH_TOKEN="+gw.APIKey,
		"ANTHROPIC_API_KEY=",
		"ANTHROPIC_BASE_URL="+gw.URL,
	)
	lc.Args = append(lc.Args, "--model", m.ID)
	return lc
}

lc.Env = append(lc.Env,
	"ANTHROPIC_AUTH_TOKEN=ollama",
	"ANTHROPIC_API_KEY=",
	"ANTHROPIC_BASE_URL="+claudeDriver{}.OllamaURL(),
)
lc.Args = append(lc.Args, "--model", m.ModelName)
return lc
```

### Step 2: Add gateway test

Append to `internal/agents/claude_test.go`:

```go
func TestClaudeBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := claudeDriver{}.Build(m, false, gw)
	assertEnv(t, lc.Env, "ANTHROPIC_BASE_URL", "http://localhost:4000")
	assertEnv(t, lc.Env, "ANTHROPIC_AUTH_TOKEN", "sk-litellm")
	if !slices.Contains(lc.Args, "ollama/qwen3.8:27b-mlx") {
		t.Fatalf("expected args to contain registry id, got %v", lc.Args)
	}
}
```

(Use the existing `assertEnv` helper in that test file, or add it if absent.)

### Step 3: Run tests

```bash
go test ./internal/agents -run 'TestClaude' -v
```

### Step 4: Commit

```bash
git add internal/agents/claude.go internal/agents/claude_test.go
git commit -m "feat(claude): route through LiteLLM gateway"
```

---

## Task 4: Codex gateway routing

**Files:**
- Modify: `internal/agents/codex.go`
- Test: `internal/agents/codex_test.go`

### Step 1: Implement gateway mode

Replace the non-native branch in `codex.go` with:

```go
if m.Native {
	return lc
}

baseURL := codexDriver{}.OllamaURL()
modelName := m.ModelName
if gw.IsLitellm() {
	baseURL = gw.URL + "/v1/"
	modelName = m.ID
}

lc.Args = append(lc.Args,
	"-c", "model_provider="+ollamaProvider,
	"-c", "model_providers."+ollamaProvider+".name=\"Ollama\"",
	"-c", "model_providers."+ollamaProvider+".base_url=\""+baseURL+"\"",
	"-c", "model_providers."+ollamaProvider+".wire_api=\"responses\"",
	"--model", modelName,
)
return lc
```

### Step 2: Add gateway test

Append to `internal/agents/codex_test.go`:

```go
func TestCodexBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := codexDriver{}.Build(m, false, gw)
	joined := strings.Join(lc.Args, " ")
	if !strings.Contains(joined, "base_url=\"http://localhost:4000/v1/\"") {
		t.Fatalf("expected LiteLLM base_url, got %v", lc.Args)
	}
	if !strings.Contains(joined, "--model ollama/qwen3.8:27b-mlx") {
		t.Fatalf("expected registry id as model, got %v", lc.Args)
	}
}
```

### Step 3: Commit

```bash
git add internal/agents/codex.go internal/agents/codex_test.go
git commit -m "feat(codex): route through LiteLLM gateway"
```

---

## Task 5: Copilot gateway routing

**Files:**
- Modify: `internal/agents/copilot.go`
- Test: `internal/agents/copilot_test.go`

### Step 1: Implement gateway mode

Replace the non-native branch in `copilot.go` with:

```go
if m.Native {
	lc.ClearEnv = []string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_WIRE_API", "COPILOT_MODEL"}
	return lc
}

baseURL := copilotDriver{}.OllamaURL()
modelName := m.ModelName
apiKey := ""
if gw.IsLitellm() {
	baseURL = gw.URL + "/v1"
	modelName = m.ID
	apiKey = gw.APIKey
}

lc.Env = append(lc.Env,
	"COPILOT_PROVIDER_BASE_URL="+baseURL,
	"COPILOT_PROVIDER_API_KEY="+apiKey,
	"COPILOT_PROVIDER_WIRE_API=completions",
	"COPILOT_MODEL="+modelName,
)
return lc
```

> Note: The plan originally specified `responses`, but the Copilot CLI's
> `responses` wire drops leading characters when routed through Ollama/LiteLLM
> (observed with `glm-5.3-flash:cloud`). `completions` is used instead.

### Step 2: Add gateway test

Append to `internal/agents/copilot_test.go`:

```go
func TestCopilotBuildLitellm(t *testing.T) {
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"}
	gw := Gateway{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"}
	lc := copilotDriver{}.Build(m, false, gw)
	assertEnv(t, lc.Env, "COPILOT_PROVIDER_BASE_URL", "http://localhost:4000/v1")
	assertEnv(t, lc.Env, "COPILOT_PROVIDER_API_KEY", "sk-litellm")
	assertEnv(t, lc.Env, "COPILOT_MODEL", "ollama/qwen3.8:27b-mlx")
}
```

### Step 3: Commit

```bash
git add internal/agents/copilot.go internal/agents/copilot_test.go
git commit -m "feat(copilot): route through LiteLLM gateway"
```

---

## Task 6: OpenCode — signature update only

**Files:**
- Modify: `internal/agents/opencode.go`
- Test: `internal/agents/opencode_test.go`

### Step 1: Update signature, keep direct behavior

```go
func (opencodeDriver) Build(m config.Model, yolo bool, gw Gateway) LaunchCmd {
	// ... existing body unchanged, still uses m.ModelName + OllamaURL() ...
}
```

### Step 2: Add a comment documenting the exception

```go
// OpenCode's only supported provider block is ollama-native. LiteLLM's /v1 is
// OpenAI-compatible, so gateway routing needs an openai provider config
// shape that OpenCode may or may not support. Kept direct-to-Ollama here;
// revisit when OpenCode's provider schema is confirmed.
```

### Step 3: Update tests for signature only

```bash
sed -i '' 's/\.Build(m, yolo)/.Build(m, yolo, directGateway())/g' internal/agents/opencode_test.go
```

### Step 4: Commit

```bash
git add internal/agents/opencode.go internal/agents/opencode_test.go
git commit -m "refactor(opencode): accept Gateway, keep direct ollama routing"
```

---

## Task 7: pi gateway sync + launch check

**Files:**
- Modify: `internal/agents/pi.go`, `internal/agents/pi_models.go`
- Test: `internal/agents/pi_models_test.go`

### Step 1: Update `syncModels` to accept gateway

Change signature:

```go
func syncModels(cfg *config.Config, path string) error
```

is already used in `pi.go` `SyncModels(cfg *config.Config)`. Add gateway handling inside `syncModels`:

```go
for _, m := range cfg.Models {
	if m.Native || m.ModelName == "" {
		continue
	}
	id := m.ModelName
	if cfg.Gateway.IsLitellm() {
		id = m.ID
	}
	if existing[id] {
		continue
	}
	f.Providers.Ollama.Models = append(f.Providers.Ollama.Models, piModel{
		Launch:        true,
		ContextWindow: 262144,
		ID:            id,
		Input:         []string{"text", "image"},
		Reasoning:     true,
	})
	existing[id] = true
	added++
}

if cfg.Gateway.IsLitellm() {
	f.Providers.Ollama.BaseURL = cfg.Gateway.URL + "/v1"
	f.Providers.Ollama.APIKey = cfg.Gateway.APIKey
} else {
	// Preserve existing provider config verbatim in direct mode.
	// (No change.)
}
```

### Step 2: Update `isLaunchable` to check both `m.ID` and `m.ModelName`

To stay compatible with pre-existing direct-mode entries and fresh gateway-mode entries:

```go
func isLaunchable(name, path string) bool {
	// ... parse file ...
	for _, m := range f.Providers.Ollama.Models {
		if (m.ID == name) && m.Launch {
			return true
		}
	}
	return false
}
```

### Step 3: Update `pi.go` `Build` to check `m.ID`

```go
if isLaunchable(m.ID, path) {
	lc.Args = append(lc.Args, "--model", m.ID)
} else {
	lc.Warn = fmt.Sprintf("pi: model %q not configured for pi, using default model", m.ID)
}
```

Wait — in direct mode pi's catalog uses `m.ModelName` as `id`. So `isLaunchable(m.ID)` would fail for direct mode unless we also check `m.ModelName`. Use:

```go
if isLaunchable(m.ID, path) || isLaunchable(m.ModelName, path) {
	// Prefer m.ID if gateway mode, but for direct mode either works.
	modelArg := m.ID
	if isLaunchable(m.ModelName, path) {
		modelArg = m.ModelName
	}
	lc.Args = append(lc.Args, "--model", modelArg)
} else { ... }
```

Actually simpler: in gateway mode sync writes `m.ID`; in direct mode sync writes `m.ModelName`. `Build` should pass the same value used during sync. Since `SyncModels` uses `m.ID` when gateway litellm, `m.ModelName` otherwise, make `Build` mirror that:

```go
modelArg := m.ModelName
if cfg.Gateway.IsLitellm() {
	modelArg = m.ID
}
if isLaunchable(modelArg, path) {
	lc.Args = append(lc.Args, "--model", modelArg)
} else {
	lc.Warn = fmt.Sprintf("pi: model %q not configured for pi, using default model", modelArg)
}
```

### Step 4: Add gateway test

Append to `internal/agents/pi_models_test.go`:

```go
func TestSyncModelsLitellm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.json")
	cfg := &config.Config{
		Gateway: config.GatewayConfig{Mode: "litellm", URL: "http://localhost:4000", APIKey: "sk-litellm"},
		Models: []config.Model{
			{ID: "ollama/qwen3.8:27b-mlx", ModelName: "qwen3.8:27b-mlx", ProviderID: "ollama"},
		},
	}
	require.NoError(t, syncModels(cfg, path))
	f := readPiModels(t, path)
	if f.Providers.Ollama.BaseURL != "http://localhost:4000/v1" {
		t.Fatalf("baseUrl = %q", f.Providers.Ollama.BaseURL)
	}
	if f.Providers.Ollama.APIKey != "sk-litellm" {
		t.Fatalf("apiKey = %q", f.Providers.Ollama.APIKey)
	}
	if len(f.Providers.Ollama.Models) != 1 || f.Providers.Ollama.Models[0].ID != "ollama/qwen3.8:27b-mlx" {
		t.Fatalf("unexpected models: %v", f.Providers.Ollama.Models)
	}
}
```

### Step 5: Commit

```bash
git add internal/agents/pi.go internal/agents/pi_models.go internal/agents/pi_models_test.go
git commit -m "feat(pi): sync gateway baseUrl/apiKey and use registry id in litellm mode"
```

---

## Task 8: Update TUI and CLI tests for exposure filter

**Files:**
- Modify: `internal/tui/agent_model_test.go`
- Modify: `internal/tui/agent_picker_test.go`

### Step 1: Expose models in test fixtures

Most existing tests build a `config.Config` directly with `Models`. Since the new
filter drops unexposed non-native models, those tests need an `exposed` map set.

Add a helper in `internal/tui/agent_model_test.go`:

```go
func exposeAll(cfg *config.Config) {
	cfg.SetExposedForTest(map[string]bool{})
	for _, m := range cfg.Models {
		if !m.Native {
			cfg.SetExposedForTest(map[string]bool{m.ID: true})
		}
	}
}
```

This requires a test-only helper in `internal/config/config.go`:

```go
// SetExposedForTest replaces the in-memory exposed set. Tests only.
func (c *Config) SetExposedForTest(exposed map[string]bool) {
	c.exposed = exposed
}
```

Then update each test that calls `cfg.EligibleModels` on non-native models to
call `exposeAll(&cfg)` first. A representative update:

```go
func TestPickerShowsModels(t *testing.T) {
	cfg := buildTestConfig()
	exposeAll(&cfg)
	// ... rest ...
}
```

### Step 2: Add a test for the exposure filter in TUI

```go
func TestPickerHidesUnexposedModels(t *testing.T) {
	cfg := buildTestConfigWithModels(
		config.Model{ID: "ollama/exposed", ModelName: "exposed", ProviderID: "ollama", Tags: []string{"code"}},
		config.Model{ID: "ollama/hidden", ModelName: "hidden", ProviderID: "ollama", Tags: []string{"code"}},
	)
	cfg.SetExposedForTest(map[string]bool{"ollama/exposed": true})
	models, err := cfg.EligibleModels("claude", "code", "")
	if err != nil {
		t.Fatalf("EligibleModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "ollama/exposed" {
		t.Fatalf("expected only exposed model, got %v", models)
	}
}
```

### Step 3: Commit

```bash
git add internal/config/config.go internal/tui/agent_model_test.go internal/tui/agent_picker_test.go
git commit -m "test(tui): honor litellm_exposed in model picker tests"
```

---

## Task 9: Documentation updates in `local-ai-setup`

**Files:**
- Modify: `local-ai-setup/docs/guides/00-config-map.md`
- Modify: `local-ai-setup/docs/guides/06-wt-agents-and-models.md`
- Modify: `local-ai-setup/docs/guides/07-usage-and-spend.md`

### Step 1: Update `00-config-map.md`

In the `modelman.toml` row, change **Consumers** from `modelman` to
`modelman` (writes), `wt` (read-only for `litellm_exposed`).

Add a `~/.config/agent-wt/config.toml` row note that `[gateway]` is new and
wt-owned.

### Step 2: Update `06-wt-agents-and-models.md`

After the existing model selection section, add a new subsection:

```markdown
### Gateway mode (LiteLLM)

By default wt agents talk directly to Ollama (`localhost:11434`). To route
non-native traffic through the LiteLLM proxy (`localhost:4000`) and populate
the dashboard, add `[gateway]` to `~/.config/agent-wt/config.toml`:

```toml
[gateway]
mode    = "litellm"
url     = "http://localhost:4000"
api_key = "sk-…"
```

In this mode:
- wt only shows models with `litellm_exposed = true` in `modelman.toml`.
- The model name passed to agents is the registry id (e.g.
  `ollama/qwen3.8:27b-mlx`), matching LiteLLM's `model_list` entries.
- Native models (`claude/native`, `copilot/native`) still use their own
  subscriptions and are always shown.
- OpenCode continues to use Ollama directly until its OpenAI-compatible
  provider block is confirmed.
```

### Step 3: Update `07-usage-and-spend.md`

In the "Interpreting mismatches" section, add:

```markdown
After enabling `[gateway]` in `wt`, non-native launches route through LiteLLM,
so matched rows should become the norm. Remaining `WT-only` rows are usually:
- native models (unmetered subscriptions)
- OpenCode launches (not routed through the gateway in this release)
- traffic that bypassed wt entirely (e.g. direct `curl` to Ollama/llama.cpp/oMLX)
```

### Step 4: Commit

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup
git add docs/guides/00-config-map.md docs/guides/06-wt-agents-and-models.md docs/guides/07-usage-and-spend.md
git commit -m "docs(guides): wt LiteLLM gateway mode and exposure filter"
```

---

## Task 10: Final verification

### Step 1: Run full agent-worktree test suite

```bash
cd /Users/keith/github/ohanaverse/agent-worktree
go test ./... -count=1
```

### Step 2: Build and install wt

```bash
go build -o /Users/keith/.local/bin/wt ./cmd/wt
```

### Step 3: Real launch + usage report

```bash
# expose a model if needed
uv run modelman expose ollama/qwen3.8:27b-mlx

# launch through the gateway
wt -W <worktree> -A claude -M ollama/qwen3.8:27b-mlx

# after exit, confirm spend logged
uv run modelman usage report --days 1
```

Expected: the launched model appears in the summary with `Requests > 0` and
no `WT-only` bullet for it.

### Step 4: Commit any final fixes

```bash
git status
# commit if any test or doc tweaks are needed
git commit -m "fix: final verification tweaks"
```

---

## Plan self-review

### Spec coverage

| Spec requirement | Task |
|---|---|
| `[gateway]` in `~/.config/agent-wt/config.toml` | Task 1 |
| wt reads `modelman.toml` for `litellm_exposed` | Task 1 |
| Filter catalog by exposure (native exempt) | Task 1, 8 |
| Non-native drivers route through LiteLLM URL + use registry id | Tasks 3–7 |
| pi provider block updated in gateway mode | Task 7 |
| Native models unchanged | unchanged branches in all driver tasks |
| Fail fast, no fallback | default behavior (no code needed) |
| Docs updates | Task 9 |

### Placeholder scan

No `TBD`, `TODO`, `implement later`, or unqualified "add validation" steps.
Every code step shows concrete Go code or concrete commands.

### Type consistency

- `Gateway` struct is defined in `internal/agents/agents.go` and passed to
  `Driver.Build(m, yolo, gw)`.
- `config.GatewayConfig` is loaded from TOML and converted to `agents.Gateway`
  in `BuildLaunchCmd`.
- `m.ID` is used as the model argument in gateway mode; `m.ModelName` stays in
  direct mode.

## Execution record

- **Approach:** Subagent-Driven (dedicated worktree at `.worktrees/wt-litellm-gateway`, fresh subagent per task, two-stage review between tasks).
- **Status:** All 10 tasks completed.
- **Final verification:** `go test ./... -count=1` passes, `go vet ./...` clean, `wt` built to `~/.local/bin/wt`, `wt --help` runs cleanly.
- **Pull request:** https://github.com/ohanaverse/agent-worktree/pull/108
- **Known caveats:** OpenCode remains direct-to-Ollama in this release; gateway URL trailing slash normalized; `gateway.mode` validated to empty/`direct`/`litellm`.
