# Resume as a Driver Capability — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move session resume handling (flag name + session lookup) out of shared code and into an optional `Driver` capability, so only the agent's driver package needs to change when adding resume support.

**Architecture:** Add a `Resumer` optional interface in `internal/agents`. Implement it in `claudeDriver` and `opencodeDriver`. Remove the agent-name `switch` from `BuildLaunchCmd` and from `session.LatestForAgent`. Update both TUI and non-TUI launch paths to consult the capability. Keep the `!m.Native` guard at both layers.

**Tech Stack:** Go, standard library, existing `internal/session` helpers.

---

## File map

| File | Responsibility |
|---|---|
| `internal/agents/agents.go` | Defines `Resumer`; `BuildLaunchCmd` appends resume flag via capability. |
| `internal/agents/claude.go` | Implements `Resumer` for claude (`--resume` + `~/.claude/projects` lookup). |
| `internal/agents/opencode.go` | Implements `Resumer` for opencode (`--session` + `~/.local/share/opencode/storage/session` lookup). |
| `internal/session/session.go` | Keeps `Session`, `Slug`, `RelativeTime`, `OpenCodeProjectID`; exports `LatestByExt`; removes per-agent dispatch. |
| `internal/tui/app.go` | `proceedToLaunch` checks `Resumer` capability instead of calling `session.LatestForAgent`. |
| `cmd/wt/launch.go` | `buildCommandForModel` checks `Resumer` capability instead of `session.LatestForAgent`. |
| `cmd/wt/main.go` | `--debug-session` checks `Resumer` capability. |
| `internal/session/session_test.go` | Removes `LatestForAgent`/`LatestClaude`/`LatestOpenCode` tests; keeps helper tests. |
| `internal/agents/agents_test.go` | Adds `Resumer` capability contract test; updates existing resume tests if needed. |
| `internal/agents/claude_test.go` | New file: tests `claudeDriver.LatestSession`. |
| `internal/agents/opencode_test.go` | New file: tests `opencodeDriver.LatestSession`. |

---

### Task 1: Refactor `internal/session/session.go`

**Files:**
- Modify: `internal/session/session.go`

- [ ] **Step 1: Export `latestByExt` as `LatestByExt`**

  Rename the function and its doc comment so drivers outside the package can use it.

  ```go
  // LatestByExt finds the newest file with the given extension in dir.
  func LatestByExt(dir, ext string, idOf func(os.FileInfo) string) (*Session, error) {
      entries, err := os.ReadDir(dir)
      if os.IsNotExist(err) {
          return nil, nil // no sessions
      }
      if err != nil {
          return nil, err
      }

      var sessions []Session
      for _, e := range entries {
          if e.IsDir() || !strings.HasSuffix(e.Name(), ext) {
              continue
          }
          info, err := e.Info()
          if err != nil {
              continue
          }
          sessions = append(sessions, Session{
              ID:    idOf(info),
              MTime: info.ModTime(),
          })
      }
      if len(sessions) == 0 {
          return nil, nil
      }
      sort.Slice(sessions, func(i, j int) bool {
          return sessions[i].MTime.After(sessions[j].MTime)
      })
      return &sessions[0], nil
  }
  ```

- [ ] **Step 2: Remove per-agent dispatch functions**

  Delete `LatestClaude`, `LatestOpenCode`, `LatestForAgent`. Keep `OpenCodeProjectID` because opencode's driver still needs it.

  Remove from `internal/session/session.go`:
  - `func LatestClaude(...)`
  - `func LatestOpenCode(...)`
  - `func LatestForAgent(...)`

