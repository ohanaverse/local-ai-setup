# shell-wt without a modelman registry — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `shell-wt` (and any command agent) work on a fresh machine with no modelman `registry.toml`, while keeping the fail-closed behavior and "seed with `modelman migrate`" message for real (model-driven) agents.

**Architecture:** Add a sentinel error `config.ErrRegistryMissing` returned by `loadRegistry()` when the file is absent. Relax the `a.cfgErr` gate in `cmd/wt/main.go` so it only fails when the agent is *not* a command, or when the error is something other than a missing registry. Command agents have no model layer, so they never need the registry.

**Tech Stack:** Go (module `github.com/ohanaverse/local-ai-setup/wt`), `github.com/BurntSushi/toml`, `github.com/spf13/cobra`.

**Spec:** `docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md`

---

### Task 1: Sentinel error `ErrRegistryMissing`

**Files:**
- Modify: `internal/config/registry.go:82-86` (the `os.IsNotExist` branch of `loadRegistry`)
- Modify: `internal/config/registry_test.go:110-119` (`TestLoad_FailsClosedWithoutRegistry`)
- Test: `internal/config/registry_test.go`

- [ ] **Step 1: Write the failing tests**

Add `"errors"` to the imports in `internal/config/registry_test.go` (currently imports `os`, `path/filepath`, `strings`, `testing`). Then update `TestLoad_FailsClosedWithoutRegistry` to also assert the sentinel, and add a focused `loadRegistry` test:

```go
func TestLoad_FailsClosedWithoutRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when registry.toml is missing")
	}
	if !errors.Is(err, ErrRegistryMissing) {
		t.Errorf("error should wrap ErrRegistryMissing, got: %v", err)
	}
	if !strings.Contains(err.Error(), "modelman migrate") {
		t.Errorf("error should point at `modelman migrate`, got: %v", err)
	}
}

// loadRegistry must return the ErrRegistryMissing sentinel (not just any
// error) when registry.toml is absent, so cmd/wt can distinguish "no
// registry yet" (tolerable for command agents) from a genuine config
// problem (malformed file, etc.).
func TestLoadRegistryMissingReturnsSentinel(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, _, err := loadRegistry()
	if !errors.Is(err, ErrRegistryMissing) {
		t.Fatalf("loadRegistry() error = %v, want ErrRegistryMissing", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/config -run 'TestLoad_FailsClosedWithoutRegistry|TestLoadRegistryMissingReturnsSentinel' -v`
Expected: FAIL — `ErrRegistryMissing` is undefined (compile error).

- [ ] **Step 3: Implement the sentinel**

Add `"errors"` to the imports in `internal/config/registry.go` (currently imports `fmt`, `os`, `path/filepath`, `strings`, `toml`). Add the sentinel near the top of the file (after the imports) and wrap it in the `os.IsNotExist` branch:

```go
// ErrRegistryMissing is returned by loadRegistry when the modelman-owned
// registry.toml does not exist yet. cmd/wt tolerates it for command agents
// (which have no model layer) while still failing closed for real agents.
var ErrRegistryMissing = errors.New("model registry not found")
```

Change the `os.IsNotExist` branch of `loadRegistry` (currently at `registry.go:82-86`):

```go
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf(
			"%w at %s — seed it with `modelman migrate`", ErrRegistryMissing, path)
	}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/config -run 'TestLoad_FailsClosedWithoutRegistry|TestLoadRegistryMissingReturnsSentinel' -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/config/registry.go internal/config/registry_test.go
git commit -m "feat(config): add ErrRegistryMissing sentinel for absent registry"
```

---

### Task 2: Conditional `a.cfgErr` gate for command agents

**Files:**
- Modify: `cmd/wt/main.go:277-281` (the `a.cfgErr` gate)
- Test: `cmd/wt/main_test.go`

- [ ] **Step 1: Write the failing tests**

Add two tests to `cmd/wt/main_test.go`. They use the existing `tuiRun`/`stdinTTY` stub seams (same pattern as `TestWorktreeWithAgentWithoutModelShowsModelPicker`). No registry is written — this simulates a fresh machine.

