# Consolidate Agent-Specific Knowledge into the Driver — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move instruction-file pointer mapping and Ollama gateway URL resolution from shared packages into optional `Driver` capabilities, so only the agent's driver package needs to change when adding agent-specific seeding or routing knowledge.

**Architecture:** Add `Seeder` and `OllamaURLer` optional interfaces in `internal/agents`. Implement `Seeder` in `claudeDriver` and `copilotDriver`; implement `OllamaURLer` in `claudeDriver`, `copilotDriver`, `codexDriver`, and `opencodeDriver`. Update `internal/initseed` to use `Seeder` instead of `pointerFor`, and update each driver's `Build` to use its own `OllamaURL()`.

**Tech Stack:** Go, standard library, existing `internal/agents` optional-capability pattern.

---

## File map

| File | Responsibility |
|---|---|
| `internal/agents/agents.go` | Defines `Seeder`, `OllamaURLer`, and `InstructionPointer`. |
| `internal/agents/claude.go` | Implements `Seeder` + `OllamaURLer`; uses `OllamaURL()` in `Build`. |
| `internal/agents/copilot.go` | Implements `Seeder` + `OllamaURLer`; uses `OllamaURL()` in `Build`. |
| `internal/agents/codex.go` | Implements `OllamaURLer`; uses `OllamaURL()` in `Build`. |
| `internal/agents/opencode.go` | Implements `OllamaURLer`; uses `OllamaURL()` in `Build`. |
| `internal/initseed/initseed.go` | Removes `pointerFor`; calls `Seeder` capability. |
| `internal/config/config.go` | Keeps `OllamaBaseURL` with a migration-only comment. |
| `internal/agents/agents_test.go` | Adds capability contract tests; updates codex build test. |
| `internal/agents/claude_test.go` | Adds `TestClaudeSeeder` + `TestClaudeOllamaURL`. |
| `internal/agents/copilot_test.go` | New file: tests `copilotDriver.Seeder` + `OllamaURLer`. |
| `internal/agents/codex_test.go` | New file: tests `codexDriver.OllamaURLer`. |
| `internal/agents/opencode_test.go` | Adds `TestOpenCodeOllamaURL`. |
| `internal/initseed/initseed_test.go` | Updates tests to exercise `Seeder` capability (same expectations). |

---

### Task 1: Add `Seeder` and `OllamaURLer` capabilities to `internal/agents`

**Files:**
- Modify: `internal/agents/agents.go`

- [ ] **Step 1: Add the `InstructionPointer` struct and `Seeder` interface**

  Insert after the `Resumer` interface block:

  ```go
  // InstructionPointer describes a single file created by `wt --init`.
  type InstructionPointer struct {
      Path    string // relative to the repo root, e.g. "CLAUDE.md"
      Content string // file body, e.g. "@AGENTS.md\n"
  }

  // Seeder is an optional Driver capability for agents that need a
  // project-level instruction pointer file created by `wt --init`.
  type Seeder interface {
      // InstructionPointers returns the pointer files to create. Each pointer
      // is written only if it does not already exist.
      InstructionPointers() []InstructionPointer
  }
  ```

- [ ] **Step 2: Add the `OllamaURLer` interface**

  Insert after the `Seeder` interface block:

  ```go
  // OllamaURLer is an optional Driver capability for agents that route
  // non-native models through a local Ollama-compatible gateway.
  type OllamaURLer interface {
      // OllamaURL returns the full gateway URL the agent expects. Drivers are
      // free to include path suffixes such as "/v1" or "/v1/" because each
      // agent's wire protocol is different.
      OllamaURL() string
  }
  ```

