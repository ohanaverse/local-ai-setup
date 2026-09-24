# Agent-Profiles Code-Review Fixes (Round 2) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 10 findings from the second `/code-review` (high effort) pass against `wt-agent-profiles-design`'s diff vs `main` — a fresh review run after the first round's 10 findings (see `docs/superpowers/plans/2026-09-24-agent-profiles-review-fixes.md`) were already fixed. This round's findings are all in `wt/internal/profiles`, `wt/cmd/wt`, and `wt/internal/tui`: two backup-safety bugs in the `config_content` snapshot/restore machinery, a silent-argv-drop bug in the wrapper mechanism, a TOML-corrupting bug in `wt profile on/off`, one dead-code/duplication cleanup, and stale documentation.

**Architecture:** Six independent-to-mostly-independent Go changes. Tasks 1–4 touch only `wt/internal/profiles` and `wt/cmd/wt/profile.go`; Task 5 adds one new file to `wt/internal/agents` and thins two call sites in `wt/cmd/wt/launch.go` and `wt/internal/tui/launch.go`; Task 6 is docs-only. Task 2 is the largest: it reworks `configcontent.go`'s snapshot/restore functions to track backup ownership (a live-process guard) and to restore only the specific keys a profile touched (so content the launched agent itself wrote during the session, e.g. a Claude Code permission grant, survives cleanup) — both changes land in the same functions, so they are one task, not two.

**Tech Stack:** Go 1.26 (module root `wt/`), `github.com/spf13/cobra`, `github.com/BurntSushi/toml`, standard `testing`.

**Spec:** No separate spec doc — this plan's source of truth is the second-round code-review findings, verified against the current source under `wt-agent-profiles-design` on 2026-09-24, plus the feature's own existing design comments in `wt/internal/profiles/*.go`, `wt/docs/wt-agents/profiles.md`, and `wt/CLAUDE.md`.

## Global Constraints

- Every new/changed Go file must pass `gofmt -l` (no diff) and `go vet ./...`.
- Every `Test*` function gets a `//` comment stating what it tests and why it matters, per `wt/CLAUDE.md`'s Go-tests convention — match the style of the tests already in each file.
- No new external dependencies.
- `go test ./...` (run from `wt/`) must pass after every task, not just at the end.
- Never touch `applyProfileForLaunch`'s core guarantee that a profile-layer failure degrades to a normal unprofiled launch — every fix below preserves that (a live-owner collision or a wrapper's missing placeholder both surface as an ordinary `ApplyConfigContent`/`ApplyWrapper` error, which the existing degrade-to-unprofiled path in `cmd/wt/launch.go`'s `applyResolvedProfile` already handles).

## Review Focus

