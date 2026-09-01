# Efficiency Fixes and Marginal Cleanup — Design

## Goal

Finish the simplification series with a single follow-up PR that applies three small efficiency wins and removes low-value dead/test-only code.

## Scope

**In:**
1. Eliminate the redundant `cfg.EligibleModels` call on the non-TUI launch path.
2. Cache `agents.Installed` in the config editor agent form.
3. Combine the two `git for-each-ref` branch lookups into one subprocess.
4. Remove production methods only used by tests: `Config.DefaultAgent`, `Config.ModelsForAgentAndTag`, `Rotation.StateDir`.
5. Remove dead provider/model fixups from `migrateConfigSchema`; keep only agent-level fixups.

**Out:** broader performance work, CLI behavior changes, registry/config data model changes.

## Status

Approved for implementation.

---

## 1. Eliminate double `EligibleModels`

### Current state

- `cmd/wt/resolve.go:resolveModel` calls `cfg.EligibleModels(agent, tags, family)`.
- On a multi-model/no-pin error, `cmd/wt/launch.go:launchFilteredImpl` calls `rotation.New().Next(cfg, agent, tags, family)`.
- `internal/rotation/rotation.go:Next` calls `cfg.EligibleModels(agent, tags, family)` again to build the allowed set.

The same list is computed twice for every launch that reaches rotation.

### Design

Change `resolveModel` to return the eligible slice alongside the resolved model:

```go
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error)
```

`resolveModelForLaunch` propagates the slice. `launchFilteredImpl` accepts an `eligible []config.Model` argument and, when it needs rotation, passes that slice to a new helper:

```go
func (r *Rotation) NextFromEligible(eligible []config.Model, cfg *config.Config) (config.Model, bool)
```

`Next` remains a thin wrapper that computes `eligible` and delegates to `NextFromEligible`, preserving the public API.

### Why this approach

- No hidden mutable cache state in `Config`.
- Explicit data flow; call sites control the list lifetime.
- Backward-compatible public API for `rotation.Next`.

---

## 2. Cache `agents.Installed` in the agent form

### Current state

`internal/configeditor/agent_form.go:handleAgentFormUpdate` runs `refreshAgentInstalled()` on every keystroke in the Name field, which calls `exec.LookPath`. The result is harmless but wasteful.

### Design

Add two fields to `model`:

```go
agInstalledName string // last name we actually looked up
agInstalled     bool   // cached result for agInstalledName
```

`refreshAgentInstalled` only calls `agents.Installed` when the trimmed name differs from `agInstalledName`:

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

### Why this approach

- Single-line change with immediate effect.
- Keeps the live "installed" indicator in the form without throttling or async complexity.

---

## 3. Single `git for-each-ref` for branches

### Current state

`internal/worktree/enumerate.go` calls:

```bash
git for-each-ref --format=%(refname:short) refs/heads
git for-each-ref --format=%(refname:short) refs/remotes/
```

### Design

Replace with one call that includes both ref namespaces:

```bash
git for-each-ref --format="%(refname:short)|%(refname)" refs/heads refs/remotes/
```

The parser partitions lines by whether the full refname starts with `refs/heads/` or `refs/remotes/`. Remote entries still apply the existing `*/HEAD` and `remote/short` filtering.

### Why this approach

- Fewer subprocesses per worktree picker open.
- Output format is stable and well-supported; no extra parsing risk.

---

## 4. Remove test-only methods

### Current state

- `Config.DefaultAgent()` — no production callers after the agent-picker and non-TUI launch refactorings. Only tests use it.
- `Config.ModelsForAgentAndTag()` — only used in tests.
- `Rotation.StateDir()` — only used in one test to build the state file path.

### Design

Delete the three methods. Inline their logic into the tests that actually exercised them:

- `DefaultAgent`: tests can read `cfg.Agents[0].Name` directly with a fallback to `"claude"`.
- `ModelsForAgentAndTag`: tests can call `cfg.ModelsForAgent` and filter by `m.HasTag(tag)`.
- `StateDir`: the one test can use `filepath.Join(rotation.New().dir, "rotation.state")`.

### Why this approach

- Shrinks the public surface of `Config` and `Rotation`.
- Tests that relied on these helpers were testing the helpers themselves, not production behavior. Removing both method and tests reduces churn surface.

---

## 5. Remove dead provider/model fixups from `migrateConfigSchema`

### Current state

`internal/config/migrate.go:migrateConfigSchema` renames providers/models, inserts the `agy` provider/model, and removes the `opencode` provider/model. But `config.Load` overwrites `cfg.Providers` and `cfg.Models` from the registry immediately after migration, so all provider/model mutations are dead code. The agent-level renames (google→agy, ensure agy agent, opencode→ollama) still run because `cfg.Agents` is wt-owned.

### Design

Delete provider/model mutations from `migrateConfigSchema`. Keep only the agent-level fixups. Update the function comment to state that provider/model data is registry-owned and therefore intentionally not migrated.

### Why this approach

- Removes misleading code and comments.
- No runtime change because the deleted paths were already overwritten.

---

## Error handling

- Efficiency changes preserve existing error semantics.
- `rotation.NextFromEligible` returns the same `(config.Model, bool)` contract as `rotation.Next`.
- Cleanup changes are pure deletions; no runtime behavior changes.

## Testing

- Add `TestResolveModelReturnsEligible` in `cmd/wt/resolve_test.go`.
- Add `TestRotationNextFromEligible` in `internal/rotation/rotation_test.go`.
- Update `cmd/wt/launch_test.go` stubs for the new `launchFiltered` signature (adds `eligible []config.Model`).
- Update `internal/configeditor/agent_form_test.go` to keep covering the installed indicator.
- Update `internal/worktree/enumerate_test.go` for the unified branch lister.
- Remove tests for deleted methods, or inline their assertions.
- Verify with `go test ./...` and `go vet ./...`.

## Follow-ups

This is the final planned simplification PR. After merge, the placeholder specs `efficiency-fixes` and `marginal-cleanup` are retired.