```go
// A command agent (shell) must launch even when registry.toml is missing:
// commands have no model layer, so the missing-registry config error must
// not block the worktree picker. Regression guard for the a.cfgErr gate.
func TestCommandAgentSkipsMissingRegistryGate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")

	var called bool
	oldTuiRun := tuiRun
	tuiRun = func(yolo bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
		called = true
		return nil
	}
	defer func() { tuiRun = oldTuiRun }()
	oldStdinTTY := stdinTTY
	stdinTTY = func() bool { return true }
	defer func() { stdinTTY = oldStdinTTY }()

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "shell"})
	if err := root.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected tuiRun to be called (gate should be skipped for command agent)")
	}
}

// A real (model-driven) agent must still fail closed with the clear
// "seed with modelman migrate" message when registry.toml is missing.
func TestNonCommandAgentStillFailsWithoutRegistry(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")

	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "claude"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected config error for non-command agent with missing registry")
	}
	if !strings.Contains(err.Error(), "modelman migrate") {
		t.Errorf("error should point at `modelman migrate`, got: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wt -run 'TestCommandAgentSkipsMissingRegistryGate|TestNonCommandAgentStillFailsWithoutRegistry' -v`
Expected: `TestCommandAgentSkipsMissingRegistryGate` FAILS (the gate returns a config error before `tuiRun` is called). `TestNonCommandAgentStillFailsWithoutRegistry` PASSES (already correct).

- [ ] **Step 3: Implement the conditional gate**

Add `"errors"` to the imports in `cmd/wt/main.go` (currently imports `fmt`, `os`, `strings`, plus the internal packages and `cobra`). Then change the gate at `main.go:277-281`:

```go
			// Launch paths require a valid config. The `wt config` subcommand
			// bypasses this so it can repair a broken config.toml. Command
			// agents (shell, etc.) have no model layer, so a missing modelman
			// registry is tolerated for them — only real agents need the
			// registry, and they still fail closed with the migrate hint.
			if a.cfgErr != nil && !(agent != "" && agents.IsCommand(agent) && errors.Is(a.cfgErr, config.ErrRegistryMissing)) {
				return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
			}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wt -run 'TestCommandAgentSkipsMissingRegistryGate|TestNonCommandAgentStillFailsWithoutRegistry' -v`
Expected: PASS (both).

- [ ] **Step 5: Run the full test suite**

Run: `go test ./...`
Expected: all packages `ok`.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/main.go cmd/wt/main_test.go
git commit -m "feat(wt): allow command agents to launch without a modelman registry"
```

---

### Task 3: Update the shell-wt reference doc

**Files:**
- Modify: `docs/wt-agents/shell-wt.md`

- [ ] **Step 1: Add a note about the missing-registry behavior**

Add a short note to `docs/wt-agents/shell-wt.md` (in the section describing how `shell-wt` differs from agent launchers) stating that `shell-wt` works without a modelman registry because it has no model layer, and that real agents still require `modelman migrate`.

- [ ] **Step 2: Commit**

```bash
git add docs/wt-agents/shell-wt.md
git commit -m "docs(wt): note shell-wt works without a modelman registry"
```

---

## Self-Review

**Spec coverage:**
- Sentinel `ErrRegistryMissing` → Task 1. ✓
- Conditional gate → Task 2. ✓
- Edge cases (malformed config.toml / registry, real agents, `wt config`, `--init`, unpinned TUI) → all preserved by the gate condition; real-agent fail-closed covered by `TestNonCommandAgentStillFailsWithoutRegistry`. ✓
- Tests for both non-TUI and TUI paths → Task 2 covers the TUI path via the stubbed `tuiRun`; the non-TUI path shares the same gate, and `TestShellPassthrough_StillWorks` already exercises `--agent shell` passthrough. ✓

**Placeholder scan:** No TBD/TODO; every step has concrete code and commands. ✓

**Type consistency:** `ErrRegistryMissing` is defined in Task 1 and referenced in Task 2; `errors.Is` used consistently; `agents.IsCommand` matches the existing API. ✓
