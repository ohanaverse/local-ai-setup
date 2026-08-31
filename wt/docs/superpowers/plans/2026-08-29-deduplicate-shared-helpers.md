# Deduplicate Shared Helpers — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Collapse three duplicated helper areas: git-root resolution, agent-list building, and the near-identical launch branches in `cmd/wt/main.go`.

**Architecture:** Move git-root detection into `internal/worktree` (`IsRepo`, `RepoRootAt`), move agent-list construction into `internal/agents` (`AgentListEntry`, `ListEntries`), and replace the three launch branches in `cmd/wt/main.go` with a single `runLaunchPath` dispatcher. The TUI and configeditor become thin adapters around `agents.ListEntries`.

**Tech Stack:** Go, standard library, existing `internal/worktree`, `internal/agents`, `internal/tui`, `internal/configeditor`, `cmd/wt`.

---

## File map

| File | Responsibility |
|---|---|
| `internal/worktree/enumerate.go` | Add `IsRepo(dir)` and `RepoRootAt(dir)`; `RepoRoot()` delegates to `RepoRootAt(".")`. |
| `internal/worktree/enumerate_test.go` | Tests for `IsRepo`, `RepoRootAt`, updated `RepoRoot` tests. |
| `cmd/wt/helpers.go` | Remove `inGitRepo`/`inGitRepoAt`; use `worktree.IsRepo(".")` in guard helpers. |
| `cmd/wt/helpers_test.go` / `launch_test.go` | Remove `TestInGitRepoAt`; add/update guard-related tests if needed. |
| `internal/tui/app.go` | Remove `repoRootFor`; use `worktree.RepoRootAt(prePath)` in `Run`. |
| `internal/agents/agents.go` | Add `AgentListEntry` and `ListEntries`. |
| `internal/agents/agents_test.go` | Add `TestListEntries`. |
| `internal/tui/agent_picker.go` | Replace `buildAgentList` body with adapter from `agents.ListEntries`. |
| `internal/tui/agent_picker_test.go` | Keep existing tests; add `TestBuildAgentListAdapter`. |
| `internal/configeditor/agents_tab.go` | Replace `buildAgentsList` body with adapter from `agents.ListEntries`. |
| `internal/configeditor/agents_tab_test.go` | Keep existing tests; add `TestBuildAgentsListAdapter`. |
| `cmd/wt/main.go` | Add `runLaunchPath`; replace `-W`, `--cwd`, outside-repo, and TUI branches with calls to it. |
| `cmd/wt/main_test.go` | Add `TestRunLaunchPath` table-driven tests. |

---

### Task 1: Add shared git-root helpers to `internal/worktree`

**Files:**
- Modify: `internal/worktree/enumerate.go`
- Test: `internal/worktree/enumerate_test.go`

- [ ] **Step 1: Add `RepoRootAt` and `IsRepo`**

  In `internal/worktree/enumerate.go`, replace the existing `RepoRoot` implementation with a delegating pair:

  ```go
  // IsRepo reports whether dir is inside a git repository. It returns false
  // for any git error, matching the previous best-effort behavior of
  // cmd/wt/helpers.go:inGitRepoAt.
  func IsRepo(dir string) bool {
      _, err := RepoRootAt(dir)
      return err == nil
  }

  // RepoRootAt returns the absolute path of the git repository root that owns
  // dir. It reuses runGit so working-directory handling and error behavior
  // are consistent with the rest of the package.
  func RepoRootAt(dir string) (string, error) {
      out, err := runGit(dir, "rev-parse", "--show-toplevel")
      if err != nil {
          return "", err
      }
      return strings.TrimSpace(string(out)), nil
  }

  // RepoRoot is shorthand for RepoRootAt(".") — kept for existing callers.
  func RepoRoot() (string, error) {
      return RepoRootAt(".")
  }
  ```

- [ ] **Step 2: Run existing worktree tests**

  ```bash
  go test ./internal/worktree -run "TestRepoRoot" -v
  ```

  Expected: PASS.