1. **A same-process cleanup call must never be mistaken for a different live session** — Task 2's owner-liveness guard must compare the recorded owner pid against the CALLING process's own pid (`os.Getpid()`), not just liveness: a process is always allowed to restore its own backup, and every existing crash-recovery test in this package simulates "kill -9" by simply skipping `cleanup()` within the same test process, so a naive "pid is alive" check (true for the test's own pid) would wrongly block those.
2. **A backup with no owner marker (or one from an older wt build) must still self-heal** — Task 2's `readBackupOwner` must treat a missing/unparseable owner file as "no known owner" (not live), or upgrading from a build that predates this file would leave every leftover backup permanently stuck.
3. **The owner-liveness check must only apply when a backup actually exists** — checking liveness before confirming a backup marker is even present would make a stray/leftover `.owner` file (with nothing to protect) block `snapshotAndWrite` forever with a false "already in use" error.
4. **A profile whose wrapper drops the agent's own arguments must fail at both validation time and apply time** — Task 3's fix belongs in `Validate` (catches a bad `profiles.toml` before any launch) AND in `ApplyWrapper` itself (protects any other caller that builds a `ResolvedProfile` by hand, bypassing `Validate`).
5. **`wt profile on/off` must never produce a `profiles.toml` that fails to parse afterward** — Task 4's fix must be proven by round-tripping the rewritten file back through `profiles.Load()`, not just by string-matching the raw bytes, since a duplicate top-level key is exactly the kind of corruption that looks fine until re-parsed.

---

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/profiles/configcontent.go` | Modify: `ConfigFileTarget`'s claude branch (Task 1); backup ownership tracking, live-session guard, and key-level surgical restore (Task 2) |
| `wt/internal/profiles/configcontent_test.go` | Modify: update 4 existing tests' assertions for Task 2's semantic (not byte-exact) restore; add new tests for Tasks 1 and 2 |
| `wt/internal/profiles/apply.go` | Modify: delete `ApplyToCmd`; `ApplyWrapper` rejects a missing `{{args}}` placeholder (Task 3) |
| `wt/internal/profiles/apply_test.go` | Modify: retarget `ApplyToCmd`-specific tests at `ApplyEnvAndArgs`/`ApplyWrapper`; add new placeholder-rejection test (Task 3) |
| `wt/internal/profiles/validate.go` | Modify: reject a wrapper profile whose `args_template` has no `{{args}}` (Task 3) |
| `wt/internal/profiles/validate_test.go` | Modify: add new test for Task 3 |
| `wt/cmd/wt/profile.go` | Modify: `topLevelEnabledLineRe`/`setEnabledLine` handle a trailing inline comment (Task 4) |
| `wt/cmd/wt/profile_test.go` | Modify: add new test for Task 4 |
| `wt/internal/agents/run.go` | Create: `RunAndCleanup`, the shared apply→run→cleanup core (Task 5) |
| `wt/internal/agents/run_test.go` | Create: unit tests for `RunAndCleanup` (Task 5) |
| `wt/cmd/wt/launch.go` | Modify: `runAgentCmd` calls `agents.RunAndCleanup`; drop the now-unused `time` import (Task 5) |
| `wt/internal/tui/launch.go` | Modify: `runAndWaitCmd` calls `agents.RunAndCleanup`; drop the now-unused `time` import (Task 5) |
| `wt/docs/wt-agents/profiles.md` | Modify: confirm-prompt section no longer calls the TUI picker a pending follow-up (Task 6) |
| `wt/CLAUDE.md` | Modify: test-seams list gains 4 profile seams; package list and module table gain `internal/profiles`/`cmd/wt/profile.go` (Task 6) |

---

### Task 1: Resolve claude's config_content target to an absolute path

**Files:**
- Modify: `wt/internal/profiles/configcontent.go:32-45` (`ConfigFileTarget`)
- Test: `wt/internal/profiles/configcontent_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ConfigFileTarget("claude", worktreePath)` now returns an absolute path even when `worktreePath` is relative (e.g. `"."`). No signature change.

**Bug:** `cmd/wt/main.go:407` passes the literal `"."` as the launch path for every outside-a-git-repo `claude-wt` invocation, regardless of which real directory the process is running in. `ConfigFileTarget`'s claude branch does `filepath.Join(worktreePath, ".claude", "settings.local.json")` with no `filepath.Abs`, so it returns the literal string `".claude/settings.local.json"` every time. `backupKey` (below it in the same file) hashes that returned STRING to pick the backup file — so two outside-repo claude launches from two different directories collide on the identical backup key. If wt is killed before its cleanup runs, the next outside-repo claude launch from an unrelated directory self-heals using the first directory's stale backup and silently overwrites the second directory's real `.claude/settings.local.json`.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/profiles/configcontent_test.go` (after `TestConfigFileTargetClaudeIsWorktreeScoped`):

```go
// TestConfigFileTargetClaudeResolvesRelativeWorktreePathToAbsolute is the
// regression lock for the code-review finding that an outside-a-git-repo
// claude launch (worktreePath == ".", see cmd/wt/main.go's outside-repo
// passthrough) always hashed the SAME backup key regardless of which real
// directory the launch actually ran in: ConfigFileTarget joined "." onto
// ".claude/settings.local.json" literally, and backupKey (which hashes the
// returned path string) collided across launches from different
// directories that all happened to pass the relative path ".". Two
// launches from different directories must resolve to different,
// directory-specific backup keys.
func TestConfigFileTargetClaudeResolvesRelativeWorktreePathToAbsolute(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()

	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })

	if err := os.Chdir(dirA); err != nil {
		t.Fatal(err)
	}
	pathA, _, _, err := ConfigFileTarget("claude", ".")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude, \".\") in dirA: %v", err)
	}

	if err := os.Chdir(dirB); err != nil {
		t.Fatal(err)
	}
	pathB, _, _, err := ConfigFileTarget("claude", ".")
	if err != nil {
		t.Fatalf("ConfigFileTarget(claude, \".\") in dirB: %v", err)
	}

	if !filepath.IsAbs(pathA) || !filepath.IsAbs(pathB) {
		t.Fatalf("ConfigFileTarget(claude, \".\") = (%q, %q), want both absolute", pathA, pathB)
	}
	if pathA == pathB {
		t.Fatalf("ConfigFileTarget(claude, \".\") returned the same path %q for two different directories", pathA)
	}
	if backupKey(pathA) == backupKey(pathB) {
		t.Error("backupKey(pathA) == backupKey(pathB), want distinct backup keys for distinct directories")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/profiles/ -run TestConfigFileTargetClaudeResolvesRelativeWorktreePathToAbsolute -v`
Expected: FAIL (`pathA == pathB`, both equal to the literal relative string)

- [ ] **Step 3: Implement the fix**

In `wt/internal/profiles/configcontent.go`, replace the whole `ConfigFileTarget` function (including its doc comment) with:

```go
// ConfigFileTarget returns the file profile config_content should be
// written to for agent, or ok=false if the agent takes config_content via
// an inline env var instead (opencode). claude's target is worktree-
// scoped (never the user's global ~/.claude/settings.json); codex's is a
// dedicated wt-owned global profile file, since codex has no
// project-scoped profile mechanism to use instead — since that file's
// whole point is to be a FIXED global path (never inside a repo or
// worktree), a failure to resolve the home directory is reported via err
// rather than silently falling back to a relative path: ok is still true
// (codex does have a target mechanism), but the caller must fail loudly
// instead of writing anywhere. claude's worktreePath is resolved to an
// absolute path for the same reason: an outside-a-git-repo launch passes
// the literal relative path "." (cmd/wt/main.go), and backupKey hashes
// the STRING this function returns — two such launches from different
// real directories would otherwise collide on the identical relative
// string ".claude/settings.local.json" and self-heal/restore over each
// other's unrelated file.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool, err error) {
	switch agent {
	case "claude":
		abs, absErr := filepath.Abs(worktreePath)
		if absErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve worktree path for claude config_content target: %w", absErr)
		}
		return filepath.Join(abs, ".claude", "settings.local.json"), false, true, nil
	case "codex":
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve home directory for codex config_content target: %w", homeErr)
		}
		return filepath.Join(home, ".codex", "agent-wt-profile.config.toml"), true, true, nil
	default:
		return "", false, false, nil
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -v`
Expected: PASS, including the existing `TestConfigFileTargetClaudeIsWorktreeScoped` (passing an already-absolute `/tmp/some-worktree` through `filepath.Abs` returns it unchanged).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/profiles/configcontent.go wt/internal/profiles/configcontent_test.go
git commit -m "fix(profiles): resolve claude's config_content target to an absolute path"
```

---

### Task 2: Guard config_content backups against a live sibling session and restore only the profile's own keys

**Files:**
- Modify: `wt/internal/profiles/configcontent.go` (whole-file rewrite — see Step 3)
- Test: `wt/internal/profiles/configcontent_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ApplyConfigContent`'s claude/JSON write now MERGES `rp.ConfigContent` into the target's existing content instead of replacing it wholesale, and the returned cleanup restores only the top-level keys the profile itself touched (leaving any other keys the launched agent wrote during the session untouched). `snapshotAndWrite`'s internal self-heal, and the exported `SelfHeal`/`restoreIfBackedUp`, now refuse to restore/overwrite a backup whose `.owner` marker names a DIFFERENT process that is still alive (never the calling process's own pid — see Review Focus #1). codex's TOML target is unaffected: it keeps its existing whole-file snapshot/replace/restore, since that file is a dedicated wt-owned global file nothing else writes to.

**Bugs fixed (two, in the same functions):**
1. `restoreIfBackedUpLocked`'s per-target flock only ever covers the brief snapshot/write/restore call, not the launched agent's whole lifetime. A second launch touching the SAME `config_content` target while the first session is still running (e.g. codex's fixed global path, shared across every worktree) self-heals over — or has its own eventual cleanup clobbered by — a backup that in fact still belongs to a live, still-running sibling session.
2. `ApplyConfigContent`'s cleanup closure unconditionally restores the pre-launch snapshot on exit, discarding any legitimate content the launched agent itself wrote to that same file during the session (e.g. Claude Code persisting a user-approved "always allow" permission grant into `settings.local.json`).

- [ ] **Step 1: Write the failing tests**

First, update four EXISTING tests in `wt/internal/profiles/configcontent_test.go` whose assertions currently compare restored content byte-for-byte — Task 2's fix restores content by re-marshaling JSON (to do the key-level merge), so the restored bytes are no longer byte-identical to the original even though they are semantically identical. Add `"reflect"` to the file's import block, then apply these four changes:

Change `TestApplyConfigContentWritesAndBacksUpExistingFile`'s tail (everything from `if err := cleanup(); err != nil {` to the end of the function) to:

```go
	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	restored, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(restored, &gotDoc); err != nil {
		t.Fatalf("restored content is not valid JSON: %v (%s)", err, restored)
	}
	if err := json.Unmarshal(original, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("restored content = %v, want %v (same keys/values as the pre-launch original)", gotDoc, wantDoc)
	}
}
```

Also update that test's doc comment (the fix now merges/restores by key, not by byte-for-byte replace/restore):

```go
// TestApplyConfigContentWritesAndBacksUpExistingFile verifies an existing
// target file's content is snapshotted before config_content is merged in,
// and that the returned cleanup restores the profile's own top-level keys
// to their pre-launch values — the "save, merge for the session, restore
// on exit" behavior the design commits to. Cleanup now does a key-level
// restore (re-marshaled JSON), not a byte-for-byte one — compared
// semantically here; see
// TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession for why.
func TestApplyConfigContentWritesAndBacksUpExistingFile(t *testing.T) {
```

Change `TestApplyConfigContentSelfHealsOrphanedBackup`'s tail (from `got, err := os.ReadFile(target)` to the end) to:

```go
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v (the original hand-edited content)", gotDoc, wantDoc)
	}
}
```

Change `TestSelfHealRestoresOrphanedBackup`'s tail (from `got, err := os.ReadFile(target)` to the end) the same way:

```go
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("self-healed content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v (the original hand-edited content)", gotDoc, wantDoc)
	}
}
```

Change `TestRestoreIfBackedUpReturnsTrueWhenCleanupFailsAfterSuccessfulRestore`'s tail (from `got, readErr := os.ReadFile(target)` to the end) the same way:

```go
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	var gotDoc, wantDoc map[string]any
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		t.Fatalf("restored content is not valid JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("target content = %v, want %v (the restored hand-edited content)", gotDoc, wantDoc)
	}
}
```

Now add the new regression-lock tests. Add `"reflect"` and `"strconv"` to the test file's imports, then append these five tests at the end of `wt/internal/profiles/configcontent_test.go`:

```go
// TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession is the
// regression lock for the code-review finding that cleanup blindly
// restored the pre-launch snapshot, discarding any legitimate content the
// launched agent itself wrote to the same file during the session (e.g.
// Claude Code persisting a user-approved permission grant into
// settings.local.json). It simulates the agent writing a new top-level key
// while the profile-merged file is in place, then asserts cleanup keeps
// that key while still reverting the profile's own "env" key to its
// pre-launch value.
func TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"env":{"MY_OWN_SETTING":"keep-me"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"MAX_THINKING_TOKENS": "4096"}}}
	cleanup, err := ApplyConfigContent(cmd, "claude", worktree, rp)
	if err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}

	// Simulate the launched agent (e.g. Claude Code) persisting a new
	// top-level key into the same file mid-session — a real "always
	// allow" permission grant, in production.
	withGrant := []byte(`{"env":{"MY_OWN_SETTING":"keep-me","MAX_THINKING_TOKENS":"4096"},"permissions":{"allow":["Bash(npm test)"]}}`)
	if err := os.WriteFile(target, withGrant, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := cleanup(); err != nil {
		t.Fatalf("cleanup() error = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(got, &doc); err != nil {
		t.Fatalf("restored file is not valid JSON: %v (%s)", err, got)
	}
	if _, has := doc["permissions"]; !has {
		t.Errorf("restored content = %s, lost the permission grant the agent wrote during the session", got)
	}
	env, ok := doc["env"].(map[string]any)
	if !ok {
		t.Fatalf("restored content = %s, want an env object", got)
	}
	if env["MY_OWN_SETTING"] != "keep-me" {
		t.Errorf("env = %v, want the pre-launch MY_OWN_SETTING preserved", env)
	}
	if _, has := env["MAX_THINKING_TOKENS"]; has {
		t.Errorf("env = %v, want the profile's own MAX_THINKING_TOKENS key removed on restore", env)
	}
}

// TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession is the
// regression lock for the code-review finding that the per-target flock
// only serializes the brief snapshot/write/restore call, not a launched
// agent's whole lifetime: a second launch touching the SAME config_content
// target (e.g. codex's fixed global path, shared across every worktree)
// used to self-heal over — or write on top of — a backup that in fact
// still belongs to a live, still-running sibling wt session. This
// simulates that sibling by writing the backup's .owner marker to a
// different (fake) pid and forcing pidAlive to report it alive; the
// second session's write must refuse instead of clobbering the first
// session's still-in-use file.
func TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd1 := exec.Command("claude")
	rp1 := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd1, "claude", worktree, rp1); err != nil {
		t.Fatalf("session A ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983 // arbitrary, distinct from this test process's own pid
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == fakeOwnerPid }
	t.Cleanup(func() { pidAlive = origPidAlive })

	sessionAContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	cmd2 := exec.Command("claude")
	rp2 := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"Y": "2"}}}
	if _, err := ApplyConfigContent(cmd2, "claude", worktree, rp2); err == nil {
		t.Fatal("session B ApplyConfigContent() error = nil, want an error (target owned by a live sibling session)")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(sessionAContent) {
		t.Errorf("target was modified by session B: got %s, want session A's untouched content %s", got, sessionAContent)
	}
	if _, err := os.Stat(backupPresentPath(target)); err != nil {
		t.Errorf("session A's backup was removed by session B's failed attempt: %v", err)
	}
}

