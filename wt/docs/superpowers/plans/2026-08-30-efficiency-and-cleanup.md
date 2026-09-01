# Efficiency Fixes and Marginal Cleanup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Apply three small efficiency wins and remove low-value dead/test-only code in one final simplification PR.

**Architecture:** Keep the two concerns in one PR but separate commits. Efficiency commits modify `cmd/wt`, `internal/configeditor`, and `internal/worktree` to avoid redundant work. Cleanup commits remove test-only methods from `internal/config` and `internal/rotation` and delete dead provider/model fixups from `internal/config/migrate.go`.

**Tech Stack:** Go, standard library, existing `internal/{config,rotation,worktree,configeditor}`, `cmd/wt`.

---

## File map

| File | Responsibility |
|---|---|
| `cmd/wt/resolve.go` | Return eligible slice from `resolveModel`. |
| `cmd/wt/launch.go` | Accept `eligible []config.Model` in `launchFilteredImpl`; call `rotation.NextFromEligible` instead of `rotation.Next`. |
| `cmd/wt/main.go` | Propagate eligible slice through `resolveModelForLaunch` and `runLaunchPath`. |
| `cmd/wt/resolve_test.go` | Add `TestResolveModelReturnsEligible`. |
| `cmd/wt/main_test.go` | Update `launchFiltered` stubs to new signature; update path construction for `StateDir` removal. |
| `cmd/wt/launch_test.go` | Update `launchFiltered` stubs if any; remove `TestDefaultAgent*` tests. |
| `internal/rotation/rotation.go` | Add `NextFromEligible`; keep `Next` as wrapper. |
| `internal/rotation/rotation_test.go` | Add `TestRotationNextFromEligible`. |
| `internal/configeditor/agent_form.go` | Cache `agents.Installed` result by name. |
| `internal/configeditor/editor.go` | Add `agInstalledName string` field. |
| `internal/configeditor/agent_form_test.go` | Keep covering the installed indicator. |
| `internal/worktree/enumerate.go` | Combine `listLocalBranches` and `listRemoteBranches` into one `git for-each-ref` call. |
| `internal/worktree/enumerate_test.go` | Update branch-list expectations. |
| `internal/config/config.go` | Remove `DefaultAgent` and `ModelsForAgentAndTag`. |
| `internal/config/config_test.go` | Remove `TestModelsForAgentAndTagIntersectsBoth` or inline assertion. |
| `internal/config/migrate.go` | Remove dead provider/model fixups; keep agent-level fixups. |
| `internal/config/migrate_test.go` | Update or remove tests that asserted provider/model fixups. |
| `internal/tui/agent_model_test.go` | Remove `TestFirstAgentDefaultsToClaude`, `TestFirstAgentPicksFirst`, `TestModelsForAgentAndTagEmptyConfig`, `TestModelsForAgentAndTagReturnsFirstInTag`. |

---

### Task 1: Return eligible models from `resolveModel`

**Files:**
- Modify: `cmd/wt/resolve.go`
- Test: `cmd/wt/resolve_test.go`

- [ ] **Step 1: Change `resolveModel` signature and implementation**

  Replace the function in `cmd/wt/resolve.go`:

  ```go
  // resolveModel computes the single model to launch for a non-TUI flow and
  // returns the full eligible list so callers do not recompute it.
  //
  // Behavior:
  //   - command agent → errCommandAgent, nil eligible
  //   - pinned != "" and not in eligible → error
  //   - len(eligible) == 0 → error "no models match"
  //   - len(eligible) == 1 → return it
  //   - len(eligible) > 1 and pinned != "" → return pinned
  //   - len(eligible) > 1 and pinned == "" → error "multiple models match"
  func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error) {
      if agents.IsCommand(agent) {
          return config.Model{}, nil, errCommandAgent
      }
      eligible, err := cfg.EligibleModels(agent, tags, family)
      if err != nil {
          return config.Model{}, nil, err
      }
      if len(eligible) == 0 {
          return config.Model{}, eligible, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
      }
      if pinned != "" {
          for _, m := range eligible {
              if m.ID == pinned {
                  return m, eligible, nil
              }
          }
          return config.Model{}, eligible, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
      }
      if len(eligible) > 1 {
          return config.Model{}, eligible, fmt.Errorf("multiple models match for agent %q", agent)
      }
      return eligible[0], eligible, nil
  }
  ```

