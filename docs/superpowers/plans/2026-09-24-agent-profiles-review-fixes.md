# Agent-Profiles Code-Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 10 confirmed findings from the `/code-review` (high effort) run against `wt-agent-profiles-design`'s diff vs `main`, so the local-model launch-profiles feature (`wt profile ...`, `internal/profiles`, the launch-path wiring in `cmd/wt/launch.go` and `internal/tui`) behaves as its own design comments and tests already claim.

**Architecture:** Nine independent-to-mostly-independent Go changes inside the existing `wt/internal/profiles`, `wt/cmd/wt`, and `wt/internal/tui` packages. No new packages except one tiny `merge.go` file inside `internal/profiles`. The largest task (8) adds a `ProfileApplier` function-type seam so `internal/tui` can apply launch profiles without creating an import cycle with `cmd/wt` (which already imports `internal/tui`).

**Tech Stack:** Go 1.26 (module root `wt/`), `github.com/spf13/cobra`, `github.com/charmbracelet/bubbletea`, standard `testing`.

**Spec:** No separate spec doc — this plan's source of truth is the code-review findings themselves (verified against the current source under `wt-agent-profiles-design` on 2026-09-24) plus the feature's own existing design comments in `wt/internal/profiles/*.go` and `wt/CLAUDE.md`'s "Agent-wt profiles" references (`docs/wt-agents/profiles.md`).

## Global Constraints

- Every new/changed Go file must pass `gofmt -l` (no diff) and `go vet ./...`.
- Every `Test*` function gets a `//` comment stating what it tests and why it matters, per `wt/CLAUDE.md`'s Go-tests convention — already followed by every existing test in this codebase; match that style exactly.
- No new external dependencies.
- `go test ./...` (run from `wt/`) must pass after every task, not just at the end.
- Never touch `applyProfileForLaunch`'s core guarantee that a profile-layer failure degrades to a normal unprofiled launch (documented repeatedly in `cmd/wt/launch.go`) — every fix below preserves that.

## Review Focus

1. **A profile that matches via an empty/unset field must never silently become a wildcard** — Task 1's `matchesTier`/`Validate` fix is pinned by tests using a zero-value `config.Model` (exactly what `wt profile show -A <agent>` with no `-M` passes) and a malformed profile missing its match value.
2. **Self-heal of an orphaned `config_content` backup must run for every launch of an agent, including native-model and command-agent launches of that same agent** — Task 2's test launches a native model, not just an ordinary local-model launch (the only case the existing tests cover).
3. **A pre-launch profile-application failure must not leak launch state** (refcount entry, no summary line) — Task 5 (non-TUI) and Task 8 (TUI) both pin that `cmd.Run()` never executes AND the refcount entry is released AND the summary line still prints, on both launch paths.
4. **Deep-merge collisions must not silently drop unrelated sibling keys** — Task 6's tests specifically collide on a shared top-level key (`env`, `provider`) across two profile tiers / the opencode driver's own env payload, not just two non-colliding keys (the only case the existing tests cover).
5. **The TUI launch path is the majority UX for `wt`** (the picker, not `-A`/`-M` flags) — Task 8 is not optional polish; before it, profiles.toml has zero effect on any launch that goes through the picker, which is most launches. Its test exercises the real `runLaunchPath` → `tuiRun` wiring, not just the internal `internal/tui` unit in isolation.

---

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/profiles/resolve.go` | Modify: `matchesTier` empty-field guard (Task 1); `Resolve`'s `ConfigContent` merge becomes deep (Task 6) |
| `wt/internal/profiles/validate.go` | Modify: reject an empty match-tier field at validation time (Task 1) |
| `wt/internal/profiles/resolve_test.go` | Modify: new tests for Tasks 1, 6 |
| `wt/internal/profiles/validate_test.go` | Modify: new test for Task 1 |
| `wt/internal/profiles/configcontent.go` | Modify: `restoreIfBackedUpLocked` bool fix (Task 3); `mergeOpenCodeEnv` deep-merges (Task 6); codex `--profile` append stays but ordering context changes (Task 7) |
| `wt/internal/profiles/configcontent_test.go` | Modify: new tests for Tasks 3, 6 |
| `wt/internal/profiles/merge.go` | Create: shared `mergeInto` deep-merge helper (Task 6) |
| `wt/internal/profiles/merge_test.go` | Create: unit tests for `mergeInto` (Task 6) |
| `wt/internal/profiles/apply.go` | Modify: split `ApplyToCmd` into `ApplyEnvAndArgs` + `ApplyWrapper` (Task 7) |
| `wt/internal/profiles/apply_test.go` | Modify: new tests for the split functions (Task 7) |
| `wt/cmd/wt/profile.go` | Modify: `list`/`show` surface `a.profilesValidateErr` (Task 4) |
| `wt/cmd/wt/profile_test.go` | Modify: new tests for Task 4 |
| `wt/cmd/wt/launch.go` | Modify: self-heal ordering (Task 2); `runAgentCmd` pre-launch error path (Task 5); `applyResolvedProfile` reordering (Task 7) |
| `wt/cmd/wt/launch_test.go` | Modify: new tests for Tasks 2, 5, 7 |
| `wt/internal/tui/launch.go` | Modify: `ProfileApplier` type + seam, `runAndWaitCmd` applies it (Task 8) |
| `wt/internal/tui/app.go` | Modify: `Run` takes a `ProfileApplier` param (Task 8) |
| `wt/internal/tui/launch_test.go` | Modify: new tests for Task 8 |
| `wt/cmd/wt/main.go` | Modify: build the real `ProfileApplier` closure, pass to both `tuiRun` call sites (Task 8) |
| `wt/cmd/wt/main_test.go` | Modify: update 8 `tuiRun` stub signatures + 1 new wiring test (Task 8) |

---

### Task 1: Reject an empty match-tier field in `matchesTier` and `Validate`

**Files:**
- Modify: `wt/internal/profiles/resolve.go:80-92` (`matchesTier`)
- Modify: `wt/internal/profiles/validate.go:19-44` (`Validate`)
- Test: `wt/internal/profiles/resolve_test.go`, `wt/internal/profiles/validate_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `matchesTier` and `Validate` now both reject a profile whose match-tier field (`Location`/`Provider`/`Model`, selected by `Match`) is empty. No signature changes.

**Bug:** `matchesTier`'s `"provider"` and `"model"` cases do `providerID(m) == p.Provider` / `m.ID == p.Model` with no non-empty check. A malformed profile missing its match value (e.g. `match = "model"` with no `model = ...` line) has `p.Model == ""`; resolved against a zero-value `config.Model{}` (exactly what `wt profile show -A <agent>` with no `-M` passes, per `cmd/wt/profile.go:70`'s own comment "no tier can match without a model"), `m.ID == "" == p.Model` is `true` — a spurious match, contradicting that comment. The same risk applies to an empty `provider` field.

- [ ] **Step 1: Write the failing tests**

Add to `wt/internal/profiles/resolve_test.go` (after `TestResolveDisabledAgentSkipsOtherAgentsProfiles`):

```go
// TestMatchesTierRejectsEmptyFieldEvenWithZeroModel is the regression lock
// for the code-review finding that matchesTier treated an empty/unset
// match-tier field as a valid match value: a malformed profile with
// `match = "model"` but no `model = ...` line (Model == "") used to match
// ANY zero-value config.Model (m.ID == ""), which `wt profile show -A
// <agent>` (no -M) resolves with — directly contradicting the design's "no
// tier can match without a model" comment. Now it must never match.
func TestMatchesTierRejectsEmptyFieldEvenWithZeroModel(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "model", Env: map[string]string{"X": "1"}}, // Model left empty (malformed)
	}}
	rp := Resolve(store, "claude", testCfg(), config.Model{})
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (an empty Model field must never match, even against a zero-value model)", rp)
	}
}

// TestMatchesTierRejectsEmptyProviderField verifies the same empty-field
// guard for the provider tier: a malformed profile with `match =
// "provider"` and no `provider = ...` line must never match a model whose
// own provider fallback also happens to be empty.
func TestMatchesTierRejectsEmptyProviderField(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "codex", Match: "provider", Args: []string{"-c", "x=1"}}, // Provider left empty (malformed)
	}}
	m := config.Model{ID: "no-slash-in-this-id"} // providerID(m) falls back to "" too
	rp := Resolve(store, "codex", testCfg(), m)
	if !rp.Empty() {
		t.Errorf("Resolve() = %+v, want Empty() (an empty Provider field must never match)", rp)
	}
}
```

Add to `wt/internal/profiles/validate_test.go` (after `TestValidateRejectsInvalidMatchTier`):

```go
// TestValidateRejectsEmptyMatchField is the regression lock for the other
// half of the matchesTier empty-field bug: a profile whose match tier
// names a field left empty (e.g. `match = "model"` with no `model = ...`)
// must fail loudly at validation time — the same way an invalid match tier
// value already does — rather than silently becoming a wildcard that
// matches every launch of its agent.
func TestValidateRejectsEmptyMatchField(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "claude", Match: "model", Env: map[string]string{"X": "1"}}, // Model left empty
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismEnv} }
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the empty model match field")
	}
	if !contains(err.Error(), "empty") {
		t.Errorf("Validate() error = %v, want it to mention the empty match field", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/ -run 'TestMatchesTierRejectsEmpty|TestValidateRejectsEmptyMatchField' -v`
Expected: FAIL (both `matchesTier` cases match when they shouldn't; `Validate` returns nil)

- [ ] **Step 3: Implement the guard in `matchesTier`**

In `wt/internal/profiles/resolve.go`, replace:

```go
func matchesTier(p Profile, cfg *config.Config, m config.Model) bool {
	switch p.Match {
	case "location":
		loc, err := cfg.ResolveLocation(m)
		return err == nil && string(loc) == p.Location
	case "provider":
		return providerID(m) == p.Provider
	case "model":
		return m.ID == p.Model
	default:
		return false
	}
}
```

with:

```go
func matchesTier(p Profile, cfg *config.Config, m config.Model) bool {
	switch p.Match {
	case "location":
		if p.Location == "" {
			return false
		}
		loc, err := cfg.ResolveLocation(m)
		return err == nil && string(loc) == p.Location
	case "provider":
		if p.Provider == "" {
			return false
		}
		return providerID(m) == p.Provider
	case "model":
		if p.Model == "" {
			return false
		}
		return m.ID == p.Model
	default:
		return false
	}
}
```

- [ ] **Step 4: Implement the guard in `Validate`**

In `wt/internal/profiles/validate.go`, replace the loop body's opening check:

```go
	for i, p := range store.Profiles {
		if !validMatchTiers[p.Match] {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s): invalid match %q (must be \"location\", \"provider\", or \"model\")",
				i, p.Agent, p.Match))
			continue
		}
```

with:

```go
	for i, p := range store.Profiles {
		if !validMatchTiers[p.Match] {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s): invalid match %q (must be \"location\", \"provider\", or \"model\")",
				i, p.Agent, p.Match))
			continue
		}
		if matchFieldEmpty(p) {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s, match=%s): empty %s value — a profile whose match tier has no value would match every launch",
				i, p.Agent, p.Match, p.Match))
			continue
		}