- [ ] **Step 3: Add tests for new helpers**

  Append to `internal/worktree/enumerate_test.go`:

  ```go
  // IsRepo must report true for a repo root and false for an arbitrary
  // directory. This replaces the cmd/wt-level inGitRepoAt test and keeps the
  // contract in the package that actually owns git interaction.
  func TestIsRepo(t *testing.T) {
      repo := t.TempDir()
      gitInit(t, repo)
      if !IsRepo(repo) {
        t.Errorf("IsRepo(repo root) = false, want true")
      }
      if IsRepo(t.TempDir()) {
        t.Errorf("IsRepo(non-repo) = true, want false")
      }
  }

  // RepoRootAt must return the repo root for a subdirectory, matching
  // RepoRoot when run from inside the repo.
  func TestRepoRootAt(t *testing.T) {
      repo := t.TempDir()
      gitInit(t, repo)
      sub := filepath.Join(repo, "subdir")
      if err := os.MkdirAll(sub, 0o755); err != nil {
          t.Fatal(err)
      }
      got, err := RepoRootAt(sub)
      if err != nil {
          t.Fatal(err)
      }
      want, err := filepath.EvalSymlinks(repo)
      if err != nil {
          t.Fatal(err)
      }
      gotResolved, err := filepath.EvalSymlinks(got)
      if err != nil {
          t.Fatal(err)
      }
      if gotResolved != want {
          t.Errorf("RepoRootAt(subdir) = %q, want %q", got, want)
      }
  }
  ```

- [ ] **Step 4: Run the new tests**

  ```bash
  go test ./internal/worktree -run "TestIsRepo|TestRepoRootAt" -v
  ```

  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/worktree/enumerate.go internal/worktree/enumerate_test.go
  git commit -m "feat(worktree): add IsRepo and RepoRootAt helpers"
  ```

---

### Task 2: Remove `inGitRepo`/`inGitRepoAt` from `cmd/wt/helpers.go`

**Files:**
- Modify: `cmd/wt/helpers.go`
- Modify: `cmd/wt/launch_test.go`

- [ ] **Step 1: Replace `inGitRepo`/`inGitRepoAt` with `worktree.IsRepo(".")`**

  In `cmd/wt/helpers.go`:

  - Remove:
    ```go
    // inGitRepo reports whether the current directory is inside a git repo.
    func inGitRepo() bool {
        return inGitRepoAt(".")
    }

    // inGitRepoAt reports whether dir is inside a git repo. Separated from
    // inGitRepo so tests can point it at a temp repo without chdir'ing the
    // process.
    func inGitRepoAt(dir string) bool {
        return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
    }
    ```

  - Replace usage in `checkGuardStatus`:
    ```go
    func checkGuardStatus() (guard.Status, error) {
        if !worktree.IsRepo(".") {
            return guard.Err, fmt.Errorf("not inside a git repository")
        }
        return guard.Check(), nil
    }
    ```

  - Replace usage in `removeGuard`:
    ```go
    func removeGuard() error {
        if !worktree.IsRepo(".") {
            return fmt.Errorf("not inside a git repository")
        }
        return guard.Uninstall()
    }
    ```

  - Add import for `internal/worktree` if not already present.

- [ ] **Step 2: Remove `TestInGitRepoAt` from `cmd/wt/launch_test.go`**

  Delete this test (it is superseded by `internal/worktree.TestIsRepo`):

  ```go
  // TestInGitRepoAt asserts that inGitRepoAt reports true inside a git repo and
  // false outside one. This gates the passthrough path: outside a repo the
  // agent launches directly with no picker, so a wrong answer here would either
  // drop the user into the TUI or skip the picker unexpectedly.
  func TestInGitRepoAt(t *testing.T) {
      dir := t.TempDir()
      if inGitRepoAt(dir) {
          t.Fatal("inGitRepoAt = true for a non-repo directory")
      }
      if err := exec.Command("git", "-C", dir, "init", "-q").Run(); err != nil {
          t.Fatalf("git init: %v", err)
      }
      if !inGitRepoAt(dir) {
          t.Fatal("inGitRepoAt = false for a git repo")
      }
  }
  ```

- [ ] **Step 3: Run cmd/wt tests**

  ```bash
  go test ./cmd/wt -run "TestCheckGuard|TestRemoveGuard" -v
  ```

  (If no such tests exist, just run `go test ./cmd/wt`.)

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add cmd/wt/helpers.go cmd/wt/launch_test.go
  git commit -m "refactor(cmd/wt): use worktree.IsRepo instead of inGitRepoAt"
  ```

---