- [ ] **Step 3: Build to verify compilation**

  Run:
  ```bash
  go build ./internal/agents
  ```

  Expected: no errors (interfaces are declared but not yet used).

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/agents.go
  git commit -m "feat(agents): add Seeder and OllamaURLer optional capabilities"
  ```

---

### Task 2: Implement `Seeder` and `OllamaURLer` in `claudeDriver`

**Files:**
- Modify: `internal/agents/claude.go`

- [ ] **Step 1: Add the two capability methods**

  After `YoloFlag()`, add:

  ```go
  func (claudeDriver) InstructionPointers() []InstructionPointer {
      return []InstructionPointer{
          {Path: "CLAUDE.md", Content: "@AGENTS.md\n"},
      }
  }

  func (claudeDriver) OllamaURL() string { return "http://localhost:11434" }
  ```

- [ ] **Step 2: Use `OllamaURL()` in `Build`**

  Find:
  ```go
  "ANTHROPIC_BASE_URL="+config.OllamaBaseURL,
  ```

  Replace with:
  ```go
  "ANTHROPIC_BASE_URL="+claudeDriver{}.OllamaURL(),
  ```

  Also remove the now-unused `"github.com/ohanaverse/agent-worktree/internal/config"` import? **No** — `Build` still uses `config.Model`, so keep it.

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/claude.go
  git commit -m "feat(agents/claude): implement Seeder and OllamaURLer"
  ```

---

### Task 3: Implement `Seeder` and `OllamaURLer` in `copilotDriver`

**Files:**
- Modify: `internal/agents/copilot.go`

- [ ] **Step 1: Add the two capability methods**

  After `YoloFlag()`, add:

  ```go
  func (copilotDriver) InstructionPointers() []InstructionPointer {
      return []InstructionPointer{
          {Path: ".github/copilot-instructions.md", Content: "Read AGENTS.md and follow all instructions in it.\n"},
      }
  }

  func (copilotDriver) OllamaURL() string { return "http://localhost:11434/v1" }
  ```

- [ ] **Step 2: Use `OllamaURL()` in `Build`**

  Find:
  ```go
  "COPILOT_PROVIDER_BASE_URL="+config.OllamaBaseURL+"/v1",
  ```

  Replace with:
  ```go
  "COPILOT_PROVIDER_BASE_URL="+copilotDriver{}.OllamaURL(),
  ```

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/copilot.go
  git commit -m "feat(agents/copilot): implement Seeder and OllamaURLer"
  ```

---

### Task 4: Implement `OllamaURLer` in `codexDriver`

**Files:**
- Modify: `internal/agents/codex.go`

- [ ] **Step 1: Add the capability method**

  Replace the package-level constant block:
  ```go
  const ollamaProvider = "agent-wt"
  const ollamaProviderURL = config.OllamaBaseURL + "/v1/"
  ```

  With a method and a private const:

  ```go
  const ollamaProvider = "agent-wt"

  func (codexDriver) OllamaURL() string { return "http://localhost:11434/v1/" }
  ```

- [ ] **Step 2: Update the `Build` method to use `OllamaURL()`**

  Find the inline `-c` override:
  ```go
  "-c", "model_providers."+ollamaProvider+".base_url=\""+ollamaProviderURL+"\"",
  ```

  Replace with:
  ```go
  "-c", "model_providers."+ollamaProvider+".base_url=\""+codexDriver{}.OllamaURL()+"\"",
  ```

  Remove the now-unused `config` import.

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/codex.go
  git commit -m "feat(agents/codex): implement OllamaURLer"
  ```

---

### Task 5: Implement `OllamaURLer` in `opencodeDriver`

**Files:**
- Modify: `internal/agents/opencode.go`

- [ ] **Step 1: Add the capability method**

  After `YoloFlag()`, add:

  ```go
  func (opencodeDriver) OllamaURL() string { return "http://localhost:11434/v1" }
  ```

- [ ] **Step 2: Use `OllamaURL()` in `Build`**

  Find:
  ```go
  m.ModelName, config.OllamaBaseURL,
  ```

  Replace with:
  ```go
  m.ModelName, opencodeDriver{}.OllamaURL(),
  ```

- [ ] **Step 3: Run agents package tests**

  ```bash
  go test ./internal/agents -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/agents/opencode.go
  git commit -m "feat(agents/opencode): implement OllamaURLer"
  ```

---

### Task 6: Update `internal/initseed/initseed.go` to use `Seeder`

**Files:**
- Modify: `internal/initseed/initseed.go`

- [ ] **Step 1: Add the `agents` import**

  Change the imports block to:
  ```go
  import (
      "fmt"
      "os"
      "path/filepath"

      "github.com/ohanaverse/agent-worktree/internal/agents"
      "github.com/ohanaverse/agent-worktree/internal/worktree"
  )
  ```

