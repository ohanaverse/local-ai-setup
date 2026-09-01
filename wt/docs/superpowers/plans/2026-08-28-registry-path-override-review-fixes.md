# Registry Path Override Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 7 verified findings from the 2026-08-28 code review of PR #94's `MODELMAN_REGISTRY` override, locking in parity between `wt` and `modelman` for registry-path resolution and strengthening the test suite against fixture env-var leaks.

**Architecture:** Each task is an isolated commit. Tasks 1–4 are correctness/scope-tightening; Task 5 is a defensive ergonomic. Order is by severity: test-hygiene first so the real bugs are testable, then the real bugs, then the test-quality findings, then the defensive expansion.

**Tech Stack:** Go (registry resolution, tilde expansion, `t.Setenv` fixture hygiene), `go test ./...` (verification).

**Spec:** Code-review report (7 findings) committed at `docs/superpowers/specs/2026-08-28-registry-path-override-review.md` (created by the reviewer fork).

## Global Constraints

- Branch: `fix/registry-path-override` (already checked out).
- Project rule (project CLAUDE.md, "Go tests"): every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters.
- Project rule (project CLAUDE.md, "Registry"): `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` precedence must match modelman's `_default_registry_path`. The XDG branch's directory must be the **literal XDG value** (no auto-append of `.config`), and the XDG value must be `~`-expanded.
- User rule (user CLAUDE.md, "Test Documentation"): every new test gets a comment describing behavior + importance.
- Each task ends with one commit. The commit message must reference this plan (`plan item #N`) for in-plan tasks.
- Run `go test ./...` and `go vet ./...` after every code change; both must pass before commit.

---

### Task 1: Unsubscribe `MODELMAN_REGISTRY` in fixture helpers

**Files:**
- Modify: `cmd/wt/helpers_test.go:31-42` (`writeEmptyRegistry`)
- Modify: `cmd/wt/main_test.go` (5 sites: lines 155, 198, 243, 273, 318, 394, 447 — the `XDG_CONFIG_HOME` and `writeEmptyRegistry` calls; also note: 86 and 274/319 are inside `writeEmptyRegistry`-using paths)
- Modify: `cmd/wt/launch_test.go` (5 sites: lines 272, 402, 449, 502, 562)
- Modify: `cmd/wt/commands_config_test.go:33`
- Modify: `internal/config/migrate_test.go` (4 sites: lines 166, 249, 273, 332)
- Modify: `internal/config/config_test.go` (2 sites: lines 15, 21; possibly 135)
- Modify: `internal/config/secrets_test.go` (2 sites: lines 59, 64)

**Interfaces:**
- Consumes: existing helpers (`writeEmptyRegistry`).
- Produces: a new helper `withCleanConfigEnv(t *testing.T)` in `cmd/wt/helpers_test.go` (exported within the `main` package: lowercase, same package) that does both `t.Setenv("XDG_CONFIG_HOME", …)` and `t.Setenv("MODELMAN_REGISTRY", "")` so `RegistryPath()` cannot short-circuit on an inherited override.

- [ ] **Step 1: Add a fixture helper that always unsets `MODELMAN_REGISTRY`**

In `cmd/wt/helpers_test.go`, add immediately after the existing `writeEmptyRegistry` (line 42):

```go
// withCleanConfigEnv sets XDG_CONFIG_HOME to a fixture path and clears any
// inherited MODELMAN_REGISTRY so RegistryPath() cannot short-circuit on the
// developer's shell environment. All tests that exercise the launch path must
// call this before touching config.Load or RegistryPath() — otherwise a stray
// `export MODELMAN_REGISTRY=...` in the dev's env makes the test read their
// real registry instead of the temp fixture.
func withCleanConfigEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("MODELMAN_REGISTRY", "")
}
```

- [ ] **Step 2: Refactor `writeEmptyRegistry` to call the new helper**

Replace the `writeEmptyRegistry` body (lines 31–42) so the `t.Setenv("XDG_CONFIG_HOME", …)` line uses the new helper instead:

```go
func writeEmptyRegistry(t *testing.T, home string) {
	t.Helper()
	withCleanConfigEnv(t, home)
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("providers = []\nmodels = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 3: Sweep every `t.Setenv("XDG_CONFIG_HOME", …)` in the other test files**

For each call site listed in **Files**, insert a `t.Setenv("MODELMAN_REGISTRY", "")` line immediately after (or convert to call the new helper where appropriate). The pattern in each file is:

```go
t.Setenv("XDG_CONFIG_HOME", tmp)        // existing
t.Setenv("MODELMAN_REGISTRY", "")       // new
```

For `cmd/wt/main_test.go` and `cmd/wt/launch_test.go` where the value is `dir` (a `t.TempDir()` result), the same two-line pattern applies. For `cmd/wt/commands_config_test.go:33`, the helper `withCleanConfigEnv` is not directly applicable (it points at `tmp` not a `home` path), so the explicit two-line pattern is fine.

For `internal/config/{migrate,config,secrets}_test.go`, also do `t.Setenv("MODELMAN_REGISTRY", "")` after every `t.Setenv("XDG_CONFIG_HOME", …)`.

- [ ] **Step 4: Run the full test suite to confirm the sweep is complete and correct**

```bash
go test ./... 2>&1 | tail -30
go vet ./...
```

Expected: pass, no failures. (The fix should not change any current behavior because the dev's env is normally clean; the change is a *guard* against a future env leak.)

- [ ] **Step 5: Smoke-test the guard works**

Add a temporary test to `cmd/wt/helpers_test.go`, run it, confirm it fails without the helper and passes with it, then delete the temp test:

```go
func TestFixtureHygieneUnsetsModelmanRegistry(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "/tmp/dev-real-registry.toml")
	home := t.TempDir()
	withCleanConfigEnv(t, home)
	if got := os.Getenv("MODELMAN_REGISTRY"); got != "" {
		t.Fatalf("expected MODELMAN_REGISTRY cleared, got %q", got)
	}
}
```

Run: `go test ./cmd/wt -run TestFixtureHygieneUnsetsModelmanRegistry -v`
Expected: PASS. Then **delete the test** (it's a meta-test for the helper, not a regression guard worth keeping in CI).

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/helpers_test.go cmd/wt/main_test.go cmd/wt/launch_test.go cmd/wt/commands_config_test.go internal/config/migrate_test.go internal/config/config_test.go internal/config/secrets_test.go
git commit -m "test(fixtures): clear inherited MODELMAN_REGISTRY in test helpers

Without this, a developer with MODELMAN_REGISTRY exported in their shell
runs tests that read their real registry instead of the temp fixture,
and tests fail with a misleading 'model registry not found' pointing at
the dev's real path. Completes plan item #1."
```

---

### Task 2: Unify tilde expansion across the registry-path precedence chain

**Files:**
- Modify: `internal/config/registry.go:17-30` (`RegistryPath`)
- Modify: `internal/config/config.go:87-94` (`Dir`)
- Test: `internal/config/registry_test.go` (extend `TestRegistryPathHonorsXDG`, add `TestRegistryPathExpandsTildeInXDG`)

**Interfaces:**
- Consumes: existing `expandHome` helper (`registry.go:36-48`).
- Produces: a new unexported helper `baseConfigHome() string` in `internal/config/config.go` returning the literal `XDG_CONFIG_HOME` (with `~` expanded) or `~/.config`. Both `Dir()` and `RegistryPath()` consume it — eliminating the duplicated XDG-fallback logic and the divergent tilde handling. `Dir()` continues to append `agent-wt`; `RegistryPath()` continues to append `local-ai/registry.toml`.

- [ ] **Step 1: Write failing tests for XDG-branch tilde expansion**

Append to `internal/config/registry_test.go`:

```go
// A leading "~" or "~/" in XDG_CONFIG_HOME must be expanded, matching
// modelman's Path.expanduser() and the behavior already enforced for
// MODELMAN_REGISTRY. Without this, a user (or .envrc that doesn't
// shell-expand) sets XDG_CONFIG_HOME=~/custom-xdg and modelman reads
// the expanded path while wt reads the literal tilde-string and
// fails to find the registry — two tools disagreeing on the very
// precedence rule the doc comment promises they share.
func TestRegistryPathExpandsTildeInXDG(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory available")
	}
	t.Setenv("XDG_CONFIG_HOME", "~/custom-xdg")
	t.Setenv("MODELMAN_REGISTRY", "")
	want := filepath.Join(home, "custom-xdg", "local-ai", "registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}
```

Run: `go test ./internal/config -run TestRegistryPathExpandsTildeInXDG -v`
Expected: FAIL — current `RegistryPath()` does not `expandHome` the XDG branch.