```

and add this helper below `Validate` (before `usedMechanisms`):

```go
// matchFieldEmpty reports whether p's match-tier field (Location/Provider/
// Model, selected by p.Match) is empty — the same condition matchesTier
// (resolve.go) guards against at resolution time. Called only after
// validMatchTiers has already confirmed p.Match is one of the three known
// values, so the switch has no default case of its own to worry about.
func matchFieldEmpty(p Profile) bool {
	switch p.Match {
	case "location":
		return p.Location == ""
	case "provider":
		return p.Provider == ""
	case "model":
		return p.Model == ""
	}
	return false
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -v`
Expected: PASS (all tests in the package, including the two new ones and `TestValidateRejectsEmptyMatchField`)

- [ ] **Step 6: Commit**

```bash
cd wt
git add internal/profiles/resolve.go internal/profiles/validate.go internal/profiles/resolve_test.go internal/profiles/validate_test.go
git commit -m "fix(profiles): reject empty match-tier field in matchesTier and Validate

completes plan item #1"
```

---

### Task 2: Run config_content self-heal unconditionally, before the native/command-agent guard

**Files:**
- Modify: `wt/cmd/wt/launch.go:333-338` (`applyProfileForLaunch`)
- Test: `wt/cmd/wt/launch_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no signature change; `applyProfileForLaunch`'s self-heal call now genuinely runs on every launch of the agent, matching its own doc comment.

**Bug:** `selfHealAgentConfigContentTarget(agent, cmd.Dir)` is called AFTER the early-return guard `if m.ID == "" || m.Native || cfg == nil { return noop, nil }`, so a native-model or command-agent launch of an agent with a `config_content` mechanism never self-heals an orphaned backup — contradicting the function's own doc comment ("It runs UNCONDITIONALLY, on every launch of that agent"). `selfHealAgentConfigContentTarget` depends only on `agent` and `cmd.Dir`, never on `m` or `cfg`, so it has no reason to sit after the guard.

- [ ] **Step 1: Write the failing test**

Add to `wt/cmd/wt/launch_test.go` (after `TestApplyProfileForLaunchSelfHealsEvenWhenProfilesDisabled`):

```go
// TestApplyProfileForLaunchSelfHealsForNativeModelLaunch is the regression
// lock for the code-review finding that self-heal used to run AFTER the
// early-return guard for native models/command agents, contradicting its
// own doc comment ("runs UNCONDITIONALLY, on every launch of that agent").
// A native-model launch never resolves or applies a profile of its own
// (ResolveRoute returns a zero Route), but it must still self-heal an
// orphaned config_content backup left by a PRIOR session's non-native
// launch of the same agent.
func TestApplyProfileForLaunchSelfHealsForNativeModelLaunch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}
	rp := profiles.ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	priorCmd := exec.Command("claude")
	if _, err := profiles.ApplyConfigContent(priorCmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed orphaned backup: %v", err)
	}

	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "claude/native", Native: true, ModelName: "native"}
	cmd := exec.Command("true")
	cmd.Dir = worktree

	cleanup, err := applyProfileForLaunch(cmd, "claude", m, cfg, nil)
	if err != nil {
		t.Fatalf("applyProfileForLaunch() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(handEdited) {
		t.Errorf("target = %s, want the orphaned hand-edited content restored on a native-model launch too", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./cmd/wt/ -run TestApplyProfileForLaunchSelfHealsForNativeModelLaunch -v`
Expected: FAIL (target still holds the profile-written content, not the hand-edited original)

- [ ] **Step 3: Move the self-heal call before the guard**

In `wt/cmd/wt/launch.go`, replace:

```go
func applyProfileForLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if m.ID == "" || m.Native || cfg == nil {
		return noop, nil
	}
	selfHealAgentConfigContentTarget(agent, cmd.Dir)

	var store profiles.Store
```

with:

```go
func applyProfileForLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) (cleanup func() error, err error) {
	noop := func() error { return nil }
	// Self-heal runs before the early-return guards below: it repairs PAST
	// session state (an orphaned config_content backup left by a prior
	// launch that was killed mid-run) and depends only on agent/cmd.Dir,
	// never on m or cfg — it must still run for a native-model or
	// command-agent launch of the same agent, which is exactly the launch
	// class the guard below skips for THIS launch's own profile
	// resolution.
	selfHealAgentConfigContentTarget(agent, cmd.Dir)
	if m.ID == "" || m.Native || cfg == nil {
		return noop, nil
	}

	var store profiles.Store
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./cmd/wt/ -run 'TestApplyProfileForLaunch|TestRunAgentCmd' -v`
Expected: PASS (the new test plus every existing self-heal/applyProfileForLaunch/runAgentCmd test)

- [ ] **Step 5: Commit**

```bash
cd wt
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "fix(wt): run profile self-heal before the native/command-agent guard

completes plan item #2"
```

---

### Task 3: Fix `restoreIfBackedUpLocked`'s inconsistent `restored` bool on cleanup failure

**Files:**
- Modify: `wt/internal/profiles/configcontent.go:251-287` (`restoreIfBackedUpLocked`)
- Test: `wt/internal/profiles/configcontent_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: no signature change; `restoreIfBackedUpLocked`/`restoreIfBackedUp`/`SelfHeal` now report `restored=true` whenever the target's content was actually put back, even if the subsequent backup-marker cleanup then fails.

**Bug:** The `.absent` branch (lines 252-257) returns `(true, err)` when `os.Remove(backupAbsentPath(target))` fails after the target itself was already removed. The `.present` branch (lines 261-282) returns `(false, err)` when `os.Remove(backupPresentPath(target))` fails AFTER `config.WriteFileAtomic(target, data, perm)` already succeeded — the restore itself worked, but the function falsely reports `restored=false`, violating its own documented `(restored, err)` contract and disagreeing with the `.absent` branch's convention for the structurally equivalent case.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/profiles/configcontent_test.go` (after `TestSelfHealNoopWithoutBackup`):

```go
// TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore is
// the regression lock for the code-review finding that
// restoreIfBackedUpLocked's two restore branches disagreed on what
// `restored` bool to return when the restore itself succeeds but the
// following backup-marker cleanup then fails: the "target was absent"
// branch already returned (true, err) for this case, but the "target
// existed" branch returned (false, err) — falsely reporting "not restored"
// despite the target's content already being back in place.
func TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	worktree := t.TempDir()
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	cmd := exec.Command("claude")
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("seed backup: %v", err)
	}

	// Make backupDir() read-only so the restore's own WriteFileAtomic(target,
	// ...) still succeeds (target lives under worktree, not backupDir()) but
	// the subsequent os.Remove(backupPresentPath(target)) inside backupDir()
	// fails with permission denied — the exact split this test pins.
	if err := os.Chmod(backupDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(backupDir(), 0o700) })

	restored, err := restoreIfBackedUp(target)
	if err == nil {
		t.Fatal("restoreIfBackedUp() error = nil, want the cleanup permission error")
	}
	if !restored {
		t.Error("restoreIfBackedUp() restored = false, want true — the target's content was already restored before cleanup failed")
	}
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(handEdited) {
		t.Errorf("target content = %s, want the restored hand-edited content %s", got, handEdited)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/profiles/ -run TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore -v`
Expected: FAIL (`restored` comes back `false`)

- [ ] **Step 3: Fix the two failure returns in the `.present` branch**

In `wt/internal/profiles/configcontent.go`, replace:

```go
		if werr := config.WriteFileAtomic(target, data, perm); werr != nil {
			return false, werr
		}
		if rmErr := os.Remove(backupPresentPath(target)); rmErr != nil {
			return false, rmErr
		}
		if rmErr := os.Remove(backupModePath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, rmErr
		}
		return true, nil
```

with:

```go
		if werr := config.WriteFileAtomic(target, data, perm); werr != nil {
			return false, werr
		}
		// From here on the target's content is already restored — a
		// cleanup failure below must still report restored=true, mirroring
		// the .absent branch above for the structurally equivalent case.
		if rmErr := os.Remove(backupPresentPath(target)); rmErr != nil {
			return true, rmErr
		}
		if rmErr := os.Remove(backupModePath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return true, rmErr
		}
		return true, nil
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./internal/profiles/... -v`
Expected: PASS (all tests, including the new one)

- [ ] **Step 5: Commit**

```bash
cd wt
git add internal/profiles/configcontent.go internal/profiles/configcontent_test.go
git commit -m "fix(profiles): report restored=true when marker cleanup fails after a successful restore

completes plan item #3"
```

---

### Task 4: `wt profile show`/`list` surface `a.profilesValidateErr`

**Files:**
- Modify: `wt/cmd/wt/profile.go:16-25` (`matchValueForDisplay`), `:33-51` (`list` RunE), `:55-97` (`show` RunE)
- Test: `wt/cmd/wt/profile_test.go`

**Interfaces:**
- Consumes: `a.profilesValidateErr` (already populated by `newApp()`, `cmd/wt/app.go:84-87`).
- Produces: no signature change.

**Bug (finding #4):** `show`'s `RunE` only checks `a.profilesLoadErr`, never `a.profilesValidateErr`, before calling `profiles.Resolve` directly — so its dry-run output can claim a profile applies when a real launch would disable ALL profile application file-wide (`applyProfileForLaunch` checks both `pp.loadErr` and `pp.validateErr`).

**Bug (finding #10):** `list`'s `matchValueForDisplay` falls through to `default: return "model=" + p.Model` for an invalid/typo'd match tier, silently misdescribing the entry instead of flagging the same problem `show`/a real launch would report via `Validate`.

- [ ] **Step 1: Write the failing tests**

Add to `wt/cmd/wt/profile_test.go` (after `TestProfileShowWithoutModelDoesNotError`):

```go
// TestProfileListFlagsInvalidMatchTier is the regression lock for the
// code-review finding that `wt profile list` silently misdescribed a
// profile with an invalid/typo'd match tier as "model=" (showing whatever
// stale/empty Model field happened to be set) instead of flagging the
// problem the same way `wt profile show`/a real launch would.
func TestProfileListFlagsInvalidMatchTier(t *testing.T) {
	a := &app{profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "claude", Match: "locaton", Location: "local"},
	}}}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "invalid") {
		t.Errorf("output = %q, want the invalid match tier flagged", got)
	}
	if strings.Contains(got, "model=") {
		t.Errorf("output = %q, misdescribed the invalid entry as a model match", got)
	}
}

// TestProfileListWarnsOnValidateError verifies `wt profile list` also
// surfaces a.profilesValidateErr as a warning after the listing — the file
// parsed fine (list still shows every entry) but no profile in it can ever
// actually apply at launch time, and the user should see that.
func TestProfileListWarnsOnValidateError(t *testing.T) {
	a := &app{
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local"},
		}},
		profilesValidateErr: errors.New(`profiles.toml[0] (agent=claude): agent does not support mechanism "wrapper"`),
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	if !strings.Contains(out.String(), "disabled for every launch") {
		t.Errorf("output = %q, want a validate-error warning", out.String())
	}
}

// TestProfileShowReportsValidateErrorInsteadOfResolving is the regression
// lock for the code-review finding that `wt profile show` ignored
// a.profilesValidateErr and called profiles.Resolve directly — so its
// dry-run output could claim a profile applies when a real launch would
// disable ALL profile application file-wide (applyProfileForLaunch checks
// both loadErr and validateErr and disables everything on either).
func TestProfileShowReportsValidateErrorInsteadOfResolving(t *testing.T) {
	a := &app{
		cfg: &config.Config{
			Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}},
			Models:    []config.Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		},
		profiles: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
			{Agent: "claude", Match: "location", Location: "local", Env: map[string]string{"X": "1"}},
		}},
		profilesValidateErr: errors.New(`profiles.toml[1] (agent=pi): agent does not support mechanism "config_file"`),
	}
	var out bytes.Buffer
	cmd := profileCmd(a)
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "-A", "claude", "-M", "ollama/x"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute error = %v", err)
	}
	got := out.String()
	if strings.Contains(got, "X=1") {
		t.Errorf("output = %q, showed a profile as applying despite a.profilesValidateErr set (a real launch would disable it)", got)
	}
	if !strings.Contains(got, "disabled") {
		t.Errorf("output = %q, want it to report profiles are disabled due to the validation error", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/ -run 'TestProfileListFlagsInvalidMatchTier|TestProfileListWarnsOnValidateError|TestProfileShowReportsValidateErrorInsteadOfResolving' -v`
Expected: FAIL (list shows `model=`, no invalid/disabled wording; show prints `X=1`)

- [ ] **Step 3: Fix `matchValueForDisplay`**

In `wt/cmd/wt/profile.go`, replace:

```go
func matchValueForDisplay(p profiles.Profile) string {
	switch p.Match {
	case "location":
		return "location=" + p.Location
	case "provider":
		return "provider=" + p.Provider
	default:
		return "model=" + p.Model
	}
}
```

with:

```go
func matchValueForDisplay(p profiles.Profile) string {
	switch p.Match {
	case "location":
		return "location=" + p.Location
	case "provider":
		return "provider=" + p.Provider
	case "model":
		return "model=" + p.Model
	default:
		return fmt.Sprintf("match=%q (invalid)", p.Match)
	}
}
```

- [ ] **Step 4: Fix the `list` RunE**

In `wt/cmd/wt/profile.go`, replace:

```go
			for _, p := range a.profiles.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
			}
			return nil
		},
	}
```

(the one inside `listC`) with:

```go
			for _, p := range a.profiles.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: match=%s %s\n", p.Agent, p.Match, matchValueForDisplay(p))
			}
			if a.profilesValidateErr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "warning: profiles.toml failed validation — profiles are disabled for every launch: %v\n", a.profilesValidateErr)
			}
			return nil
		},
	}
```

- [ ] **Step 5: Fix the `show` RunE**

In `wt/cmd/wt/profile.go`, replace:

```go
			if a.profilesLoadErr != nil {
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
			// -M is optional (design spec: `wt profile show -A <agent> [-M
```

with:

```go
			if a.profilesLoadErr != nil {
				return fmt.Errorf("profiles.toml: %w", a.profilesLoadErr)
			}
			if a.profilesValidateErr != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "(profiles disabled for every launch — profiles.toml failed validation: %v)\n", a.profilesValidateErr)
				return nil
			}
			// -M is optional (design spec: `wt profile show -A <agent> [-M
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/ -run TestProfile -v`
Expected: PASS (all `TestProfile*` tests, including the three new ones)

- [ ] **Step 7: Commit**

```bash
cd wt
git add cmd/wt/profile.go cmd/wt/profile_test.go
git commit -m "fix(wt): surface profiles.toml validate errors in 'profile list'/'show'

completes plan item #4"
```

---

### Task 5: `runAgentCmd` releases the refcount entry and prints the summary on a pre-launch profile-apply error

**Files:**
- Modify: `wt/cmd/wt/launch.go:439-442` (`runAgentCmd`)
- Test: `wt/cmd/wt/launch_test.go`

**Interfaces:**
- Consumes: existing `releaseSession` seam (`wt/cmd/wt/launch.go:36`), existing `agents.Summary`.
- Produces: no signature change.

**Bug:** When `applyProfileForLaunch` returns a non-nil error (the one legitimately-fatal case: a wrapper mechanism naming a missing binary), `runAgentCmd` returns immediately — skipping `releaseSession()`, the survey prompt, the stop picker, AND the summary line. `refcount.NewStore().Record(os.Getpid(), m.ID)` already ran in `launchFilteredImpl` before `runAgentCmd` was called, so the refcount entry leaks until the next `wt` invocation's sweep. The file's own established convention (the earlier ollama-check failure path) explicitly prints the summary first "so the user sees the same post-run line on pre-launch config errors as on a real exit" — this path does not follow it. Survey and the stop picker are intentionally skipped here (unlike a real exit): nothing ran, so there is no session to ask "did it work?" about or a model that might now be idle.

- [ ] **Step 1: Write the failing test**

Add to `wt/cmd/wt/launch_test.go` (after `TestRunAgentCmdMalformedProfilesTomlDegradesGracefully`):

```go
// TestRunAgentCmdProfileApplyErrorReleasesAndPrintsSummary is the
// regression lock for the code-review finding that a pre-launch profile
// application error (e.g. a wrapper profile naming a missing binary) used
// to return immediately from runAgentCmd, skipping releaseSession() and the
// summary line — unlike the ollama-check failure path in launchFilteredImpl,
// which explicitly prints the summary before returning. This verifies
// cmd.Run() never executes (the sentinel file is never created), the
// refcount entry is released, and the summary line is printed with a 0
// duration.
func TestRunAgentCmdProfileApplyErrorReleasesAndPrintsSummary(t *testing.T) {
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	released := false
	oldRelease := releaseSession
	releaseSession = func() { released = true }
	t.Cleanup(func() { releaseSession = oldRelease })

	pp := &precomputedProfiles{store: profiles.Store{Enabled: true, Profiles: []profiles.Profile{
		{Agent: "pi", Match: "location", Location: "local",
			Wrapper: &profiles.WrapperSpec{Binary: "wt-test-definitely-missing-binary-xyz"}},
	}}}
	cfg := &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}

	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "should-not-exist")
	cmd := exec.Command("touch", sentinel)

	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatal(perr)
	}
	oldStdout := os.Stdout
	os.Stdout = w

	err := runAgentCmd(cmd, "pi", m, cfg, pp)

	w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)

	if err == nil {
		t.Fatal("runAgentCmd() error = nil, want the wrapper-not-installed error")
	}
	if !strings.Contains(err.Error(), "not installed") {
		t.Errorf("error = %v, want a wrapper-not-installed error", err)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("sentinel file exists — cmd.Run() executed despite the profile apply error")
	}
	if !released {
		t.Error("releaseSession() was not called on a pre-launch profile apply error")
	}
	if !strings.Contains(string(out), "wt: pi · ollama/x") {
		t.Errorf("stdout = %q, want the summary line printed", out)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./cmd/wt/ -run TestRunAgentCmdProfileApplyErrorReleasesAndPrintsSummary -v`
Expected: FAIL (`released` stays `false`, stdout is empty)

- [ ] **Step 3: Fix `runAgentCmd`**

In `wt/cmd/wt/launch.go`, replace:

```go
	profileCleanup, perr := applyProfileForLaunch(cmd, agent, m, cfg, pp)
	if perr != nil {
		return perr
	}

	start := time.Now()
	err := cmd.Run()
```

with:

```go
	profileCleanup, perr := applyProfileForLaunch(cmd, agent, m, cfg, pp)
	if perr != nil {
		// Mirrors the ollama-check failure path in launchFilteredImpl above:
		// a pre-launch config error must still release the refcount entry
		// already recorded before runAgentCmd was called, and print the
		// summary line, so the user sees it on a pre-launch config error
		// exactly as on a real exit. Duration is 0 — the subprocess never
		// started. Survey and the stop picker are deliberately skipped:
		// there is no session to ask "did it work?" about.
		releaseSession()
		fmt.Println("\n" + agents.Summary(agent, m, 0))
		return perr
	}

	start := time.Now()
	err := cmd.Run()
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./cmd/wt/ -run 'TestRunAgentCmd' -v`
Expected: PASS (all `TestRunAgentCmd*` tests, including the new one)

- [ ] **Step 5: Commit**

```bash
cd wt
git add cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "fix(wt): release refcount and print summary on pre-launch profile apply error

completes plan item #5"
```

---

### Task 6: Deep-merge `ConfigContent` instead of a shallow top-level overwrite

**Files:**
- Create: `wt/internal/profiles/merge.go`
- Create: `wt/internal/profiles/merge_test.go`
- Modify: `wt/internal/profiles/resolve.go:54-77` (`Resolve`)
- Modify: `wt/internal/profiles/configcontent.go:113-134` (`mergeOpenCodeEnv`)
- Test: `wt/internal/profiles/resolve_test.go`, `wt/internal/profiles/configcontent_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `mergeInto(dst, src map[string]any)` — merges `src` into `dst` in place, recursing into nested `map[string]any` values instead of replacing them wholesale. Used by both `Resolve` (findings #7) and `mergeOpenCodeEnv` (finding #6).

**Bug (finding #6):** `mergeOpenCodeEnv` does `for k, v := range content { doc[k] = v }` — a shallow, top-level-only merge into `OPENCODE_CONFIG_CONTENT`. The opencode driver's own `Build()` already sets a top-level `provider` key (baseURL/apiKey/models — see `wt/internal/agents/opencode.go`). A profile author writing `config_content = { provider = { some_nested_field = ... } }` to add one nested field instead wholesale-replaces the whole `provider` object, silently dropping the connectivity fields the opencode gateway depends on.

**Bug (finding #7):** `Resolve`'s per-tier `ConfigContent` merge (`out.ConfigContent[k] = substituteAny(v, m)`) is the same shallow pattern: two tiers that both set `config_content.env = {...}` (a common shape — see `wt/CLAUDE.md`'s `ANTHROPIC_DEFAULT_*_MODEL`/`MAX_THINKING_TOKENS` examples) collide on the top-level `env` key, and the more specific tier's `env` map wholesale-replaces the less specific tier's, silently dropping sibling env vars. This is a real behavioral gap: nothing in `resolve_test.go` exercises multi-tier `ConfigContent` merging (only `Env`, a flat `map[string]string`, is tested for this).

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/profiles/merge_test.go`:

```go
// wt/internal/profiles/merge_test.go
package profiles

import (
	"reflect"
	"testing"
)

// TestMergeIntoDeepMergesNestedMaps verifies mergeInto recurses into a
// nested map[string]any shared by both sides instead of replacing it
// wholesale — the core behavior findings #6 and #7 depend on: a profile
// adding one nested field to an object (e.g. config_content.env.SOME_KEY)
// must not drop that object's other existing fields.
func TestMergeIntoDeepMergesNestedMaps(t *testing.T) {
	dst := map[string]any{
		"provider": map[string]any{"baseURL": "http://x", "apiKey": "k"},
		"other":    "untouched",
	}
	src := map[string]any{
		"provider": map[string]any{"models": []any{"m1"}},
	}
	mergeInto(dst, src)
	want := map[string]any{
		"provider": map[string]any{"baseURL": "http://x", "apiKey": "k", "models": []any{"m1"}},
		"other":    "untouched",
	}
	if !reflect.DeepEqual(dst, want) {
		t.Errorf("mergeInto() dst = %#v, want %#v", dst, want)
	}
}

// TestMergeIntoReplacesNonMapValues verifies a scalar or list value on
// either side is whole-value replacement (not merged), matching TOML's own
// "last value wins" semantics — only map[string]any values recurse.
func TestMergeIntoReplacesNonMapValues(t *testing.T) {
	dst := map[string]any{"model": "old", "tags": []any{"a"}}
	src := map[string]any{"model": "new", "tags": []any{"b", "c"}}
	mergeInto(dst, src)
	if dst["model"] != "new" {
		t.Errorf(`dst["model"] = %v, want "new"`, dst["model"])
	}
	if !reflect.DeepEqual(dst["tags"], []any{"b", "c"}) {
		t.Errorf(`dst["tags"] = %v, want [b c] (whole-list replacement)`, dst["tags"])
	}
}

// TestMergeIntoMapReplacesNonMapDstValue verifies that when src's value at
// a key is a map but dst's existing value at that key is NOT a map (a type
// mismatch a hand-edited profiles.toml could produce), the whole value is
// replaced rather than panicking or silently keeping the stale scalar.
func TestMergeIntoMapReplacesNonMapDstValue(t *testing.T) {
	dst := map[string]any{"provider": "not-a-map"}
	src := map[string]any{"provider": map[string]any{"baseURL": "http://x"}}
	mergeInto(dst, src)
	want := map[string]any{"baseURL": "http://x"}
	if !reflect.DeepEqual(dst["provider"], want) {
		t.Errorf(`dst["provider"] = %#v, want %#v`, dst["provider"], want)
	}
}
```

Add to `wt/internal/profiles/resolve_test.go` (after `TestResolveModelTierOverridesLocationTierSameKey`):

```go
// TestResolveConfigContentDeepMergesAcrossTiers is the regression lock for
// the code-review finding that Resolve's ConfigContent merge was a shallow
// top-level overwrite: a location-tier profile setting
// config_content.env.MAX_THINKING_TOKENS and a model-tier profile setting
// config_content.env.ANTHROPIC_DEFAULT_SONNET_MODEL both collide on the
// SAME top-level "env" key. Both nested fields must survive in the merged
// result — the model tier must not wholesale-replace the location tier's
// "env" object just because they share that one top-level key.
func TestResolveConfigContentDeepMergesAcrossTiers(t *testing.T) {
	store := Store{Enabled: true, Profiles: []Profile{
		{Agent: "claude", Match: "location", Location: "local", ConfigContent: map[string]any{
			"env": map[string]any{"MAX_THINKING_TOKENS": "4096"},
		}},
		{Agent: "claude", Match: "model", Model: "ollama/qwen3.8:27b-mlx", ConfigContent: map[string]any{
			"env": map[string]any{"ANTHROPIC_DEFAULT_SONNET_MODEL": "{{model_name}}"},
		}},
	}}
	m := config.Model{ID: "ollama/qwen3.8:27b-mlx", ProviderID: "ollama", ModelName: "qwen3.8:27b-mlx"}
	rp := Resolve(store, "claude", testCfg(), m)
	env, ok := rp.ConfigContent["env"].(map[string]any)
	if !ok {
		t.Fatalf("ConfigContent[env] = %#v, want a map", rp.ConfigContent["env"])
	}
	if env["MAX_THINKING_TOKENS"] != "4096" {
		t.Errorf("env[MAX_THINKING_TOKENS] = %v, want 4096 (location-tier value must survive the model-tier merge)", env["MAX_THINKING_TOKENS"])
	}
	if env["ANTHROPIC_DEFAULT_SONNET_MODEL"] != "qwen3.8:27b-mlx" {
		t.Errorf("env[ANTHROPIC_DEFAULT_SONNET_MODEL] = %v, want the substituted model name", env["ANTHROPIC_DEFAULT_SONNET_MODEL"])
	}
}
```

Add to `wt/internal/profiles/configcontent_test.go` (after `TestApplyConfigContentOpenCodeMergesIntoEnv`):

```go
// TestMergeOpenCodeEnvDeepMergesCollidingTopLevelKey is the regression lock
// for the code-review finding that mergeOpenCodeEnv did a shallow merge: a
// profile's config_content.provider colliding with the opencode driver's
// own top-level "provider" key (baseURL/apiKey/models — set by Build())
// must merge the profile's new nested field in, not wholesale-replace the
// whole object and silently drop the connectivity fields.
func TestMergeOpenCodeEnvDeepMergesCollidingTopLevelKey(t *testing.T) {
	cmd := exec.Command("opencode")
	cmd.Env = []string{`OPENCODE_CONFIG_CONTENT={"provider":{"agent-wt":{"baseURL":"http://x","apiKey":"k"}}}`}
	rp := ResolvedProfile{ConfigContent: map[string]any{
		"provider": map[string]any{"agent-wt": map[string]any{"extra": "field"}},
	}}
	cleanup, err := ApplyConfigContent(cmd, "opencode", "/tmp/wt", rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}
	defer cleanup()

	merged := strings.TrimPrefix(cmd.Env[0], openCodeConfigEnvPrefix)
	var doc map[string]any
	if err := json.Unmarshal([]byte(merged), &doc); err != nil {
		t.Fatalf("merged env is not valid JSON: %v (%s)", err, merged)
	}
	provider, ok := doc["provider"].(map[string]any)
	if !ok {
		t.Fatalf("doc[provider] = %#v, want a map", doc["provider"])
	}
	agentWT, ok := provider["agent-wt"].(map[string]any)
	if !ok {
		t.Fatalf("doc[provider][agent-wt] = %#v, want a map", provider["agent-wt"])
	}
	if agentWT["baseURL"] != "http://x" || agentWT["apiKey"] != "k" {
		t.Errorf("doc[provider][agent-wt] = %v, lost the driver's own baseURL/apiKey", agentWT)
	}
	if agentWT["extra"] != "field" {
		t.Errorf("doc[provider][agent-wt] = %v, missing the profile's merged field", agentWT)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/ -run 'TestMergeInto|TestResolveConfigContentDeepMergesAcrossTiers|TestMergeOpenCodeEnvDeepMergesCollidingTopLevelKey' -v`
Expected: FAIL (`mergeInto` doesn't exist yet — compile error; once it exists as a stub the two merge-behavior tests fail on the shallow overwrite)

- [ ] **Step 3: Create the deep-merge helper**

Create `wt/internal/profiles/merge.go`:

```go
// wt/internal/profiles/merge.go
package profiles

// mergeInto recursively merges src into dst: a key present in both whose
// values are themselves map[string]any is merged key-by-key instead of
// replaced wholesale, so a profile's config_content can add or override
// one nested field (e.g. config_content.env.SOME_KEY, or opencode's own
// config_content.provider.agent-wt.<field>) without clobbering sibling
// fields already present at that same nested key. A non-map value on
// either side — including a src map colliding with a non-map dst value —
// is whole-value replacement, matching TOML's own "last value wins"
// semantics for scalars and lists.
func mergeInto(dst, src map[string]any) {
	for k, v := range src {
		if srcMap, ok := v.(map[string]any); ok {
			if dstMap, ok := dst[k].(map[string]any); ok {
				mergeInto(dstMap, srcMap)
				continue
			}
		}
		dst[k] = v
	}
}
```

- [ ] **Step 4: Use it in `Resolve`**

In `wt/internal/profiles/resolve.go`, replace:

```go
		for k, v := range p.ConfigContent {
			if out.ConfigContent == nil {
				out.ConfigContent = map[string]any{}
			}
			out.ConfigContent[k] = substituteAny(v, m)
		}
```

with:

```go
		if len(p.ConfigContent) > 0 {
			if out.ConfigContent == nil {
				out.ConfigContent = map[string]any{}
			}
			substituted, _ := substituteAny(p.ConfigContent, m).(map[string]any)
			mergeInto(out.ConfigContent, substituted)
		}
```

- [ ] **Step 5: Use it in `mergeOpenCodeEnv`**

In `wt/internal/profiles/configcontent.go`, replace:

```go
		for k, v := range content {
			doc[k] = v
		}
```

with:

```go
		mergeInto(doc, content)
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -v`
Expected: PASS (all tests in the package)

- [ ] **Step 7: Commit**

```bash
cd wt
git add internal/profiles/merge.go internal/profiles/merge_test.go internal/profiles/resolve.go internal/profiles/configcontent.go internal/profiles/resolve_test.go internal/profiles/configcontent_test.go
git commit -m "fix(profiles): deep-merge ConfigContent instead of shallow top-level overwrite

completes plan item #6"
```

---

### Task 7: Codex `--profile` flag lands after a profile's own ExtraArgs, not before

**Files:**
- Modify: `wt/internal/profiles/apply.go` (split `ApplyToCmd`)
- Modify: `wt/cmd/wt/launch.go:386-422` (`applyResolvedProfile`)
- Test: `wt/internal/profiles/apply_test.go`, `wt/cmd/wt/launch_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `profiles.ApplyEnvAndArgs(cmd *exec.Cmd, rp ResolvedProfile)` (no error — env/args application never fails) and `profiles.ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error` (the former unexported `applyWrapper`, now exported so `cmd/wt` can call it directly). `ApplyToCmd` becomes a thin composition of the two plus the `rp.Empty()` short-circuit, unchanged from a caller's perspective.

**Bug:** codex is the only agent declaring `env`, `args`, AND `config_file` together (`wt/internal/agents/codex.go:75-77`), so `Validate` legitimately accepts a codex profile combining `args` and `config_content`. `applyResolvedProfile` currently calls `profiles.ApplyConfigContent` FIRST — which, for codex, appends `--profile agent-wt-profile` directly to `cmd.Args` — and only then calls `profiles.ApplyToCmd`, which appends `rp.ExtraArgs` after that. The result: a profile's own declared `args` land AFTER `--profile agent-wt-profile` instead of before it, an ordering the code's own comments (`applyResolvedProfile`'s doc comment) admit was designed only for the wrapper-splice case, not this one. Splitting `ApplyToCmd` lets `applyResolvedProfile` apply env/args first, then config_content's own args, then the wrapper last (still capturing everything appended before it, preserving the one interaction that IS designed/tested).

- [ ] **Step 1: Write the failing test**

Add to `wt/cmd/wt/launch_test.go` (after `TestApplyResolvedProfileRestoresConfigContentWhenWrapperMissing`):

```go
// TestApplyResolvedProfileExtraArgsPrecedeCodexProfileFlag is the
// regression lock for the code-review finding that a codex profile
// combining the args and config_file mechanisms (both declared by codex's
// ProfileMechanisms, and therefore Validate-legal) placed the profile's own
// ExtraArgs AFTER the config_content-driven "--profile agent-wt-profile"
// flag instead of before it. This hand-builds a ResolvedProfile with both
// (the only way to exercise it — no Phase-1 example profile combines them)
// and asserts the ExtraArgs appear before "--profile" in cmd.Args.
func TestApplyResolvedProfileExtraArgsPrecedeCodexProfileFlag(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("HOME", t.TempDir())

	rp := profiles.ResolvedProfile{
		ExtraArgs:     []string{"-c", "model_reasoning_effort=\"low\""},
		ConfigContent: map[string]any{"some_key": "some_value"},
	}
	cmd := exec.Command("codex", "--model", "x")

	cleanup, err := applyResolvedProfile(cmd, "codex", rp)
	if err != nil {
		t.Fatalf("applyResolvedProfile() error = %v", err)
	}
	t.Cleanup(func() { _ = cleanup() })

	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\"", "--profile", "agent-wt-profile"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (ExtraArgs before --profile)", cmd.Args, want)
	}
}
```

Add to `wt/internal/profiles/apply_test.go` (after `TestApplyToCmdEmptyIsNoop`):

```go
// TestApplyEnvAndArgsNeverAppliesWrapper verifies the split-out
// ApplyEnvAndArgs applies Env/ExtraArgs but leaves Wrapper untouched — a
// caller (cmd/wt's applyResolvedProfile) that wants to interleave a
// config_content mutation between args and wrapper depends on this.
func TestApplyEnvAndArgsNeverAppliesWrapper(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	rp := ResolvedProfile{
		ExtraArgs: []string{"--extra"},
		Wrapper:   &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz"},
	}
	ApplyEnvAndArgs(cmd, rp)
	want := []string{"pi", "--model", "x", "--extra"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (wrapper not applied)", cmd.Args, want)
	}
}

// TestApplyWrapperExported verifies the exported ApplyWrapper (formerly
// unexported applyWrapper) behaves identically — cmd/wt calls it directly
// so it can apply config_content between args and wrapper.
func TestApplyWrapperExported(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	w := &WrapperSpec{Binary: "true", ArgsTemplate: []string{"--wrapped", "{{args}}"}}
	if err := ApplyWrapper(cmd, w); err != nil {
		t.Fatalf("ApplyWrapper() error = %v", err)
	}
	want := []string{"--wrapped", "--model", "x"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/ ./cmd/wt/ -run 'TestApplyResolvedProfileExtraArgsPrecedeCodexProfileFlag|TestApplyEnvAndArgsNeverAppliesWrapper|TestApplyWrapperExported' -v`
Expected: FAIL to compile (`ApplyEnvAndArgs`/`ApplyWrapper` don't exist yet) — this is expected; proceed to implementation.

- [ ] **Step 3: Split `ApplyToCmd` in `apply.go`**

In `wt/internal/profiles/apply.go`, replace the whole file body (after the package/import block) with:

```go
// ApplyToCmd mutates cmd in place per rp: Env is appended (so it wins on
// a duplicate key, matching exec.Cmd.Env's documented last-value-wins
// behavior), ExtraArgs are appended after everything cmd.Args already
// carries, and a Wrapper — applied last — replaces cmd.Path/Args[0] with
// the wrapper binary, splicing the command's existing argv (built so far,
// including any ExtraArgs just appended) into the "{{args}}" slot in
// ArgsTemplate. A no-op ResolvedProfile changes nothing.
//
// It is the composition of ApplyEnvAndArgs then ApplyWrapper, exported
// separately so a caller combining config_content with args/wrapper (only
// codex declares all three mechanisms together) can interleave a
// config_content-driven cmd.Args mutation between them: env/args first, so
// a codex profile's own declared args land before config_content's
// "--profile agent-wt-profile" flag instead of after it, then wrapper
// last, so its {{args}} splice still captures everything appended before
// it (the one interaction this package's own tests pin).
func ApplyToCmd(cmd *exec.Cmd, rp ResolvedProfile) error {
	if rp.Empty() {
		return nil
	}
	ApplyEnvAndArgs(cmd, rp)
	if rp.Wrapper != nil {
		return ApplyWrapper(cmd, rp.Wrapper)
	}
	return nil
}

// ApplyEnvAndArgs appends rp.Env (last value wins on a duplicate key,
// matching exec.Cmd.Env's documented behavior) and rp.ExtraArgs (appended
// after everything cmd.Args already carries) to cmd. It never touches
// Wrapper — see ApplyToCmd and ApplyWrapper.
func ApplyEnvAndArgs(cmd *exec.Cmd, rp ResolvedProfile) {
	for k, v := range rp.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if len(rp.ExtraArgs) > 0 {
		cmd.Args = append(cmd.Args, rp.ExtraArgs...)
	}
}

// ApplyWrapper replaces cmd.Path/Args[0] with w.Binary, splicing the
// command's existing argv (everything after the old argv[0], built so far)
// into the "{{args}}" slot in w.ArgsTemplate.
func ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
	binPath, err := exec.LookPath(w.Binary)
	if err != nil {
		return fmt.Errorf("profile wrapper %q not installed: %w", w.Binary, err)
	}
	original := cmd.Args[1:] // drop the old argv[0] (the pre-wrap binary name)
	var newArgs []string
	for _, tok := range w.ArgsTemplate {
		if tok == "{{args}}" {
			newArgs = append(newArgs, original...)
			continue
		}
		newArgs = append(newArgs, tok)
	}
	cmd.Path = binPath
	cmd.Args = append([]string{binPath}, newArgs...)
	return nil
}
```

- [ ] **Step 4: Reorder `applyResolvedProfile` in `cmd/wt/launch.go`**

Replace:

```go
// applyResolvedProfile applies an already-confirmed ResolvedProfile to
// cmd: config_content first, then ApplyToCmd (env/args/wrapper) — so a
// codex profile's "--profile agent-wt-profile" arg it appends is captured
// by a later wrapper's {{args}} splice (no Phase-1 profile combines the
// two, but this keeps the ordering correct if one ever does). Extracted
// from applyProfileForLaunch so this config_content/wrapper interaction —
// specifically that a wrapper failure must still trigger the
// already-obtained content cleanup — is directly testable with a
// hand-built ResolvedProfile, without needing a profiles.toml entry that
// passes Validate (no Phase-1 agent declares both config_file and wrapper
// mechanisms together).
func applyResolvedProfile(cmd *exec.Cmd, agent string, rp profiles.ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	contentCleanup, err := profiles.ApplyConfigContent(cmd, agent, cmd.Dir, rp)
	if err != nil {
		// Per the design's global constraint, a profile resolution/
		// application error must degrade to a normal, unprofiled launch —
		// never block the agent from starting — so a file-write error here
		// (permissions, a resolve-home-dir failure for codex, …) is a
		// warning, not a launch-aborting error.
		fmt.Fprintf(os.Stderr, "wt: profile config_content: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	if err := profiles.ApplyToCmd(cmd, rp); err != nil {
		// ApplyToCmd's one error case (a wrapper mechanism naming a
		// missing binary) is the sole exception the design calls out as
		// fatal — but any config file ApplyConfigContent already wrote or
		// backed up above must still be restored before we return, or a
		// combined config_content+wrapper profile would leak the
		// rewritten file on disk.
		if cerr := contentCleanup(); cerr != nil {
			fmt.Fprintf(os.Stderr, "wt: profile cleanup after wrapper error: %v\n", cerr)
		}
		return noop, err
	}
	return contentCleanup, nil
}
```

with:

```go
// applyResolvedProfile applies an already-confirmed ResolvedProfile to
// cmd, in three steps: env/args first, then config_content, then the
// wrapper last. This ordering means a codex profile's own declared args
// (the "args" mechanism) land BEFORE config_content's own
// "--profile agent-wt-profile" flag rather than after it, and the wrapper
// — applied last — still captures everything appended before it in its
// {{args}} splice (the one interaction this package's tests pin; no
// Phase-1 profile combines config_file with wrapper, but this keeps the
// ordering correct if one ever does). Extracted from applyProfileForLaunch
// so this config_content/wrapper interaction — specifically that a
// wrapper failure must still trigger the already-obtained content cleanup
// — is directly testable with a hand-built ResolvedProfile, without
// needing a profiles.toml entry that passes Validate.
func applyResolvedProfile(cmd *exec.Cmd, agent string, rp profiles.ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	profiles.ApplyEnvAndArgs(cmd, rp)
	contentCleanup, err := profiles.ApplyConfigContent(cmd, agent, cmd.Dir, rp)
	if err != nil {
		// Per the design's global constraint, a profile resolution/
		// application error must degrade to a normal, unprofiled launch —
		// never block the agent from starting — so a file-write error here
		// (permissions, a resolve-home-dir failure for codex, …) is a
		// warning, not a launch-aborting error.
		fmt.Fprintf(os.Stderr, "wt: profile config_content: %v (profiles disabled for this launch)\n", err)
		return noop, nil
	}
	if rp.Wrapper != nil {
		if err := profiles.ApplyWrapper(cmd, rp.Wrapper); err != nil {
			// ApplyWrapper's one error case (a wrapper mechanism naming a
			// missing binary) is the sole exception the design calls out
			// as fatal — but any config file ApplyConfigContent already
			// wrote or backed up above must still be restored before we
			// return, or a combined config_content+wrapper profile would
			// leak the rewritten file on disk.
			if cerr := contentCleanup(); cerr != nil {
				fmt.Fprintf(os.Stderr, "wt: profile cleanup after wrapper error: %v\n", cerr)
			}
			return noop, err
		}
	}
	return contentCleanup, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... ./cmd/wt/... -v`
Expected: PASS (all tests in both packages, including `TestApplyResolvedProfileRestoresConfigContentWhenWrapperMissing`, which exercises the wrapper-error cleanup path this reorder must not break)

- [ ] **Step 6: Commit**

```bash
cd wt
git add internal/profiles/apply.go internal/profiles/apply_test.go cmd/wt/launch.go cmd/wt/launch_test.go
git commit -m "fix(profiles): apply a codex profile's ExtraArgs before its config_content --profile flag

completes plan item #7"
```

---

### Task 8: Wire local-model launch profiles into the TUI launch path

**Files:**
- Modify: `wt/internal/tui/launch.go` (add `ProfileApplier` type + seam, update `runAndWaitCmd`)
- Modify: `wt/internal/tui/app.go:1250` (`Run` signature)
- Modify: `wt/cmd/wt/main.go` (build the real closure, pass to both `tuiRun` calls)
- Test: `wt/internal/tui/launch_test.go`, `wt/cmd/wt/main_test.go`

**Interfaces:**
- Consumes: `cmd/wt`'s existing `applyProfileForLaunch(cmd *exec.Cmd, agent string, m config.Model, cfg *config.Config, pp *precomputedProfiles) (func() error, error)` and `a.profileState()` (both already exist, unchanged by this task except that Task 7 changed `applyResolvedProfile`'s internals, not this signature).
- Produces: `tui.ProfileApplier` — `type ProfileApplier func(cmd *exec.Cmd, agent string, m config.Model) (cleanup func() error, err error)`. `tui.Run`'s signature gains a trailing `applyProfile ProfileApplier` parameter.

**Bug (finding #1, the most severe):** `internal/tui`'s `runAndWaitCmd` (the interactive picker's launch path — the majority of real `wt` usage) never calls anything resembling `applyProfileForLaunch`/`ApplyConfigContent`/`ApplyToCmd`. `cmd/wt/main.go`'s two `tuiRun(...)` call sites pass `a.cfg` but never `a.profileState()`. The entire profiles feature is inert for anyone launching through the picker instead of `-A`/`-M` flags.

**Design constraint:** `internal/tui` cannot import `cmd/wt` (package `main` — not importable, and it would be a cycle regardless since `cmd/wt` already imports `internal/tui`), and `applyProfileForLaunch`'s "not precomputed" fallback branch needs `agentProfileMechanisms` (`cmd/wt/app.go`, which itself needs `internal/agents` — a package `internal/profiles` deliberately never imports, per `internal/profiles/profiles.go`'s own comment). Rather than relocate that logic (which ~15 existing `cmd/wt` tests exercise directly against unexported functions), this task adds a function-type seam: `internal/tui` defines `ProfileApplier` and a package-level `profileApplier` var (the same pattern `currentProgram`/`launchAgent`/`buildPassthrough` in this same file already use); `cmd/wt/main.go` builds the real implementation as a closure over its own `a.cfg`/`a.profileState()` and passes it into `tui.Run`.

- [ ] **Step 1: Write the failing tests in `internal/tui`**

Add to `wt/internal/tui/launch_test.go` (after the last existing test in the file — check the end of the file first with `tail -40 wt/internal/tui/launch_test.go` to place these after it, inside the same `package tui`):

```go
// TestRunAndWaitCmdAppliesProfileApplier is the regression lock for the
// code-review finding that runAndWaitCmd (the interactive picker's launch
// path) never applied local-model launch profiles at all. It stubs
// profileApplier to record the (cmd, agent, m) it was called with and
// returns a cleanup that records whether it ran; both must fire around
// cmd.Run(), exactly mirroring the non-TUI path's runAgentCmd.
func TestRunAndWaitCmdAppliesProfileApplier(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })

	var gotAgent string
	var gotModel config.Model
	cleanupCalled := false
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		gotAgent, gotModel = agent, m
		cmd.Env = append(cmd.Env, "WT_TEST_PROFILE_APPLIED=1")
		return func() error { cleanupCalled = true; return nil }, nil
	}

	cmd := exec.Command("true")
	m := config.Model{ID: "ollama/x", ModelName: "x"}
	msg := runAndWaitCmd(cmd, "claude", m)()

	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
	if gotAgent != "claude" || gotModel.ID != "ollama/x" {
		t.Errorf("profileApplier called with (%q, %+v), want (claude, {ID: ollama/x})", gotAgent, gotModel)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "WT_TEST_PROFILE_APPLIED=1" {
			found = true
		}
	}
	if !found {
		t.Error("profileApplier's cmd.Env mutation did not survive into cmd.Run()")
	}
	if !cleanupCalled {
		t.Error("profileApplier's cleanup was not called after cmd.Run()")
	}
}

// TestRunAndWaitCmdProfileApplierErrorSkipsRun is the regression lock for
// the TUI-side equivalent of the non-TUI runAgentCmd fix (plan item #5): a
// pre-launch profileApplier error must skip cmd.Run() entirely, still
// release the refcount entry already recorded by launchAndRecord before
// runAndWaitCmd was returned, and still populate pendingSummary (duration
// 0) — but must NOT populate pendingSurveyState, since nothing ran and
// there is no session to survey or offer a stop picker for.
func TestRunAndWaitCmdProfileApplierErrorSkipsRun(t *testing.T) {
	oldApplier := profileApplier
	t.Cleanup(func() { profileApplier = oldApplier })
	oldRelease := releaseSession
	t.Cleanup(func() { releaseSession = oldRelease })
	pendingSummary, pendingSurveyState = "", pendingSurvey{}
	t.Cleanup(func() { pendingSummary, pendingSurveyState = "", pendingSurvey{} })

	released := false
	releaseSession = func() { released = true }
	profileApplier = func(cmd *exec.Cmd, agent string, m config.Model) (func() error, error) {
		return nil, errors.New("wrapper not installed")
	}

	sentinelDir := t.TempDir()
	sentinel := filepath.Join(sentinelDir, "should-not-exist")
	cmd := exec.Command("touch", sentinel)
	m := config.Model{ID: "ollama/x", ModelName: "x"}

	msg := runAndWaitCmd(cmd, "pi", m)()

	done, ok := msg.(launchDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a launchDoneMsg carrying the profileApplier error", msg)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Error("sentinel file exists — cmd.Run() executed despite the profileApplier error")
	}
	if !released {
		t.Error("releaseSession() was not called on a profileApplier error")
	}
	if pendingSummary == "" {
		t.Error("pendingSummary was not set on a profileApplier error")
	}
	if pendingSurveyState.agent != "" {
		t.Error("pendingSurveyState was populated despite the profileApplier error — nothing ran, there is no session to survey")
	}
}

// TestRunAndWaitCmdNilProfileApplierIsNoop verifies a nil profileApplier
// (its zero value — the state every other test in this file runs under,
// since only Run() ever sets it) leaves runAndWaitCmd's behavior exactly
// as it was before this task, so the profile layer is opt-in via Run's new
// parameter and never a behavior change for a caller that doesn't wire it.
func TestRunAndWaitCmdNilProfileApplierIsNoop(t *testing.T) {
	oldApplier := profileApplier
	profileApplier = nil
	t.Cleanup(func() { profileApplier = oldApplier })

	cmd := exec.Command("true")
	msg := runAndWaitCmd(cmd, "shell", config.Model{})()
	if done, ok := msg.(launchDoneMsg); !ok || done.err != nil {
		t.Fatalf("runAndWaitCmd() = %#v, want a successful launchDoneMsg", msg)
	}
}
```

Check the top of `wt/internal/tui/launch_test.go` for its existing import block; add `"errors"` and `"path/filepath"` if not already present (the file already imports `"os/exec"` and `"github.com/ohanaverse/local-ai-setup/wt/internal/config"`, used by the existing tests read earlier in this session).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run 'TestRunAndWaitCmd' -v`
Expected: FAIL to compile (`profileApplier` doesn't exist yet)

- [ ] **Step 3: Add the `ProfileApplier` type/seam and update `runAndWaitCmd`**

In `wt/internal/tui/launch.go`, replace:

```go
// pendingSurveyState is populated by runAndWaitCmd next to pendingSummary.
// Run() invokes the survey through it after printing the summary. Reset by
// Run() before launch.
var pendingSurveyState pendingSurvey
```

with:

```go
// pendingSurveyState is populated by runAndWaitCmd next to pendingSummary.
// Run() invokes the survey through it after printing the summary. Reset by
// Run() before launch.
var pendingSurveyState pendingSurvey

// ProfileApplier applies a local-model launch profile (env/args/
// config_content/wrapper) to cmd before it runs, returning a cleanup func
// the caller MUST invoke after cmd.Run() returns (success or failure) to
// restore anything the profile's config_content mechanism rewrote. It
// mirrors cmd/wt's applyProfileForLaunch signature, minus cfg/pp: this
// package cannot import cmd/wt (package main — not importable, and it
// would be a cycle regardless, since cmd/wt already imports internal/tui)
// or internal/profiles directly (mechanism validation needs an
// agent-driver lookup that lives with cmd/wt's own app state). Run's
// caller builds the real implementation as a closure over its own
// cfg/precomputedProfiles.
type ProfileApplier func(cmd *exec.Cmd, agent string, m config.Model) (cleanup func() error, err error)

// profileApplier is a seam: production is set by Run() from its
// ProfileApplier parameter before p.Run() starts. nil — the zero value
// every test in this file that calls runAndWaitCmd directly (without going
// through Run()) runs under — means "no profile layer available", and
// runAndWaitCmd treats that as a no-op, matching the non-TUI path's own
// graceful-degradation posture for a missing/disabled profiles.toml.
var profileApplier ProfileApplier
```

Then replace `runAndWaitCmd`:

```go
func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
			// defer the restore so a panic in cmd.Run() still returns
			// the terminal to the alt-screen state; otherwise the user's
			// shell is left in raw mode with no TUI frame.
			defer func() {
				if currentProgram != nil {
					_ = currentProgram.RestoreTerminal()
				}
			}()
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		err := cmd.Run()
		// Capture (do not print) the summary line. Printing here would
		// land inside the alt-screen buffer, which bubbletea discards at
		// tea.Quit shutdown. Run() reads pendingSummary after p.Run()
		// returns and prints it to the parent terminal — the only point
		// in the TUI lifecycle where stdout reaches the user's terminal.
		pendingSummary = agents.Summary(agent, m, time.Since(start))
		pendingSurveyState = pendingSurvey{agent: agent, m: m}
		return launchDoneMsg{err: err}
	}
}
```

with:

```go
func runAndWaitCmd(cmd *exec.Cmd, agent string, m config.Model) tea.Cmd {
	return func() tea.Msg {
		if currentProgram != nil {
			currentProgram.ReleaseTerminal()
			// defer the restore so a panic in cmd.Run() still returns
			// the terminal to the alt-screen state; otherwise the user's
			// shell is left in raw mode with no TUI frame.
			defer func() {
				if currentProgram != nil {
					_ = currentProgram.RestoreTerminal()
				}
			}()
		}
		var profileCleanup func() error
		if profileApplier != nil {
			var perr error
			profileCleanup, perr = profileApplier(cmd, agent, m)
			if perr != nil {
				// Mirrors cmd/wt's runAgentCmd: a pre-launch profile
				// application error (e.g. a wrapper profile naming a
				// missing binary) must still release the refcount entry
				// launchAndRecord already recorded before this command
				// was returned, and still populate pendingSummary
				// (duration 0) so Run() prints it exactly as on a real
				// exit — but there is no session to survey or a stop
				// picker to offer, since nothing ever ran, so
				// pendingSurveyState is deliberately left at its zero
				// value.
				releaseSession()
				pendingSummary = agents.Summary(agent, m, 0)
				return launchDoneMsg{err: perr}
			}
		}
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		err := cmd.Run()
		if profileCleanup != nil {
			if cerr := profileCleanup(); cerr != nil {
				fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
			}
		}
		// Capture (do not print) the summary line. Printing here would
		// land inside the alt-screen buffer, which bubbletea discards at
		// tea.Quit shutdown. Run() reads pendingSummary after p.Run()
		// returns and prints it to the parent terminal — the only point
		// in the TUI lifecycle where stdout reaches the user's terminal.
		pendingSummary = agents.Summary(agent, m, time.Since(start))
		pendingSurveyState = pendingSurvey{agent: agent, m: m}
		return launchDoneMsg{err: err}
	}
}
```

- [ ] **Step 4: Update `Run`'s signature in `app.go`**

In `wt/internal/tui/app.go`, replace:

```go
func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
	p := tea.NewProgram(newRunModel(yolo, allowReplace, agent, pinned, tags, family, extraArgs, theme, prePath, cfg), tea.WithAltScreen())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{}
	finalModel, err := p.Run()
```

with:

```go
func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile ProfileApplier) error {
	p := tea.NewProgram(newRunModel(yolo, allowReplace, agent, pinned, tags, family, extraArgs, theme, prePath, cfg), tea.WithAltScreen())
	currentProgram = p
	// Reset any summary/survey state captured by a previous run (e.g.
	// from a test invocation sharing the process).
	pendingSummary = ""
	pendingSurveyState = pendingSurvey{}
	profileApplier = applyProfile
	finalModel, err := p.Run()
```

Also update the doc comment above `Run` (just before `func Run(...)`) to add one sentence: after the existing sentence ending "...it is not re-loaded here.", append:

```
// applyProfile, when non-nil, applies a local-model launch profile before
// the agent's cmd.Run() and is called from runAndWaitCmd for every launch
// this session makes; cmd/wt's main.go builds it as a closure over the
// already-loaded profiles.toml state (mirrors the non-TUI path's
// applyProfileForLaunch).
```

- [ ] **Step 5: Wire the real closure in `cmd/wt/main.go`**

Add `"os/exec"` to `wt/cmd/wt/main.go`'s import block (it currently has no `os/exec` import — check with `grep -n '"os/exec"' cmd/wt/main.go` first; add it alphabetically among the standard-library imports).

In `wt/cmd/wt/main.go`, inside `runLaunchPath` (the function starting at line 117), replace:

```go
	sweepRefcounts()

	// Install the guard once when inside any git repo.
	if root != "" {
		maybeInstallGuard()
	}

	// launchPath == "" means the worktree picker should be shown; the launch
	// directory is not known until the user picks inside the TUI, so never
	// short-circuit to launchFiltered here — a pinned agent or command must
	// still pick a worktree.
	if launchPath == "" {
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
	}
```

with:

```go
	sweepRefcounts()

	// Install the guard once when inside any git repo.
	if root != "" {
		maybeInstallGuard()
	}

	// applyProfile closes over a's already-loaded profiles.toml state
	// (newApp() loaded and validated it once) so the TUI launch path
	// applies local-model launch profiles exactly like the non-TUI path
	// does, via the same applyProfileForLaunch — internal/tui cannot call
	// it directly (import-cycle/package-boundary reasons documented on
	// tui.ProfileApplier), so it is handed in as a closure instead.
	applyProfile := func(cmd *exec.Cmd, agentName string, m config.Model) (func() error, error) {
		return applyProfileForLaunch(cmd, agentName, m, a.cfg, a.profileState())
	}

	// launchPath == "" means the worktree picker should be shown; the launch
	// directory is not known until the user picks inside the TUI, so never
	// short-circuit to launchFiltered here — a pinned agent or command must
	// still pick a worktree.
	if launchPath == "" {
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg, applyProfile)
	}
```

Then further down in the same function, replace:

```go
		if !stdinTTY() {
			return pickerNeedsTTYError(agent)
		}
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
	}
```

with:

```go
		if !stdinTTY() {
			return pickerNeedsTTYError(agent)
		}
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg, applyProfile)
	}
```

- [ ] **Step 6: Update the 8 `tuiRun` stubs in `cmd/wt/main_test.go`**

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/tui"` to `wt/cmd/wt/main_test.go`'s import block.

Every `tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {` in that file becomes `tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, _ tui.ProfileApplier) error {` (append `, _ tui.ProfileApplier` before the closing `)`). There are 7 occurrences of this exact signature (verify the count first: `grep -c 'tuiRun = func(yolo, allowReplace bool' cmd/wt/main_test.go`).

The one occurrence using bare positional types, `tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config) error {`, becomes `tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config, tui.ProfileApplier) error {`.

- [ ] **Step 7: Add the wiring test in `cmd/wt/main_test.go`**

Add after the last existing test in the file (check `tail -30 cmd/wt/main_test.go` first to confirm placement inside `package main`):

```go
// TestRunLaunchPathPassesWorkingProfileApplierToTUI is the regression lock
// for the code-review finding that the TUI launch path never applied
// local-model launch profiles at all (finding #1, the most severe of the
// review). It captures the tui.ProfileApplier closure runLaunchPath hands
// to tuiRun and invokes it directly against a hand-written profiles.toml,
// verifying it actually resolves and applies a profile exactly like the
// non-TUI path's applyProfileForLaunch does — not a nil or no-op closure.
func TestRunLaunchPathPassesWorkingProfileApplierToTUI(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_TEST_TUI_PROFILE_APPLIED = "1" }
`
	if err := os.WriteFile(filepath.Join(dir, "agent-wt", "profiles.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	oldConfirm := confirmProfile
	confirmProfile = func(profiles.ResolvedProfile) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmProfile = oldConfirm })

	var captured tui.ProfileApplier
	oldTuiRun := tuiRun
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config, applyProfile tui.ProfileApplier) error {
		captured = applyProfile
		return nil
	}
	t.Cleanup(func() { tuiRun = oldTuiRun })

	a := &app{cfg: &config.Config{Providers: []config.Provider{{ID: "ollama", Location: config.LocationLocal}}}}
	var loadErr error
	a.profiles, loadErr = profiles.Load()
	if loadErr != nil {
		t.Fatalf("profiles.Load() error = %v", loadErr)
	}
	a.profilesValidateErr = profiles.Validate(a.profiles, agentProfileMechanisms)

	if err := runLaunchPath(&cobra.Command{}, a, "", "", "", "", nil, "", ""); err != nil {
		t.Fatalf("runLaunchPath() error = %v", err)
	}
	if captured == nil {
		t.Fatal("tuiRun received a nil ProfileApplier — the TUI launch path never wires profile application")
	}

	cmd := exec.Command("true")
	m := config.Model{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}
	if _, err := captured(cmd, "claude", m); err != nil {
		t.Fatalf("captured ProfileApplier() error = %v", err)
	}
	found := false
	for _, e := range cmd.Env {
		if e == "WT_TEST_TUI_PROFILE_APPLIED=1" {
			found = true
		}
	}
	if !found {
		t.Errorf("cmd.Env = %v, want WT_TEST_TUI_PROFILE_APPLIED=1 (the captured applier must actually apply the profile)", cmd.Env)
	}
}
```

Note: `profiles` and `cobra` must already be imported in `main_test.go` — verify with `grep -n '"github.com/ohanaverse/local-ai-setup/wt/internal/profiles"\|spf13/cobra' cmd/wt/main_test.go`; add either import if missing.

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd wt && go build ./... && go test ./internal/tui/... ./cmd/wt/... -v`
Expected: PASS (the whole build compiles, and every test in both packages passes, including all new ones from this task)