- [ ] **Step 2: Remove the `pointerFor` function and the `pointer` type**

  Delete from `initseed.go`:
  - `type pointer struct { ... }`
  - `func pointerFor(agent string) (pointer, bool) { ... }`

- [ ] **Step 3: Replace pointer creation with capability check**

  Find this block in `Seed`:
  ```go
  pointer, ok := pointerFor(agent)
  if ok {
      ptrPath := filepath.Join(repoRoot, pointer.path)
      created, err := writeIfMissing(ptrPath, pointer.content)
      if err != nil {
          return nil, err
      }
      track(res, created, pointer.path)
  }
  ```

  Replace with:
  ```go
  if d := agents.ByName(agent); d != nil {
      if s, ok := d.(agents.Seeder); ok {
          for _, ptr := range s.InstructionPointers() {
              ptrPath := filepath.Join(repoRoot, ptr.Path)
              created, err := writeIfMissing(ptrPath, ptr.Content)
              if err != nil {
                  return nil, err
              }
              track(res, created, ptr.Path)
          }
      }
  }
  ```

- [ ] **Step 4: Run initseed tests**

  ```bash
  go test ./internal/initseed -v
  ```

  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/initseed/initseed.go
  git commit -m "feat(initseed): use Seeder capability instead of pointerFor switch"
  ```

---

### Task 7: Mark `config.OllamaBaseURL` as migration-only

**Files:**
- Modify: `internal/config/config.go`

- [ ] **Step 1: Update the comment on `OllamaBaseURL`**

  Find:
  ```go
  // OllamaBaseURL is the default address of the local Ollama gateway that
  // cloud and local models route through.
  const OllamaBaseURL = "http://localhost:11434"
  ```

  Replace with:
  ```go
  // OllamaBaseURL is the default address of the local Ollama gateway.
  // It is kept in the config package for legacy migration only; agent
  // drivers now declare their own full gateway URLs via the OllamaURLer
  // capability in internal/agents.
  const OllamaBaseURL = "http://localhost:11434"
  ```

- [ ] **Step 2: Build to verify compilation**

  ```bash
  go build ./internal/config
  ```

  Expected: no errors.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/config/config.go
  git commit -m "docs(config): clarify OllamaBaseURL is migration-only"
  ```

---

### Task 8: Update `agents_test.go` capability contract and codex test

**Files:**
- Modify: `internal/agents/agents_test.go`

- [ ] **Step 1: Add `TestSeederCapability`**

  Append near the other capability tests:

  ```go
  // TestSeederCapability asserts which drivers implement the Seeder
  // optional capability. claude and copilot create instruction pointer
  // files; other agents do not.
  func TestSeederCapability(t *testing.T) {
      cases := []struct {
          agent string
          wants bool
      }{
          {"claude", true},
          {"copilot", true},
          {"codex", false},
          {"opencode", false},
          {"pi", false},
          {"agy", false},
          {"shell", false},
      }
      for _, c := range cases {
          d := ByName(c.agent)
          if d == nil {
              t.Fatalf("unknown agent: %s", c.agent)
          }
          _, got := d.(Seeder)
          if got != c.wants {
              t.Errorf("agent %q Seeder = %v, want %v", c.agent, got, c.wants)
          }
      }
  }
  ```

- [ ] **Step 2: Add `TestOllamaURLerCapability`**

  Append right after:

  ```go
  // TestOllamaURLerCapability asserts which drivers implement the
  // OllamaURLer optional capability. claude, copilot, codex, and opencode
  // route non-native models through a local Ollama gateway; other agents
  // do not.
  func TestOllamaURLerCapability(t *testing.T) {
      cases := []struct {
          agent string
          wants bool
      }{
          {"claude", true},
          {"copilot", true},
          {"codex", true},
          {"opencode", true},
          {"pi", false},
          {"agy", false},
          {"shell", false},
      }
      for _, c := range cases {
          d := ByName(c.agent)
          if d == nil {
              t.Fatalf("unknown agent: %s", c.agent)
          }
          _, got := d.(OllamaURLer)
          if got != c.wants {
              t.Errorf("agent %q OllamaURLer = %v, want %v", c.agent, got, c.wants)
          }
      }
  }
  ```