// TestSelfHealSkipsBackupOwnedByLiveProcess verifies the exported SelfHeal
// entry point (used by cmd/wt's applyProfileForLaunch on every launch of an
// agent) also respects the live-owner guard: it must never restore a
// backup a different, still-running wt process still owns.
func TestSelfHealSkipsBackupOwnedByLiveProcess(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	original := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, original, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(pid int) bool { return pid == fakeOwnerPid }
	t.Cleanup(func() { pidAlive = origPidAlive })

	liveContent, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := SelfHeal(target)
	if err != nil {
		t.Fatalf("SelfHeal() error = %v, want nil (a live owner must never fail the launch)", err)
	}
	if restored {
		t.Error("SelfHeal() restored = true, want false (backup is owned by a live sibling session)")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(liveContent) {
		t.Errorf("SelfHeal() modified a live-owned target: got %s, want %s", got, liveContent)
	}
}

// TestRestoreIfBackedUpStillHealsWhenOwnerIsDead verifies the pre-existing
// crash-recovery self-heal behavior survives the new owner-liveness guard:
// when the recorded owner pid is a DIFFERENT process that is no longer
// alive (the owning wt process was killed, e.g. `kill -9`), the backup is
// still treated as orphaned and restored, exactly as before this fix.
func TestRestoreIfBackedUpStillHealsWhenOwnerIsDead(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "xdg"))
	worktree := filepath.Join(dir, "worktree")
	target := filepath.Join(worktree, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	handEdited := []byte(`{"hooks":{"my-own":"thing"}}`)
	if err := os.WriteFile(target, handEdited, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("claude")
	rp := ResolvedProfile{ConfigContent: map[string]any{"env": map[string]any{"X": "1"}}}
	if _, err := ApplyConfigContent(cmd, "claude", worktree, rp); err != nil {
		t.Fatalf("ApplyConfigContent() error = %v", err)
	}

	fakeOwnerPid := os.Getpid() + 999983
	if err := os.WriteFile(backupOwnerPath(target), []byte(strconv.Itoa(fakeOwnerPid)), 0o600); err != nil {
		t.Fatal(err)
	}
	origPidAlive := pidAlive
	pidAlive = func(int) bool { return false } // the recorded owner is no longer running
	t.Cleanup(func() { pidAlive = origPidAlive })

	restored, err := restoreIfBackedUp(target)
	if err != nil {
		t.Fatalf("restoreIfBackedUp() error = %v", err)
	}
	if !restored {
		t.Fatal("restoreIfBackedUp() restored = false, want true (owner pid is dead — orphaned backup)")
	}
	var gotDoc, wantDoc map[string]any
	gotBytes, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &gotDoc); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(handEdited, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Errorf("self-healed content = %v, want %v", gotDoc, wantDoc)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/ -run 'TestApplyConfigContent|TestSelfHeal|TestRestoreIfBackedUp' -v`
Expected: FAIL — the new tests fail (no owner tracking, no key-level restore yet); the four updated existing tests currently still pass against the OLD implementation (they'll only start exercising the new code path once Step 3 lands, so it's fine if they pass here — the important ones to see fail are `TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession`, `TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession`, `TestSelfHealSkipsBackupOwnedByLiveProcess`).

- [ ] **Step 3: Implement the fix**

Replace the entire contents of `wt/internal/profiles/configcontent.go` (the `ConfigFileTarget` function here already includes Task 1's fix) with:

```go
// wt/internal/profiles/configcontent.go
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// ConfigFileTarget returns the file profile config_content should be
// written to for agent, or ok=false if the agent takes config_content via
// an inline env var instead (opencode). claude's target is worktree-
// scoped (never the user's global ~/.claude/settings.json); codex's is a
// dedicated wt-owned global profile file, since codex has no
// project-scoped profile mechanism to use instead — since that file's
// whole point is to be a FIXED global path (never inside a repo or
// worktree), a failure to resolve the home directory is reported via err
// rather than silently falling back to a relative path: ok is still true
// (codex does have a target mechanism), but the caller must fail loudly
// instead of writing anywhere. claude's worktreePath is resolved to an
// absolute path for the same reason: an outside-a-git-repo launch passes
// the literal relative path "." (cmd/wt/main.go), and backupKey hashes
// the STRING this function returns — two such launches from different
// real directories would otherwise collide on the identical relative
// string ".claude/settings.local.json" and self-heal/restore over each
// other's unrelated file.
func ConfigFileTarget(agent, worktreePath string) (path string, isTOML bool, ok bool, err error) {
	switch agent {
	case "claude":
		abs, absErr := filepath.Abs(worktreePath)
		if absErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve worktree path for claude config_content target: %w", absErr)
		}
		return filepath.Join(abs, ".claude", "settings.local.json"), false, true, nil
	case "codex":
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return "", false, true, fmt.Errorf("profile: resolve home directory for codex config_content target: %w", homeErr)
		}
		return filepath.Join(home, ".codex", "agent-wt-profile.config.toml"), true, true, nil
	default:
		return "", false, false, nil
	}
}

const openCodeConfigEnvPrefix = "OPENCODE_CONFIG_CONTENT="

// ApplyConfigContent writes rp.ConfigContent for agent: a real,
// snapshot/restored file for claude and codex, or a merge into cmd.Env's
// existing OPENCODE_CONFIG_CONTENT entry for opencode. It returns a
// cleanup function the caller MUST call after the launched process exits
// (success or failure) to restore whatever the write touched.
//
// claude's target (settings.local.json) is NOT a wt-exclusive file — the
// launched Claude Code session itself writes to it during the session
// (e.g. persisting a user-approved "always allow" permission grant) — so
// the write MERGES rp.ConfigContent into whatever is already there
// (mirroring the opencode env-merge below) instead of replacing the whole
// file, and cleanup restores only the specific top-level keys
// config_content itself touched, leaving anything the agent wrote
// elsewhere alone. codex's target is a dedicated, wt-owned global file
// nothing else writes to, so it keeps the simpler whole-file
// snapshot/replace/restore.
func ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error) {
	noop := func() error { return nil }
	if len(rp.ConfigContent) == 0 {
		return noop, nil
	}
	if agent == "opencode" {
		if err := mergeOpenCodeEnv(cmd, rp.ConfigContent); err != nil {
			return nil, err
		}
		return noop, nil
	}
	path, isTOML, ok, targetErr := ConfigFileTarget(agent, worktreePath)
	if targetErr != nil {
		return nil, targetErr
	}
	if !ok {
		return nil, fmt.Errorf("profile: agent %q has no config_content target", agent)
	}

	var buildData func(existing []byte, existed bool) (data []byte, touchedKeys []string, err error)
	if isTOML {
		buildData = func(existing []byte, existed bool) ([]byte, []string, error) {
			var buf bytes.Buffer
			if err := toml.NewEncoder(&buf).Encode(rp.ConfigContent); err != nil {
				return nil, nil, fmt.Errorf("encode profile config_content: %w", err)
			}
			return buf.Bytes(), nil, nil // nil touchedKeys: codex's target is dedicated, whole-file restore is correct
		}
	} else {
		buildData = func(existing []byte, existed bool) ([]byte, []string, error) {
			doc := map[string]any{}
			if existed {
				if err := json.Unmarshal(existing, &doc); err != nil {
					return nil, nil, fmt.Errorf("profile: existing %s is not valid JSON, refusing to merge config_content: %w", path, err)
				}
			}
			mergeInto(doc, rp.ConfigContent)
			data, err := json.MarshalIndent(doc, "", "  ")
			if err != nil {
				return nil, nil, fmt.Errorf("encode profile config_content: %w", err)
			}
			keys := make([]string, 0, len(rp.ConfigContent))
			for k := range rp.ConfigContent {
				keys = append(keys, k)
			}
			return data, keys, nil
		}
	}

	if err := snapshotAndWrite(path, 0o644, buildData); err != nil {
		return nil, err
	}
	// The --profile flag is only appended once the write above has
	// actually succeeded: appending it earlier (even when the write then
	// fails and the caller degrades to an unprofiled launch) would still
	// point codex at a file that is missing or stale, since a failed write
	// leaves no guarantee about the target's contents.
	if agent == "codex" {
		cmd.Args = append(cmd.Args, "--profile", "agent-wt-profile")
	}
	return func() error {
		_, err := restoreIfBackedUp(path)
		return err
	}, nil
}

