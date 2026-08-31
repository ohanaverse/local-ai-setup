# Pi Model Sync and Launch Check (Go) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Port the pi launcher fixes from PRs #25/#26/#27 into the Go `wt` tool — sync non-native models into `~/.pi/agent/models.json` and verify `_launch: true` before passing `--model`, using native Go JSON (no `jq`).

**Architecture:** Add an optional `Syncer` interface to the `agents` package that `piDriver` implements for the pre-launch sync (which needs the full config). The per-model `_launch` check lives in `piDriver.Build`, which now uses `ModelName` (bare name) instead of `ID` (provider-prefixed). A new `Warn` field on `LaunchCmd` surfaces the "using default model" fallback without writing to `os.Stderr` from `Build`.

**Tech Stack:** Go 1.26, `encoding/json`, `os`, `path/filepath`, standard `testing`.

**Spec:** `docs/superpowers/specs/2026-08-16-pi-model-sync-and-launch-check-design.md`

---

## File Structure

| File | Action | Responsibility |
|---|---|---|
| `internal/agents/agents.go` | Modify | Add `Syncer` interface, `Warn` field on `LaunchCmd`, print `Warn` in `Command` |
| `internal/agents/pi_models.go` | Create | `models.json` structs + `syncModels` + `isLaunchable` (path-injected) |
| `internal/agents/pi_models_test.go` | Create | Unit tests for `syncModels` / `isLaunchable` |
| `internal/agents/pi.go` | Modify | Implement `SyncModels`, rewrite `Build` |
| `internal/agents/agents_test.go` | Modify | Replace `TestPi` with granular build tests |
| `cmd/wt/launch.go` | Modify | Thread `cfg` into `buildLaunch`, invoke sync |
| `cmd/wt/launch_test.go` | Modify | Update `buildLaunch` calls, add sync wiring test |
| `internal/tui/launch.go` | Modify | Thread `cfg` into `launchAgent`, invoke sync |
| `internal/tui/app.go` | Modify | Update 3 `launchAgent` call sites |
| `internal/tui/launch_test.go` | Modify | Update `launchAgent` calls, add sync wiring test |
| `CLAUDE.md` | Modify | Correct the pi driver description |

---

## Task 1: `Syncer` interface + `Warn` field + `Command` warning

**Files:**
- Modify: `internal/agents/agents.go`
- Test: `internal/agents/agents_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/agents/agents_test.go` (add `io` and `os` to imports):

```go
// warnDriver is a test-only Driver whose Build returns a fixed warning, used
// to verify Command surfaces it on stderr.
type warnDriver struct{}

func (warnDriver) Build(m config.Model, yolo bool) LaunchCmd {
	return LaunchCmd{Bin: "true", Warn: "test warning"}
}
func (warnDriver) YoloFlag() string { return "" }

// Command must print a non-empty LaunchCmd.Warn to stderr before returning the
// command. This is how the pi driver tells the user it fell back to pi's
// default model; if the warning is swallowed, the user silently gets a
// different model than they selected.
func TestCommandPrintsWarning(t *testing.T) {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w
	_, cmdErr := Command(warnDriver{}, config.Model{}, false, "/tmp")
	w.Close()
	os.Stderr = old
	if cmdErr != nil {
		t.Fatalf("Command: %v", cmdErr)
	}
	out, _ := io.ReadAll(r)
	if !strings.Contains(string(out), "test warning") {
		t.Errorf("stderr = %q, want it to contain %q", string(out), "test warning")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agents -run TestCommandPrintsWarning -v`
Expected: FAIL — compile error: `LaunchCmd` has no field `Warn`.

- [ ] **Step 3: Implement `Warn` field, `Syncer` interface, and `Command` printing**

In `internal/agents/agents.go`, change the `LaunchCmd` struct and `Command`, and add the `Syncer` interface:

```go
// LaunchCmd describes a fully-built process to exec.
type LaunchCmd struct {
	Bin  string
	Args []string
	Env  []string // extra env vars, merged over os.Environ() by the caller
	Warn string   // printed to stderr by Command when non-empty
}

// Driver knows how to build a launch command for one agent.
type Driver interface {
	// Build returns the command to run agent for the given model.
	// yolo adds the agent's skip-permissions flag.
	Build(m config.Model, yolo bool) LaunchCmd
	// YoloFlag is the agent's permission-skip flag.
	YoloFlag() string
}

// Syncer is an optional Driver capability: a pre-launch step that needs the
// full config (e.g. pi syncing its model catalog). Launch paths call it once
// before Build.
type Syncer interface {
	SyncModels(cfg *config.Config) error
}
```

And in `Command`, after the `LookPath` check, print the warning:

```go
func Command(d Driver, m config.Model, yolo bool, workdir string) (*exec.Cmd, error) {
	lc := d.Build(m, yolo)
	bin, err := exec.LookPath(lc.Bin)
	if err != nil {
		return nil, fmt.Errorf("agent %s not installed", lc.Bin)
	}
	if lc.Warn != "" {
		fmt.Fprintln(os.Stderr, lc.Warn)
	}
	cmd := exec.Command(bin, lc.Args...)
	cmd.Dir = workdir
	cmd.Env = append(os.Environ(), lc.Env...)
	return cmd, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agents -run TestCommandPrintsWarning -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agents/agents.go internal/agents/agents_test.go
git commit -m "feat(agents): add Syncer interface and LaunchCmd.Warn warning"
```

---

## Task 2: `pi_models.go` — structs, `syncModels`, `isLaunchable`

**Files:**
- Create: `internal/agents/pi_models.go`
- Test: `internal/agents/pi_models_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/agents/pi_models_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agents -run 'TestPiSyncModels|TestIsLaunchable' -v`
Expected: FAIL — compile error: `syncModels`, `isLaunchable`, `piModelsFile` undefined.

- [ ] **Step 3: Implement `pi_models.go`**

Create `internal/agents/pi_models.go`:

```go
package agents

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// piModelsFile mirrors the on-disk shape of ~/.pi/agent/models.json. Only the
// ollama provider is relevant to wt; its api/apiKey/baseUrl fields are
// preserved verbatim so pi's own configuration is never clobbered by a sync.
type piModelsFile struct {
	Providers struct {
		Ollama struct {
			API     string    `json:"api"`
			APIKey  string    `json:"apiKey"`
			BaseURL string    `json:"baseUrl"`
			Models  []piModel `json:"models"`
		} `json:"ollama"`
	} `json:"providers"`
}

// piModel is a single entry in pi's model catalog.
type piModel struct {
	Launch        bool     `json:"_launch"`
	ContextWindow int      `json:"contextWindow"`
	ID            string   `json:"id"`
	Input         []string `json:"input"`
	Reasoning     bool     `json:"reasoning"`
}

// piModelsPath resolves the location of pi's model catalog.
func piModelsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi", "agent", "models.json"), nil
}

// syncModels adds any non-native models from cfg that are missing from pi's
// models.json, marking each _launch: true. It is idempotent (only adds, never
// removes) and preserves existing entries and pi's provider config verbatim.
func syncModels(cfg *config.Config, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil // nothing to sync
	}
	if err != nil {
		return err
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return err
	}

	existing := make(map[string]bool, len(f.Providers.Ollama.Models))
	for _, m := range f.Providers.Ollama.Models {
		existing[m.ID] = true
	}

	added := 0
	for _, m := range cfg.Models {
		if m.IsNative() || m.ModelName == "" {
			continue
		}
		if existing[m.ModelName] {
			continue
		}
		f.Providers.Ollama.Models = append(f.Providers.Ollama.Models, piModel{
			Launch:        true,
			ContextWindow: 262144,
			ID:            m.ModelName,
			Input:         []string{"text", "image"},
			Reasoning:     true,
		})
		existing[m.ModelName] = true
		added++
	}

	if added == 0 {
		return nil
	}

	out, err := json.MarshalIndent(&f, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// isLaunchable reports whether name is present in pi's models.json and marked
// _launch: true. A missing or unparseable file is treated as "not launchable"
// (the caller falls back to pi's default model).
func isLaunchable(name, path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var f piModelsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return false
	}
	for _, m := range f.Providers.Ollama.Models {
		if m.ID == name && m.Launch {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agents -run 'TestPiSyncModels|TestIsLaunchable' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agents/pi_models.go internal/agents/pi_models_test.go
git commit -m "feat(agents): add pi models.json sync and launch check"
```

