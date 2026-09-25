# pi-local accept-all permissions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make pi sessions launched from the main repo (`pi-local` profile) skip little-coder's shell whitelist by setting `LITTLE_CODER_PERMISSION_MODE=accept-all` via the profile's env mechanism.

**Architecture:** Two parts. (1) A small wt change: the pi driver currently declares wrapper-only profile mechanisms, so profile `env` on a pi profile fails global validation and disables every profile. Add `profiles.MechanismEnv` to `piDriver.ProfileMechanisms()` — `cmd/wt/launch.go` already applies env to the launched process before the wrapper, and with the little-coder wrapper the launched process IS little-coder, which reads `LITTLE_CODER_PERMISSION_MODE`. (2) The config change: one `env` line in the pi-local profile block of `~/.config/agent-wt/profiles.toml`, verified with the newly installed wt.

**Tech Stack:** Go (wt module, TDD), TOML config (wt profiles), `wt profile list` / `wt profile show` CLI for verification.

**Spec:** `docs/superpowers/specs/2026-09-25-pi-local-accept-all-design.md` (amended on this branch with the scope change)

**Workspace:** worktree at `.worktrees/pi-profile-env-mechanism`, branch `pi-profile-env-mechanism`. Tasks 1–2 run in the worktree; Task 3 mutates the dotfile (`~/.config/agent-wt/profiles.toml`, outside any repo) after the new wt binary is installed.

---

### Task 1: wt — allow the env mechanism for pi profiles

**Files:**
- Modify: `wt/internal/agents/pi.go:67-72` (`ProfileMechanisms` + its comment)
- Test: `wt/internal/agents/` (existing pi tests; add mechanism coverage where the pattern lives — see claude/codex/opencode drivers and their tests for the established pattern)

- [ ] **Step 1: Write the failing test**

Follow the existing test pattern for driver profile mechanisms (see how claude/codex/opencode drivers' `ProfileMechanisms` are tested in `wt/internal/agents/`). Add a pi test asserting pi declares BOTH `MechanismWrapper` and `MechanismEnv`:

```go
func TestPiDriverProfileMechanisms(t *testing.T) {
	got := (piDriver{}).ProfileMechanisms()
	want := []profiles.Mechanism{profiles.MechanismWrapper, profiles.MechanismEnv}
	// compare order-insensitively or exactly, matching the package's
	// existing conventions for the other drivers' mechanism tests
}
```

Adapt names/assertion style to the existing tests — match, don't invent. If the other drivers' mechanisms are tested via a shared table, add pi to that table with both mechanisms.

- [ ] **Step 2: Run it to verify it fails**

Run: `cd wt && go test ./internal/agents/ -run ProfileMechanisms -v`
Expected: FAIL — pi reports only `MechanismWrapper`.

- [ ] **Step 3: Implement**

In `wt/internal/agents/pi.go`, replace:

```go
// ProfileMechanisms declares that pi only accepts a wrapper profile — the
// little-coder integration; pi has no env/config-file lever of its own
// for this.
func (piDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismWrapper}
}
```

with:

```go
// ProfileMechanisms declares the profile mechanisms pi supports: wrapper
// (the little-coder integration) and env. There is no pi-specific env
// lever, but profile env lands on the launched process (cmd/wt applies
// env before the wrapper), and with the little-coder wrapper that process
// is little-coder itself — which reads LITTLE_CODER_* env vars.
func (piDriver) ProfileMechanisms() []profiles.Mechanism {
	return []profiles.Mechanism{profiles.MechanismWrapper, profiles.MechanismEnv}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/agents/ ./internal/profiles/ ./cmd/wt/`
Expected: PASS (all three packages).

- [ ] **Step 5: Vet + full wt test suite**

Run: `cd wt && go vet ./... && go test ./...`
Expected: clean vet, all tests pass.

- [ ] **Step 6: Install the new wt binary**

Run: `cd wt && make install`
Expected: wt binary rebuilt and installed on PATH. Sanity: `wt profile list` still lists all four profiles with no validation warning (profiles.toml is unchanged at this point).

- [ ] **Step 7: Commit**

```bash
git add wt/internal/agents/pi.go wt/internal/agents/<test-file>
git commit -m "wt: allow env profiles for pi (wrapper+env); env lands on little-coder"
```

### Task 2: Amend spec + plan docs on the branch

**Files:**
- Modify: `docs/superpowers/specs/2026-09-25-pi-local-accept-all-design.md` (add scope-change note: wt pi driver now declares env; replace the "no repo code is touched" claim)
- Modify: this plan file (already amended — commit it; it was previously uncommitted)

- [ ] **Step 1: Commit the doc amendments**

```bash
git add docs/superpowers/specs/2026-09-25-pi-local-accept-all-design.md docs/superpowers/plans/2026-09-25-pi-local-accept-all.md
git commit -m "docs: pi-local accept-all — scope change (wt env mechanism for pi)"
```

### Task 3: Re-apply the profile env line and verify

**Files:**
- Modify: `~/.config/agent-wt/profiles.toml` (first `[[profiles]]` block, `agent = "pi"`) — dotfile, no commit

- [ ] **Step 1: Back up the dotfile**

```bash
cp ~/.config/agent-wt/profiles.toml ~/.config/agent-wt/profiles.toml.bak.$(date +%Y%m%d-%H%M%S)
```

- [ ] **Step 2: Confirm pre-state**

```bash
grep -n -A5 'agent = "pi"' ~/.config/agent-wt/profiles.toml
```

Expected: the block with `wrapper = { binary = "little-coder", args_template = ["{{args}}"] }` and no `env` line.

- [ ] **Step 3: Make the edit**

The `agent = "pi"` block reads, after the edit:

```toml
[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["{{args}}"] }
env = { LITTLE_CODER_PERMISSION_MODE = "accept-all" }
```

Nothing else changes — claude/codex/opencode blocks stay untouched.

- [ ] **Step 4: Verify TOML parses, no validation warning**

```bash
wt profile list
```

Expected: all four profiles listed; NO `(profiles disabled …)` line.

- [ ] **Step 5: Verify resolution applies the env (dry-run, no launch)**

```bash
wt profile show -A pi -M ollama/ornith-1.5:35b
```

Expected:

```
env: LITTLE_CODER_PERMISSION_MODE=accept-all
wrapper: little-coder [{{args}}]
```

- [ ] **Step 6: Negative check — cloud model still matches nothing**

```bash
wt profile show -A pi -M ollama/glm-5.3-flash:cloud
```

Expected: `(no matching profile)`.

---

## Self-Review

- **Spec coverage:** Decision (env line) → Task 3 Step 3. New scope (wt env mechanism for pi, approved by user 2026-09-25) → Task 1. Verification (list, show with local model, cloud negative) → Task 3 Steps 4–6. Scope note (other agents/worktree launches untouched) → Task 3 Step 3's "nothing else changes".
- **Placeholder scan:** Task 1 Step 1 intentionally points at the existing test pattern rather than inventing one — the instruction is to match the package's established conventions (claude/codex/opencode already have mechanism tests to copy). All other steps have exact commands/content.
- **Consistency:** env key/value `LITTLE_CODER_PERMISSION_MODE=accept-all` matches spec; model ids match the live registry (verified); branch/worktree names match.
