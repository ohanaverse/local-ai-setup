# Reject Removed `wt models` / `wt agents` Subcommands

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `wt models` and `wt agents` exit non-zero with a helpful migration message instead of silently creating a worktree named "models" or "agents".

**Architecture:** Add a `RunE` guard that mirrors the existing `-w` legacy-flag rejection pattern (`main.go:60-62`). When the first positional arg is a known-removed subcommand name (`models`, `agents`), return an error that names `wt config` as the replacement. The `cobra.ArbitraryArgs` setting stays — it's load-bearing for `shell-wt` passthrough (`shell-wt ls -la` → `wt --agent shell ls -la`).

**Tech Stack:** Go 1.26.3, cobra/pflag (existing), `go test`.

**Spec:** Code review of PR #79 — finding "plan/code mismatch on `wt models` / `wt agents` behavior" (verdict: CONFIRMED, see `/private/tmp/claude-502/.../a0a162606814dd60b.output`).

**Implements plan:** `docs/superpowers/plans/2026-08-21-wt-config-viewer.md` Task 14 (which specified the test but not the rejection mechanism — this plan delivers both).

## Global Constraints

- Tests must use the existing `rootCmd()` / `cmd.SetArgs()` / `cmd.SetOut` pattern from `cmd/wt/main_test.go`.
- Tests must NOT require a TTY, a git repo, or a valid config (the rejection should fire before any of those checks).
- The migration message must be actionable: name the removed command AND the replacement (`wt config`).
- Keep the `cobra.ArbitraryArgs` setting (it is load-bearing for `shell-wt` passthrough; see comment at `main.go:47-52`).
- Every `Test*` must have a top-level `//` comment stating **what** it tests and **why** it matters (per `docs/go-course/lesson-18-testing.md` and repo-wide convention).

---

### Task 1: Reject `wt models` and `wt agents` in root `RunE`

**Files:**
- Modify: `cmd/wt/main.go:54-62` (insert rejection in root `RunE`, before `--check-guard` block at line 64)
- Test: `cmd/wt/main_test.go` (append two test functions)

**Interfaces:**
- Consumes: nothing new (operates on `args []string` already in scope inside `RunE`).
- Produces: returns `error` with message `wt models is removed; use \`wt config\` to view models` (or `wt agents`).

- [ ] **Step 1: Write the failing tests**

Append to `cmd/wt/main_test.go` (after the existing `TestPickerSkippedOnWorktreeFlag` at line 95, before the closing of the file):

```go
// TestRemovedSubcommand_Rejected verifies that `wt models` and `wt agents`
// exit non-zero with a migration message pointing at `wt config`. Without
// this guard, cobra's ArbitraryArgs (required for shell-wt passthrough)
// silently swallows the unknown first positional and the root RunE
// creates a worktree literally named "models" or "agents" — a silent
// footgun for users with muscle-memory invocations or stale doc snippets.
func TestRemovedSubcommand_Rejected(t *testing.T) {
	cases := []struct {
		name        string
		args        []string
		wantInError string
	}{
		{"wt models", []string{"models"}, "wt models is removed"},
		{"wt models with flags", []string{"models", "-A", "claude"}, "wt models is removed"},
		{"wt agents", []string{"agents"}, "wt agents is removed"},
		{"wt agents with flags", []string{"agents", "-A", "codex"}, "wt agents is removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			root := rootCmd()
			root.SetOut(&buf)
			root.SetErr(&buf)
			root.SetArgs(tc.args)
			err := root.Execute()
			if err == nil {
				t.Fatalf("expected error for %v, got nil (output: %q)", tc.args, buf.String())
			}
			if !strings.Contains(err.Error(), tc.wantInError) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantInError)
			}
			// Must point users at the replacement.
			if !strings.Contains(err.Error(), "wt config") {
				t.Errorf("error %q does not mention `wt config` as the replacement", err.Error())
			}
		})
	}
}

// TestShellPassthrough_StillWorks guards against an over-tightened Args
// validator regressing shell-wt. `wt --agent shell ls -la` (the form the
// shell-wt shim produces) must NOT be rejected by the new guard.
func TestShellPassthrough_StillWorks(t *testing.T) {
	// We only need to assert the new guard does not fire for "ls" — the
	// rest of the launch path will fail in this unit test environment
	// (no TTY, no config) but that's after the guard.
	oldWd, _ := os.Getwd()
	t.Cleanup(func() { _ = os.Chdir(oldWd) })
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("HOME", t.TempDir())
	var buf bytes.Buffer
	root := rootCmd()
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--agent", "shell", "ls", "-la"})
	err := root.Execute()
	if err == nil {
		// Tolerated: would mean a full launch path ran (won't in this env).
		return
	}
	if strings.Contains(err.Error(), "is removed") {
		t.Fatalf("shell passthrough was incorrectly rejected as removed subcommand: %v", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./cmd/wt -run TestRemovedSubcommand_Rejected -v`
