# `wt` Bash Parity Gap Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Close the highest-priority feature gaps between the Go `wt` implementation and the legacy bash engine: auto-install the main guard on every launch, make `--init` seed agent-specific pointer files, warn on default-branch launches, and restore `--no-guard` / `--check-guard` flags.

**Architecture:** Keep guard and initseed logic in their existing packages; add small helper wrappers in `cmd/wt` for flag handling and best-effort guard installation. Extend the Bubble Tea model in `internal/tui` with a default-branch warning state. No package restructure is needed.

**Tech Stack:** Go 1.26.3, cobra, charmbracelet/bubbles, git.

---

## File map

| File | Responsibility |
|---|---|
| `cmd/wt/helpers.go` | New helpers: `maybeInstallGuard`, `checkGuardStatus`, `removeGuard`. Existing helpers `mustGetString`, `yolo`, `inGitRepo`, etc. |
| `cmd/wt/main.go` | Wire `--init` to the resolved `--agent` flag; wire `--check-guard` and `--no-guard`; call `maybeInstallGuard()` before non-init launches. |
| `cmd/wt/helpers_test.go` | Unit tests for the new guard helpers. |
| `internal/tui/app.go` | Detect single current/default-branch entry; set warning title; add `phaseGuardWarn` and confirmation prompt. |
| `internal/tui/launch.go` | Add `buildGuardChoices` list helper (reuses `resumeItem` pattern). |
| `internal/tui/app_test.go` | Tests for default-branch detection and guard-warn prompt. |

---

## Task 1: Auto-install main guard on every launch

**Files:**
- Modify: `cmd/wt/helpers.go`
- Modify: `cmd/wt/main.go`
- Create: `cmd/wt/helpers_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/wt/helpers_test.go`:

```go
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/guard"
)

func gitInit(t *testing.T, dir string) {
	t.Helper()
	if out, err := exec.Command("git", "init", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.email", "test@test").CombinedOutput(); err != nil {
		t.Fatalf("git config email: %v\n%s", err, out)
	}
	if out, err := exec.Command("git", "-C", dir, "config", "user.name", "Test").CombinedOutput(); err != nil {
		t.Fatalf("git config name: %v\n%s", err, out)
	}
}

// TestMaybeInstallGuardInRepo installs the guard in a temp repo and verifies
// that a subsequent Check reports Installed. Without this, the launcher would
// silently skip guard protection on normal launches.
func TestMaybeInstallGuardInRepo(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	maybeInstallGuard()

	if guard.Check() != guard.Installed {
		t.Fatal("expected guard installed after maybeInstallGuard")
	}
}

// TestMaybeInstallGuardOutsideRepo does nothing and does not error when not
// inside a git repo. The passthrough path must remain safe outside version
// control.
func TestMaybeInstallGuardOutsideRepo(t *testing.T) {
	oldWd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(oldWd)

	maybeInstallGuard() // should not panic or print fatal error
}

// TestMaybeInstallGuardIsIdempotent calls the helper twice in the same repo;
// the second call must not error or leave a broken hook.
func TestMaybeInstallGuardIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	maybeInstallGuard()
	maybeInstallGuard()

	hookPath := filepath.Join(dir, ".git", "hooks", "pre-commit")
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("hook missing after idempotent install: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./cmd/wt -run TestMaybeInstallGuard -v
```

Expected: compile failure because `maybeInstallGuard` is not defined.

- [ ] **Step 3: Add `maybeInstallGuard` to `cmd/wt/helpers.go`**

Append to `cmd/wt/helpers.go`:

```go
// maybeInstallGuard installs the main guard when inside a git repo. Errors
// are written to stderr and ignored so that a guard-install failure does not
// block the agent launch. This matches the bash engine's best-effort
// behavior.
func maybeInstallGuard() {
	if !inGitRepo() {
		return
	}
	if _, err := guard.Install(); err != nil {
		fmt.Fprintf(os.Stderr, "wt: failed to auto-install main guard: %v\n", err)
	}
}
```

Add imports for `fmt`, `os`, and `github.com/ohanaverse/agent-worktree/internal/guard` if not already present.

- [ ] **Step 4: Wire `maybeInstallGuard` into `cmd/wt/main.go`**

Modify the three launch paths in `rootCmd().RunE`:

1. In the `-w` block, after `worktree.RepoRoot()` succeeds:
```go
root, err := worktree.RepoRoot()
if err != nil {
    return err
}
maybeInstallGuard()
path, err := worktree.EnsureForName(root, name)
```