- [ ] **Step 9: Commit**

```bash
cd wt
git add internal/tui/launch.go internal/tui/app.go internal/tui/launch_test.go cmd/wt/main.go cmd/wt/main_test.go
git commit -m "fix(wt): apply local-model launch profiles on the TUI launch path

The interactive picker (the majority of real wt usage) never called
applyProfileForLaunch — profiles.toml had zero effect on any launch that
didn't go through -A/-M flags. internal/tui now takes a ProfileApplier
closure from Run's caller (cmd/wt/main.go), mirroring the non-TUI path's
runAgentCmd behavior including its pre-launch-error handling.

completes plan item #8"
```

---

### Task 9: Whole-branch verification

**Files:** none (verification only).

**Interfaces:** none.

- [ ] **Step 1: Full test suite**

Run: `cd wt && go build ./... && go vet ./... && go test ./... -count=1`
Expected: build succeeds, vet is clean, all tests pass (0 failures) across every package touched by Tasks 1–8 plus every package that wasn't (regression check).

- [ ] **Step 2: Format check**

Run: `cd wt && gofmt -l .`
Expected: no output (every file already gofmt-clean)

- [ ] **Step 3: Repo-wide check target**

Run: `cd wt && make check` (requires `make install` first per `wt/CLAUDE.md` — run `make install` if `make check` reports the binary missing)
Expected: shellcheck/shfmt/go-format-check all pass (this branch touches no shell scripts, so this is a regression check, not expected to find anything new)