- [ ] **Step 3: Build and run session package tests**

  Run:
  ```bash
  go test ./internal/session -v
  ```

  Expected: FAIL because `session_test.go` still references removed functions.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/session/session.go
  git commit -m "refactor(session): export LatestByExt, remove per-agent dispatch"
  ```

---

### Task 2: Update session tests

**Files:**
- Modify: `internal/session/session_test.go`

- [ ] **Step 1: Remove `TestLatestClaude`, `TestLatestOpenCode`, and references to removed functions**

  Delete these test functions entirely. They will move into driver-specific test files.

- [ ] **Step 2: Update `TestLatestByExtNoDir` and `TestLatestByExtRanking` to call `LatestByExt`**

  In `TestLatestByExtNoDir`:
  ```go
  s, err := LatestByExt(filepath.Join(t.TempDir(), "nope"), ".jsonl", func(os.FileInfo) string { return "" })
  ```

  In `TestLatestByExtRanking`:
  ```go
  s, err := LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
      return f.Name()
  })
  ```

- [ ] **Step 3: Run session package tests**

  ```bash
  go test ./internal/session -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/session/session_test.go
  git commit -m "test(session): remove per-agent tests, use exported LatestByExt"
  ```

---

### Task 3: Add the `Resumer` interface

**Files:**
- Modify: `internal/agents/agents.go`

- [ ] **Step 1: Add the `Resumer` interface below `Commanded`**

  ```go
  // Resumer is an optional Driver capability for agents that support
  // resuming a previous session. Drivers that do not implement Resumer are
  // assumed to have no session-resume support.
  type Resumer interface {
      // ResumeFlag is the CLI flag the agent expects before the session ID.
      // Examples: "--resume" for claude, "--session" for opencode.
      ResumeFlag() string

      // LatestSession returns the most recently modified resumable session
      // for the worktree at path, or nil if none exists. Errors indicate
      // a lookup failure (e.g. unreadable session directory); callers decide
      // whether to surface or swallow them.
      LatestSession(path string) (*session.Session, error)
  }
  ```

- [ ] **Step 2: Build to verify compilation**

  Run:
  ```bash
  go build ./internal/agents
  ```

  Expected: no errors (the interface is declared but not yet used).

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/agents.go
  git commit -m "feat(agents): add Resumer optional Driver capability"
  ```

---

### Task 4: Implement `Resumer` in `claudeDriver`

**Files:**
- Modify: `internal/agents/claude.go`

- [ ] **Step 1: Add required imports**

  Change the imports block to:
  ```go
  import (
      "os"
      "path/filepath"
      "strings"

      "github.com/ohanaverse/agent-worktree/internal/config"
      "github.com/ohanaverse/agent-worktree/internal/session"
  )
  ```

- [ ] **Step 2: Add `Resumer` methods to `claudeDriver`**

  After `YoloFlag()`, add:
  ```go
  func (claudeDriver) ResumeFlag() string { return "--resume" }

  func (claudeDriver) LatestSession(path string) (*session.Session, error) {
      dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", session.Slug(path))
      return session.LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
          return strings.TrimSuffix(f.Name(), ".jsonl")
      })
  }
  ```

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/claude.go
  git commit -m "feat(agents/claude): implement Resumer capability"
  ```

---

### Task 5: Implement `Resumer` in `opencodeDriver`

**Files:**
- Modify: `internal/agents/opencode.go`

- [ ] **Step 1: Add required imports**

  Change the imports block to:
  ```go
  import (
      "fmt"
      "os"
      "path/filepath"

      "github.com/ohanaverse/agent-worktree/internal/config"
      "github.com/ohanaverse/agent-worktree/internal/session"
  )
  ```

- [ ] **Step 2: Add `Resumer` methods to `opencodeDriver`**

  After `YoloFlag()`, add:
  ```go
  func (opencodeDriver) ResumeFlag() string { return "--session" }

  func (opencodeDriver) LatestSession(path string) (*session.Session, error) {
      projectID, err := session.OpenCodeProjectID(path)
      if err != nil {
          return nil, err
      }
      dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode",
          "storage", "session", projectID)
      return session.LatestByExt(dir, ".json", func(f os.FileInfo) string {
          return f.Name()
      })
  }
  ```

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/opencode.go
  git commit -m "feat(agents/opencode): implement Resumer capability"
  ```