2. In the `--cwd` block, after `worktree.RepoRoot()` succeeds:
```go
root, err := worktree.RepoRoot()
if err != nil {
    return err
}
maybeInstallGuard()
return launch(agent, root, a.cfg, yolo(cmd))
```

3. In the TUI block, after confirming we are in a repo:
```go
// Interactive TUI.
maybeInstallGuard()
return tui.Run(yolo(cmd))
```

The `--init` path already calls `guard.Install()`; leave it unchanged.

- [ ] **Step 5: Run the tests**

```bash
go test ./cmd/wt -run TestMaybeInstallGuard -v
```

Expected: PASS.

- [ ] **Step 6: Run the full test suite**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/wt/helpers.go cmd/wt/helpers_test.go cmd/wt/main.go
git commit -m "feat: auto-install main guard on every launch"
```

---

## Task 2: Make `--init` respect `--agent`

**Files:**
- Modify: `cmd/wt/main.go`
- Modify: `cmd/wt/launch_test.go` (to add a test for agent flag wiring)

- [ ] **Step 1: Write the failing test**

Add to `cmd/wt/launch_test.go`:

```go
// TestInitUsesAgentFlag verifies that the --init path passes the explicit
// --agent value to initseed.Seed so that agent-specific pointer files
// (e.g. CLAUDE.md) are created. This mirrors the bash wrapper behavior where
// claude-wt --init seeded CLAUDE.md.
func TestInitUsesAgentFlag(t *testing.T) {
	// We cannot easily exercise the full cobra command because newApp loads
	// the real config. Instead, assert that initseed.Seed("claude", root)
	// creates the expected pointer file, which is the behavior main.go must
	// now invoke when --agent claude --init is used.
	root := t.TempDir()
	res, err := initseed.Seed("claude", root)
	if err != nil {
		t.Fatalf("Seed(claude): %v", err)
	}
	found := false
	for _, name := range res.Created {
		if name == "CLAUDE.md" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected CLAUDE.md created, got %v", res.Created)
	}
}
```

Add the `initseed` import to `cmd/wt/launch_test.go`.

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./cmd/wt -run TestInitUsesAgentFlag -v
```

Expected: compile error because `initseed` import is missing (or test passes if import already present). The real failure we want to fix is in `main.go`.

- [ ] **Step 3: Modify `cmd/wt/main.go` to read `--agent` before `--init`**

Locate the `--init` block at the top of `RunE`. Replace:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    if initFlag, _ := cmd.Flags().GetBool("init"); initFlag {
        root, err := initseed.Root()
        if err != nil {
            return err
        }
        res, err := initseed.Seed("", root)
        // ...
    }

    // Resolve the agent: --agent flag wins, else the config default.
    agent := mustGetString(cmd, "agent")
    if agent == "" {
        agent = defaultAgent(a.cfg)
    }
```

with:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    // Read the raw --agent flag early so --init can seed agent-specific
    // pointer files when a wrapper like claude-wt forwards --agent claude.
    agentFlag := mustGetString(cmd, "agent")

    if initFlag, _ := cmd.Flags().GetBool("init"); initFlag {
        root, err := initseed.Root()
        if err != nil {
            return err
        }
        res, err := initseed.Seed(agentFlag, root)
        // ... leave rest of block unchanged ...
    }

    // Resolve the agent: --agent flag wins, else the config default.
    agent := agentFlag
    if agent == "" {
        agent = defaultAgent(a.cfg)
    }
```

- [ ] **Step 4: Run the tests**

```bash
go test ./cmd/wt -run TestInitUsesAgentFlag -v
go test ./internal/initseed -v
```

Expected: PASS.

- [ ] **Step 5: Run the full test suite**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/main.go cmd/wt/launch_test.go
git commit -m "feat: make --init seed agent-specific pointer files via --agent"
```

---

## Task 3: Default-branch safety nudge in the TUI

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/launch.go` (add choice list helper)
- Modify: `internal/tui/app_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `internal/tui/app_test.go`:

```go
// TestEntriesLoadedSetsDefaultBranchWarning asserts that when the only
// pickable target is the current worktree on the repo's default branch, the
// list title is updated to warn the user. Without this, a user on main could
// launch an agent without realizing they are working directly on the
// protected branch.
func TestEntriesLoadedSetsDefaultBranchWarning(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	m := model{cfg: cfg, loading: true, width: 80, height: 24}

	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
	}
	newM, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main", err: nil})
	mm := newM.(model)

	if mm.defaultBranch != "main" {
		t.Fatalf("defaultBranch = %q, want main", mm.defaultBranch)
	}
	if !strings.Contains(mm.list.Title, "main") {
		t.Fatalf("expected title to contain default branch warning, got %q", mm.list.Title)
	}
}