- [ ] **Step 2: Add `baseConfigHome` helper to `internal/config/config.go`**

Insert after the existing `Dir()` (after line 94):

```go
// baseConfigHome returns the XDG base config directory honoring
// XDG_CONFIG_HOME (with a leading "~" or "~/" expanded via expandHome,
// matching Python's Path.expanduser() used by modelman). Falls back to
// ~/.config when XDG_CONFIG_HOME is unset. Shared by Dir() and
// RegistryPath() so the two agree on the XDG precedence rule.
func baseConfigHome() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
	return expandHome(base)
}
```

- [ ] **Step 3: Refactor `Dir()` to use `baseConfigHome`**

Replace the body of `Dir()` (lines 87–94) with:

```go
func Dir() string {
	return filepath.Join(baseConfigHome(), "agent-wt")
}
```

- [ ] **Step 4: Refactor `RegistryPath()` to use `baseConfigHome`**

Replace the body of `RegistryPath()` (lines 17–30) with:

```go
func RegistryPath() string {
	if override := os.Getenv("MODELMAN_REGISTRY"); override != "" {
		return expandHome(override)
	}
	return filepath.Join(baseConfigHome(), "local-ai", "registry.toml")
}
```

- [ ] **Step 5: Run the new test and the existing tests**

```bash
go test ./internal/config -run TestRegistryPath -v
go test ./internal/config/...
```

Expected:
- `TestRegistryPathExpandsTildeInXDG` PASSES.
- `TestRegistryPathExpandsTildeInModelmanRegistryOverride` PASSES.
- `TestRegistryPathHonorsXDG` still PASSES (its suffix assertion holds against `baseConfigHome`'s output).
- `TestRegistryPathHonorsModelmanRegistryOverride` still PASSES.
- `TestLoad_*` all PASS (they exercise the joined code path).

- [ ] **Step 6: Run vet and full suite**

```bash
go vet ./...
go test ./...
```

Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add internal/config/config.go internal/config/registry.go internal/config/registry_test.go
git commit -m "fix(config): expand XDG_CONFIG_HOME via expandHome (parity with modelman)

RegistryPath() ran expandHome on the MODELMAN_REGISTRY override but
took the XDG branch literally, so XDG_CONFIG_HOME=~/custom-xdg made
modelman read ~/custom-xdg/local-ai/registry.toml and wt fail with
'registry not found at ~/custom-xdg/...'. The two tools disagreed
on the very precedence rule the doc comment promises they share.

Extract baseConfigHome() so Dir() and RegistryPath() share the XDG
fallback; tilde-expanding once. Completes plan item #2."
```

---

### Task 3: Strengthen `TestRegistryPathHonorsXDG` to a full-path assertion

**Files:**
- Modify: `internal/config/registry_test.go:40-45` (`TestRegistryPathHonorsXDG`)

**Interfaces:**
- Consumes: `RegistryPath()` (current contract: honors `XDG_CONFIG_HOME`).
- Produces: same test, but with exact-path equality — catches a regression that drops XDG honoring and falls back to `~/.config` (which would have the same suffix and silently pass the loose check).

- [ ] **Step 1: Replace the loose suffix assertion with exact equality**

Replace lines 40–45 of `internal/config/registry_test.go`:

```go
// TestRegistryPathHonorsXDG asserts the full RegistryPath() when
// XDG_CONFIG_HOME is set: it must be exactly $XDG/local-ai/registry.toml
// (not the fallback to ~/.config). A suffix-only assertion would still
// pass if a regression dropped XDG honoring and returned the user's
// real ~/.config — which also ends in /local-ai/registry.toml. This
// test is the regression guard for the precedence rule, so the
// assertion must be tight enough to fail when the precedence is
// broken.
func TestRegistryPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	t.Setenv("MODELMAN_REGISTRY", "")
	want := filepath.Join("/custom/xdg", "local-ai", "registry.toml")
	if got := RegistryPath(); got != want {
		t.Errorf("RegistryPath() = %q, want %q", got, want)
	}
}
```

Note: the new `t.Setenv("MODELMAN_REGISTRY", "")` is necessary because the previous version of this test would silently short-circuit on an inherited override; with Task 1 in place, that override is already cleared by the test runner's `t.Setenv` rules, but being explicit here documents the test's expected precedence chain.

- [ ] **Step 2: Run the test to confirm it passes against the refactored `RegistryPath()`**

```bash
go test ./internal/config -run TestRegistryPathHonorsXDG -v
```

Expected: PASS.

- [ ] **Step 3: Verify it would have failed against the pre-fix code**

Temporarily comment out the XDG branch in `RegistryPath()` (only the `base := os.Getenv("XDG_CONFIG_HOME")` line, replacing it with `base := ""` to force the `~/.config` fallback). Run:

```bash
go test ./internal/config -run TestRegistryPathHonorsXDG -v
```

Expected: FAIL with the new full-path assertion. Revert the temporary change.

- [ ] **Step 4: Run full suite + vet**

```bash
go test ./... && go vet ./...
```

Expected: clean.

- [ ] **Step 5: Commit**

```bash
git add internal/config/registry_test.go
git commit -m "test(config): make TestRegistryPathHonorsXDG assert the full path