---

### Task 6: Update `BuildLaunchCmd` to use `Resumer`

**Files:**
- Modify: `internal/agents/agents.go`

- [ ] **Step 1: Replace the agent-name switch in `BuildLaunchCmd`**

  Find this block:
  ```go
  if sess != nil && !m.Native {
      switch agent {
      case "claude":
          cmd.Args = append(cmd.Args, "--resume", sess.ID)
      case "opencode":
          cmd.Args = append(cmd.Args, "--session", sess.ID)
      }
  }
  ```

  Replace with:
  ```go
  if sess != nil && !m.Native {
      if r, ok := d.(Resumer); ok {
          cmd.Args = append(cmd.Args, r.ResumeFlag(), sess.ID)
      }
  }
  ```

- [ ] **Step 2: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/agents.go
  git commit -m "feat(agents): remove agent-name switch from resume handling in BuildLaunchCmd"
  ```

---

### Task 7: Update the TUI launch path

**Files:**
- Modify: `internal/tui/app.go`

- [ ] **Step 1: Replace `session.LatestForAgent` call in `proceedToLaunch`**

  Find this block (around line 690):
  ```go
  var sess *session.Session
  if !highlighted.model.Native {
      var err error
      sess, err = session.LatestForAgent(m.agent, m.selectedPath)
      if err != nil {
          m.status = "session check failed: " + err.Error()
          return m, nil
      }
  }
  ```

  Replace with:
  ```go
  var sess *session.Session
  if !highlighted.model.Native {
      if r, ok := agents.ByName(m.agent).(agents.Resumer); ok {
          var err error
          sess, err = r.LatestSession(m.selectedPath)
          if err != nil {
              m.status = "session check failed: " + err.Error()
              return m, nil
          }
      }
  }
  ```

  Note: `agents` is already imported in this file.

- [ ] **Step 2: Run TUI tests**

  ```bash
  go test ./internal/tui -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/tui/app.go
  git commit -m "feat(tui): use Resumer capability for session lookup"
  ```

---

### Task 8: Update the non-TUI launch path

**Files:**
- Modify: `cmd/wt/launch.go`

- [ ] **Step 1: Replace `session.LatestForAgent` call in `buildCommandForModel`**

  Find this block:
  ```go
  var sess *session.Session
  if !m.Native {
      sess, _ = session.LatestForAgent(agent, worktreePath)
  }
  ```

  Replace with:
  ```go
  var sess *session.Session
  if !m.Native {
      if r, ok := agents.ByName(agent).(agents.Resumer); ok {
          sess, _ = r.LatestSession(worktreePath)
      }
  }
  ```

- [ ] **Step 2: Run cmd/wt tests**

  ```bash
  go test ./cmd/wt -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add cmd/wt/launch.go
  git commit -m "feat(cmd/wt): use Resumer capability in non-TUI launch"
  ```

---

### Task 9: Update `--debug-session`

**Files:**
- Modify: `cmd/wt/main.go`

- [ ] **Step 1: Replace the `session.LatestForAgent` call in the `--debug-session` branch**

  Find this block (around line 170):
  ```go
  s, err := session.LatestForAgent(agent, root)
  if err != nil {
      return err
  }
  if s == nil {
      fmt.Println("(no sessions)")
      return nil
  }
  fmt.Printf("resume %s (last %s)\n", s.ID, session.RelativeTime(s.MTime))
  ```

  Replace with:
  ```go
  d := agents.ByName(agent)
  if d == nil {
      return fmt.Errorf("unknown agent: %s", agent)
  }
  r, ok := d.(agents.Resumer)
  if !ok {
      fmt.Printf("%s: no resume support\n", agent)
      return nil
  }
  s, err := r.LatestSession(root)
  if err != nil {
      return err
  }
  if s == nil {
      fmt.Println("(no sessions)")
      return nil
  }
  fmt.Printf("resume %s (last %s)\n", s.ID, session.RelativeTime(s.MTime))
  ```

  Note: `agents` is already imported in this file.

- [ ] **Step 2: Build to verify compilation**

  ```bash
  go build ./cmd/wt
  ```

  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  git add cmd/wt/main.go
  git commit -m "feat(cmd/wt): use Resumer capability in debug-session helper"
  ```