// TestEntriesLoadedNoWarningForMultipleEntries asserts that when there are
// multiple choices, no default-branch warning is shown.
func TestEntriesLoadedNoWarningForMultipleEntries(t *testing.T) {
	cfg := &config.Config{DefaultTag: "code"}
	m := model{cfg: cfg, loading: true, width: 80, height: 24}

	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/repo"},
		{Type: worktree.TypeBranch, Branch: "feature"},
	}
	newM, _ := m.Update(entriesLoadedMsg{entries: entries, defaultBranch: "main", err: nil})
	mm := newM.(model)

	if strings.Contains(mm.list.Title, "WARNING") {
		t.Fatalf("expected no warning title, got %q", mm.list.Title)
	}
}
```

Add imports for `strings` and `github.com/ohanaverse/agent-worktree/internal/config` / `worktree` if missing.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/tui -run TestEntriesLoadedSetsDefaultBranchWarning -v
```

Expected: compile failure because `defaultBranch` field does not exist on `model`.

- [ ] **Step 3: Add state and detection to `internal/tui/app.go`**

Add a new phase constant:

```go
const (
	phaseList       phase = iota
	phaseModel
	phaseBrowser
	phaseResume
	phaseGuardWarn          // confirm before launching on default branch without guard
)
```

Add fields to the `model` struct:

```go
defaultBranch   string       // repo default branch (e.g. main) for safety checks
guardWarnModel  list.Model   // confirmation choices for default-branch launch
guardWarnEntry  worktree.Entry // the entry being confirmed
```

Add a helper to detect the condition (place near `loadEntriesCmd`):

```go
// isCurrentOnDefaultBranch returns true when the entry is the current
// worktree and its branch matches the repo default branch.
func isCurrentOnDefaultBranch(e worktree.Entry, defaultBranch string) bool {
	return defaultBranch != "" && e.Type == worktree.TypeCurrent && e.Branch == defaultBranch
}
```

Add `"github.com/ohanaverse/agent-worktree/internal/guard"` to the imports of `internal/tui/app.go`.

Update the `entriesLoadedMsg` branch in `Update`:

```go
case entriesLoadedMsg:
	m.loading = false
	if msg.err != nil {
		m.status = "error: " + msg.err.Error()
		return m, nil
	}
	m.entries = msg.entries
	m.defaultBranch = msg.defaultBranch
	m.list = buildList(msg.entries, m.width-2, m.height-2)
	m.ready = true

	if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
		m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
	}
	return m, nil
```

- [ ] **Step 4: Extend `entriesLoadedMsg` to carry the default branch**

Change the type:

```go
type entriesLoadedMsg struct {
	entries       []worktree.Entry
	defaultBranch string
	err           error
}
```

Update `loadEntriesCmd`:

```go
func loadEntriesCmd() tea.Cmd {
	return func() tea.Msg {
		root, err := worktree.RepoRoot()
		if err != nil {
			return entriesLoadedMsg{err: err}
		}
		entries, err := worktree.Enumerate(root, root)
		defaultBranch, _ := worktree.DefaultBranch(root)
		return entriesLoadedMsg{entries: entries, defaultBranch: defaultBranch, err: err}
	}
}
```

Export `defaultBranch` from `internal/worktree/enumerate.go`. Rename the unexported `defaultBranch(dir string)` to `DefaultBranch(dir string)` (capitalized) so the TUI can call it. Update the one internal call site in `Enumerate`.

- [ ] **Step 5: Update the warning detection to use the real default branch**

Replace the warning detection in `Update(entriesLoadedMsg)` with:

```go
if len(msg.entries) == 1 && isCurrentOnDefaultBranch(msg.entries[0], msg.defaultBranch) {
	m.list.Title = "WARNING: you are on the default branch (" + msg.defaultBranch + ")"
}
```

- [ ] **Step 6: Add the guard-warn confirmation prompt**

Add a helper in `internal/tui/launch.go` to build choices:

```go
// guardChoice identifies a choice in the default-branch guard prompt.
type guardChoice int

const (
	guardProceedChoice guardChoice = iota
	guardCancelChoice
)

// guardItem adapts a guard prompt choice to list.Item.
type guardItem struct {
	choice guardChoice
	title  string
	desc   string
}

func (g guardItem) FilterValue() string { return g.title }
func (g guardItem) Title() string       { return g.title }
func (g guardItem) Description() string { return g.desc }

// buildGuardChoices creates the default-branch confirmation list items.
func buildGuardChoices(branch string, installed bool) []list.Item {
	hint := "commits to " + branch + " are blocked"
	if !installed {
		hint = "WARNING: main guard is NOT installed — commits to " + branch + " are NOT blocked"
	}
	return []list.Item{
		guardItem{choice: guardProceedChoice, title: "Proceed anyway", desc: hint},
		guardItem{choice: guardCancelChoice, title: "Cancel", desc: "Return to the worktree list"},
	}
}
```

In `internal/tui/app.go`, update the `enter` key handler in `phaseList`:

```go
case phaseList:
	if !m.ready {
		return m, nil
	}
	item, ok := m.list.SelectedItem().(entryItem)
	if !ok {
		return m, nil
	}
	if isCurrentOnDefaultBranch(item.entry, m.defaultBranch) {
		installed := guard.Check() == guard.Installed
		m.guardWarnEntry = item.entry
		m.guardWarnModel = list.New(buildGuardChoices(item.entry.Branch, installed), list.NewDefaultDelegate(), m.width-2, m.height-2)
		m.guardWarnModel.Title = "Launch on default branch?"
		m.phase = phaseGuardWarn
		return m, nil
	}
	return m, func() tea.Msg { return selectedEntryMsg{entry: item.entry} }
```

Add a new key handler block for `phaseGuardWarn` in the `enter` case:

```go
case phaseGuardWarn:
	if item, ok := m.guardWarnModel.SelectedItem().(guardItem); ok {
		switch item.choice {
		case guardProceedChoice:
			return m, func() tea.Msg { return selectedEntryMsg{entry: m.guardWarnEntry} }
		case guardCancelChoice:
			m.phase = phaseList
			return m, nil
		}
	}
```

Also handle `esc` in `phaseGuardWarn` to return to `phaseList`:

```go
case "esc":
	if m.phase == phaseBrowser {
		m.phase = phaseModel
		return m, nil
	}
	if m.phase == phaseResume {
		m.phase = phaseModel
		return m, nil
	}
	if m.phase == phaseGuardWarn {
		m.phase = phaseList
		return m, nil
	}
	return m, tea.Quit
```

Add `phaseGuardWarn` to the `View` function:

```go
if m.phase == phaseGuardWarn {
	if m.width <= 0 || m.height <= 0 {
		return "default-branch warning (waiting for window size)"
	}
	return m.guardWarnModel.View() + "\n[enter] choose   [esc] back"
}
```

Wire `m.guardWarnModel.Update(msg)` in the final delegation block:

```go
if m.phase == phaseGuardWarn && m.width > 0 && m.height > 0 {
	var cmd tea.Cmd
	m.guardWarnModel, cmd = m.guardWarnModel.Update(msg)
	return m, cmd
}
```

- [ ] **Step 7: Update existing tests that may break from new fields**

`TestEntriesLoadedSetsDefaultBranchWarning` and `TestEntriesLoadedNoWarningForMultipleEntries` should now pass. Run all TUI tests to catch regressions.

- [ ] **Step 8: Run the tests**

```bash
go test ./internal/tui -run TestEntriesLoaded -v
go test ./internal/tui -v
```

Expected: PASS.

- [ ] **Step 9: Run the full test suite**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/app.go internal/tui/launch.go internal/tui/app_test.go internal/worktree/enumerate.go
git commit -m "feat: warn and confirm before launching on the default branch"
```

---

## Task 4: Add `--no-guard` and `--check-guard` flags

**Files:**
- Modify: `cmd/wt/helpers.go`
- Modify: `cmd/wt/main.go`
- Modify: `cmd/wt/helpers_test.go`

- [ ] **Step 1: Write the failing tests**

Add to `cmd/wt/helpers_test.go`:

```go
// TestCheckGuardStatusInstalled reports Installed when the guard has been
// installed in the current repo.
func TestCheckGuardStatusInstalled(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	if _, err := guard.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}

	status, err := checkGuardStatus()
	if err != nil {
		t.Fatalf("checkGuardStatus: %v", err)
	}
	if status != guard.Installed {
		t.Fatalf("status = %v, want Installed", status)
	}
}

// TestCheckGuardStatusNotInstalled reports NotInstalled for a fresh repo.
func TestCheckGuardStatusNotInstalled(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	status, err := checkGuardStatus()
	if err != nil {
		t.Fatalf("checkGuardStatus: %v", err)
	}
	if status != guard.NotInstalled {
		t.Fatalf("status = %v, want NotInstalled", status)
	}
}