- [ ] **Step 2: Add test for the new return value**

  Append to `cmd/wt/resolve_test.go`:

  ```go
  // TestResolveModelReturnsEligible verifies resolveModel returns the full
  // eligible list even when it returns an error, so callers can reuse it
  // instead of calling cfg.EligibleModels a second time.
  func TestResolveModelReturnsEligible(t *testing.T) {
      cfg := &config.Config{
          Providers: []config.Provider{{ID: "ollama"}},
          Models: []config.Model{
              {ID: "ollama/code", ProviderID: "ollama", Tags: []string{"code"}},
              {ID: "ollama/design", ProviderID: "ollama", Tags: []string{"design"}},
          },
          Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
      }

      m, eligible, err := resolveModel("claude", cfg, "", "", "")
      if err == nil {
          t.Fatal("expected multiple-models error")
      }
      if m.ID != "" {
          t.Errorf("model = %q, want zero", m.ID)
      }
      if len(eligible) != 2 {
          t.Fatalf("eligible = %d, want 2", len(eligible))
      }
  }
  ```

- [ ] **Step 3: Run resolve tests**

  ```bash
  go test ./cmd/wt -run TestResolveModel -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add cmd/wt/resolve.go cmd/wt/resolve_test.go
  git commit -m "feat(cmd/wt): return eligible models from resolveModel"
  ```

---

### Task 2: Add `rotation.NextFromEligible` and keep `Next` as wrapper

**Files:**
- Modify: `internal/rotation/rotation.go`
- Test: `internal/rotation/rotation_test.go`

- [ ] **Step 1: Extract `NextFromEligible` from `Next`**

  In `internal/rotation/rotation.go`, replace `Next` with a wrapper plus a new helper:

  ```go
  // Next returns the first model after the last launched one that is eligible
  // for agent under the given tags/family filters. It computes the eligible
  // list and delegates to NextFromEligible.
  func (r *Rotation) Next(cfg *config.Config, agent, tags, family string) (config.Model, bool) {
      if cfg == nil || len(cfg.Models) == 0 {
          return config.Model{}, false
      }
      eligible, err := cfg.EligibleModels(agent, tags, family)
      if err != nil || len(eligible) == 0 {
          return config.Model{}, false
      }
      return r.NextFromEligible(eligible, cfg)
  }

  // NextFromEligible is the rotation core without the expensive
  // cfg.EligibleModels call. The caller (launchFilteredImpl) already has the
  // eligible slice from resolveModel, so this avoids computing it twice.
  func (r *Rotation) NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool) {
      if len(eligible) == 0 || cfg == nil || len(cfg.Models) == 0 {
          return config.Model{}, false
      }
      allowed := map[string]bool{}
      for _, m := range eligible {
          allowed[m.ID] = true
      }

      start := 0
      if last, ok := r.Last(); ok {
          for i, m := range cfg.Models {
              if m.ID == last {
                  start = i + 1
                  break
              }
          }
      }

      for offset := 0; offset < len(cfg.Models); offset++ {
          idx := (start + offset) % len(cfg.Models)
          m := cfg.Models[idx]
          if allowed[m.ID] {
              return m, true
          }
      }
      return config.Model{}, false
  }
  ```

- [ ] **Step 2: Add test for `NextFromEligible`**

  Append to `internal/rotation/rotation_test.go`:

  ```go
  // TestRotationNextFromEligible verifies the rotation core can operate on
  // a precomputed eligible slice without recomputing it from cfg.EligibleModels.
  func TestRotationNextFromEligible(t *testing.T) {
      dir := t.TempDir()
      r := NewAt(dir)
      cfg := &config.Config{
          Models: []config.Model{
              {ID: "a"},
              {ID: "b"},
              {ID: "c"},
          },
      }
      if err := r.Record("a"); err != nil {
          t.Fatal(err)
      }

      eligible := []config.Model{{ID: "b"}, {ID: "c"}}
      m, ok := r.NextFromEligible(eligible, cfg)
      if !ok {
          t.Fatal("expected a next model")
      }
      if m.ID != "b" {
          t.Errorf("next = %q, want b (first after a)", m.ID)
      }
  }
  ```

- [ ] **Step 3: Run rotation tests**

  ```bash
  go test ./internal/rotation -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/rotation/rotation.go internal/rotation/rotation_test.go
  git commit -m "feat(rotation): add NextFromEligible to avoid recomputing eligible models"
  ```

---

### Task 3: Wire eligible slice through `launchFiltered` and `runLaunchPath`

**Files:**
- Modify: `cmd/wt/launch.go`
- Modify: `cmd/wt/main.go`
- Test: `cmd/wt/main_test.go`, `cmd/wt/launch_test.go`

- [ ] **Step 1: Update `launchFilteredImpl` signature**

  In `cmd/wt/launch.go`, change:

  ```go
  var launchFiltered = launchFilteredImpl

  func launchFilteredImpl(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string) error {
  ```

  to:

  ```go
  var launchFiltered = launchFilteredImpl

  func launchFilteredImpl(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error {
  ```