// mergeOpenCodeEnv merges content into the OPENCODE_CONFIG_CONTENT entry in
// cmd.Env. It searches from the END of cmd.Env, not the start: Command()
// (internal/agents) builds cmd.Env as os.Environ() (the inherited parent
// environment) followed by the driver's own env entries, so if the parent
// process already happens to export OPENCODE_CONFIG_CONTENT (e.g. wt was
// itself launched from inside an opencode session), the FIRST match would
// be that inherited, unrelated value rather than the opencode driver's own
// (later) entry — and since exec.Cmd.Env uses the LAST value for a
// duplicate key, the driver's own untouched entry would then win at exec
// time, silently dropping the merged profile content. The last match is
// always the one that actually takes effect.
func mergeOpenCodeEnv(cmd *exec.Cmd, content map[string]any) error {
	for i := len(cmd.Env) - 1; i >= 0; i-- {
		e := cmd.Env[i]
		if !strings.HasPrefix(e, openCodeConfigEnvPrefix) {
			continue
		}
		var doc map[string]any
		if err := json.Unmarshal([]byte(strings.TrimPrefix(e, openCodeConfigEnvPrefix)), &doc); err != nil {
			return fmt.Errorf("profile: existing OPENCODE_CONFIG_CONTENT is not valid JSON: %w", err)
		}
		mergeInto(doc, content)
		merged, err := json.Marshal(doc)
		if err != nil {
			return fmt.Errorf("profile: re-encode OPENCODE_CONFIG_CONTENT: %w", err)
		}
		cmd.Env[i] = openCodeConfigEnvPrefix + string(merged)
		return nil
	}
	return fmt.Errorf("profile: opencode launch has no OPENCODE_CONFIG_CONTENT to merge into")
}

// backupDir holds pre-write snapshots of files config_content rewrites,
// never a sibling file next to the target (so nothing lands in a repo or
// worktree).
func backupDir() string { return filepath.Join(config.Dir(), "profile-backups") }

func backupKey(target string) string {
	sum := sha256.Sum256([]byte(target))
	return filepath.Join(backupDir(), hex.EncodeToString(sum[:]))
}

func backupPresentPath(target string) string { return backupKey(target) + ".present" }
func backupAbsentPath(target string) string  { return backupKey(target) + ".absent" }

// backupModePath holds target's original permission bits (octal, e.g.
// "0600") alongside its content backup, so restoreIfBackedUpLocked can put
// the file back exactly as it found it instead of guessing a mode.
func backupModePath(target string) string { return backupKey(target) + ".mode" }

// backupKeysPath holds the JSON-encoded list of top-level keys
// config_content itself wrote into target, when target's write merged into
// existing content (JSON targets only — see ApplyConfigContent). Its
// presence at restore time is what selects the key-level surgical restore
// over the legacy whole-file restore; a codex/TOML write never creates
// this file, so codex is unaffected by this mechanism.
func backupKeysPath(target string) string { return backupKey(target) + ".keys" }

// backupOwnerPath records the pid of the wt process that owns target's
// current backup, written right after the backup snapshot and before the
// target itself is rewritten. restoreIfBackedUpLocked uses it to refuse to
// self-heal or restore a backup that still belongs to a different,
// still-running wt process — see withTargetLock's doc for why the lock
// alone isn't enough to prevent that.
func backupOwnerPath(target string) string { return backupKey(target) + ".owner" }