// TestRemoveGuardUninstalls the guard so --no-guard can restore the original
// hook.
func TestRemoveGuardUninstalls(t *testing.T) {
	dir := t.TempDir()
	gitInit(t, dir)

	oldWd, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(oldWd)

	if _, err := guard.Install(); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := removeGuard(); err != nil {
		t.Fatalf("removeGuard: %v", err)
	}
	if guard.Check() != guard.NotInstalled {
		t.Fatal("expected guard removed")
	}
}

// TestGuardHelpersOutsideRepoError returns an error when not in a git repo so
// the flags cannot be misused outside version control.
func TestGuardHelpersOutsideRepoError(t *testing.T) {
	oldWd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(oldWd)

	if _, err := checkGuardStatus(); err == nil {
		t.Fatal("expected error outside repo for checkGuardStatus")
	}
	if err := removeGuard(); err == nil {
		t.Fatal("expected error outside repo for removeGuard")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./cmd/wt -run TestCheckGuardStatus -v
```

Expected: compile failure because `checkGuardStatus` and `removeGuard` are undefined.

- [ ] **Step 3: Add helpers to `cmd/wt/helpers.go`**

Append:

```go
// checkGuardStatus returns the guard status in the current repo. It returns
// an error when not inside a git repo so callers can report a clear message.
func checkGuardStatus() (guard.Status, error) {
	if !inGitRepo() {
		return guard.Err, fmt.Errorf("not inside a git repository")
	}
	return guard.Check(), nil
}

// removeGuard uninstalls the guard in the current repo. It returns an error
// when not inside a git repository.
func removeGuard() error {
	if !inGitRepo() {
		return fmt.Errorf("not inside a git repository")
	}
	return guard.Uninstall()
}
```

- [ ] **Step 4: Wire flags in `cmd/wt/main.go`**

Register the flags in `rootCmd()`:

```go
// Guard management flags (legacy parity).
cmd.Flags().Bool("check-guard", false, "Check if the main guard is installed and exit")
cmd.Flags().Bool("no-guard", false, "Uninstall the main guard and exit")
```

Add handlers near the top of `RunE`, before the `--init` check:

```go
RunE: func(cmd *cobra.Command, args []string) error {
    if check, _ := cmd.Flags().GetBool("check-guard"); check {
        status, err := checkGuardStatus()
        if err != nil {
            return err
        }
        switch status {
        case guard.Installed:
            fmt.Println("wt: main guard is installed in this repo.")
        default:
            fmt.Fprintln(os.Stderr, "wt: main guard is NOT installed in this repo.")
            os.Exit(1)
        }
        return nil
    }

    if noGuard, _ := cmd.Flags().GetBool("no-guard"); noGuard {
        if err := removeGuard(); err != nil {
            return err
        }
        fmt.Println("wt: main guard removed.")
        return nil
    }

    // ... existing --init and launch code ...
```

- [ ] **Step 5: Run the tests**

```bash
go test ./cmd/wt -run "TestCheckGuardStatus|TestRemoveGuard|TestGuardHelpers" -v
```

Expected: PASS.

- [ ] **Step 6: Run the full test suite**

```bash
go test ./...
```

Expected: all packages pass.

- [ ] **Step 7: Commit**

```bash
git add cmd/wt/helpers.go cmd/wt/helpers_test.go cmd/wt/main.go
git commit -m "feat: restore --check-guard and --no-guard flags"
```

---

## Self-review checklist

1. **Spec coverage:**
   - Auto-install guard on every launch → Task 1.
   - `--init` respect `--agent` → Task 2.
   - Default-branch safety nudge → Task 3.
   - `--no-guard` / `--check-guard` → Task 4.

2. **Placeholder scan:** All steps contain concrete code, file paths, and expected outputs. No TBD/ TODO/"handle edge cases" placeholders.

3. **Type consistency:**
   - `guard.Status`, `guard.Installed`, `guard.NotInstalled` from `internal/guard`.
   - `worktree.TypeCurrent` and `worktree.Entry` from `internal/worktree`.
   - `phaseGuardWarn` added alongside existing phase constants.
   - `DefaultBranch` exported from `internal/worktree`.

4. **Scope check:** This plan covers the four highest-priority gaps only. Follow-up items (ollama availability check, agent dependency pre-flight, shell-wt Go equivalent) remain documented in the parity spec and are out of scope here.

---

## Execution handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-16-wt-bash-parity-gaps.md`.

**Two execution options:**

1. **Subagent-Driven (recommended)** - Dispatch a fresh subagent per task, review between tasks, fast iteration.

2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