---

### Task 10: Add `Resumer` capability contract test

**Files:**
- Modify: `internal/agents/agents_test.go`

- [ ] **Step 1: Add a capability contract test**

  Append this test near the other `BuildLaunchCmd` tests:
  ```go
  // TestResumerCapability asserts which drivers implement the Resumer
  // optional capability. claude and opencode support session resume; the
  // other agents do not. This is the contract that lets shared code stop
  // switching on the agent name.
  func TestResumerCapability(t *testing.T) {
      cases := []struct {
          agent    string
          resumable bool
      }{
          {"claude", true},
          {"opencode", true},
          {"codex", false},
          {"copilot", false},
          {"pi", false},
          {"agy", false},
          {"shell", false},
      }
      for _, c := range cases {
          d := ByName(c.agent)
          if d == nil {
              t.Fatalf("unknown agent: %s", c.agent)
          }
          _, got := d.(Resumer)
          if got != c.resumable {
              t.Errorf("agent %q Resumer = %v, want %v", c.agent, got, c.resumable)
          }
      }
  }
  ```

- [ ] **Step 2: Run the new test**

  ```bash
  go test ./internal/agents -run TestResumerCapability -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/agents_test.go
  git commit -m "test(agents): add Resumer capability contract test"
  ```

---

### Task 11: Add `claudeDriver.LatestSession` test

**Files:**
- Create: `internal/agents/claude_test.go`

- [ ] **Step 1: Create the test file**

  ```go
  package agents

  import (
      "os"
      "path/filepath"
      "testing"
      "time"

      "github.com/ohanaverse/agent-worktree/internal/session"
  )

  // TestClaudeLatestSession asserts claudeDriver finds the newest .jsonl
  // session file under ~/.claude/projects/<slug> and strips the extension.
  func TestClaudeLatestSession(t *testing.T) {
      home := t.TempDir()
      t.Setenv("HOME", home)

      worktree := "/some/worktree/path"
      dir := filepath.Join(home, ".claude", "projects", session.Slug(worktree))
      if err := os.MkdirAll(dir, 0o755); err != nil {
          t.Fatal(err)
      }
      old := filepath.Join(dir, "old.jsonl")
      new := filepath.Join(dir, "new.jsonl")
      if err := os.WriteFile(old, []byte("a"), 0o644); err != nil {
          t.Fatal(err)
      }
      if err := os.WriteFile(new, []byte("b"), 0o644); err != nil {
          t.Fatal(err)
      }
      oldInfo, _ := os.Stat(old)
      os.Chtimes(new, oldInfo.ModTime(), oldInfo.ModTime().Add(time.Second))

      d := claudeDriver{}
      s, err := d.LatestSession(worktree)
      if err != nil {
          t.Fatal(err)
      }
      if s == nil || s.ID != "new" {
          t.Fatalf("expected newest claude session id \"new\", got %+v", s)
      }
  }

  // TestClaudeLatestSessionNoDir asserts claudeDriver returns nil when no
  // session directory exists, without returning an error.
  func TestClaudeLatestSessionNoDir(t *testing.T) {
      home := t.TempDir()
      t.Setenv("HOME", home)

      d := claudeDriver{}
      s, err := d.LatestSession("/nonexistent/worktree")
      if err != nil {
          t.Fatal(err)
      }
      if s != nil {
          t.Fatalf("expected nil session, got %+v", s)
      }
  }
  ```

- [ ] **Step 2: Run the new tests**

  ```bash
  go test ./internal/agents -run TestClaudeLatestSession -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/claude_test.go
  git commit -m "test(agents/claude): add LatestSession tests"
  ```

---