// pidAlive reports whether pid is a live process, mirroring
// internal/refcount.pidAlive's signal-0-probe convention (duplicated
// rather than imported: refcount's contract is scoped to live-session
// model-usage counts, and a cross-package dependency for one liveness
// probe isn't worth it). Package-level var so tests can inject live/dead
// pids deterministically.
var pidAlive = func(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

// readBackupOwner reads target's owner marker, reporting ok=false for any
// missing/unreadable/unparseable file — treated as "no known owner",
// which restoreIfBackedUpLocked handles as "not live" (safe to restore),
// matching a backup written by a wt version that predates this file.
func readBackupOwner(target string) (pid int, ok bool) {
	data, err := os.ReadFile(backupOwnerPath(target))
	if err != nil {
		return 0, false
	}
	parsed, perr := strconv.Atoi(strings.TrimSpace(string(data)))
	if perr != nil {
		return 0, false
	}
	return parsed, true
}

// surgicalRestoreJSON restores originalSnapshot's values for touchedKeys
// into target's CURRENT on-disk content, leaving every other top-level key
// in the current content untouched — so content the launched agent itself
// wrote during the session (e.g. a permission grant) survives cleanup. ok
// is false (caller falls back to the legacy whole-file restore) when
// either originalSnapshot or target's current content isn't a JSON object,
// since a safe key-level merge isn't possible in that case.
func surgicalRestoreJSON(originalSnapshot []byte, target string, touchedKeys []string) (merged []byte, ok bool) {
	var origDoc map[string]any
	if err := json.Unmarshal(originalSnapshot, &origDoc); err != nil {
		return nil, false
	}
	current, err := os.ReadFile(target)
	if err != nil {
		return nil, false
	}
	var curDoc map[string]any
	if err := json.Unmarshal(current, &curDoc); err != nil {
		return nil, false
	}
	for _, k := range touchedKeys {
		if v, has := origDoc[k]; has {
			curDoc[k] = v
		} else {
			delete(curDoc, k)
		}
	}
	data, err := json.MarshalIndent(curDoc, "", "  ")
	if err != nil {
		return nil, false
	}
	return data, true
}

// withTargetLock serializes snapshotAndWrite/restoreIfBackedUp for target
// across processes via an flock on a lock file keyed by target (mirrors
// internal/litellm/configfile.go's WithLock). The lock only serializes the
// brief critical section, though — it says nothing about how long the
// OWNING process then keeps running with that content in place, which is
// what the separate owner-liveness check (backupOwnerPath/pidAlive above)
// guards against.
func withTargetLock(target string, fn func() error) error {
	if err := os.MkdirAll(backupDir(), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(backupKey(target)+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}

// snapshotAndWrite self-heals any leftover backup for target first (see
// restoreIfBackedUpLocked) unless it is still owned by a live sibling
// process (in which case it refuses with an error, rather than writing on
// top of a file a different running wt session still relies on), then
// snapshots target's current state (content, permission bits, and this
// process's pid as owner — or "did not exist") before calling buildData to
// compute the bytes to write, plus the set of top-level keys it touched
// (JSON targets only; nil for a whole-file target like codex's). The whole
// sequence runs under withTargetLock, so a concurrent snapshotAndWrite or
// restoreIfBackedUp for the same target from another process waits instead
// of racing it. buildData receives target's existing content (nil if it
// did not exist) so a JSON target can merge rather than replace.
func snapshotAndWrite(target string, perm os.FileMode, buildData func(existing []byte, existed bool) (data []byte, touchedKeys []string, err error)) error {
	return withTargetLock(target, func() error {
		if _, live, err := restoreIfBackedUpLocked(target); err != nil {
			return err
		} else if live {
			return fmt.Errorf("profile: config_content target %s is already in use by another live wt session", target)
		}
		existing, err := os.ReadFile(target)
		existed := true
		switch {
		case os.IsNotExist(err):
			existed = false
			if err := config.WriteFileAtomic(backupAbsentPath(target), []byte{}, 0o600); err != nil {
				return err
			}
		case err != nil:
			return err
		default:
			info, statErr := os.Stat(target)
			if statErr != nil {
				return statErr
			}
			mode := fmt.Sprintf("%04o", info.Mode().Perm())
			if err := config.WriteFileAtomic(backupModePath(target), []byte(mode), 0o600); err != nil {
				return err
			}
			if err := config.WriteFileAtomic(backupPresentPath(target), existing, 0o600); err != nil {
				return err
			}
		}
		if err := config.WriteFileAtomic(backupOwnerPath(target), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			return err
		}
		data, touchedKeys, berr := buildData(existing, existed)
		if berr != nil {
			return berr
		}
		if existed && len(touchedKeys) > 0 {
			keysJSON, jerr := json.Marshal(touchedKeys)
			if jerr != nil {
				return jerr
			}
			if err := config.WriteFileAtomic(backupKeysPath(target), keysJSON, 0o600); err != nil {
				return err
			}
		}
		return config.WriteFileAtomic(target, data, perm)
	})
}

// restoreIfBackedUp restores target from whatever snapshotAndWrite backed
// up, removing the backup marker, and reports whether it found one to
// restore. It is safe to call with no backup present (a no-op) — this is
// the SAME operation for the normal post-launch restore and for
// self-healing an orphaned backup before a fresh write; the two are not
// distinguished by the function, only by when the caller invokes it. Runs
// under withTargetLock so it can't race a concurrent snapshotAndWrite or
// restoreIfBackedUp for the same target in another process. A backup still
// owned by a different, live wt process is left untouched (restored=false,
// err=nil) rather than restored out from under it.
//
// A real filesystem error (permission denied, I/O error) reading either
// marker is returned rather than treated as "no backup" — silently
// swallowing it here would let a caller proceed to overwrite a `.present`
// backup that in fact still holds the true pre-write original, which is
// exactly the data loss self-heal exists to prevent.
// SelfHeal restores target from an orphaned config_content backup, if one
// exists — the exported entry point for callers outside this package
// (cmd/wt's applyProfileForLaunch) that want to self-heal a claude/codex
// config_content target on EVERY launch of that agent, not only the next
// launch that happens to resolve a matching profile for that exact target
// (which is what snapshotAndWrite's own internal self-heal call already
// covers). Safe to call with no backup present (a no-op, restored=false).
func SelfHeal(target string) (restored bool, err error) {
	return restoreIfBackedUp(target)
}

func restoreIfBackedUp(target string) (restored bool, err error) {
	err = withTargetLock(target, func() error {
		var lockErr error
		restored, _, lockErr = restoreIfBackedUpLocked(target)
		return lockErr
	})
	return restored, err
}

// statExists reports whether path exists, treating a not-exist error as
// (false, nil) and any other stat failure as a real error to propagate.
func statExists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

// restoreIfBackedUpLocked is restoreIfBackedUp's lock-free core. It exists
// separately so snapshotAndWrite's internal self-heal call can run inside
// the single withTargetLock it already holds for its whole snapshot+write
// sequence, instead of nesting a second (deadlocking) lock acquisition.
//
// live=true means a backup exists but its owner marker names a different
// process that is still alive — this deliberately never restores/deletes
// in that case (see backupOwnerPath's doc). The owner check only runs when
// a backup actually exists: a stray/leftover owner marker with no backup
// to protect is meaningless and is cleared instead of blocking anything.
// The owner marker naming THIS SAME process (the common case: a session
// calling restoreIfBackedUp on its own backup at normal cleanup) is never
// treated as live, since a process is always allowed to restore its own
// backup.
func restoreIfBackedUpLocked(target string) (restored bool, live bool, err error) {
	absentPresent, err := statExists(backupAbsentPath(target))
	if err != nil {
		return false, false, err
	}
	presentPresent, err := statExists(backupPresentPath(target))
	if err != nil {
		return false, false, err
	}
	if !absentPresent && !presentPresent {
		_ = os.Remove(backupOwnerPath(target))
		return false, false, nil
	}
	if owner, ok := readBackupOwner(target); ok && owner != os.Getpid() && pidAlive(owner) {
		return false, true, nil
	}

	if absentPresent {
		if rmErr := os.Remove(target); rmErr != nil && !os.IsNotExist(rmErr) {
			return false, false, rmErr
		}
		_ = os.Remove(backupOwnerPath(target))
		return true, false, os.Remove(backupAbsentPath(target))
	}

	data, readErr := os.ReadFile(backupPresentPath(target))
	switch {
	case readErr == nil:
		// The stored mode is best-effort: a backup written by an older wt
		// version (or one whose .mode file was lost) has none, and 0o644
		// remains the pre-existing fallback rather than a new failure mode.
		perm := os.FileMode(0o644)
		if modeData, merr := os.ReadFile(backupModePath(target)); merr == nil {
			if parsed, perr := strconv.ParseUint(strings.TrimSpace(string(modeData)), 8, 32); perr == nil {
				perm = os.FileMode(parsed)
			}
		}
		finalData := data
		if keysRaw, kerr := os.ReadFile(backupKeysPath(target)); kerr == nil {
			var touchedKeys []string
			if json.Unmarshal(keysRaw, &touchedKeys) == nil {
				if mergedData, ok := surgicalRestoreJSON(data, target, touchedKeys); ok {
					finalData = mergedData
				}
			}
		}
		if werr := config.WriteFileAtomic(target, finalData, perm); werr != nil {
			return false, false, werr
		}
		// From here on the target's content is already restored — a
		// cleanup failure below must still report restored=true, mirroring
		// the .absent branch above for the structurally equivalent case.
		if rmErr := os.Remove(backupPresentPath(target)); rmErr != nil {
			return true, false, rmErr
		}
		if rmErr := os.Remove(backupModePath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return true, false, rmErr
		}
		if rmErr := os.Remove(backupKeysPath(target)); rmErr != nil && !os.IsNotExist(rmErr) {
			return true, false, rmErr
		}
		_ = os.Remove(backupOwnerPath(target))
		return true, false, nil
	case !os.IsNotExist(readErr):
		return false, false, readErr
	}
	return false, false, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -v`
Expected: PASS — all of `configcontent_test.go`, plus the rest of the package (nothing else in the package references the functions this rewrite changed, other than the split-out `ApplyEnvAndArgs`/`ApplyWrapper` in `apply.go`, which Task 3 touches next and is untouched so far).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/profiles/configcontent.go wt/internal/profiles/configcontent_test.go
git commit -m "fix(profiles): guard config_content backups against live sessions, restore only touched keys"
```

---

### Task 3: Fix ApplyWrapper's silent argv drop and remove the unused ApplyToCmd

**Files:**
- Modify: `wt/internal/profiles/apply.go`
- Modify: `wt/internal/profiles/apply_test.go`
- Modify: `wt/internal/profiles/validate.go`
- Modify: `wt/internal/profiles/validate_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ApplyWrapper(cmd, w)` now returns an error when `w.ArgsTemplate` has no `"{{args}}"` element. `Validate` rejects the same condition at validation time. `ApplyToCmd` is deleted (no production caller — `cmd/wt`'s `applyResolvedProfile` already calls `ApplyEnvAndArgs`/`ApplyConfigContent`/`ApplyWrapper` directly to interleave `config_content` between them, which is the only reason `ApplyToCmd`'s narrower env→wrapper composition existed as a separate, untested-in-production path).

**Bugs fixed:**
1. `ApplyWrapper` silently drops the launched agent's entire original argv (e.g. `--model`, worktree flags) when a wrapper profile's `ArgsTemplate` has a typo'd or missing `"{{args}}"` token — `newArgs` is built from only the literal tokens in `ArgsTemplate`, and `original := cmd.Args[1:]` is captured but never spliced in.
2. `ApplyToCmd` is exported and unit-tested as if it were the production entry point, but `cmd/wt/launch.go`'s `applyResolvedProfile` never calls it — a maintainer could fix a real bug in `ApplyToCmd` while the actual launch path (a separately hand-inlined sequence) never picks up the fix.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/profiles/validate_test.go`, append:

```go
// TestValidateRejectsWrapperArgsTemplateMissingPlaceholder is the
// regression lock for the code-review finding that a wrapper profile
// missing "{{args}}" in its args_template passed validation cleanly, only
// to silently drop the launched agent's own arguments at launch time.
func TestValidateRejectsWrapperArgsTemplateMissingPlaceholder(t *testing.T) {
	store := Store{Profiles: []Profile{
		{Agent: "pi", Match: "location", Location: "local", Wrapper: &WrapperSpec{Binary: "little-coder", ArgsTemplate: []string{"--foo"}}},
	}}
	mechs := func(agent string) []Mechanism { return []Mechanism{MechanismWrapper} }
	err := Validate(store, mechs)
	if err == nil {
		t.Fatal("Validate() = nil, want an error naming the missing {{args}} placeholder")
	}
	if !contains(err.Error(), "{{args}}") {
		t.Errorf("Validate() error = %v, want it to mention the missing {{args}} placeholder", err)
	}
}
```

In `wt/internal/profiles/apply_test.go`, replace the ENTIRE file with:

```go
// wt/internal/profiles/apply_test.go
package profiles

import (
	"os/exec"
	"strings"
	"testing"
)

// TestApplyEnvAndArgsEnvWinsOnCollision verifies a profile's Env value for
// a key the command already carries wins — exec.Cmd.Env is documented to
// use the LAST value for a duplicate key, so ApplyEnvAndArgs must APPEND
// (never prepend or dedupe) so profile values always land after
// driver-set ones.
func TestApplyEnvAndArgsEnvWinsOnCollision(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"FOO=driver-value"}
	rp := ResolvedProfile{Env: map[string]string{"FOO": "profile-value"}}
	ApplyEnvAndArgs(cmd, rp)
	if cmd.Env[len(cmd.Env)-1] != "FOO=profile-value" {
		t.Errorf("cmd.Env = %v, want profile value appended last", cmd.Env)
	}
}

// TestApplyEnvAndArgsExtraArgsAppended verifies ExtraArgs land at the end
// of cmd.Args, after whatever BuildLaunchCmd already assembled (driver
// args, user passthrough, resume flag) — matching cmd/wt's
// applyResolvedProfile ordering (env/args before config_content/wrapper).
func TestApplyEnvAndArgsExtraArgsAppended(t *testing.T) {
	cmd := exec.Command("codex", "--model", "x")
	rp := ResolvedProfile{ExtraArgs: []string{"-c", "model_reasoning_effort=\"low\""}}
	ApplyEnvAndArgs(cmd, rp)
	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\""}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, want)
	}
}

// TestApplyEnvAndArgsThenApplyWrapperComposesCorrectly verifies the real
// production sequence (cmd/wt's applyResolvedProfile: ApplyEnvAndArgs,
// then — with a config_content step interleaved in production, omitted
// here since it's tested separately — ApplyWrapper) swaps cmd.Path/Args[0]
// for the wrapper binary and splices the ORIGINAL argv, including
// anything ApplyEnvAndArgs already appended, into the "{{args}}" template
// slot — the little-coder-wraps-pi case.
func TestApplyEnvAndArgsThenApplyWrapperComposesCorrectly(t *testing.T) {
	cmd := exec.Command("pi", "--model", "ollama/qwen3.8:27b-mlx")
	rp := ResolvedProfile{
		ExtraArgs: []string{"--extra"},
		Wrapper: &WrapperSpec{
			Binary:       "true", // a binary guaranteed to exist on PATH for the test
			ArgsTemplate: []string{"--pi-args", "{{args}}"},
		},
	}
	ApplyEnvAndArgs(cmd, rp)
	if err := ApplyWrapper(cmd, rp.Wrapper); err != nil {
		t.Fatalf("ApplyWrapper() error = %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "true") {
		t.Errorf("cmd.Path = %q, want the resolved `true` binary", cmd.Path)
	}
	want := []string{"--pi-args", "--model", "ollama/qwen3.8:27b-mlx", "--extra"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}

// TestApplyWrapperMissingBinaryErrors verifies a wrapper binary that isn't
// on PATH is a real launch error (not a silent fallback to unwrapped) —
// the user asked for little-coder and it must actually run.
func TestApplyWrapperMissingBinaryErrors(t *testing.T) {
	cmd := exec.Command("pi")
	w := &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
	if err := ApplyWrapper(cmd, w); err == nil {
		t.Fatal("ApplyWrapper() error = nil, want an error for a missing wrapper binary")
	}
}

// TestApplyWrapperErrorsWhenArgsTemplateMissingPlaceholder is the
// regression lock for the code-review finding that a wrapper profile's
// ArgsTemplate omitting the "{{args}}" placeholder silently dropped the
// launched agent's entire original argv (e.g. --model, worktree flags)
// instead of failing loudly.
func TestApplyWrapperErrorsWhenArgsTemplateMissingPlaceholder(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	w := &WrapperSpec{Binary: "true", ArgsTemplate: []string{"--wrapped-with-no-placeholder"}}
	if err := ApplyWrapper(cmd, w); err == nil {
		t.Fatal("ApplyWrapper() error = nil, want an error for an args_template with no \"{{args}}\" placeholder")
	}
}

// TestApplyEnvAndArgsNeverAppliesWrapper verifies ApplyEnvAndArgs applies
// Env/ExtraArgs but leaves Wrapper untouched — cmd/wt's applyResolvedProfile
// depends on this to interleave a config_content mutation between the two.
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

// TestApplyWrapperExported verifies ApplyWrapper behaves as documented —
// cmd/wt calls it directly so it can apply config_content between args and
// wrapper.
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

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/profiles/ -run 'TestValidateRejectsWrapperArgsTemplateMissingPlaceholder|TestApplyWrapperErrorsWhenArgsTemplateMissingPlaceholder' -v`
Expected: FAIL (`Validate` doesn't check the placeholder yet; `ApplyWrapper` doesn't either — it would currently succeed, dropping the args silently)
Also expected: the rest of `apply_test.go` fails to compile, since `ApplyToCmd` doesn't exist as a test target anymore until Step 3 also updates `apply.go` — run both files' fix together before re-testing (Step 3 below), or run `go vet ./internal/profiles/...` first to confirm the only compile error is the now-removed `ApplyToCmd` symbol.

- [ ] **Step 3: Implement the fix**

Replace the entire contents of `wt/internal/profiles/apply.go` with:

```go
// wt/internal/profiles/apply.go
package profiles

import (
	"fmt"
	"os/exec"
	"slices"
)

// ApplyEnvAndArgs appends rp.Env (last value wins on a duplicate key,
// matching exec.Cmd.Env's documented behavior) and rp.ExtraArgs (appended
// after everything cmd.Args already carries) to cmd. It never touches
// Wrapper: cmd/wt's applyResolvedProfile calls ApplyConfigContent between
// this and ApplyWrapper, so a codex profile's own declared args land
// before config_content's "--profile agent-wt-profile" flag, and the
// wrapper — applied last — still captures everything appended before it in
// its {{args}} splice.
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
// into the "{{args}}" slot in w.ArgsTemplate. Rejects an ArgsTemplate with
// no "{{args}}" placeholder at all: silently building newArgs from only
// the literal tokens would drop the launched agent's entire original argv
// (e.g. --model, worktree flags) with no error anywhere. Validate also
// rejects this at profiles.toml load time — this check exists too so any
// other caller building a ResolvedProfile by hand (bypassing Validate) is
// still protected.
func ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
	if !slices.Contains(w.ArgsTemplate, "{{args}}") {
		return fmt.Errorf("profile wrapper %q: args_template %v has no \"{{args}}\" placeholder — the launched agent's own arguments would be silently dropped", w.Binary, w.ArgsTemplate)
	}
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

In `wt/internal/profiles/validate.go`, add `"slices"` to the import block (`import ("errors"; "fmt"; "slices"; "strings")`), then insert this check right after the existing mechanism-mismatch loop inside `Validate`'s `for i, p := range store.Profiles` loop (i.e. immediately after the closing `}` of `for _, mech := range usedMechanisms(p) { ... }`, still inside the outer loop):

```go
		if p.Wrapper != nil && !slices.Contains(p.Wrapper.ArgsTemplate, "{{args}}") {
			problems = append(problems, fmt.Sprintf(
				"profiles.toml[%d] (agent=%s, match=%s): wrapper args_template %v has no \"{{args}}\" placeholder — the launched agent's own arguments would be silently dropped",
				i, p.Agent, p.Match, p.Wrapper.ArgsTemplate))
		}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/profiles/... -v && go vet ./internal/profiles/...`
Expected: PASS

- [ ] **Step 5: Check for any other reference to the deleted ApplyToCmd**

Run: `cd wt && grep -rn "ApplyToCmd" --include="*.go" .`
Expected: no output (the symbol has no other callers — confirmed during code review; if this finds anything, update that call site to call `ApplyEnvAndArgs` then `ApplyWrapper` directly, matching `cmd/wt/launch.go`'s existing `applyResolvedProfile`).

- [ ] **Step 6: Commit**

```bash
git add wt/internal/profiles/apply.go wt/internal/profiles/apply_test.go wt/internal/profiles/validate.go wt/internal/profiles/validate_test.go
git commit -m "fix(profiles): reject a wrapper profile that drops the launched agent's args, remove unused ApplyToCmd"
```

---

### Task 4: Fix `wt profile on/off` corrupting profiles.toml when the enabled line has a trailing comment

**Files:**
- Modify: `wt/cmd/wt/profile.go:168-195` (`topLevelEnabledLineRe`, `setEnabledLine`)
- Test: `wt/cmd/wt/profile_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `setEnabledLine` now correctly locates and replaces an `enabled = true|false` line that carries a same-line trailing `#` comment, preserving that comment. No signature change.

**Bug:** `topLevelEnabledLineRe` (`^enabled\s*=\s*(true|false)\s*$`) requires the line to end immediately after `true`/`false`. A hand-edited `enabled = true  # keep local profiles on` line does not match, so `setEnabledLine` falls through to PREPENDING a brand-new `enabled = false` line ahead of the untouched original — producing a file with two top-level `enabled` keys, which `BurntSushi/toml` rejects on the next `Load` with "Key 'enabled' has already been defined," permanently breaking `wt profile on/off` on that file until manually repaired.

- [ ] **Step 1: Write the failing test**

Add to `wt/cmd/wt/profile_test.go` (after `TestProfileToggleInsertsEnabledLineWhenAbsent`):

```go
// TestProfileToggleHandlesTrailingCommentOnEnabledLine is the regression
// lock for the code-review finding that setEnabledLine produced invalid
// TOML (two conflicting top-level `enabled` keys) when the existing
// `enabled = ...` line carried a trailing comment: the old regex required
// the line to end immediately after true/false, so it fell through to
// PREPENDING a brand-new enabled line ahead of the untouched original,
// leaving both in the file — which BurntSushi/toml then rejects as a
// duplicate key on the next Load.
func TestProfileToggleHandlesTrailingCommentOnEnabledLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "agent-wt"), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `enabled = true  # keep local profiles on

[[profiles]]
agent = "claude"
match = "location"
location = "local"
`
	path := filepath.Join(dir, "agent-wt", "profiles.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	store, err := profiles.Load()
	if err != nil {
		t.Fatal(err)
	}
	a := &app{profiles: store}
	off := profileCmd(a)
	off.SetArgs([]string{"off"})
	if err := off.Execute(); err != nil {
		t.Fatalf("off: execute error = %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if strings.Count(got, "enabled = ") != 1 {
		t.Fatalf("profiles.toml after toggle has %d 'enabled = ' occurrences, want exactly 1 (no duplicate top-level key): %q", strings.Count(got, "enabled = "), got)
	}
	if !strings.Contains(got, "enabled = false") {
		t.Errorf("profiles.toml after toggle = %q, want enabled = false", got)
	}
	if !strings.Contains(got, "# keep local profiles on") {
		t.Errorf("profiles.toml after toggle = %q, want the trailing comment preserved", got)
	}

	reloaded, err := profiles.Load()
	if err != nil {
		t.Fatalf("profiles.toml is not valid TOML after toggle: %v", err)
	}
	if reloaded.Enabled {
		t.Error("reloaded.Enabled = true, want false")
	}
	if len(reloaded.Profiles) != 1 {
		t.Errorf("reloaded.Profiles = %v, want the one entry preserved", reloaded.Profiles)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./cmd/wt/ -run TestProfileToggleHandlesTrailingCommentOnEnabledLine -v`
Expected: FAIL (`profiles.Load()` after toggle errors — duplicate `enabled` key)

- [ ] **Step 3: Implement the fix**

In `wt/cmd/wt/profile.go`, replace both `topLevelEnabledLineRe` and `setEnabledLine` with:

```go
// topLevelEnabledLineRe matches a top-level `enabled = true|false` line,
// with or without a trailing same-line `#` comment (capture group 1).
// Profile's Go struct (internal/profiles.Profile) has no field named
// "enabled", so this can only ever match the file's own top-level toggle —
// but the search is still scoped to the text BEFORE the first
// "[[profiles]]" table header (see setEnabledLine) so a future field
// addition, or a profile entry with stray top-level-looking text in a
// string value, can never be mistaken for the toggle line. The trailing
// group uses [ \t] (not \s) so it can never cross a newline and swallow an
// unrelated comment on the FOLLOWING line (e.g. one documenting the first
// [[profiles]] entry below it).
var topLevelEnabledLineRe = regexp.MustCompile(`(?m)^enabled\s*=\s*(?:true|false)([ \t]*#[^\n]*)?$`)

// setEnabledLine returns content with its top-level `enabled = ...` line
// set to want, editing only that one line (or inserting one at the top
// when absent) — everything else in the file (comments, [[profiles]]
// entries, their own formatting) is byte-for-byte untouched, including a
// same-line trailing comment on the enabled line itself (preserved via
// topLevelEnabledLineRe's capture group — a naive regex without it used to
// fail to match such a line at all, falling through to PREPENDING a second
// enabled line and producing invalid, duplicate-key TOML). The search for
// an existing line is scoped to the text before the first "[[profiles]]"
// occurrence, so a hypothetical profile field that also happened to be
// named "enabled" inside a table body could never be mismatched (Profile
// has no such field today; this is defense in depth).
func setEnabledLine(content string, want bool) string {
	newLine := fmt.Sprintf("enabled = %t", want)
	head, tail := content, ""
	if i := strings.Index(content, "[[profiles]]"); i >= 0 {
		head, tail = content[:i], content[i:]
	}
	if loc := topLevelEnabledLineRe.FindStringSubmatchIndex(head); loc != nil {
		trailing := ""
		if loc[2] != -1 {
			trailing = head[loc[2]:loc[3]]
		}
		return head[:loc[0]] + newLine + trailing + head[loc[1]:] + tail
	}
	return newLine + "\n\n" + head + tail
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/... -run TestProfile -v`
Expected: PASS, including the pre-existing `TestProfileTogglePreservesCommentsAndFormatting` (its `enabled = true` line has NO trailing comment, so `[ \t]*#[^\n]*` matches nothing there and behavior is unchanged) and `TestProfileToggleInsertsEnabledLineWhenAbsent`.

- [ ] **Step 5: Commit**

```bash
git add wt/cmd/wt/profile.go wt/cmd/wt/profile_test.go
git commit -m "fix(profile): handle a trailing comment on profiles.toml's enabled line"
```

---

### Task 5: Extract the duplicated apply→run→cleanup sequence into a shared helper

**Files:**
- Create: `wt/internal/agents/run.go`
- Create: `wt/internal/agents/run_test.go`
- Modify: `wt/cmd/wt/launch.go` (`runAgentCmd`, imports)
- Modify: `wt/internal/tui/launch.go` (`runAndWaitCmd`, imports)

**Interfaces:**
- Consumes: nothing new.
- Produces: `agents.RunAndCleanup(cmd *exec.Cmd, cleanup func() error) (time.Duration, error)` — runs `cmd`, always calls `cleanup` after (success or failure, warning to stderr on a cleanup failure), and returns the elapsed duration plus `cmd.Run()`'s own error.

**Issue fixed:** The "apply profile → run → cleanup" core is hand-written identically in both `cmd/wt/launch.go`'s `runAgentCmd` and `internal/tui/launch.go`'s `runAndWaitCmd` (two different packages, connected only by the `ProfileApplier` closure type). A future change to this exact sequence (e.g. how a cleanup failure is reported) has to be applied by hand in both places. Only the `cmd.Run()` + `cleanup()` + duration-measurement part is genuinely identical between the two — everything before it (applying the profile, wiring stdio) and after it (summary formatting, survey, stop picker) differs enough between the TUI's capture-then-emit alt-screen pattern and the non-TUI path to stay separate, so only that shared core is extracted.

- [ ] **Step 1: Write the failing test**

Create `wt/internal/agents/run_test.go`:

```go
// wt/internal/agents/run_test.go
package agents

import (
	"errors"
	"os/exec"
	"testing"
)

// TestRunAndCleanupCallsCleanupAfterRunRegardlessOfOutcome verifies
// RunAndCleanup always invokes cleanup exactly once after cmd.Run()
// returns, whether the command succeeds or fails — the shared
// apply->run->cleanup sequence duplicated between cmd/wt's runAgentCmd and
// internal/tui's runAndWaitCmd depends on cleanup running unconditionally
// so a profile's config_content file is always restored.
func TestRunAndCleanupCallsCleanupAfterRunRegardlessOfOutcome(t *testing.T) {
	calls := 0
	cleanup := func() error { calls++; return nil }

	if _, err := RunAndCleanup(exec.Command("true"), cleanup); err != nil {
		t.Fatalf("RunAndCleanup() error = %v, want nil for a successful command", err)
	}
	if calls != 1 {
		t.Errorf("cleanup called %d times after success, want 1", calls)
	}

	if _, err := RunAndCleanup(exec.Command("false"), cleanup); err == nil {
		t.Fatal("RunAndCleanup() error = nil, want the command's own exit error")
	}
	if calls != 2 {
		t.Errorf("cleanup called %d times total, want 2 (once per Run, including the failing one)", calls)
	}
}

// TestRunAndCleanupNeverPromotesCleanupErrorOverCommandError verifies a
// cleanup failure is reported as a warning, never returned as
// RunAndCleanup's own error — cmd.Run()'s result always wins, matching
// both existing call sites' behavior before this helper was extracted.
func TestRunAndCleanupNeverPromotesCleanupErrorOverCommandError(t *testing.T) {
	cleanupErr := errors.New("cleanup boom")
	_, err := RunAndCleanup(exec.Command("true"), func() error { return cleanupErr })
	if err != nil {
		t.Errorf("RunAndCleanup() error = %v, want nil (a successful command's result, not the cleanup error)", err)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/agents/ -run TestRunAndCleanup -v`
Expected: FAIL to compile (`RunAndCleanup` undefined)

- [ ] **Step 3: Implement RunAndCleanup**

Create `wt/internal/agents/run.go`:

```go
// wt/internal/agents/run.go
package agents

import (
	"fmt"
	"os"
	"os/exec"
	"time"
)

// RunAndCleanup runs cmd, then unconditionally calls cleanup (success or
// failure) before returning cmd.Run()'s own error and the elapsed
// duration. It is the one piece of the apply-profile -> run -> cleanup
// sequence that was hand-written identically in both cmd/wt's runAgentCmd
// and internal/tui's runAndWaitCmd; everything before it (applying the
// profile, wiring stdio) and after it (summary, survey, stop picker) still
// differs enough between the TUI and non-TUI launch paths — the TUI's
// capture-then-emit pattern for the alt-screen buffer — to stay separate.
// A cleanup failure is reported to stderr as a warning, never promoted to
// the returned error: cmd.Run()'s own result always wins, matching both
// callers' pre-existing behavior. cleanup must never be nil — pass
// func() error { return nil } when no profile layer applied.
func RunAndCleanup(cmd *exec.Cmd, cleanup func() error) (time.Duration, error) {
	start := time.Now()
	err := cmd.Run()
	if cerr := cleanup(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
	}
	return time.Since(start), err
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `cd wt && go test ./internal/agents/ -run TestRunAndCleanup -v`
Expected: PASS

- [ ] **Step 5: Wire it into cmd/wt/launch.go**

In `wt/cmd/wt/launch.go`, replace:

```go
	start := time.Now()
	err := cmd.Run()
	if cerr := profileCleanup(); cerr != nil {
		fmt.Fprintf(os.Stderr, "warning: profile cleanup failed: %v\n", cerr)
	}
	summary := agents.Summary(agent, m, time.Since(start))
```

with:

```go
	duration, err := agents.RunAndCleanup(cmd, profileCleanup)
	summary := agents.Summary(agent, m, duration)
```

Then remove `"time"` from that file's import block — it is unused elsewhere in `cmd/wt/launch.go` after this change (verify with `goimports`/`go vet`; if any other use remains, keep the import instead of removing it).

- [ ] **Step 6: Wire it into internal/tui/launch.go**

In `wt/internal/tui/launch.go`, replace:

```go
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
		pendingSummary = agents.Summary(agent, m, time.Since(start))
```

with:

```go
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if profileCleanup == nil {
			profileCleanup = func() error { return nil }
		}
		duration, err := agents.RunAndCleanup(cmd, profileCleanup)
		pendingSummary = agents.Summary(agent, m, duration)
```

Then remove `"time"` from that file's import block — it is unused elsewhere in `internal/tui/launch.go` after this change (verify with `go vet`; if any other use remains, keep the import instead of removing it).

- [ ] **Step 7: Run the full test suite to verify nothing broke**

Run: `cd wt && go build ./... && go vet ./... && go test ./...`
Expected: PASS, clean build, no vet warnings (in particular, no "imported and not used" for `time` in either modified file, and no unused-import warnings from the removal).

- [ ] **Step 8: Commit**

```bash
git add wt/internal/agents/run.go wt/internal/agents/run_test.go wt/cmd/wt/launch.go wt/internal/tui/launch.go
git commit -m "refactor(agents): extract the shared apply-run-cleanup core out of the TUI and non-TUI launch paths"
```

---

### Task 6: Refresh stale profiles.md and wt/CLAUDE.md documentation

**Files:**
- Modify: `wt/docs/wt-agents/profiles.md`
- Modify: `wt/CLAUDE.md`

**Interfaces:** None (docs only).

**Issues fixed:**
1. `profiles.md`'s confirm-prompt section still calls the TUI picker a pending follow-up ("or the picker once the TUI follow-up lands"), but this feature branch already wires profile application into the TUI launch path (`internal/tui/launch.go`'s `ProfileApplier`/`runAndWaitCmd`).
2. `wt/CLAUDE.md`'s "Test seams" list is presented as an enumeration of every seam var, but omits the four this feature adds: `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`.
3. `wt/CLAUDE.md`'s package list and module table document every other `internal/` package and `cmd/wt/*.go` file with an entry, but have none for the new `internal/profiles/` package or `cmd/wt/profile.go`.

- [ ] **Step 1: Fix profiles.md's stale TUI note**

In `wt/docs/wt-agents/profiles.md`, replace:

```
An interactive launch (`wt -A <agent> -M <model>`, or the picker once the
TUI follow-up lands) that resolves a non-empty profile asks before
applying it — default **yes** on a bare Enter. A non-interactive launch
(no controlling terminal: scripts, CI) applies automatically with no
prompt.
```

with:

```
An interactive launch (`wt -A <agent> -M <model>`, or the worktree/agent/
model picker) that resolves a non-empty profile asks before applying it —
default **yes** on a bare Enter. A non-interactive launch (no controlling
terminal: scripts, CI) applies automatically with no prompt.
```

- [ ] **Step 2: Add the four new seams to wt/CLAUDE.md's Test seams list**

In `wt/CLAUDE.md`, find the "Test seams" paragraph. Replace:

```
`newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`, `pickModelTUI`, `pickStartModelTUI`, `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`) — production code calls the var, tests swap it.
```

with:

```
`newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`, `pickModelTUI`, `pickStartModelTUI`, `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`) — production code calls the var, tests swap it.
```

- [ ] **Step 3: Add `profiles` to the package list**

In `wt/CLAUDE.md`, replace:

```
Package list: `internal/{config,rotation,usage,refcount,survey,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,smoke}`, `cmd/wt`.
```

with:

```
Package list: `internal/{config,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,smoke}`, `cmd/wt`.
```

- [ ] **Step 4: Add rows for cmd/wt/profile.go and internal/profiles/ to the module table**

In `wt/CLAUDE.md`'s module table, replace:

```
| `cmd/wt/smoke.go` | `wt smoke` command — one-shot model×agent smoke test, human/JSON output |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
```

with:

```
| `cmd/wt/smoke.go` | `wt smoke` command — one-shot model×agent smoke test, human/JSON output |
| `cmd/wt/profile.go` | `wt profile list/show/status/on/off` — inspect and toggle `internal/profiles`; owns `setEnabledLine`'s surgical `enabled = ...` line edit |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
```

Then replace:

```
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
```

with:

```
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell; `RunAndCleanup` (the shared apply-run-cleanup core for the TUI and non-TUI launch paths) |
| `internal/profiles/` | Local-model launch profile overlays (`wt profile ...`, see `docs/wt-agents/profiles.md`): `Load`/`Save`/`Validate`/`Resolve`; `ApplyEnvAndArgs`/`ApplyWrapper`/`ApplyConfigContent` apply a resolved profile to an `exec.Cmd`; `SelfHeal` restores an orphaned config_content backup left by a session that never cleaned up |
```

- [ ] **Step 5: Verify links and formatting**

Run: `cd wt && make check-links 2>/dev/null || uv run ../bin/check-links` (from the monorepo root if the `wt` package's own Makefile has no `check-links` target — see the monorepo root `Makefile`'s `check-links` target) to confirm no broken references were introduced. If neither is easily runnable from this checkout, visually confirm the two doc files still render as valid markdown (matched code spans, no dangling backticks) instead.

- [ ] **Step 6: Commit**

```bash
git add wt/docs/wt-agents/profiles.md wt/CLAUDE.md
git commit -m "docs(wt): refresh profiles.md and CLAUDE.md for the landed TUI wiring and internal/profiles package"
```

---

## Final verification

After all six tasks:

```bash
cd wt
gofmt -l .            # expect no output
go vet ./...
go build ./...
go test ./...
```

All must pass cleanly before considering this plan complete.