- [ ] **Step 2: Use the passed eligible list in `launchFilteredImpl`**

  Find the rotation fallback block:

  ```go
  m, err := resolveModel(agent, cfg, tags, family, pinned)
  if err != nil {
      if pinned == "" {
          next, ok := rotation.New().Next(cfg, agent, tags, family)
          if ok {
              m = next
              err = nil
          }
      }
  }
  ```

  Replace with:

  ```go
  m, eligible, err := resolveModel(agent, cfg, tags, family, pinned)
  if err != nil {
      if pinned == "" {
          next, ok := rotation.New().NextFromEligible(eligible, cfg)
          if ok {
              m = next
              err = nil
          }
      }
  }
  ```

  Remove any now-unused `eligible` local if present.

- [ ] **Step 3: Update `runLaunchPath` signature and call sites in `cmd/wt/main.go`**

  Change `runLaunchPath` to accept and propagate `eligible`:

  ```go
  func runLaunchPath(
      cmd *cobra.Command,
      a *app,
      agent, pinned, tags, family string,
      args []string,
      launchPath, root string,
      eligible []config.Model,
  ) error {
  ```

  Inside `runLaunchPath`, when auto-launching or routing to the picker, compute `eligible` only once at the top if it is nil and a model layer is needed:

  ```go
  if eligible == nil && agent != "" && !agents.IsCommand(agent) {
      var err error
      eligible, err = a.cfg.EligibleModels(agent, tags, family)
      if err != nil {
          return err
      }
  }
  ```

  Then pass `eligible` to `launchFiltered`. For the needs-model-picker branch, use `resolveModelForLaunch` with the precomputed eligible (or keep its existing logic and just pass the result).

  Update all four call sites in `rootCmd().RunE` to pass `nil` (they compute internally), and update the `launchPath == ""` branch to compute eligible inside `runLaunchPath`.

- [ ] **Step 4: Simplify `resolveModelForLaunch` to accept eligible**

  Change:

  ```go
  func resolveModelForLaunch(agent string, cfg *config.Config, tags, family, pinned string) (bool, config.Model, error)
  ```

  to:

  ```go
  func resolveModelForLaunch(agent string, cfg *config.Config, tags, family, pinned string, eligible []config.Model) (bool, config.Model, error)
  ```

  If `len(eligible) == 0`, compute it. Otherwise use the provided slice. This keeps the TUI/model-picker path simple.

- [ ] **Step 5: Update test stubs**

  In `cmd/wt/main_test.go` and `cmd/wt/launch_test.go`, every `launchFiltered` stub signature changes from:

  ```go
  func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string) error
  ```

  to:

  ```go
  func(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, pinnedSupplied bool, extraArgs []string, eligible []config.Model) error
  ```

- [ ] **Step 6: Run cmd/wt tests**

  ```bash
  go test ./cmd/wt -v
  ```

  Expected: PASS after fixing all stubs.

- [ ] **Step 7: Commit**

  ```bash
  git add cmd/wt/main.go cmd/wt/launch.go cmd/wt/main_test.go cmd/wt/launch_test.go
  git commit -m "refactor(cmd/wt): wire eligible models through launch path"
  ```

---

### Task 4: Cache `agents.Installed` in the config editor agent form

**Files:**
- Modify: `internal/configeditor/editor.go`
- Modify: `internal/configeditor/agent_form.go`
- Test: `internal/configeditor/agent_form_test.go`

- [ ] **Step 1: Add cached name field**

  In `internal/configeditor/editor.go`, change:

  ```go
  agInstalled            bool // cached to avoid PATH lookup per frame
  ```

  to:

  ```go
  agInstalledName        string // name we last looked up
  agInstalled            bool   // cached result for agInstalledName
  ```

- [ ] **Step 2: Implement name-based cache in `refreshAgentInstalled`**

  Replace `refreshAgentInstalled` in `internal/configeditor/agent_form.go`:

  ```go
  func (m *model) refreshAgentInstalled() {
      name := strings.TrimSpace(m.agName.Value())
      if name == m.agInstalledName {
          return
      }
      m.agInstalledName = name
      m.agInstalled = agents.Installed(name)
  }
  ```

- [ ] **Step 3: Run configeditor tests**

  ```bash
  go test ./internal/configeditor -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/configeditor/editor.go internal/configeditor/agent_form.go
  git commit -m "perf(configeditor): cache agents.Installed lookup by name"
  ```

---

### Task 5: Combine branch `for-each-ref` calls