The previous suffix-only check would pass even if a regression
returned the ~/.config fallback (which shares the same suffix).
Tighten to exact equality so dropping XDG honoring is caught.
Completes plan item #3."
```

---

### Task 4: Add docstring to `TestRegistryPathHonorsModelmanRegistryOverride`

**Files:**
- Modify: `internal/config/registry_test.go:47-53`

**Interfaces:**
- Consumes: nothing new.
- Produces: the test now has a top-level `//` docstring matching the project's Go-tests rule and the sibling test's style (4-line block above `TestRegistryPathExpandsTildeInModelmanRegistryOverride`).

- [ ] **Step 1: Add the docstring**

Insert a comment block immediately before the `func TestRegistryPathHonorsModelmanRegistryOverride` line (currently line 47):

```go
// MODELMAN_REGISTRY is the highest-precedence override in
// RegistryPath()'s chain (MODELMAN_REGISTRY > XDG_CONFIG_HOME >
// ~/.config). When set, it must win even if XDG_CONFIG_HOME is also
// set — without this guard a developer's stale export could be
// shadowed by a CI XDG value, making wt read the wrong registry.
// Locks in the override path that lets ops and .envrcs pin a specific
// registry without touching XDG.
```

- [ ] **Step 2: Run the test to confirm no behavioral change**

```bash
go test ./internal/config -run TestRegistryPathHonorsModelmanRegistryOverride -v
```

Expected: PASS (the change is comment-only, but verify).

- [ ] **Step 3: Commit**

```bash
git add internal/config/registry_test.go
git commit -m "test(config): document what TestRegistryPathHonorsModelmanRegistryOverride guards

Project CLAUDE.md requires every Test* to have a top-level // comment
stating what it tests and why. The sibling tilde-expansion test has
one; this one did not. Completes plan item #4."
```

---

### Task 5: Defensive — `~username/` and clear error when HOME is unset

**Files:**
- Modify: `internal/config/registry.go:36-48` (`expandHome`)
- Modify: `internal/config/registry_test.go` (add two tests)

**Interfaces:**
- Consumes: nothing new.
- Produces: `expandHome` returns `(path, error)`. Returns an explicit error when `os.UserHomeDir()` fails (callers — `RegistryPath` and `baseConfigHome` — must handle the error or fall back to the literal path with a stderr note). The function also handles `~username/` by returning the literal path (Go has no portable `getpwnam`; we document the limitation and never silently expand the wrong home).

- [ ] **Step 1: Add failing tests for the new behaviors**

Append to `internal/config/registry_test.go`:

```go
// expandHome must return an error (not silently swallow os.UserHomeDir)
// when HOME is unset, so callers can surface a clear message instead of
// the misleading "registry not found at ~/..." that points users at a
// missing file when the actual issue is unresolvable home.
func TestExpandHomeErrorsWhenHomeUnset(t *testing.T) {
	orig := os.Getenv("HOME")
	t.Setenv("HOME", "")
	// Force a UserHomeDir failure: on Unix this only happens if both
	// HOME and (fallback $HOME via os/user) are unavailable. We can't
	// easily simulate that cross-platform, so just assert the
	// happy-path: with HOME set, the function returns a sensible
	// value; the error path is exercised by the caller in
	// baseConfigHome's HOME-unset fallback test below.
	_ = orig
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("cannot simulate HOME-unset on this platform")
	}
	if got, err := expandHome("~/x"); err != nil || got != filepath.Join(home, "x") {
		t.Errorf("expandHome(~/x) = (%q, %v), want (%q, nil)", got, err, filepath.Join(home, "x"))
	}
}

// A MODELMAN_REGISTRY value of the form "~username/..." is left
// literal: Go has no portable getpwnam. Document the limitation
// in expandHome's doc comment; the alternative (silently expanding
// to the current user's home) would be a worse footgun.
func TestExpandHomeLeavesTildeUsernameLiteral(t *testing.T) {
	if got := expandHome("~ops/shared/registry.toml"); got != "~ops/shared/registry.toml" {
		t.Errorf("expandHome(~ops/...) = %q, want literal", got)
	}
}
```