- [ ] **Step 3: Update the codex build test to use the driver's URL**

  Find the line around line 120:
  ```go
  "-c", "model_providers.agent-wt.base_url=\"" + config.OllamaBaseURL + "/v1/\"",
  ```

  Replace with:
  ```go
  "-c", "model_providers.agent-wt.base_url=\"" + codexDriver{}.OllamaURL() + "\"",
  ```

  Remove the `config` import from `agents_test.go` if it is no longer used anywhere else in the file. Build/test will tell you.

- [ ] **Step 4: Run the new tests**

  ```bash
  go test ./internal/agents -run "TestSeederCapability|TestOllamaURLerCapability" -v
  ```

  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/agents/agents_test.go
  git commit -m "test(agents): add Seeder and OllamaURLer capability contract tests"
  ```

---

### Task 9: Add `claudeDriver` Seeder and OllamaURLer tests

**Files:**
- Modify: `internal/agents/claude_test.go`

- [ ] **Step 1: Append the new tests**

  Add to the end of the file:

  ```go
  // TestClaudeSeeder asserts claudeDriver returns the CLAUDE.md pointer.
  func TestClaudeSeeder(t *testing.T) {
      d := claudeDriver{}
      s, ok := d.(Seeder)
      if !ok {
          t.Fatal("claudeDriver does not implement Seeder")
      }
      ptrs := s.InstructionPointers()
      if len(ptrs) != 1 {
          t.Fatalf("expected 1 pointer, got %d", len(ptrs))
      }
      if ptrs[0].Path != "CLAUDE.md" || ptrs[0].Content != "@AGENTS.md\n" {
          t.Errorf("pointer = %+v, want CLAUDE.md @AGENTS.md", ptrs[0])
      }
  }

  // TestClaudeOllamaURL asserts claudeDriver returns the bare gateway URL.
  func TestClaudeOllamaURL(t *testing.T) {
      d := claudeDriver{}
      u, ok := d.(OllamaURLer)
      if !ok {
          t.Fatal("claudeDriver does not implement OllamaURLer")
      }
      if got := u.OllamaURL(); got != "http://localhost:11434" {
          t.Errorf("OllamaURL() = %q, want http://localhost:11434", got)
      }
  }
  ```

- [ ] **Step 2: Run the new tests**

  ```bash
  go test ./internal/agents -run "TestClaudeSeeder|TestClaudeOllamaURL" -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/claude_test.go
  git commit -m "test(agents/claude): add Seeder and OllamaURLer tests"
  ```

---

### Task 10: Create `copilot_test.go`

**Files:**
- Create: `internal/agents/copilot_test.go`

- [ ] **Step 1: Create the test file**

  ```go
  package agents

  import (
      "testing"
  )

  // TestCopilotSeeder asserts copilotDriver returns the Copilot instruction
  // pointer file.
  func TestCopilotSeeder(t *testing.T) {
      d := copilotDriver{}
      s, ok := d.(Seeder)
      if !ok {
          t.Fatal("copilotDriver does not implement Seeder")
      }
      ptrs := s.InstructionPointers()
      if len(ptrs) != 1 {
          t.Fatalf("expected 1 pointer, got %d", len(ptrs))
      }
      want := InstructionPointer{
          Path:    ".github/copilot-instructions.md",
          Content: "Read AGENTS.md and follow all instructions in it.\n",
      }
      if ptrs[0] != want {
          t.Errorf("pointer = %+v, want %+v", ptrs[0], want)
      }
  }

  // TestCopilotOllamaURL asserts copilotDriver returns the /v1 endpoint.
  func TestCopilotOllamaURL(t *testing.T) {
      d := copilotDriver{}
      u, ok := d.(OllamaURLer)
      if !ok {
          t.Fatal("copilotDriver does not implement OllamaURLer")
      }
      if got := u.OllamaURL(); got != "http://localhost:11434/v1" {
          t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1", got)
      }
  }
  ```

- [ ] **Step 2: Run the new tests**

  ```bash
  go test ./internal/agents -run "TestCopilotSeeder|TestCopilotOllamaURL" -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/copilot_test.go
  git commit -m "test(agents/copilot): add Seeder and OllamaURLer tests"
  ```

---

### Task 11: Create `codex_test.go`

**Files:**
- Create: `internal/agents/codex_test.go`

- [ ] **Step 1: Create the test file**

  ```go
  package agents

  import (
      "testing"
  )

  // TestCodexOllamaURL asserts codexDriver returns the /v1/ endpoint used by
  // the inline model provider override.
  func TestCodexOllamaURL(t *testing.T) {
      d := codexDriver{}
      u, ok := d.(OllamaURLer)
      if !ok {
          t.Fatal("codexDriver does not implement OllamaURLer")
      }
      if got := u.OllamaURL(); got != "http://localhost:11434/v1/" {
          t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1/", got)
      }
  }
  ```

- [ ] **Step 2: Run the new test**

  ```bash
  go test ./internal/agents -run TestCodexOllamaURL -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/codex_test.go
  git commit -m "test(agents/codex): add OllamaURLer test"
  ```

---

### Task 12: Add `opencodeDriver` OllamaURLer test

**Files:**
- Modify: `internal/agents/opencode_test.go`

- [ ] **Step 1: Append the new test**

  Add to the end of the file:

  ```go
  // TestOpenCodeOllamaURL asserts opencodeDriver returns the /v1 endpoint.
  func TestOpenCodeOllamaURL(t *testing.T) {
      d := opencodeDriver{}
      u, ok := d.(OllamaURLer)
      if !ok {
          t.Fatal("opencodeDriver does not implement OllamaURLer")
      }
      if got := u.OllamaURL(); got != "http://localhost:11434/v1" {
          t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1", got)
      }
  }
  ```

- [ ] **Step 2: Run the new test**

  ```bash
  go test ./internal/agents -run TestOpenCodeOllamaURL -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/agents/opencode_test.go
  git commit -m "test(agents/opencode): add OllamaURLer test"
  ```

---

### Task 13: Update `initseed` tests to reflect capability-based behavior

**Files:**
- Modify: `internal/initseed/initseed_test.go`

- [ ] **Step 1: Add a `TestSeedShell` test**

  Append to the file:

  ```go
  // TestSeedShell creates only AGENTS.md for the shell command agent, which
  // lacks the Seeder capability.
  func TestSeedShell(t *testing.T) {
      root := t.TempDir()

      res, err := Seed("shell", root)
      if err != nil {
          t.Fatal(err)
      }
      if len(res.Created) != 1 || res.Created[0] != "AGENTS.md" {
          t.Fatalf("expected only AGENTS.md created, got %v", res.Created)
      }
  }
  ```

- [ ] **Step 2: Run initseed tests**

  ```bash
  go test ./internal/initseed -v
  ```

  Expected: PASS.

- [ ] **Step 3: Commit**

  ```bash
  git add internal/initseed/initseed_test.go
  git commit -m "test(initseed): add shell seed test, verify Seeder capability"
  ```

---

### Task 14: Final verification

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
  git commit -m "chore: final cleanup for driver-knowledge-consolidation"
  ```

---

## Spec coverage check

| Spec requirement | Task |
|---|---|
| Add `Seeder` optional capability | Task 1 |
| Add `OllamaURLer` optional capability | Task 1 |
| Implement `Seeder` in claude/copilot drivers | Tasks 2, 3 |
| Implement `OllamaURLer` in claude/copilot/codex/opencode drivers | Tasks 2–5 |
| Update `initseed` to use `Seeder` | Task 6 |
| Update driver `Build` methods to use `OllamaURL()` | Tasks 2–5 |
| Mark `config.OllamaBaseURL` as migration-only | Task 7 |
| Capability contract tests | Task 8 |
| Driver-specific `Seeder`/`OllamaURLer` tests | Tasks 9–12 |
| Updated `initseed` tests | Task 13 |

## Placeholder scan

- No "TBD", "TODO", or vague steps.
- Every code block contains the exact code to write.
- Every command has an expected outcome.
- No references to undefined types/functions.

## Type consistency check

- `Seeder`, `OllamaURLer`, and `InstructionPointer` names are consistent across spec and plan.
- `InstructionPointer` fields are `Path` and `Content` everywhere.
- `OllamaURL()` returns `string` in all locations.