- [ ] **Step 4: Manual smoke check of the TUI wiring (Task 8) against a real profiles.toml**

This is the one behavior change with no automated end-to-end coverage (a real Bubble Tea TUI needs a TTY). Build the branch and manually verify:

```bash
cd wt && go build -o /tmp/wt-verify ./cmd/wt
mkdir -p /tmp/wt-profile-check/.config/agent-wt
cat > /tmp/wt-profile-check/.config/agent-wt/profiles.toml <<'EOF'
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { WT_MANUAL_CHECK = "1" }
EOF
XDG_CONFIG_HOME=/tmp/wt-profile-check/.config /tmp/wt-verify --cwd -A claude -M <some-local-ollama-model-id>
```

Expected: before pressing Enter to actually run claude, the confirm prompt (`wt: local-model profile available for this launch (location=local) — apply? [Y/n]`) should appear on the parent terminal once the TUI hands off to the agent process. Decline it (`n`) to avoid actually launching claude. This confirms the wiring reaches a real, non-test invocation of `wt` and is not just passing its own tests.

- [ ] **Step 5: Report**

No commit for this task (verification only) — report the results of Steps 1–4 to the user.

---

## Execution Order

Tasks 1–5 are fully independent of each other and of Task 6–8; do them in any order, but numeric order is recommended since it goes smallest/lowest-risk first. Task 6 (deep merge) is independent of 1–5 and 7–8. Task 7 (codex arg order) touches `apply.go`/`launch.go` that Task 8 also touches (`applyResolvedProfile`'s call into `profiles.ApplyToCmd`/`ApplyConfigContent` is inside the same `applyProfileForLaunch` the Task 8 closure calls) — do Task 7 before Task 8 to avoid rebasing Task 8's closure test data against a mid-flight `apply.go` split. Task 9 is last, always.