---

## Task 3: `pi.go` — implement `SyncModels` + rewrite `Build`

**Files:**
- Modify: `internal/agents/pi.go`
- Test: `internal/agents/agents_test.go` (replace `TestPi`)

- [ ] **Step 1: Write the failing tests**

In `internal/agents/agents_test.go`, replace the existing `TestPi` function with the following (add `os` and `path/filepath` to imports):

```go
// writePiModels points $HOME at a temp dir and writes a models.json there, so
// piDriver.Build (which resolves the real path via os.UserHomeDir) reads the
// test fixture instead of the user's real catalog.
func writePiModels(t *testing.T, content string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.json"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Pi has no yolo flag (the pi CLI doesn't support skipping permissions).
func TestPiYoloFlag(t *testing.T) {
	d := ByName("pi")
	if d == nil {
		t.Fatal("pi driver not registered")
	}
	if d.YoloFlag() != "" {
		t.Errorf("pi yolo flag = %q, want empty", d.YoloFlag())
	}
}

// Pi passes --model <ModelName> only when the model is present in models.json
// and marked _launch: true. Passing --model for an unverified model would
// launch a model pi doesn't intend to use.
func TestPiBuildVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":true,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if lc.Warn != "" {
		t.Errorf("warn = %q, want empty for a verified model", lc.Warn)
	}
}

// Pi falls back to its default model (no --model) and sets a warning when the
// model is present but not marked _launch: true.
func TestPiBuildNotVerified(t *testing.T) {
	writePiModels(t, `{"providers":{"ollama":{"models":[{"_launch":false,"id":"deepseek-v4-pro:cloud"}]}}}`)
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when falling back")
	}
}

// Pi falls back to its default model when models.json is missing entirely.
func TestPiBuildMissingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no models.json created
	d := ByName("pi")
	lc := d.Build(config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}, false)
	if len(lc.Args) != 0 {
		t.Errorf("args = %v, want none (fallback to default)", lc.Args)
	}
	if lc.Warn == "" {
		t.Error("warn should be set when models.json is missing")
	}
}

// Pi native models produce no args and no warning.
func TestPiBuildNative(t *testing.T) {
	d := ByName("pi")
	lc := d.Build(nativeModel("pi"), false)
	if len(lc.Args) != 0 || lc.Warn != "" {
		t.Errorf("native build = %+v, want bare pi", lc)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/agents -run 'TestPi' -v`
Expected: FAIL — `TestPiBuildVerified` fails because `Build` passes `--model` unconditionally (and uses `m.ID`, not `m.ModelName`).

- [ ] **Step 3: Implement `SyncModels` + rewrite `Build`**

Replace the contents of `internal/agents/pi.go`:

```go
package agents

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("pi", func() Driver { return piDriver{} }) }

type piDriver struct{}

// Pi has no documented permission-bypass flag.
func (piDriver) YoloFlag() string { return "" }

// SyncModels adds any non-native models from cfg that are missing from pi's
// models.json, so rotation-selected models are always available to pi.
func (piDriver) SyncModels(cfg *config.Config) error {
	path, err := piModelsPath()
	if err != nil {
		return err
	}
	return syncModels(cfg, path)
}

// Build passes --model <ModelName> only when the model is present in pi's
// models.json and marked _launch: true. Otherwise it falls back to pi's
// default model and surfaces a warning.
func (piDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "pi"}
	if m.IsNative() {
		return lc
	}
	path, err := piModelsPath()
	if err != nil {
		lc.Warn = fmt.Sprintf("pi: cannot locate models.json (%v), using default model", err)
		return lc
	}
	if isLaunchable(m.ModelName, path) {
		lc.Args = append(lc.Args, "--model", m.ModelName)
	} else {
		lc.Warn = fmt.Sprintf("pi: model %q not configured for pi, using default model", m.ModelName)
	}
	return lc
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/agents -v`
Expected: PASS (all agents tests, including the new pi tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agents/pi.go internal/agents/agents_test.go
git commit -m "feat(agents): pi driver syncs models and verifies _launch before --model"
```

---

## Task 4: Wire sync into `cmd/wt` launch path

**Files:**
- Modify: `cmd/wt/launch.go`
- Test: `cmd/wt/launch_test.go`

- [ ] **Step 1: Write the failing test**

Append to `cmd/wt/launch_test.go` (add `os` and `path/filepath` to imports):

```go
// buildLaunch must invoke the pi driver's SyncModels before building the
// command, so a rotation-selected model is present in models.json by the time
// the _launch check runs. Without the sync, pi would fall back to its default.
// The sync runs before LookPath, so it is observable even when pi is not
// installed (the "not installed" error is tolerated).
func TestBuildLaunchSyncsPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"models":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}
	cmd, err := buildLaunch("pi", m, "/tmp", false, nil, cfg)
	if err != nil && !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("buildLaunch: %v", err)
	}
	// The sync must have added the model to models.json regardless of whether
	// pi is installed (the sync runs before LookPath).
	data, _ := os.ReadFile(modelsPath)
	if !strings.Contains(string(data), "deepseek-v4-pro:cloud") {
		t.Errorf("models.json = %s, want it to contain deepseek-v4-pro:cloud (sync ran)", string(data))
	}
	if err == nil {
		got := strings.Join(cmd.Args, " ")
		if !strings.Contains(got, "--model deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model deepseek-v4-pro:cloud (sync + verify)", got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/wt -run TestBuildLaunchSyncsPi -v`
Expected: FAIL — compile error: `buildLaunch` takes 5 args, not 6.

- [ ] **Step 3: Thread `cfg` and invoke sync in `buildLaunch`**

In `cmd/wt/launch.go`, change `buildLaunch`'s signature and body:

```go
func buildLaunch(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	if s, ok := d.(agents.Syncer); ok {
		if err := s.SyncModels(cfg); err != nil {
			return nil, err
		}
	}
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
}
```

And update the call in `launch`:

```go
	cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg)
```

- [ ] **Step 4: Update the existing `buildLaunch` test calls**

In `cmd/wt/launch_test.go`, add a trailing `nil` (or a config) to the four existing `buildLaunch` calls:

- `buildLaunch("not-an-agent", config.Model{}, "/tmp", false, nil, nil)`
- `buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false, &session.Session{...}, nil)`
- `buildLaunch("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false, &session.Session{...}, nil)`
- `buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native"}, "/tmp/repo", false, nil, nil)`

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/wt -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "feat(cmd/wt): sync pi models before non-TUI launch"
```

---

## Task 5: Wire sync into `internal/tui` launch path

**Files:**
- Modify: `internal/tui/launch.go`
- Modify: `internal/tui/app.go`
- Test: `internal/tui/launch_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/launch_test.go` (add `os` and `path/filepath` to imports):

```go
// launchAgent must invoke the pi driver's SyncModels before building the
// command, mirroring the non-TUI path. Without it, the TUI launch would fall
// back to pi's default model for a rotation-selected model. The sync runs
// before LookPath, so it is observable even when pi is not installed.
func TestLaunchAgentSyncsPi(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".pi", "agent")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsPath := filepath.Join(dir, "models.json")
	if err := os.WriteFile(modelsPath, []byte(`{"providers":{"ollama":{"models":[]}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Models: []config.Model{
		{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"},
	}}
	m := config.Model{ID: "ollama/deepseek-v4-pro:cloud", ModelName: "deepseek-v4-pro:cloud"}
	cmd, err := launchAgent("pi", m, "/tmp", false, nil, cfg)
	if err != nil && !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("launchAgent: %v", err)
	}
	data, _ := os.ReadFile(modelsPath)
	if !strings.Contains(string(data), "deepseek-v4-pro:cloud") {
		t.Errorf("models.json = %s, want it to contain deepseek-v4-pro:cloud (sync ran)", string(data))
	}
	if err == nil {
		got := strings.Join(cmd.Args, " ")
		if !strings.Contains(got, "--model deepseek-v4-pro:cloud") {
			t.Errorf("args = %q, want --model deepseek-v4-pro:cloud (sync + verify)", got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tui -run TestLaunchAgentSyncsPi -v`
Expected: FAIL — compile error: `launchAgent` takes 5 args, not 6.

- [ ] **Step 3: Thread `cfg` and invoke sync in `launchAgent`**

In `internal/tui/launch.go`, change `launchAgent`'s signature and body:

```go
func launchAgent(agent string, m config.Model, worktreePath string, yolo bool, sess *session.Session, cfg *config.Config) (*exec.Cmd, error) {
	d := agents.ByName(agent)
	if d == nil {
		return nil, fmt.Errorf("unknown agent: %s", agent)
	}
	if s, ok := d.(agents.Syncer); ok {
		if err := s.SyncModels(cfg); err != nil {
			return nil, err
		}
	}
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return nil, err
	}
	if sess != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", sess.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", sess.ID)
		}
	}
	return cmd, nil
}
```

- [ ] **Step 4: Update the three `launchAgent` call sites in `app.go`**

In `internal/tui/app.go`, add `m.cfg` as the final argument to each call:

- Line ~170: `launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg)`
- Line ~194: `launchAgent(m.agent, m.current, m.selectedPath, m.yolo, nil, m.cfg)`
- Line ~201: `launchAgent(m.agent, m.current, m.selectedPath, m.yolo, m.resume.session, m.cfg)`

- [ ] **Step 5: Update the existing `launchAgent` test calls**

In `internal/tui/launch_test.go`, add a trailing `nil` to the four existing `launchAgent` calls:

- `launchAgent("not-an-agent", config.Model{}, "/tmp", false, nil, nil)`
- `launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false, &session.Session{...}, nil)`
- `launchAgent("opencode", config.Model{ID: "ollama/gemma4:9b"}, "/tmp/repo", false, &session.Session{...}, nil)`
- `launchAgent("claude", config.Model{ID: "claude-sonnet"}, "/tmp/repo", false, nil, nil)`

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./internal/tui -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/launch.go internal/tui/app.go internal/tui/launch_test.go
git commit -m "feat(tui): sync pi models before TUI launch"
```

---

## Task 6: Correct the `CLAUDE.md` pi description

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the pi driver bullet**

In `CLAUDE.md`, find the line (currently inaccurate — it claims the check exists but the code didn't):

```
- **pi** — `--model <id>` if model is present in `models.json` with `._launch: true`; requires `jq`; no yolo flag
```

Replace it with:

```
- **pi** — syncs non-native models into `~/.pi/agent/models.json` (idempotent, `_launch: true`) and passes `--model <ModelName>` only when the model is present and marked `_launch: true`; falls back to pi's default model with a warning otherwise; no `jq` dependency (native Go JSON); no yolo flag
```

- [ ] **Step 2: Commit**

```bash
git add CLAUDE.md
git commit -m "docs: correct pi driver description in CLAUDE.md"
```

---

## Final Verification

- [ ] Run the full test suite and vet:

```bash
go test ./...
go vet ./...
go build ./...
```

Expected: all pass, no vet warnings, clean build.

---

## Self-Review Notes

- **Spec coverage:** sync (Task 2/3), `_launch` check (Task 2/3), fallback + warning (Task 1/3), `ModelName` mapping (Task 2/3), wiring in both launch paths (Task 4/5), docs (Task 6). All spec sections covered.
- **Type consistency:** `syncModels(cfg, path) error` and `isLaunchable(name, path) bool` are used consistently across Tasks 2 and 3. `Syncer.SyncModels(cfg)` matches the interface in Task 1. `LaunchCmd.Warn` is set in Task 3 and printed in Task 1.
- **Correction to spec:** the spec's illustrative `piModelsFile` struct omitted pi's `api`/`apiKey`/`baseUrl` provider fields; the plan's struct (Task 2) captures them so a sync never clobbers pi's own config.