Expected: FAIL. Current behavior — `rootCmd().Execute()` either:
- errors at `worktree.RepoRoot()` with "not in a git repo" (we're in the repo, so this won't fire), OR
- errors at `config.Load()` because `HOME` is empty (will produce a different error string), OR
- silently passes the first positional to a launch path.

In any of those cases, the test's `wantInError` substring ("wt models is removed") will not be present, so the assertion fails.

Run: `go test ./cmd/wt -run TestShellPassthrough_StillWorks -v`
Expected: PASS already (current code allows arbitrary args, so no removal error). This is the guard test — it would only fail if we over-tighten.

- [ ] **Step 3: Implement the rejection in `cmd/wt/main.go`**

In `cmd/wt/main.go`, inside the root `RunE` closure (currently starting at line 54), insert the guard immediately after the `legacyShortW` rejection (currently lines 60-62) and before the `--check-guard` block (currently line 64). The resulting code:

```go
RunE: func(cmd *cobra.Command, args []string) error {
	// Read the raw --agent flag early so --init can seed agent-specific
	// pointer files when a wrapper like claude-wt forwards --agent claude.
	agentFlag := mustGetString(cmd, "agent")

	// Legacy short flag rejection: `-w` was removed in favor of `-W`.
	if legacyShortW != "" {
		return fmt.Errorf("-w is removed; use -W or --worktree")
	}

	// Reject removed subcommands. `cobra.ArbitraryArgs` (set below) is
	// load-bearing for shell-wt passthrough (`shell-wt ls -la` →
	// `wt --agent shell ls -la`), so it swallows the first positional
	// without complaining. Without this guard, `wt models -A claude`
	// silently creates a worktree named "models" and launches claude
	// there — a footgun for users with stale muscle-memory invocations.
	// Keep this list in sync with removed subcommand names.
	if len(args) > 0 {
		switch args[0] {
		case "models":
			return fmt.Errorf("wt models is removed; use `wt config` to view models")
		case "agents":
			return fmt.Errorf("wt agents is removed; use `wt config` to view agents")
		}
	}
```

(The rest of the `RunE` is unchanged.)

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./cmd/wt -run 'TestRemovedSubcommand_Rejected|TestShellPassthrough_StillWorks' -v`
Expected: both PASS. `TestRemovedSubcommand_Rejected` passes because the guard returns the expected substring. `TestShellPassthrough_StillWorks` passes because `"ls"` is not in the removal list.

- [ ] **Step 5: Run the full test suite + vet**

Run: `go test ./... && go vet ./... && go build ./...`
Expected: all green. No other tests should regress — the only behavioral change is rejecting two specific first-positionals; all other paths (including the legacy `-w` test) are unchanged.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/main.go cmd/wt/main_test.go
git commit -m "fix(wt): reject removed \`wt models\` and \`wt agents\` subcommands

cobra.ArbitraryArgs (required for shell-wt passthrough) was silently
swallowing \`wt models\` and \`wt agents\` as positional args, causing
the root RunE to create worktrees named \"models\"/\"agents\" instead
of erroring. Add a guard in root RunE matching the existing -w legacy
flag pattern; tests cover both the rejection and that shell-wt
passthrough still works."
```

---

### Task 2: Add an explicit assertion to the PR's Test Plan

**Files:**
- Modify: PR #79 description (the `[x] Verified \`wt models\` and \`wt agents\` now return "unknown command" errors` checkbox).

**Note:** The PR body is on GitHub, not in the repo. Skip if PR is owned by the user and the false claim bothers them; otherwise this is a comment on the PR, not a code change. The "fix" task in the codebase stands on its own.

- [ ] **Step 1: Post a PR comment with the test output**

After Task 1 lands, on PR #79 post:

> The "Verified `wt models` and `wt agents` now return 'unknown command' errors" claim was inaccurate — `cobra.ArbitraryArgs` (required for `shell-wt` passthrough) silently swallowed them. Fixed in `<commit-sha>` with a `RunE` guard matching the existing `-w` legacy-flag pattern. New tests in `cmd/wt/main_test.go`: `TestRemovedSubcommand_Rejected` (4 sub-cases) and `TestShellPassthrough_StillWorks`.

(Adjust commit SHA once the commit is pushed.)

- [ ] **Step 2: No commit** — PR comments aren't versioned.

---

## Self-Review

1. **Spec coverage:** Code review finding is "test rejected, fix behavior." Task 1 delivers both (test + rejection). Task 2 covers the doc-rot claim. ✓
2. **Placeholder scan:** No TBDs, no "implement later." All code blocks are complete. ✓
3. **Type consistency:** `error` from `RunE` matches existing pattern; `args[0]` is a `string` (same as `-w` rejection's `legacyShortW` is a `string`). ✓

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-22-reject-removed-subcommands.md`. Two execution options:

1. **Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration
2. **Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

Which approach?