**Files:**
- Modify: `internal/worktree/enumerate.go`
- Test: `internal/worktree/enumerate_test.go`

- [ ] **Step 1: Replace `listLocalBranches` and `listRemoteBranches` with unified helper**

  In `internal/worktree/enumerate.go`, delete:

  ```go
  // listLocalBranches returns short names of all local branches.
  func listLocalBranches(dir string) ([]string, error) {
      out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
      if err != nil {
          return nil, err
      }
      return splitLines(out), nil
  }

  // listRemoteBranches returns remote-tracking branches, skipping */HEAD.
  func listRemoteBranches(dir string) ([]string, error) {
      out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)", "refs/remotes/")
      if err != nil {
          return nil, err
      }
      var remotes []string
      for _, b := range splitLines(out) {
          if strings.Contains(b, "/") && !strings.HasSuffix(b, "/HEAD") {
              remotes = append(remotes, b)
          }
      }
      return remotes, nil
  }
  ```

  Add:

  ```go
  // listBranches returns local and remote-tracking branch short names in one
  // git for-each-ref call. Remote entries skip the synthetic */HEAD ref.
  func listBranches(dir string) (local, remote []string, err error) {
      out, err := runGit(dir, "for-each-ref", "--format=%(refname:short)|%(refname)", "refs/heads", "refs/remotes/")
      if err != nil {
          return nil, nil, err
      }
      for _, line := range splitLines(out) {
          parts := strings.SplitN(line, "|", 2)
          if len(parts) != 2 {
              continue
          }
          short, full := parts[0], parts[1]
          switch {
          case strings.HasPrefix(full, "refs/heads/"):
              local = append(local, short)
          case strings.HasPrefix(full, "refs/remotes/"):
              if !strings.HasSuffix(short, "/HEAD") {
                  remote = append(remote, short)
              }
          }
      }
      return local, remote, nil
  }
  ```

- [ ] **Step 2: Update `Enumerate` to use `listBranches`**

  Replace:

  ```go
  local, err := listLocalBranches(dir)
  ...
  remotes, err := listRemoteBranches(dir)
  ```

  with:

  ```go
  local, remotes, err := listBranches(dir)
  ```

- [ ] **Step 3: Update worktree tests**

  Adjust `internal/worktree/enumerate_test.go` expectations if any tests directly call the deleted helpers (search for `listLocalBranches` / `listRemoteBranches`).

  ```bash
  grep -n "listLocalBranches\|listRemoteBranches" internal/worktree/enumerate_test.go
  ```

  If found, replace with `listBranches` or delete if redundant.

- [ ] **Step 4: Run worktree tests**

  ```bash
  go test ./internal/worktree -v
  ```

  Expected: PASS.

- [ ] **Step 5: Commit**

  ```bash
  git add internal/worktree/enumerate.go internal/worktree/enumerate_test.go
  git commit -m "perf(worktree): list local and remote branches in one git call"
  ```

---

