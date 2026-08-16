# Lesson 18: Testing

## Concept Intro

The bash tool has no automated unit test suite (only `make lint`/`make format` + manual smoke tests). Go gives us table-driven unit tests and, crucially, real integration tests against temporary git repos. This lesson formalizes testing across the packages you've built.

A few conventions make the suite self-documenting:

- **What/why comments** — every `Test*` function starts with a `//` block explaining what it tests and why that matters (the user-facing consequence of a regression). This is required in this repo; it tells future maintainers why each assertion is worth keeping.
- **Unit tests** — config parsing/validation, migration, rotation state, registry merging, slug computation.
- **Integration tests** — run `git init` in a `t.TempDir()` and exercise `worktree.Enumerate` / `EnsureForName` / `EnsureForBranch` against a real repo.
- **Driver tests** — assert the `exec.Cmd`/`LaunchCmd` built by each agent driver (env vars, args, yolo flag).
- **TUI tests** — feed synthetic `tea.Msg` values into `Update` and assert the resulting state (no terminal needed).

The Go tests are the gate for the `wt` binary. The Makefile's `make test` target is still the *bash* smoke-test loop; for Go you run `go test ./...` directly (usually alongside `go vet ./...`).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| table-driven test | `tests := []struct{...}{...}` + loop over cases. |
| `t.TempDir()` | Creates an auto-cleaned temp dir per test. |
| `t.Setenv(key, val)` | Sets an environment variable for the duration of the test. |
| `exec.Command("git", ...)` in tests | Sets up real repos via `CombinedOutput()`. |
| `testing.T.Run(name, fn)` | Sub-tests for a case table. |
| `go test ./...` | Runs all package tests. |
| `go vet ./...` | Static analysis gate. |

## Worked Walkthrough

### Unit: config validation

`internal/config/config_test.go`:

```go
package config

import "testing"

// Model IDs are the unique keys for the registry. Duplicates would cause
// silent overwrites or ambiguous lookups when the TUI or rotation tries to
// select a model.
func TestValidate_DuplicateModelID(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Models: []Model{
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x", Location: "local"},
			{ID: "ollama/x", Family: "x", ProviderID: "ollama", ModelName: "x", Location: "local"},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate model id")
	}
}
```

### Unit: rotation advance

`internal/rotation/rotation_test.go`:

```go
package rotation

import (
	"os"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Next must advance the index each call and persist it to disk so the
// rotation survives process restarts. After cycling through all models it
// must wrap around. The state file must contain both the next index and
// the last selected model ID.
func TestNext_AdvancesAndPersists(t *testing.T) {
	dir := t.TempDir()
	models := []config.Model{
		{ID: "alpha"},
		{ID: "beta"},
		{ID: "gamma"},
	}
	r := New("code", models, dir)

	got := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		m, ok := r.Next("")
		if !ok {
			t.Fatalf("iteration %d: Next returned !ok", i)
		}
		got = append(got, m.ID)
	}

	want := []string{"alpha", "beta", "gamma", "alpha"}
	if len(got) != len(want) {
		t.Fatalf("got %d picks, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("pick %d: got %q, want %q", i, got[i], want[i])
		}
	}

	data, err := os.ReadFile(StateFile(dir, "code"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	gotState := string(data)
	wantState := "1\nalpha\n"
	if gotState != wantState {
		t.Errorf("state file = %q, want %q", gotState, wantState)
	}
}
```

### Integration: worktree enumeration in a temp repo

`internal/worktree/enumerate_test.go`:

```go
package worktree

import (
	"os/exec"
	"testing"
)

// gitInit creates a minimal git repo with one commit on main/master so
// subsequent git commands work. Used by every test that needs a repo.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
}

// Enumerate must list bare branches that are not checked out in any
// worktree. A branch that was created and then switched away from should
// appear as TypeBranch so the user can select it in the TUI.
func TestEnumerateFindsBranch(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	cmd := exec.Command("git", "checkout", "-b", "feature")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git checkout -b feature: %v\n%s", err, out)
	}

	// Switch back to main/master so feature is not in use.
	defaultBranch := "main"
	cmd = exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = dir
	if _, err := cmd.CombinedOutput(); err != nil {
		defaultBranch = "master"
		cmd = exec.Command("git", "checkout", defaultBranch)
		cmd.Dir = dir
		if _, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git checkout main/master: %v", err)
		}
	}

	entries, err := Enumerate(dir, dir)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Type == TypeBranch && e.Branch == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected feature branch entry, got %+v", entries)
	}
}
```