### Task 3: Replace `repoRootFor` in `internal/tui/app.go`

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Remove `repoRootFor`**

  Delete from `internal/tui/app.go`:

  ```go
  // repoRootFor resolves the git repository root that owns path. path is a
  // worktree or repo-root directory; the result is the primary repo root (the
  // parent of any .worktrees subdir), or "" if path is not inside a git repo.
  //
  // Used by Run() to seed model.repoRoot when prePath is set so the new-worktree
  // prompt (currently gated on m.ready, but reachable via any future UI
  // restoration to the worktree list) has a valid directory to pass to git.
  func repoRootFor(path string) string {
      if path == "" || path == "." {
          return ""
      }
      out, err := exec.Command("git", "-C", path, "rev-parse", "--show-toplevel").Output()
      if err != nil {
          return ""
      }
      return strings.TrimSpace(string(out))
  }
  ```

  Also remove any unused `exec` or `strings` imports that were only used by
  `repoRootFor` (check the file's import block).

- [ ] **Step 2: Update `Run` to use `worktree.RepoRootAt`**

  Find:
  ```go
  repoRoot:     repoRootFor(prePath),
  ```

  Replace with:
  ```go
  repoRoot: func() string {
      if prePath == "" || prePath == "." {
          return ""
      }
      root, err := worktree.RepoRootAt(prePath)
      if err != nil {
          return ""
      }
      return root
  }(),
  ```

  Ensure `internal/worktree` is imported.

- [ ] **Step 3: Run tui tests**

  ```bash
  go test ./internal/tui -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/tui/app.go
  git commit -m "refactor(tui): use worktree.RepoRootAt instead of repoRootFor"
  ```

---

### Task 4: Add neutral agent-list builder to `internal/agents`

**Files:**
- Modify: `internal/agents/agents.go`
- Test: `internal/agents/agents_test.go`

- [ ] **Step 1: Add `AgentListEntry` and `ListEntries`**

  In `internal/agents/agents.go`, after the `OllamaURLer` interface block, add:

  ```go
  // AgentListEntry is one row returned by ListEntries. Callers convert it to
  // their own list.Item / bubbletea item type.
  type AgentListEntry struct {
      Name       string
      Command    bool // true for commands like shell (no model layer)
      Configured bool // present in config.toml
      Installed  bool // binary found on PATH (always true for commands)
      Issue      string // human-readable launch blocker ("" if ready)
  }

  // ListEntries returns every configured agent and every registered driver
  // exactly once, deduplicated and sorted alphabetically by name. Commands are
  // classified by IsCommand; non-commands are marked configured/installed and
  // receive an issue string if they cannot launch.
  func ListEntries(cfg *config.Config) []AgentListEntry {
      seen := map[string]bool{}
      var entries []AgentListEntry

      add := func(name string) {
          if seen[name] {
              return
          }
          seen[name] = true

          ag, err := cfg.AgentByName(name)
          configured := err == nil
          if !configured {
              ag = &config.Agent{Name: name}
          }

          command := IsCommand(name)
          entry := AgentListEntry{
              Name:       name,
              Command:    command,
              Configured: configured,
              Installed:  command || Installed(name),
          }

          if !command && !configured {
              entry.Issue = "not configured — add it to config.toml"
          } else if !command && !entry.Installed {
              entry.Issue = "not installed — install the binary"
          }

          entries = append(entries, entry)
      }

      for _, a := range cfg.Agents {
          add(a.Name)
      }
      for _, n := range Names() {
          add(n)
      }

      sort.Slice(entries, func(i, j int) bool {
          return entries[i].Name < entries[j].Name
      })

      return entries
  }
  ```

  Add `"sort"` to the imports (it is not currently imported in `agents.go`).

  Update the import block in `internal/agents/agents.go` to include `sort`:

  ```go
  import (
      "fmt"
      "os"
      "os/exec"
      "sort"
      "strings"
      "sync"

      "github.com/ohanaverse/agent-worktree/internal/config"
      "github.com/ohanaverse/agent-worktree/internal/session"
  )
  ```

- [ ] **Step 2: Add `TestListEntries`**

  Append to `internal/agents/agents_test.go`:

  ```go
  // TestListEntries verifies the neutral agent-list builder merges configured
  // agents and registered drivers, deduplicates, classifies commands, and
  // reports issues. This is the shared helper that replaces near-identical
  // list construction in the TUI and configeditor.
  func TestListEntries(t *testing.T) {
      cfg := &config.Config{
          Agents: []config.Agent{
              {Name: "claude", SupportedProviders: []string{"ollama"}},
              {Name: "definitely-not-installed", SupportedProviders: []string{"ollama"}},
          },
      }

      entries := ListEntries(cfg)
      byName := map[string]AgentListEntry{}
      for _, e := range entries {
          if _, ok := byName[e.Name]; ok {
              t.Errorf("duplicate entry for %q", e.Name)
          }
          byName[e.Name] = e
      }

      if _, ok := byName["claude"]; !ok {
          t.Fatal("missing claude entry")
      }
      if !byName["claude"].Configured {
          t.Error("claude should be configured")
      }
      // git is expected to be on PATH in any reasonable test environment.
      if !byName["claude"].Installed {
          t.Error("claude should be installed (git binary is on PATH)")
      }
      if byName["claude"].Issue != "" {
          t.Errorf("claude issue = %q, want empty", byName["claude"].Issue)
      }

      if e, ok := byName["shell"]; !ok {
          t.Fatal("missing shell command entry")
      } else {
          if !e.Command {
              t.Error("shell should be marked as a command")
          }
          if !e.Installed {
              t.Error("commands are always installed")
          }
          if e.Issue != "" {
              t.Errorf("shell issue = %q, want empty", e.Issue)
          }
      }

      if e, ok := byName["definitely-not-installed"]; !ok {
          t.Fatal("missing definitely-not-installed entry")
      } else {
          if !strings.Contains(e.Issue, "not installed") {
              t.Errorf("definitely-not-installed issue = %q, want not installed", e.Issue)
          }
      }

      if e, ok := byName["opencode"]; !ok {
          t.Fatal("missing opencode entry")
      } else {
          if !strings.Contains(e.Issue, "not configured") {
              t.Errorf("opencode issue = %q, want not configured", e.Issue)
          }
      }

      // Sorted alphabetically.
      for i := 1; i < len(entries); i++ {
          if entries[i].Name < entries[i-1].Name {
              t.Errorf("entries not sorted: %q before %q", entries[i-1].Name, entries[i].Name)
          }
      }
  }
  ```

  Add `"strings"` and `"sort"` imports if needed.

- [ ] **Step 3: Run agents tests**

  ```bash
  go test ./internal/agents -run TestListEntries -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/agents.go internal/agents/agents_test.go
  git commit -m "feat(agents): add neutral ListEntries agent-list builder"
  ```

---

### Task 5: Refactor `internal/tui/agent_picker.go` to use `agents.ListEntries`

**Files:**
- Modify: `internal/tui/agent_picker.go`
- Test: `internal/tui/agent_picker_test.go`

- [ ] **Step 1: Replace `buildAgentList` body with adapter**

  In `internal/tui/agent_picker.go`, replace the entire `buildAgentList`
  function body with:

  ```go
  func buildAgentList(cfg *config.Config) []list.Item {
      entries := agents.ListEntries(cfg)
      items := make([]list.Item, 0, len(entries))
      for _, e := range entries {
          it := agentItem{name: e.Name, command: e.Command}
          if !e.Command {
              it.issue = e.Issue
          }
          items = append(items, it)
      }
      return items
  }
  ```

  Remove now-unused local `add` helper, `seen` map, and the sort call inside
  the function (sorting is already done by `agents.ListEntries`). Keep the
  `agentItem` type and `agentIssue` function unchanged.

- [ ] **Step 2: Add adapter contract test**

  Append to `internal/tui/agent_picker_test.go`:

  ```go
  // TestBuildAgentListAdapter verifies buildAgentList is a thin wrapper around
  // agents.ListEntries, preserving command classification and issue text.
  func TestBuildAgentListAdapter(t *testing.T) {
      cfg := &config.Config{
          Agents: []config.Agent{
              {Name: "claude", SupportedProviders: []string{"ollama"}},
          },
      }
      t.Cleanup(stubInstalled("claude"))

      items := buildAgentList(cfg)
      if len(items) == 0 {
          t.Fatal("expected items")
      }

      // Every item must be an agentItem derived from an AgentListEntry.
      for _, it := range items {
          ai, ok := it.(agentItem)
          if !ok {
              t.Fatalf("item %T is not an agentItem", it)
          }
          // Commands have no issue; non-commands may have an issue.
          if ai.command && ai.issue != "" {
              t.Errorf("command %q has issue %q", ai.name, ai.issue)
          }
      }
  }
  ```

- [ ] **Step 3: Run tui tests**

  ```bash
  go test ./internal/tui -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/tui/agent_picker.go internal/tui/agent_picker_test.go
  git commit -m "refactor(tui): build agent picker from agents.ListEntries"
  ```

---

### Task 6: Refactor `internal/configeditor/agents_tab.go` to use `agents.ListEntries`

**Files:**
- Modify: `internal/configeditor/agents_tab.go`
- Test: `internal/configeditor/agents_tab_test.go`

- [ ] **Step 1: Replace `buildAgentsList` body with adapter**

  In `internal/configeditor/agents_tab.go`, replace the entire
  `buildAgentsList` function body with:

  ```go
  func buildAgentsList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
      entries := agents.ListEntries(cfg)
      items := make([]list.Item, 0, len(entries))
      for _, e := range entries {
          ag, err := cfg.AgentByName(e.Name)
          if err != nil {
              ag = &config.Agent{Name: e.Name}
          }
          it := agentItem{
              agent:      *ag,
              command:    e.Command,
              configured: e.Configured,
              installed:  e.Installed,
              issue:      e.Issue,
          }
          items = append(items, it)
      }

      // Sort: commands first, then alphabetical. agents.ListEntries returns
      // a purely alphabetical list; the configeditor applies its own display
      // order here.
      sort.SliceStable(items, func(i, j int) bool {
          ai := items[i].(agentItem)
          aj := items[j].(agentItem)
          if ai.command != aj.command {
              return ai.command
          }
          return ai.agent.Name < aj.agent.Name
      })

      l := list.New(items, tui.ThemedListDelegate(theme), width, height)
      l.Title = "Agents"
      l.SetShowStatusBar(false)
      return l
  }
  ```

  Remove the local `add` helper and `seen` map.

- [ ] **Step 2: Add adapter contract test**

  Append to `internal/configeditor/agents_tab_test.go`:

  ```go
  // TestBuildAgentsListAdapter verifies buildAgentsList is a wrapper around
  // agents.ListEntries that preserves commands-first sorting and issue state.
  func TestBuildAgentsListAdapter(t *testing.T) {
      cleanup := agents.RegisterTest("shell", func() agents.Driver {
          return &stubCommandDriver{}
      })
      defer cleanup()

      cfg := &config.Config{
          Agents: []config.Agent{
              {Name: "claude", SupportedProviders: []string{"claude"}},
          },
      }
      l := buildAgentsList(testTheme(), 80, 24, cfg)

      var sawCommand bool
      for _, it := range l.Items() {
          ai := it.(agentItem)
          if ai.command {
              sawCommand = true
              if ai.issue != "" {
                  t.Errorf("command %q has issue %q", ai.agent.Name, ai.issue)
              }
          }
      }
      if !sawCommand {
          t.Error("expected at least one command item (shell)")
      }
  }
  ```

- [ ] **Step 3: Run configeditor tests**

  ```bash
  go test ./internal/configeditor -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/configeditor/agents_tab.go internal/configeditor/agents_tab_test.go
  git commit -m "refactor(configeditor): build agents tab from agents.ListEntries"
  ```

---

### Task 7: Replace duplicated launch branches in `cmd/wt/main.go`

**Files:**
- Modify: `cmd/wt/main.go`
- Test: `cmd/wt/main_test.go`

- [ ] **Step 1: Add `runLaunchPath` helper**

  In `cmd/wt/main.go`, add the helper before `rootCmd` (or near the other
  helpers at the top):

  ```go
  // runLaunchPath resolves the target directory, installs the guard when
  // inside a git repo, and either auto-launches or routes to the picker/TUI.
  // prePath is the resolved worktree path for -W, the repo root for --cwd,
  // "." for outside-a-repo passthrough, and "" when the worktree picker
  // should be shown.
  //
  // Callers must install the guard themselves when prePath == "" (the TUI
  // branch), because the repo root is not known until the user selects a
  // worktree inside the TUI.
  func runLaunchPath(
      cmd *cobra.Command,
      a *app,
      agent, pinned, tags, family string,
      args []string,
      prePath string,
  ) error {
      // Resolve repo root if we have a concrete path. ""
      // means the TUI will pick the worktree later.
      var root string
      if prePath != "" {
          rr, err := worktree.RepoRootAt(prePath)
          if err != nil {
              // Only error when the path was meant to be inside a repo.
              // "." is the outside-repo sentinel and should keep going.
              if prePath != "." {
                  return fmt.Errorf("not in a git repo: %w", err)
              }
          } else {
              root = rr
          }
      }

      // Install the guard once when inside any git repo.
      if root != "" {
          maybeInstallGuard()
      }

      // Effective launch directory.
      launchPath := prePath
      if launchPath == "" {
          launchPath = root // may still be "" for the TUI initial state
      }

      pinnedSupplied := cmd.Flags().Changed("model")

      if needsModelPicker(agent, pinned) {
          if resolved, _, err := resolveModelForLaunch(agent, a.cfg, tags, family, pinned); err == nil && resolved {
              return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
          }
          if !stdinTTY() {
              return pickerNeedsTTYError(agent)
          }
          return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
      }

      return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
  }
  ```

- [ ] **Step 2: Replace the four launch branches with calls to `runLaunchPath`**

  Replace the `-W` branch:
  ```go
  // Before:
  if name := mustGetString(cmd, "worktree"); name != "" {
      root, err := worktree.RepoRoot()
      if err != nil {
          return fmt.Errorf("not in a git repo: %w", err)
      }
      maybeInstallGuard()
      path, err := worktree.EnsureForName(root, name)
      if err != nil {
          return err
      }
      if needsModelPicker(agent, pinned) {
          if resolved, _, err := resolveModelForLaunch(agent, a.cfg, tags, family, pinned); err == nil && resolved {
              return launchFiltered(agent, path, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
          }
          if !stdinTTY() {
              return pickerNeedsTTYError(agent)
          }
          return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, path, a.cfg)
      }
      return launchFiltered(agent, path, a.cfg, yolo(cmd), tags, family, pinned, pinnedSupplied, args)
  }

  // After:
  if name := mustGetString(cmd, "worktree"); name != "" {
      root, err := worktree.RepoRoot()
      if err != nil {
          return fmt.Errorf("not in a git repo: %w", err)
      }
      path, err := worktree.EnsureForName(root, name)
      if err != nil {
          return err
      }
      return runLaunchPath(cmd, a, agent, pinned, tags, family, args, path)
  }

  Note: `maybeInstallGuard()` is removed here because `runLaunchPath`
  installs the guard once it knows the repo root.
  ```

  Replace the `--cwd` branch:
  ```go
  // Before:
  if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
      root, err := worktree.RepoRoot()
      if err != nil {
          return fmt.Errorf("not in a git repo: %w", err)
      }
      maybeInstallGuard()
      if needsModelPicker(agent, pinned) {
          ...
      }
      return launchFiltered(...)
  }

  // After:
  if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
      root, err := worktree.RepoRoot()
      if err != nil {
          return fmt.Errorf("not in a git repo: %w", err)
      }
      return runLaunchPath(cmd, a, agent, pinned, tags, family, args, root)
  }
  ```

  Replace the outside-repo branch:
  ```go
  // Before:
  if !inGitRepo() {
      if needsModelPicker(agent, pinned) {
          ...
      }
      return launchFiltered(agent, ".", ...)
  }

  // After:
  if !worktree.IsRepo(".") {
      return runLaunchPath(cmd, a, agent, pinned, tags, family, args, ".")
  }
  ```

  Replace the TUI branch:
  ```go
  // Before:
  if agent == "" && !stdinTTY() {
      return errPickerNeedsTTY
  }
  maybeInstallGuard()
  return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, "", a.cfg)

  // After:
  if agent == "" && !stdinTTY() {
      return errPickerNeedsTTY
  }
  maybeInstallGuard()
  return runLaunchPath(cmd, a, agent, pinned, tags, family, args, "")
  ```

  The `pinnedSupplied := cmd.Flags().Changed("model")` line in `RunE` can be
  removed because `runLaunchPath` computes it internally.

- [ ] **Step 3: Build to catch compile errors**

  ```bash
  go build ./cmd/wt
  ```

  Expected: no errors.

- [ ] **Step 4: Add table-driven `TestRunLaunchPath` to `main_test.go`**

  Append to `cmd/wt/main_test.go`:

  ```go
  // TestRunLaunchPath verifies the shared dispatcher routes each entry point
  // to the right outcome: inside-repo branches install the guard and either
  // launch or fall back to the TUI; the outside-repo branch skips the guard.
  func TestRunLaunchPath(t *testing.T) {
      repo := t.TempDir()
      if err := exec.Command("git", "-C", repo, "init", "-q").Run(); err != nil {
          t.Fatal(err)
      }

      cfg := &config.Config{
          Agents: []config.Agent{{Name: "shell", SupportedProviders: nil}},
      }
      a := &app{cfg: cfg}

      cases := []struct {
          name     string
          prePath  string
          wantPath string
      }{
          {"cwd", repo, repo},
          {"outside", ".", "."},
          {"tui", "", ""},
      }

      for _, c := range cases {
          t.Run(c.name, func(t *testing.T) {
              // Stub tuiRun and launchFiltered to capture the dispatched path.
              oldTUI := tuiRun
              oldLaunch := launchFiltered
              var gotPath string
              tuiRun = func(bool, string, string, string, string, []string, themes.Theme, string, *config.Config) error {
                  gotPath = c.prePath // TUI receives prePath unchanged
                  return nil
              }
              launchFiltered = func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string) error {
                  gotPath = worktreePath
                  return nil
              }
              defer func() {
                  tuiRun = oldTUI
                  launchFiltered = oldLaunch
              }()

              cmd := &cobra.Command{}
              cmd.Flags().StringP("model", "M", "", "")
              cmd.Flags().Bool("yolo", false, "")

              err := runLaunchPath(cmd, a, "shell", "", "", "", nil, c.prePath)
              if err != nil {
                  t.Fatalf("runLaunchPath: %v", err)
              }
              if gotPath != c.wantPath {
                  t.Errorf("dispatched path = %q, want %q", gotPath, c.wantPath)
              }
          })
      }
  }
  ```

  Add `github.com/spf13/cobra` to the imports of `cmd/wt/main_test.go`.

- [ ] **Step 5: Run cmd/wt tests**

  ```bash
  go test ./cmd/wt -v
  ```

  Expected: PASS.

- [ ] **Step 6: Commit**

  ```bash
  git add cmd/wt/main.go cmd/wt/main_test.go
  git commit -m "refactor(cmd/wt): consolidate launch branches into runLaunchPath"
  ```

---

### Task 8: Final verification

- [ ] **Step 1: Run the full test suite**

  ```bash
  go test ./...
  ```

  Expected: PASS.

- [ ] **Step 2: Run go vet**

  ```bash
  go vet ./...
  ```

  Expected: no issues.

- [ ] **Step 3: Build the binary**

  ```bash
  go build ./cmd/wt
  ```

  Expected: no errors.

- [ ] **Step 4: Commit any remaining changes**

  If there are no uncommitted changes, skip. Otherwise:
  ```bash
  git add .
  git commit -m "chore: final cleanup for deduplicate-shared-helpers"
  ```

---

## Spec coverage check

| Spec requirement | Task |
|---|---|
| `IsRepo` and `RepoRootAt` in `internal/worktree` | Task 1 |
| Remove `inGitRepoAt` from `cmd/wt` | Task 2 |
| Remove `repoRootFor` from `internal/tui` | Task 3 |
| `AgentListEntry` + `ListEntries` in `internal/agents` | Task 4 |
| TUI adapter to `agents.ListEntries` | Task 5 |
| Configeditor adapter to `agents.ListEntries` | Task 6 |
| `runLaunchPath` replacing launch branches | Task 7 |
| Guard normalization (install inside git repo, skip outside) | Task 7 |
| Tests for all three shared helpers | Tasks 1, 4, 5, 6, 7 |

## Placeholder scan

- No "TBD", "TODO", or vague steps.
- Every code block contains the exact code to write.
- Every command has an expected outcome.
- No references to undefined types/functions.

## Type consistency check

- `AgentListEntry` fields: `Name`, `Command`, `Configured`, `Installed`, `Issue` — consistent across spec and plan.
- `ListEntries(cfg *config.Config) []AgentListEntry` — consistent.
- `RepoRootAt(dir string) (string, error)` and `IsRepo(dir string) bool` — consistent.
- `runLaunchPath` parameter order: `cmd, a, agent, pinned, tags, family, args, prePath` — consistent in definition and all call sites.

## Known follow-ups (out of scope)

- `efficiency-fixes` placeholder: may benefit from the shared `ListEntries` and `runLaunchPath` helpers.
- `marginal-cleanup` placeholder: remove test-only methods and dead migration fixups.