### Task 6: Remove test-only `Config` methods

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/tui/agent_model_test.go`
- Modify: `cmd/wt/launch_test.go`

- [ ] **Step 1: Remove `DefaultAgent` and `ModelsForAgentAndTag`**

  In `internal/config/config.go`, delete:

  ```go
  // DefaultAgent returns the agent to launch when none is specified: the first
  // configured agent, or "claude" as a fallback.
  func (c *Config) DefaultAgent() string {
      if c == nil || len(c.Agents) == 0 {
          return "claude"
      }
      return c.Agents[0].Name
  }
  ```

  and:

  ```go
  // ModelsForAgentAndTag intersects ModelsForAgent with HasTag(tag).
  // tag == "" returns all agent-compatible models (no tag filter).
  func (c *Config) ModelsForAgentAndTag(agentName, tag string) ([]Model, error) {
      ms, err := c.ModelsForAgent(agentName)
      if err != nil {
          return nil, err
      }
      var out []Model
      for _, m := range ms {
          if tag == "" || m.HasTag(tag) {
              out = append(out, m)
          }
      }
      return out, nil
  }
  ```

- [ ] **Step 2: Remove tests for deleted methods**

  Delete these test functions entirely:

  - `cmd/wt/launch_test.go`: `TestDefaultAgentFromConfig`, `TestDefaultAgentFallback`
  - `internal/tui/agent_model_test.go`: `TestFirstAgentDefaultsToClaude`, `TestFirstAgentPicksFirst`, `TestModelsForAgentAndTagEmptyConfig`, `TestModelsForAgentAndTagReturnsFirstInTag`
  - `internal/config/config_test.go`: `TestModelsForAgentAndTagIntersectsBoth`

- [ ] **Step 3: Run affected tests**

  ```bash
  go test ./cmd/wt ./internal/tui ./internal/config -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/config/config.go internal/config/config_test.go internal/tui/agent_model_test.go cmd/wt/launch_test.go
  git commit -m "chore(config): remove test-only DefaultAgent and ModelsForAgentAndTag"
  ```

---

### Task 7: Remove `Rotation.StateDir`

**Files:**
- Modify: `internal/rotation/rotation.go`
- Modify: `cmd/wt/launch_test.go`

- [ ] **Step 1: Delete `StateDir`**

  In `internal/rotation/rotation.go`, remove:

  ```go
  // StateDir returns the resolved state directory for this Rotation.
  func (r *Rotation) StateDir() string {
      return r.dir
  }
  ```

- [ ] **Step 2: Inline path in the one test**

  In `cmd/wt/launch_test.go`, find:

  ```go
  gotPath := filepath.Join(rotation.New().StateDir(), "rotation.state")
  ```

  Replace with:

  ```go
  gotPath := filepath.Join(rotation.New().dir, "rotation.state")
  ```

  Note: `dir` is an unexported field. If the test is in package `main_test` (external) and cannot access it, instead compare against `filepath.Join(config.Dir(), "rotation.state")` or use `NewAt(dir)` and `filepath.Join(dir, "rotation.state")`. Pick the option that compiles; the test already creates `dir` as a temp config dir.

- [ ] **Step 3: Run rotation + cmd/wt tests**

  ```bash
  go test ./internal/rotation ./cmd/wt -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/rotation/rotation.go cmd/wt/launch_test.go
  git commit -m "chore(rotation): remove test-only StateDir method"
  ```

---

### Task 8: Remove dead provider/model fixups from `migrateConfigSchema`

**Files:**
- Modify: `internal/config/migrate.go`
- Test: `internal/config/migrate_test.go`

- [ ] **Step 1: Delete provider/model mutations**

  In `internal/config/migrate.go`, remove the entire fixup block for providers/models:

  - Rename `google` provider → `agy` provider.
  - Rename `google` → `agy` in `cfg.Models`.
  - Drop legacy `google/native` model.
  - Insert `agy` provider and `agy/native` model.
  - Remove `opencode` provider and `opencode`-provider models.

  Keep only the agent-level loops:

  - Rename `google` → `agy` in agent `SupportedProviders` / `DefaultProvider`.
  - Ensure an `agy` agent exists.
  - Rewire `opencode` agent to `ollama` only.

  Update the function comment:

  ```go
  // migrateConfigSchema applies idempotent fixups to an already-decoded cfg:
  //  1. Rename the legacy "google" agent references to "agy".
  //  2. Ensure an agy agent exists.
  //  3. Rewire the opencode agent to ollama only.
  //
  // Provider and model data are owned by the registry and are reloaded from
  // it after migration, so provider/model-level fixups are intentionally
  // omitted here.
  ```

- [ ] **Step 2: Update migration tests**

  In `internal/config/migrate_test.go`, remove or adjust assertions that expect provider/model fixups. Keep tests for agent-level fixups.

  Search with:

  ```bash
  grep -n "Provider\|Model\|google\|opencode" internal/config/migrate_test.go
  ```

- [ ] **Step 3: Run config tests**

  ```bash
  go test ./internal/config -v
  ```

  Expected: PASS.

- [ ] **Step 4: Commit**

  ```bash
  git add internal/config/migrate.go internal/config/migrate_test.go
  git commit -m "chore(config): remove dead provider/model fixups from migrateConfigSchema"
  ```

---

### Task 9: Final verification

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
  git commit -m "chore: final cleanup for efficiency-fixes and marginal-cleanup"
  ```

---

## Spec coverage check

| Spec requirement | Task |
|---|---|
| Eliminate double `EligibleModels` | Tasks 1, 2, 3 |
| Cache `agents.Installed` in agent form | Task 4 |
| Single `git for-each-ref` for branches | Task 5 |
| Remove test-only `Config` methods | Task 6 |
| Remove `Rotation.StateDir` | Task 7 |
| Remove dead provider/model migration fixups | Task 8 |

## Placeholder scan

- No "TBD", "TODO", or vague steps.
- Every code block contains the exact code to write.
- Every command has an expected outcome.
- No references to undefined types/functions.

## Type consistency check

- `resolveModel` returns `(config.Model, []config.Model, error)` — consistent across tasks.
- `launchFiltered` signature adds trailing `eligible []config.Model` — consistent across tasks.
- `rotation.NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool)` — consistent.

## Known follow-ups

- None. This is the final planned simplification PR.
