# Native Unification Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the three incompatible "native" definitions (`IsNative()` = `ModelName == "native"`, `ProviderID == "claude"`/`"copilot"`, and `IsNative()` in codex/pi) with a single derived `Model.Native` field sourced from the registry's `auth.type == "native"`.

**Architecture:** Add a `Native bool` field to `config.Model` (tagged `toml:"-"` so it is never persisted), derive it once at load time via a `deriveNative(cfg)` helper that reads each model's provider `Auth.Type`, and switch every launch-path native check to `m.Native`. The two sentinel `ModelName == "native"` checks (claude bare-vs-named, legacy migration) stay as-is because they are a different concept.

**Tech Stack:** Go 1.26.7, BurntSushi/toml, cobra, Bubble Tea.

**Spec:** `docs/superpowers/specs/2026-08-28-native-unification-design.md`

## Global Constraints

- Go 1.26.7 (see `go.mod`).
- `Model.Native` is tagged `toml:"-"` — never decoded from the registry, never persisted by `Save`.
- The registry is read-only; wt never writes providers/models.
- Every `Test*` has a top-level `//` comment stating what it tests and why (user-facing consequence of a regression).
- `block-main-commit` guard refuses commits to `main` — commit on the `feat/native-unification` branch.
- Verify with `go test ./...`, `go vet ./...`, `go build ./...` before each commit.

---

## File Structure

- `internal/config/config.go` — add `Native` field, `deriveNative()`, wire into `Load()`, delete `IsNative()`.
- `internal/config/migrate.go` — change the legacy `m.IsNative()` to `m.ModelName == "native"`.
- `internal/agents/agents.go`, `codex.go`, `pi.go`, `pi_models.go`, `claude.go`, `copilot.go` — switch to `m.Native`.
- `cmd/wt/launch.go` — switch to `m.Native`.
- `internal/tui/app.go` — switch to `m.Native`.
- Tests: `internal/config/config_test.go`, `internal/agents/agents_test.go`, `internal/agents/pi_models_test.go`, `cmd/wt/launch_test.go`, `internal/tui/agent_model_test.go`.

---

### Task 1: Add `Native` field and `deriveNative()` helper

**Files:**
- Modify: `internal/config/config.go:54-62` (Model struct), `:123-167` (Load), `:263-267` (IsNative — leave for now)
- Modify: `internal/config/migrate.go:122`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `Model.Native bool` (field), `deriveNative(cfg *Config)` (unexported helper). Later tasks read `m.Native`; no task calls `deriveNative` directly except `Load`.

- [ ] **Step 1: Add the `Native` field to `Model`**

In `internal/config/config.go`, add a field to the `Model` struct (after `Source`):

```go
	Source     Source   `toml:"source,omitempty"` // curated or discovered
	Native     bool     `toml:"-"`                // derived: provider auth.type == "native"; not persisted
```

- [ ] **Step 2: Write the failing test**

Append to `internal/config/config_test.go`:

```go
// TestDeriveNative marks models whose provider authenticates natively
// (auth.type == "native") as Native, and leaves others non-native. This is
// the single source of truth for native-ness: a model is native iff its
// provider's auth type is "native", regardless of the model name. Without
// this, a future named native-provider model (e.g. claude/opus) would be
// misrouted through the ollama gateway.
func TestDeriveNative(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{
			{ID: "claude", Auth: AuthConfig{Type: "native"}},
			{ID: "ollama", Auth: AuthConfig{Type: "none"}},
		},
		Models: []Model{
			{ID: "claude/native", ProviderID: "claude", ModelName: "native"},
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus"},
			{ID: "ollama/gemma4", ProviderID: "ollama", ModelName: "gemma4"},
			{ID: "orphan/x", ProviderID: "missing", ModelName: "x"},
		},
	}
	deriveNative(cfg)
	want := []bool{true, true, false, false}
	for i, m := range cfg.Models {
		if m.Native != want[i] {
			t.Errorf("model %q Native = %v, want %v", m.ID, m.Native, want[i])
		}
	}
}
```

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./internal/config -run TestDeriveNative -v`
Expected: FAIL with `undefined: deriveNative`.

- [ ] **Step 4: Implement `deriveNative`**

Add to `internal/config/config.go` (near `ProviderByID`):

```go
// deriveNative marks each model whose provider authenticates natively
// (auth.type == "native") as Native. It runs after the registry join so the
// in-memory Native field reflects the registry's auth data — the single
// source of truth for native-ness. A model whose provider is missing (or has
// a non-native auth type) is left non-native.
func deriveNative(cfg *Config) {
	for i := range cfg.Models {
		p := cfg.ProviderByID(cfg.Models[i].ProviderID)
		cfg.Models[i].Native = p != nil && p.Auth.Type == "native"
	}
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/config -run TestDeriveNative -v`
Expected: PASS.

- [ ] **Step 6: Wire `deriveNative` into `Load()` at both join points**

In `internal/config/config.go`, after each `cfg.Providers, cfg.Models = providers, models` assignment, add `deriveNative(cfg)`:

Early-return path (config.toml missing):

```go
		cfg.Providers, cfg.Models = providers, models
		deriveNative(cfg)
		return cfg, nil
```

Normal path (after the registry join):

```go
	cfg.Providers, cfg.Models = providers, models
	deriveNative(cfg)
	return cfg, nil
```

- [ ] **Step 7: Change the legacy migration check**

In `internal/config/migrate.go:122`, replace `m.IsNative()` with the raw model-name test (the migration constructs models manually, so `Native` is never set here):

```go
			if m.ModelName == "native" && (m.Family == "google" || noNativeAgents[m.Family]) {
```

- [ ] **Step 8: Run the config package tests**

Run: `go test ./internal/config -v`
Expected: PASS (existing migration tests still pass — the `m.ModelName == "native"` change is behavior-identical).

- [ ] **Step 9: Commit**

```bash
git add internal/config/config.go internal/config/migrate.go internal/config/config_test.go
git commit -m "feat(config): add Model.Native field and deriveNative helper - completes plan item #1"
```

---

### Task 2: Switch the agents package to `m.Native`

**Files:**
- Modify: `internal/agents/agents.go:172`, `codex.go:36`, `pi.go:31`, `pi_models.go:66`, `claude.go:28`, `copilot.go:23`
- Test: `internal/agents/agents_test.go:16-25` (helpers), `:651-675` (comments), `internal/agents/pi_models_test.go:44`

**Interfaces:**
- Consumes: `Model.Native` (from Task 1).
- Produces: no new symbols — the drivers' `Build` behavior is unchanged; only the dispatch predicate changes.

- [ ] **Step 1: Switch the six call sites**

In `internal/agents/agents.go:172`:

```go
	if sess != nil && !m.Native {
```

In `internal/agents/codex.go:36`:

```go
	if !m.Native {
```

In `internal/agents/pi.go:31`:

```go
	if m.Native {
```

In `internal/agents/pi_models.go:66`:

```go
		if m.Native || m.ModelName == "" {
```

In `internal/agents/claude.go:28` (replace the `ProviderID == "claude"` dispatch):

```go
	if m.Native {
```

In `internal/agents/copilot.go:23` (replace the `ProviderID == "copilot"` dispatch):

```go
	if m.Native {
```

- [ ] **Step 2: Set `Native: true` on the native-model test helpers**

In `internal/agents/agents_test.go`, update both helpers:

```go
func nativeModel(agent string) config.Model {
	return config.Model{ID: agent + "/native", ProviderID: agent, ModelName: "native", Location: config.LocationCloud, Native: true}
}

// namedNativeModel returns a non-sentinel native-provider model (e.g.
// "claude/opus"): same provider key as the sentinel but with a real
// model name that should be passed via --model.
func namedNativeModel(provider, name string) config.Model {
	return config.Model{ID: provider + "/" + name, ProviderID: provider, ModelName: name, Location: config.LocationCloud, Native: true}
}
```

- [ ] **Step 3: Set `Native: true` on the inline native model in pi_models_test.go**

In `internal/agents/pi_models_test.go:44`:

```go
		{ID: "claude/native", ModelName: "native", Native: true},
```

- [ ] **Step 4: Update the `!m.IsNative()` references in comments**

In `internal/agents/agents_test.go`, update the three comment references (`:651`, `:654`, `:675`) from `!m.IsNative()` to `!m.Native` so the docs match the code.

- [ ] **Step 5: Run the agents package tests**

Run: `go test ./internal/agents -v`
Expected: PASS. The claude/copilot/codex/pi/resume-skip tests all pass because `Native: true` reproduces the prior `ProviderID == "claude"`/`IsNative()` behavior.

- [ ] **Step 6: Commit**

```bash
git add internal/agents/
git commit -m "feat(agents): dispatch on Model.Native instead of IsNative/ProviderID - completes plan item #2"
```

---

### Task 3: Switch cmd/wt to `m.Native`

**Files:**
- Modify: `cmd/wt/launch.go:33`
- Test: `cmd/wt/launch_test.go:82`, `:113` (comment)

**Interfaces:**
- Consumes: `Model.Native` (from Task 1).

- [ ] **Step 1: Switch the call site**

In `cmd/wt/launch.go:33`:

```go
	if !m.Native {
```

- [ ] **Step 2: Set `Native: true` on the inline native model**

In `cmd/wt/launch_test.go:82`:

```go
	cmd, err := buildLaunch("claude", config.Model{ID: "claude/native", ModelName: "native", Native: true}, "/tmp/repo", false,
```

- [ ] **Step 3: Update the `!m.IsNative()` reference in the comment**

In `cmd/wt/launch_test.go:113`, change `!m.IsNative()` to `!m.Native`.

- [ ] **Step 4: Run the cmd/wt tests**

Run: `go test ./cmd/wt -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "feat(cmd/wt): dispatch on Model.Native in launch path - completes plan item #3"
```

---

### Task 4: Switch internal/tui to `m.Native`

**Files:**
- Modify: `internal/tui/app.go:695`
- Test: `internal/tui/agent_model_test.go:385`

**Interfaces:**
- Consumes: `Model.Native` (from Task 1).

- [ ] **Step 1: Switch the call site**

In `internal/tui/app.go:695`:

```go
	if !highlighted.model.Native {
```

- [ ] **Step 2: Set `Native: true` on the inline native model**

In `internal/tui/agent_model_test.go:385`:

```go
		selectedPath: repo, models: singleModelList(config.Model{ID: "claude/native", ModelName: "native", Native: true})}
```

- [ ] **Step 3: Run the tui tests**

Run: `go test ./internal/tui -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go internal/tui/agent_model_test.go
git commit -m "feat(tui): dispatch on Model.Native in resume-skip - completes plan item #4"
```

---

### Task 5: Delete `IsNative()` and verify

**Files:**
- Modify: `internal/config/config.go:263-267`

**Interfaces:**
- Consumes: nothing (all call sites switched in Tasks 2-4; migrate.go switched in Task 1).
- Produces: removal of `Model.IsNative()`.

- [ ] **Step 1: Delete `IsNative()`**

In `internal/config/config.go`, delete the method (lines 263-267):

```go
// IsNative reports whether this model is an agent's native model
// (e.g. "claude/native"), as opposed to a provider-hosted model.
func (m Model) IsNative() bool {
	return m.ModelName == "native"
}
```

- [ ] **Step 2: Confirm no remaining references**

Run: `grep -rn "IsNative" --include="*.go" .`
Expected: no output (all references gone).

- [ ] **Step 3: Full verification**

Run: `go test ./... && go vet ./... && go build ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "refactor(config): delete Model.IsNative, superseded by Model.Native - completes plan item #5"
```

---

## Self-Review

**Spec coverage:**
- Spec §1 (`Native` field) → Task 1 Step 1. ✓
- Spec §2 (`deriveNative` + Load wiring) → Task 1 Steps 4, 6. ✓
- Spec §3 (8 call sites + delete `IsNative`) → Tasks 2-5. ✓
- Spec §4 (keep sentinel `ModelName == "native"` in claude.go:30 and migrate.go:122) → claude.go:30 untouched; migrate.go:122 changed in Task 1 Step 7. ✓
- Spec §5 (behavior change: resume-skip covers all native-provider models) → implicit in Tasks 2-4 (the `m.Native` predicate is broader than `ModelName == "native"`). ✓
- Spec "modelman: no changes needed" → no task (correctly, nothing to do). ✓
- Spec "Tests" → Tasks 1-4 test steps. ✓

**Placeholder scan:** no TBD/TODO; every code step has actual code. ✓

**Type consistency:** `Model.Native` (field) and `deriveNative(cfg *Config)` are defined in Task 1 and referenced identically in Tasks 2-4. ✓
