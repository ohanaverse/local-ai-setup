# Lesson 18: Testing

## Concept Intro

The bash tool has no test suite (only `make lint`/`make format` + manual smoke
tests). Go gives us table-driven unit tests and, crucially, real integration
tests against temporary git repos. This lesson formalizes testing across the
packages you've built:

- **Unit tests** — config parsing/validation, migration, rotation state,
  registry merging, slug computation.
- **Integration tests** — run `git init` in a `t.TempDir()` and exercise
  `worktree.Enumerate` / `EnsureForName` against a real repo.
- **Driver tests** — assert the `exec.Cmd`/`LaunchCmd` built by each agent
  driver (env vars, args, yolo flag).
- **TUI tests** — feed synthetic `tea.Msg` values into `Update` and assert
  the resulting state (no terminal needed).

We also add a `Makefile` target (replacing the shell-based `make test`) so
`go test ./...` is the gate.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| table-driven test | `tests := []struct{...}{...}` + loop over cases. |
| `t.TempDir()` | Creates an auto-cleaned temp dir per test. |
| `exec.Command("git", ...)` in tests | Sets up real repos via `CombinedOutput()`. |
| `testing.T.Run(name, fn)` | Sub-tests for a case table. |
| `go test ./...` | Runs all package tests. |
| `go vet ./...` | Static analysis gate (optional but recommended). |

## Worked Walkthrough

### Unit: config validation

`internal/config/config_test.go`:

```go
package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
		wantErr bool
	}{
		{"ok", &Config{DefaultTag: "code", Models: []Model{
			{ID: "m1", Provider: "ollama", Location: LocationCloud, Tags: []string{"code"}},
		}}, false},
		{"dup id", &Config{DefaultTag: "code", Models: []Model{
			{ID: "m1", Provider: "ollama", Location: LocationCloud},
			{ID: "m1", Provider: "ollama", Location: LocationCloud},
		}}, true},
		{"bad location", &Config{DefaultTag: "code", Models: []Model{
			{ID: "m1", Provider: "ollama", Location: "edge"},
		}}, true},
		{"empty default tag", &Config{DefaultTag: "", Models: nil}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.Validate()
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
```

### Unit: rotation advance

`internal/rotation/rotation_test.go`:

```go
package rotation

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func TestNextAdvances(t *testing.T) {
	models := []config.Model{
		{ID: "a", Tags: []string{"code"}},
		{ID: "b", Tags: []string{"code"}},
	}
	r := New("code", models, t.TempDir())
	first, _ := r.Next("")
	second, _ := r.Next("")
	if first.ID == second.ID {
		t.Fatalf("expected rotation to advance, got %s twice", first.ID)
	}
}
```

### Integration: worktree enumeration in a temp repo

`internal/worktree/worktree_test.go`:

```go
package worktree

import (
	"os/exec"
	"testing"
)

// gitIn runs a git command in dir, failing the test on error.
func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestEnumerateFindsWorktreeAndBranch(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init")
	gitIn(t, dir, "config", "user.email", "t@t")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "commit", "--allow-empty", "-m", "init")
	gitIn(t, dir, "checkout", "-b", "feature")

	entries, err := Enumerate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if entries[0].Type != TypeCurrent {
		t.Fatalf("expected first entry to be current, got %s", entries[0].Type)
	}
}
```

### Driver test

`internal/agents/codex_test.go`:

```go
package agents

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func TestCodexBuildCloud(t *testing.T) {
	d := ByName("codex")
	if d == nil {
		t.Fatal("codex driver not registered")
	}
	lc := d.Build(config.Model{ID: "deepseek-v4-pro:cloud", Location: config.LocationCloud}, true)
	// native=false -> passes --model; yolo=true -> adds yolo flag
	found := map[string]bool{}
	for _, a := range lc.Args {
		found[a] = true
	}
	if !found["--model"] || !found["deepseek-v4-pro:cloud"] || !found["--skip-permission"] {
		t.Fatalf("unexpected args: %v", lc.Args)
	}
}
```

### TUI test (state only)

`internal/tui/app_test.go`:

```go
package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func TestRotateKeyAdvancesModel(t *testing.T) {
	m := model{
		phase: phaseModel,
		cfg:   &config.Config{DefaultTag: "code", Models: codeModels()},
		tag:   "code",
		agent: "claude",
	}
	m.current = firstModel(m.cfg, "code")

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("r")})
	nm := updated.(model)
	if nm.current.ID == m.current.ID {
		t.Fatalf("expected model to change after rotate")
	}
}
```

### Makefile target

Replace the smoke-test loop in `Makefile` with:

```makefile
test:
	go vet ./...
	go test ./... -count=1
```

## Run It

```bash
go test ./... -count=1
```

```
ok  	github.com/ohanaverse/agent-worktree/internal/config
ok  	github.com/ohanaverse/agent-worktree/internal/rotation
ok  	github.com/ohanaverse/agent-worktree/internal/worktree
...
```

## Try It Yourself

Add an integration test for `EnsureForName` in a temp repo that asserts the
returned path exists and is a registered worktree.

<details>
<summary>Solution</summary>

```go
func TestEnsureForName(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init")
	gitIn(t, dir, "config", "user.email", "t@t")
	gitIn(t, dir, "config", "user.name", "t")
	gitIn(t, dir, "commit", "--allow-empty", "-m", "init")
	gitIn(t, dir, "checkout", "-b", "base") // avoid main

	path, err := EnsureForName("my-feature")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("worktree path missing: %v", err)
	}
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 18: testing" && git tag lesson-18
```