### Task 12: Add `opencodeDriver.LatestSession` test

**Files:**
- Create: `internal/agents/opencode_test.go`

- [ ] **Step 1: Create the test file**

  ```go
  package agents

  import (
      "os"
      "os/exec"
      "path/filepath"
      "testing"
      "time"

      "github.com/ohanaverse/agent-worktree/internal/session"
  )

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

  // TestOpenCodeLatestSession asserts opencodeDriver finds the newest .json
  // session file under the project-id directory.
  func TestOpenCodeLatestSession(t *testing.T) {
      home := t.TempDir()
      t.Setenv("HOME", home)

      repo := t.TempDir()
      gitInit(t, repo)
      id, err := session.OpenCodeProjectID(repo)
      if err != nil {
          t.Fatal(err)
      }

      dir := filepath.Join(home, ".local", "share", "opencode", "storage", "session", id)
      if err := os.MkdirAll(dir, 0o755); err != nil {
          t.Fatal(err)
      }
      old := filepath.Join(dir, "old.json")
      new := filepath.Join(dir, "new.json")
      if err := os.WriteFile(old, []byte("a"), 0o644); err != nil {
          t.Fatal(err)
      }
      if err := os.WriteFile(new, []byte("b"), 0o644); err != nil {
          t.Fatal(err)
      }
      oldInfo, _ := os.Stat(old)
      os.Chtimes(new, oldInfo.ModTime(), oldInfo.ModTime().Add(time.Second))

      d := opencodeDriver{}
      s, err := d.LatestSession(repo)
      if err != nil {
          t.Fatal(err)
      }
      if s == nil || s.ID != "new.json" {
          t.Fatalf("expected newest opencode session id \"new.json\", got %+v", s)
      }
  }

  // TestOpenCodeLatestSessionNoDir asserts opencodeDriver returns nil when no
  // session directory exists, without returning an error.
  func TestOpenCodeLatestSessionNoDir(t *testing.T) {
      home := t.TempDir()
      t.Setenv("HOME", home)

      repo := t.TempDir()
      gitInit(t, repo)

      d := opencodeDriver{}
      s, err := d.LatestSession(repo)
      if err != nil {
          t.Fatal(err)
      }
      if s != nil {
          t.Fatalf("expected nil session, got %+v", s)
      }
  }
  ```

- [ ] **Step 2: Run the new tests**

  ```bash
  go test ./internal/agents -run TestOpenCodeLatestSession -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/opencode_test.go
  git commit -m "test(agents/opencode): add LatestSession tests"
  ```

---

### Task 13: Final verification

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
  git commit -m "chore: final cleanup for resume-driver-capability"
  ```

| Spec requirement | Task |
|---|---|
| Add `Resumer` optional capability | Task 3 |
| Implement `Resumer` in claude/opencode drivers | Tasks 4, 5 |
| Remove agent-name switch from `BuildLaunchCmd` | Task 6 |
| Update TUI path to use capability | Task 7 |
| Update non-TUI path to use capability | Task 8 |
| Update `--debug-session` | Task 9 |
| Refactor `internal/session` to remove per-agent dispatch | Tasks 1, 2 |
| Keep native-model skip | Tasks 6, 7, 8 (guard preserved) |
| Capability contract test | Task 10 |
| Driver-specific session tests | Tasks 11, 12 |

## Placeholder scan

- No "TBD", "TODO", or vague steps.
- Every code block contains the exact code to write.
- Every command has an expected outcome.
- No references to undefined types/functions.

## Type consistency check

- `Resumer` interface name is consistent across spec and plan.
- `LatestByExt` is exported in Task 1 and used in Tasks 4 and 5.
- `ResumeFlag()` and `LatestSession(path string) (*session.Session, error)` signatures match everywhere.
- `LatestByExt` is exported in Task 1 and used in Tasks 4 and 5.
- `ResumeFlag()` and `LatestSession(path string) (*session.Session, error)` signatures match everywhere.