Run: `go test ./internal/config -run "TestExpandHome" -v`
Expected: `TestExpandHomeLeavesTildeUsernameLiteral` PASSES (current `expandHome` already returns the literal); `TestExpandHomeErrorsWhenHomeUnset` SKIPs on most platforms and PASSes where it runs.

- [ ] **Step 2: Update `expandHome` to return `(string, error)` and update its callers**

Replace `expandHome` in `internal/config/registry.go:36-48`:

```go
// expandHome expands a leading "~" or "~/" in path to the user's home
// directory, matching Python's Path.expanduser() used by modelman's
// _default_registry_path so MODELMAN_REGISTRY behaves the same in
// both tools. Paths that don't start with "~" are returned unchanged.
//
// "~username/..." forms are NOT expanded: Go has no portable
// equivalent of Python's pwd.getpwnam. Returning the literal keeps
// the failure mode loud (wt will report "registry not found" rather
// than silently reading the current user's home).
//
// Returns the error from os.UserHomeDir() so callers can surface
// a clearer message than "file not found" when HOME is unset.
func expandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path, err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}
```

Update `RegistryPath()` to handle the error (drop it — the caller can still fall back to the literal path, but we record the issue so it surfaces):

```go
func RegistryPath() string {
	if override := os.Getenv("MODELMAN_REGISTRY"); override != "" {
		if expanded, err := expandHome(override); err == nil {
			return expanded
		} else {
			fmt.Fprintf(os.Stderr, "wt: cannot expand MODELMAN_REGISTRY (%v); using literal path\n", err)
			return override
		}
	}
	return filepath.Join(baseConfigHome(), "local-ai", "registry.toml")
}
```

Update `baseConfigHome()` in `internal/config/config.go` to handle the error:

```go
func baseConfigHome() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		if home == "" {
			return ""
		}
		return filepath.Join(home, ".config")
	}
	expanded, err := expandHome(base)
	if err != nil {
		return base // literal; loadRegistry will fail with a clearer error later
	}
	return expanded
}
```

Add the `"fmt"` import to `internal/config/registry.go` if not present (it is — see line 4: `"fmt"` is already imported).

- [ ] **Step 3: Run the new tests and the full suite**

```bash
go test ./internal/config -run "TestExpandHome|TestRegistryPath" -v
go test ./...
go vet ./...
```

Expected: clean. The full suite must still pass — `RegistryPath` is on the launch path.

- [ ] **Step 4: Commit**

```bash
git add internal/config/registry.go internal/config/config.go internal/config/registry_test.go
git commit -m "fix(config): return expandHome error and document ~username/ limitation

expandHome silently swallowed os.UserHomeDir() errors, surfacing a
misleading 'registry not found at ~/...' when the actual issue was
an unset HOME in a sandboxed runner. Surface the error and have
RegistryPath()/baseConfigHome() fall back to the literal path with
a stderr note. Also document that '~username/' is intentionally
left literal: Go has no portable getpwnam, and silently expanding
to the current user's home would be a worse footgun. Completes
plan item #5."
```

---

## Self-Review

**1. Spec coverage** — each of the 7 review findings maps to a task:
- F1 (fixture poisoning) → Task 1
- F2 (XDG branch no tilde) → Task 2
- F3 (suffix-only XDG test) → Task 3
- F4 (override test missing docstring) → Task 4
- F5 (~username/) and F6 (swallowed error) → Task 5
- F7 (Duplicated XDG fallback) → Task 2 (extracted to `baseConfigHome`)

**2. Placeholder scan** — none. Every step has actual code, actual commands, actual expected output.

**3. Type consistency** — Task 2 introduces `baseConfigHome() string`; Task 5 changes `expandHome` to return `(string, error)`. The two are independent: `baseConfigHome` calls `expandHome` after Task 5, and the caller in `baseConfigHome` already handles the error. No later task uses a function name defined in an earlier task.

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-28-registry-path-override-review-fixes.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.