### Driver test

`internal/agents/agents_test.go`:

```go
package agents

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func cloudModel(id string) config.Model {
	return config.Model{ID: id, ModelName: id, Location: config.LocationCloud}
}

func nativeModel(agent string) config.Model {
	return config.Model{ID: agent + "/native", ModelName: "native", Location: config.LocationCloud}
}

// Codex is a cloud-only agent that passes --model for non-native models and
// nothing for native. It has no custom env vars. Verify both paths.
func TestCodex(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}
	lc := d.Build(cloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 2 || lc.Args[0] != "--model" || lc.Args[1] != "deepseek-v4-pro:cloud" {
		t.Errorf("args = %v, want [--model deepseek-v4-pro:cloud]", lc.Args)
	}
	if len(lc.Env) != 0 {
		t.Errorf("env = %v, want none", lc.Env)
	}
	if d.Build(nativeModel("codex"), false).Args != nil {
		t.Errorf("native build should have no args")
	}
}
```

### TUI test (state only)

`internal/tui/app_test.go`:

```go
package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/rotation"
)

func testConfig() *config.Config {
	return &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "ollama"},
		},
		Models: []config.Model{
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:14b", ProviderID: "ollama", Tags: []string{"code"}},
			{ID: "ollama/gemma4:design", ProviderID: "ollama", Tags: []string{"design"}},
		},
		Agents: []config.Agent{
			{Name: "claude"},
		},
	}
}

// TestRotateKeyAdvancesModel asserts pressing 'r' in the model phase advances
// the current model through the active tag group via rotation.Next. This is
// the explicit replacement for the bash tool's silent auto-rotation.
func TestRotateKeyAdvancesModel(t *testing.T) {
	// rotation.ForTag reads its next index from a per-tag state file on disk,
	// so isolate the state in a temp XDG_CONFIG_HOME and seed it to point at
	// the second model. Without this the test depends on the host's real
	// rotation-code.state and is non-deterministic.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	stateDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "agent-wt")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("mkdir state dir: %v", err)
	}
	if err := os.WriteFile(rotation.StateFile(stateDir, "code"),
		[]byte("1\nollama/gemma4:9b\n"), 0o600); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	m := model{cfg: testConfig(), phase: phaseModel, tag: "code",
		current: config.Model{ID: "ollama/gemma4:9b"}}
	got, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	gotModel := got.(model)
	if gotModel.current.ID != "ollama/gemma4:14b" {
		t.Errorf("current = %q, want second code model", gotModel.current.ID)
	}
}
```

### Running the Go gate

The `Makefile` still owns the bash-script smoke tests. For the Go rewrite the gate is:

```bash
go vet ./...
go test ./... -count=1
```

## Run It

```bash
go test ./... -count=1
```

```
ok  	github.com/ohanaverse/agent-worktree/cmd/wt
ok  	github.com/ohanaverse/agent-worktree/internal/agents
ok  	github.com/ohanaverse/agent-worktree/internal/config
ok  	github.com/ohanaverse/agent-worktree/internal/guard
ok  	github.com/ohanaverse/agent-worktree/internal/initseed
ok  	github.com/ohanaverse/agent-worktree/internal/registry
ok  	github.com/ohanaverse/agent-worktree/internal/rotation
ok  	github.com/ohanaverse/agent-worktree/internal/session
ok  	github.com/ohanaverse/agent-worktree/internal/tui
ok  	github.com/ohanaverse/agent-worktree/internal/worktree
```

## Try It Yourself

Add an integration test for `EnsureForName` in a temp repo that asserts the returned path exists and is a registered worktree.

<details>
<summary>Solution</summary>

```go
// EnsureForName must create a worktree at .worktrees/<name> for a brand-new
// branch. The returned path must exist and the branch must be checked out
// there.
func TestEnsureForNameCreatesWorktree(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	path, err := EnsureForName(dir, "my-feature")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, ".worktrees", "my-feature")
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree not created: %v", err)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 18: testing" && git tag lesson-18
```
