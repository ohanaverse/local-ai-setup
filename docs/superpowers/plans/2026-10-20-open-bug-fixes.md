# Open wt Bug Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix #398 (dead `SlotProfile.FilledOrder`), #401 (nested `config_content` restore drops session writes), #402 (claude local profile overrides the user's `defaultMode`) and #388 (picker tags remote rows with another remote's PR).

**Architecture:** Three PRs, each on its own branch from `main` in its own worktree (`.worktrees/`). PR 2 is two stacked PRs: the `.keys` backup marker moves from top-level key names to leaf records (path + hash of the written value), restore and delete work per leaf, then a new `config_defaults` table writes only absent leaves through the same records. PR 3 replaces the picker's `map[string]PR` with a `PRSet` that also carries the base repo `gh pr list` reports, and tags a remote row only when its remote resolves to that repo.

**Tech Stack:** Go 1.26.7 (`wt/`, module `github.com/ohanaverse/local-ai-setup/wt`), `encoding/json`, `crypto/sha256`, BurntSushi/toml, bubbletea list model, `gh` CLI (JSON only; never run from tests).

**Spec:** `docs/superpowers/specs/2026-10-20-open-bug-fixes-design.md`. One refinement to it, made after reading the code: #388 keys PRs by branch plus one `Repo` per listing (`PRSet`), not by a (repo, branch) map. Only same-repo PRs are kept today (`parsePRs` skips cross-repository ones), so every kept PR's head repo is the base repo, and the result is the same with far less churn. #387 will widen it.

## Global Constraints

- Run from `wt/`: `go build ./... && go vet ./... && go test ./...` must pass at the end of every task that changes Go code. The repo-wide gate is `make test-all` from the repo root before each PR.
- Do not change `mergeInto`'s semantics (`wt/internal/profiles/merge.go`); `resolve.go` and the opencode env merge rely on them.
- codex (TOML, whole-file restore) and opencode (env merge, no restore) behavior does not change.
- "Absent" for `config_defaults` means absent from `<worktree>/.claude/settings.local.json` only. `.claude/settings.json` and `~/.claude/settings.json` are never read.
- When the session changed a pinned leaf after the merge, restore keeps the session's value.
- The `.keys` marker stores a SHA-256 hash of each written value, never the value (config values can hold env secrets).
- No test may reach the developer's GitHub account or real config home: use `claudeTarget(t, ...)` (profiles) and the existing `StubPRListForTest` (worktree, tui).
- Commit messages end with the Co-Authored-By line naming the model that wrote the commit; PR bodies end with `🤖 Generated with [Claude Code](https://claude.com/claude-code)`.
- Guides never embed live model state (`docs/guides/`); these changes touch `wt/docs/` only.

## Review Focus

Inputs and conditions the spec implies but the obvious tests would miss, most likely first. Each has a pinned test in the task named in brackets.

1. A session adds `permissions.allow` while the original file holds a scalar at `permissions`: the profile's leaf must not outlive the session and the scalar must come back. [Task 3: `TestRestoreTypeConflictKeepsSessionSiblingAndRestoresScalar`]
2. A crash straddling an upgrade leaves a legacy `.keys` marker (a JSON array of top-level names): self-heal must still restore. [Task 2: `TestDecodeLeafRecsAcceptsLegacyKeyList`; Task 3: `TestSelfHealRestoresLegacyKeysMarker`]
3. The user's `defaultMode` already equals the default, or is `auto`: nothing is written, recorded or reverted. [Task 6: `TestConfigDefaultsLeavesAnExistingValueAlone`]
4. Remote rows whose remote is unknown (a stale `refs/remotes/<gone>/x`), whose name contains `/`, or whose URL is ssh/scp/`.git`/mixed-case must tag only when truly the base repo. [Task 8: `TestRepoFromURLForms`, `TestSplitRemoteBranchLongestRemoteWins`; Task 9: `TestRemoteRowUnknownRemoteIsUntagged`]
5. `#<num>` filter typed before the PRs arrive, or a stale filter run, on remote rows: tags and matches must agree. [Task 9: `TestStaleFilterRunWithRemoteRowsKeepsTheTags`]

## File Structure

| File | Responsibility | Tasks |
|---|---|---|
| `wt/internal/profiles/slot.go`, `slot_test.go`, `slot_fixture_test.go` | #398 | 1 |
| `wt/internal/profiles/leaves.go` (new) | leaf records: collect, hash, path get/set, marker encode/decode, revert | 2, 3, 4 |
| `wt/internal/profiles/leaves_test.go` (new) | unit tests for the above | 2, 3, 4 |
| `wt/internal/profiles/configcontent.go` | write side and restore wiring | 3, 6 |
| `wt/internal/profiles/profiles.go`, `resolve.go`, `validate.go`, `internal/agents/claude.go`, `cmd/wt/profile.go` | `config_defaults` plumbing | 5 |
| `wt/internal/worktree/prs.go`, `remotes.go` (new), `enumerate.go` | `PRSet`, remote repo resolution, `Entry.RemoteRepo`/`RemoteBranch` | 7, 8 |
| `wt/internal/tui/app.go`, `worktree_list.go` | picker wiring | 7, 9 |
| `wt/docs/wt-agents/profiles.md`, `wt-agents/README.md`, `docs/internals/*.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md` | docs | 3, 6, 9 |

---

## PR 1: #398 (branch `fix/slotprofile-filledorder`)

### Task 1: Delete `SlotProfile.FilledOrder`

**Files:**
- Modify: `wt/internal/profiles/slot.go:38-46` (delete the method, keep `filledOrderFrom`)
- Modify: `wt/internal/profiles/slot_test.go:240-300` (`TestFilledOrder`)
- Modify: `wt/internal/profiles/slot_fixture_test.go:69`

**Interfaces:**
- Consumes: `SlotCheck.FilledOrder() []string` (existing; resolves `opusplan` to the working slot first).
- Produces: `SlotProfile` loses `FilledOrder`; nothing else in the repo calls it.

- [ ] **Step 1: Create the worktree and branch.** From the repo root: `git worktree add .worktrees/slotprofile-filledorder -b fix/slotprofile-filledorder main`; work there for this task.
- [ ] **Step 2: Repoint the tests (they fail to compile once the method is gone, so do this first and keep them green).** In `TestFilledOrder`, call `SlotCheck{Profile: tc.profile, Declared: tc.declared}.FilledOrder()`; the seven cases keep their expectations because `RestSlot` falls back to `Profile.Default` when `DefaultSlot` is empty. Add one case, default `opusplan` filling `opus`, `sonnet`, `haiku`, with `DefaultSlot: "sonnet"`, expecting `["sonnet", "opus", "haiku"]`. In `slot_fixture_test.go:69` replace `checks[0].Profile.FilledOrder(checks[0].Declared)` with `checks[0].FilledOrder()`.
- [ ] **Step 3: Run** `cd wt && go test ./internal/profiles -run 'TestFilledOrder|Fixture' -v`. Expected: PASS.
- [ ] **Step 4: Delete `SlotProfile.FilledOrder`** and move its first two comment lines (declared order, filled-slot filter) onto `filledOrderFrom`; drop the sentence that points to `SlotCheck.FilledOrder`.
- [ ] **Step 5: Verify no caller is left:** `grep -rn "\.FilledOrder(" wt --include='*.go'` shows only `SlotCheck` callers (`tui/layout.go`, `catalog/profile.go`, tests). Run `cd wt && go build ./... && go vet ./... && go test ./internal/profiles ./internal/catalog ./internal/tui`. Expected: PASS.
- [ ] **Step 6: Commit** `fix(wt): remove SlotProfile.FilledOrder, which has no caller and misorders an opusplan default (#398)`, then push and open the PR (`gh pr create`) after `make test-all` passes.

---

## PR 2a: #401 (branch `fix/config-content-leaf-restore`)

### Task 2: Leaf records and the marker format

**Files:**
- Create: `wt/internal/profiles/leaves.go`, `wt/internal/profiles/leaves_test.go`

**Interfaces:**
- Produces (all in package `profiles`, used by Tasks 3, 4, 6):
  - `type leafRec struct { Path []string \`json:"path"\`; Hash string \`json:"hash,omitempty"\` }`. `Hash == ""` marks a legacy record: restore unconditionally.
  - `func collectLeaves(doc, content map[string]any) []leafRec`: the leaves `mergeInto(doc, content)` will write. A leaf is any value that is not a non-empty `map[string]any`. An empty `{}` is a leaf only when `doc` lacks the key or holds a non-object there (merging `{}` into an existing object is a no-op and records nothing). `doc` is read before the merge and not changed.
  - `func hashLeaf(v any) string`: hex SHA-256 of `json.Marshal(v)`.
  - `func getPath(doc map[string]any, path []string) (v any, ok bool)`: `ok=false` when a segment is missing or an intermediate is not an object. `null` is `ok=true, v=nil`.
  - `func encodeLeafRecs(recs []leafRec) ([]byte, error)` and `func decodeLeafRecs(raw []byte) (recs []leafRec, ok bool)`; decode also accepts the legacy `["a","b"]` list as one-segment records with empty `Hash`; `ok=false` for anything else.

- [ ] **Step 1: Branch.** `git worktree add .worktrees/config-content-leaf-restore -b fix/config-content-leaf-restore main`.
- [ ] **Step 2: Write the failing tests** in `leaves_test.go`:
  - `TestCollectLeavesRecordsEachLeafPath`: content `{"permissions":{"defaultMode":"default"},"env":{"A":"1","B":"2"}}` over an empty doc gives paths `[permissions defaultMode]`, `[env A]`, `[env B]` (compare sorted by joined path) with `Hash == hashLeaf(<the value>)`.
  - `TestCollectLeavesTreatsArraysNullAndNumbersAsWholeValues`: an array, a `nil` and an `int64(5)` each become one leaf; `hashLeaf(int64(5)) == hashLeaf(float64(5))`.
  - `TestCollectLeavesEmptyMapIsALeafOnlyWhenItWillBeWritten`: `{"x":{}}` over `{}` records `[x]`; over `{"x":{"a":1}}` records nothing.
  - `TestCollectLeavesKeysWithDotsSlashesAndEmptyNames`: keys `"a.b"`, `"c/d"`, `""` stay single path segments.
  - `TestGetPathDistinguishesNullFromAbsent`.
  - `TestEncodeDecodeLeafRecsRoundTrip`, and `TestDecodeLeafRecsAcceptsLegacyKeyList` (`["env","permissions"]` decodes to two records, `Hash == ""`), and `TestDecodeLeafRecsRejectsGarbage`.
- [ ] **Step 3: Run** `cd wt && go test ./internal/profiles -run 'Leaf|GetPath' -v`. Expected: FAIL, undefined `collectLeaves`.
- [ ] **Step 4: Implement** the five functions above in `leaves.go`. `collectLeaves` walks `content` recursively with the current path, descending through any map value (it does not stop at a non-object in `doc`: restore handles that case, Task 3).
- [ ] **Step 5: Run** the same command. Expected: PASS.
- [ ] **Step 6: Commit** `feat(wt): leaf records for config_content backups (#401)`.

### Task 3: Per-leaf restore and delete, wired into the backup flow

**Files:**
- Modify: `wt/internal/profiles/configcontent.go` (`ApplyConfigContent`'s claude `buildData`, `snapshotAndWrite`, `surgicalRestoreJSON`, `surgicalDeleteJSON`, both marker reads in `restoreIfBackedUpLocked`, the doc comments naming "top-level keys" at lines 66-71, 191-197, 234-241, 268-276, 379-383, 479-486)
- Modify: `wt/internal/profiles/leaves.go` (add `revertLeaves`)
- Test: `wt/internal/profiles/configcontent_test.go`, `leaves_test.go`
- Docs: `wt/docs/wt-agents/profiles.md` (lines ~84-95 File safety, ~169 and ~196 "restores both", ~225-237 the permissions caveat), `wt/docs/internals/launch-flow.md` (check; edit only if it describes the marker), `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: Task 2's `leafRec`, `collectLeaves`, `hashLeaf`, `getPath`, `encodeLeafRecs`, `decodeLeafRecs`.
- Produces:
  - `snapshotAndWrite(target string, perm os.FileMode, buildData func(existing []byte, existed bool) (data []byte, recs []leafRec, err error)) error` (the middle return changes from `[]string`).
  - `surgicalRestoreJSON(originalSnapshot []byte, target string, recs []leafRec) (merged []byte, ok bool)` and `surgicalDeleteJSON(target string, recs []leafRec) (reduced []byte, ok bool)`, same `ok` contract as today (false: caller falls back to whole-file restore or delete).
  - `func revertLeaves(cur, orig map[string]any, recs []leafRec)` in `leaves.go`: `orig == nil` means the file did not exist before.

**Algorithm for `revertLeaves`** (the part the signature does not determine). For each record, in any order:
1. `getPath(cur, path)` not ok (missing leaf or a non-object intermediate): skip. The session deleted it or replaced the container; its state wins.
2. `Hash != ""` and `hashLeaf(current) != Hash`: skip. The session changed it after the merge; its value wins.
3. Otherwise put the original back: if `orig` has the path (`getPath(orig, path)` ok), set it, creating missing parent objects; else delete the leaf key.
After all records, prune ancestors of every touched path, deepest first. An ancestor in `cur` that is now an empty object is handled by the original: if `orig` holds a non-object value at that path, set that value back (type-conflict case: the scalar returns once the profile's subtree is gone); else if `orig` has no value there, delete it; else (the original had an object there) leave it empty. Never touch the root.

- [ ] **Step 1: Write the failing tests** in `configcontent_test.go`, using `claudeTarget` and mirroring `TestApplyConfigContentCleanupPreservesKeysWrittenDuringSession`:
  - `TestCleanupKeepsSessionPermissionsAllowBesideThePinnedDefaultMode`: file `{"permissions":{"defaultMode":"plan"}}`; profile `permissions.defaultMode = "default"`; after the write, the test adds `"allow":["Bash(ls)"]` under `permissions` on disk; after cleanup the file is `{"permissions":{"allow":["Bash(ls)"],"defaultMode":"plan"}}` (assertSameJSON).
  - `TestCleanupKeepsSessionPermissionsAllowWhenTargetDidNotExistBefore`: no file; after cleanup `{"permissions":{"allow":["Bash(ls)"]}}` and `defaultMode` is gone.
  - `TestCleanupKeepsAValueTheSessionChangedAfterTheMerge`: session sets `permissions.defaultMode = "acceptEdits"`; cleanup leaves `"acceptEdits"` (both the existing-file and new-file variants).
  - `TestCleanupDoesNotResurrectALeafTheSessionDeleted`.
  - `TestCleanupPrunesAContainerTheProfileCreatedAndRemovesTheFileWhenNothingIsLeft` (profile-only write, no session content: the file is deleted).
  - `TestCleanupKeepsAnEmptyContainerTheOriginalHad` (original `{"permissions":{}}`).
  - `TestRestoreTypeConflictKeepsSessionSiblingAndRestoresScalar`: original `{"permissions":"legacy"}`; profile sets `permissions.defaultMode`; session adds `permissions.allow`; cleanup leaves `{"permissions":{"allow":[...]}}` with no `defaultMode`; and the variant with no session sibling restores `{"permissions":"legacy"}`.
  - `TestCleanupArraysAreWholeValues`: profile pins an array, session appends; the session's array stays.
  - `TestCleanupHandlesNullLeaf` (profile writes `null`; an absent original is deleted, a `null` original stays `null`).
  - `TestSelfHealRestoresLegacyKeysMarker`: hand-write an orphan backup (`.present`, `.keys` = `["permissions"]`, a target with extra session content under another key); `SelfHeal` restores `permissions` from the snapshot and keeps the other key.
  - `TestSecondLaunchRefusedWhileFirstIsLiveWithLeafRecords` (reuse the owner/`pidAlive` stub pattern from `TestApplyConfigContentRefusesWhenTargetOwnedByLiveSession`).
  - Also: the `.keys` file holds no written value: write `config_content = {env = {TOKEN = "s3cret"}}` and assert the raw `.keys` bytes do not contain `s3cret`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/profiles -run 'Cleanup|Restore|SelfHeal|Marker' -v`. Expected: the new tests FAIL (session `allow` lost, etc.).
- [ ] **Step 3: Write the claude `buildData`.** It calls `collectLeaves(doc, rp.ConfigContent)` before `mergeInto`, and returns those records. `snapshotAndWrite` stores `encodeLeafRecs(recs)` in `.keys` when the records are non-nil (an empty `[]` marker when the merge wrote no leaf): without a marker, cleanup falls back to whole-file restore and would lose the session's grants.
- [ ] **Step 4: Implement `revertLeaves`** per the algorithm above, and rewrite `surgicalRestoreJSON` / `surgicalDeleteJSON` on top of it: both unmarshal current content (fall back with `ok=false` when it is not an object), call `revertLeaves` (`orig` nil for delete), and keep today's `MarshalIndent(..., "", "  ")` output; delete keeps returning `ok=false` when the root is empty.
- [ ] **Step 5: Update both `.keys` reads** in `restoreIfBackedUpLocked` to `decodeLeafRecs`. A marker that fails to decode keeps today's fallthrough (whole-file restore or delete), which is also what an older wt does with the new format.
- [ ] **Step 6: Run the whole package** `cd wt && go test ./internal/profiles -v 2>&1 | tail -40`. New tests PASS. Inspect each failing old test: change it only when it asserts the superseded "restore the whole top-level key / revert a hand-edited touched key" rule (candidates: `configcontent_test.go` near 236-263, 441, 511, 682, 854, 901), and say so in the commit message. Expected: PASS.
- [ ] **Step 7: Docs.** `profiles.md`: rewrite the File safety bullet to say restore is per leaf path the profile wrote, a leaf the session changed keeps the session's value, a hand edit of an untouched sibling is kept, and a hand edit of a profile-pinned leaf mid-session is kept too; delete the "restores only top-level keys / `permissions.allow` is lost" caveat from the claude cost note (keep the `defaultMode` override cost until Task 6); change "restores both" wording. Fix the doc comments in `configcontent.go`. `CHANGELOG.md` under Unreleased: `### Fixed`, one entry citing #401. Run `make check-links` from the repo root.
- [ ] **Step 8: Run** `cd wt && go build ./... && go vet ./... && go test ./...`. Expected: PASS. **Commit** `fix(wt): restore config_content per leaf so a session's nested writes survive (#401)`; push; open the PR.

---

## PR 2b: #402 (branch `feat/config-defaults`, stacked on PR 2a)

### Task 4: Absent-leaf collection

**Files:**
- Modify: `wt/internal/profiles/leaves.go`, `leaves_test.go`

**Interfaces:**
- Consumes: Task 2's `leafRec`, `getPath`, `hashLeaf`.
- Produces:
  - `func collectAbsentLeaves(doc, defaults map[string]any) []leafRec`: the leaves of `defaults` whose path is absent from `doc`. A leaf whose path crosses a non-object in `doc` (a scalar `permissions`) counts as present and is skipped, as is a leaf already in `doc` with any value, including `null`.
  - `func setLeaves(doc map[string]any, defaults map[string]any, recs []leafRec)`: writes each record's value from `defaults` into `doc`, creating parent objects.

- [ ] **Step 1: Branch** from PR 2a's branch: `git worktree add .worktrees/config-defaults -b feat/config-defaults fix/config-content-leaf-restore`.
- [ ] **Step 2: Write the failing tests:** `TestCollectAbsentLeavesSkipsExistingValues`, `TestCollectAbsentLeavesSkipsAScalarAncestor`, `TestCollectAbsentLeavesSkipsAnExistingNull`, `TestCollectAbsentLeavesKeepsSiblingsOfAnExistingContainer` (doc `{"permissions":{"allow":[]}}`, defaults `permissions.defaultMode` gives one record), `TestSetLeavesCreatesParents`.
- [ ] **Step 3: Run** `go test ./internal/profiles -run 'AbsentLeaves|SetLeaves' -v`. Expected: FAIL, undefined.
- [ ] **Step 4: Implement** both functions. Expected after: PASS.
- [ ] **Step 5: Commit** `feat(wt): collect the leaves a defaults table would write (#402)`.

### Task 5: `config_defaults` plumbing

**Files:**
- Modify: `wt/internal/profiles/profiles.go` (`Profile.ConfigDefaults map[string]any \`toml:"config_defaults,omitempty"\``; `MechanismConfigDefaults Mechanism = "config_defaults"`)
- Modify: `wt/internal/profiles/resolve.go` (`ResolvedProfile.ConfigDefaults`, `Empty()`, `Resolve`)
- Modify: `wt/internal/profiles/validate.go` (`usedMechanisms`)
- Modify: `wt/internal/agents/claude.go:95-96` (declare it)
- Modify: `wt/cmd/wt/profile.go:112` (print it)
- Test: `wt/internal/profiles/resolve_test.go`, `validate_test.go`, `wt/internal/agents/` claude driver test, `wt/cmd/wt/profile_test.go`

**Interfaces:**
- Consumes: nothing from Tasks 2-4.
- Produces: `ResolvedProfile.ConfigDefaults map[string]any` (merged across tiers with `mergeInto`, so a later tier's value wins per leaf, with `{{model_name}}` substituted like `ConfigContent`); a profile using `config_defaults` on an agent that does not declare `MechanismConfigDefaults` fails `Validate` with `agent does not support mechanism "config_defaults"`. Only claude declares it. Slot profiles do not gain the field.

- [ ] **Step 1: Write the failing tests:** `TestResolveMergesConfigDefaultsPerLeafLaterTierWins`, `TestResolveSubstitutesModelNameInConfigDefaults`, `TestResolvedProfileEmptyCountsConfigDefaults`, `TestValidateRejectsConfigDefaultsOnAgentsWithoutIt` (codex and opencode rejected, claude accepted), `TestLoadParsesConfigDefaultsTable`, `TestProfileShowPrintsConfigDefaults`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/profiles ./internal/agents ./cmd/wt -run 'ConfigDefaults' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the fields and wiring listed above. `Resolve` handles `ConfigDefaults` exactly like `ConfigContent`.
- [ ] **Step 4: Run** the same command plus `go test ./...` in `wt/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): config_defaults profile field and mechanism (#402)`.

### Task 6: Apply defaults only where absent, and move the claude example

**Files:**
- Modify: `wt/internal/profiles/configcontent.go` (`ApplyConfigContent`)
- Test: `wt/internal/profiles/configcontent_test.go`
- Docs: `wt/docs/wt-agents/profiles.md` (the claude example at ~166-169, cost note ~225-237, table row ~196), `wt/docs/internals/agents.md`, `wt/docs/internals/launch-flow.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: Task 3's `snapshotAndWrite` and records; Task 4's `collectAbsentLeaves` and `setLeaves`; Task 5's `rp.ConfigDefaults`.
- Produces: `ApplyConfigContent(cmd *exec.Cmd, agent, worktreePath string, rp ResolvedProfile) (cleanup func() error, err error)` unchanged in signature. It is a no-op only when both `ConfigContent` and `ConfigDefaults` are empty. For claude, `buildData` merges `ConfigContent` (Task 3), then `collectAbsentLeaves(doc, rp.ConfigDefaults)` against the merged doc and `setLeaves`, and returns both record sets together. For a TOML target or opencode with `ConfigDefaults` set it returns `profile: agent %q does not take config_defaults` (defense in depth; `Validate` already rejects it).

- [ ] **Step 1: Write the failing tests:**
  - `TestConfigDefaultsWritesAnAbsentLeafAndRestoresIt` (no `permissions` in the file: `defaultMode` is written during the session and gone after cleanup, a session-added `allow` survives).
  - `TestConfigDefaultsLeavesAnExistingValueAlone`: file has `permissions.defaultMode = "plan"`; the file is unchanged during the session and after cleanup. Variants: value equal to the default (`"default"`), and `"auto"`.
  - `TestConfigDefaultsLeavesANonObjectPermissionsAlone` (original `{"permissions":"x"}` is never rewritten).
  - `TestConfigContentStillOverridesWhileConfigDefaultsDoNot` (one profile: `env.CLAUDE_CODE_ATTRIBUTION_HEADER` in `config_content` overwrites an existing value, `permissions.defaultMode` in `config_defaults` does not).
  - `TestApplyConfigContentRejectsConfigDefaultsForCodexAndOpenCode`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/profiles -run 'ConfigDefaults|ConfigContentStillOverrides|RejectsConfigDefaults' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** the `ApplyConfigContent` changes above.
- [ ] **Step 4: Run** the same command, then `go test ./...` in `wt/`. Expected: PASS.
- [ ] **Step 5: Docs.** In the claude example, keep `config_content = { env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0" } }` and add `config_defaults = { permissions = { defaultMode = "default" } }` with a comment that a value already in `settings.local.json` wins. Replace the "Two costs of that pin" paragraph: the override cost becomes "a `permissions.defaultMode` in `.claude/settings.local.json` wins; one set in `.claude/settings.json` or `~/.claude/settings.json` is still overridden for the session, and an existing `auto` brings back the classifier slowdown (#393)". Update the table row to name both tables. Add a short `config_defaults` entry to the profile field description in `profiles.md` (same shape as `config_content`, merge-if-absent, claude only, not available on slot profiles) and to `docs/internals/agents.md` / `launch-flow.md` where `config_content` is described. CHANGELOG: `### Added` for `config_defaults`, `### Fixed` for #402. `make check-links`.
- [ ] **Step 6: Run** `cd wt && go build ./... && go vet ./... && go test ./...`. Expected: PASS. **Commit** `feat(wt): claude local profile no longer overrides the user's defaultMode (#402)`; push; open the PR with base `fix/config-content-leaf-restore` (retarget to `main` after PR 2a merges).

---

## PR 3: #388 (branch `fix/picker-remote-pr-tags`)

### Task 7: `PRSet`, with the base repo from `gh pr list`

**Files:**
- Modify: `wt/internal/worktree/prs.go` (`PRs`, `MergePRs`, `parsePRs`, `ghPR`, the `--json` field list)
- Modify: `wt/internal/tui/app.go` (`prs` field, `prsLoadedMsg`, `loadPRsCmd`, the two `m.prs != nil` checks at 278 and 641), `wt/internal/tui/worktree_list.go` (`applyPRs`, `prFor`, `prsCurrent`)
- Test: `wt/internal/worktree/prs_test.go`, `wt/internal/tui/worktree_prs_test.go` (mechanical type updates)

**Interfaces:**
- Produces:
  - `type PRSet struct { Repo string; ByBranch map[string]PR }`: `Repo` is `owner/repo` of the listing's PRs' head repository (lower-cased; every kept PR is same-repo, so it is the base repo); `ByBranch` is today's map.
  - `func (s PRSet) Loaded() bool` reports `s.ByBranch != nil`. It replaces `m.prs != nil`.
  - `func PRs(dir, state string) PRSet` (failure returns `PRSet{ByBranch: map[string]PR{}}`), `func MergePRs(a, b PRSet) PRSet` (branch rule unchanged via `preferPR`; `Repo` is the first non-empty), `func parsePRs(data []byte) (PRSet, error)`.
  - `ghPR` gains `HeadRepository struct{ NameWithOwner string \`json:"nameWithOwner"\` } \`json:"headRepository"\``; the `--json` list adds `headRepository`.
  - tui: `prFor(ei entryItem, prs worktree.PRSet) *worktree.PR`, `applyPRs(l *list.Model, prs worktree.PRSet)`, `prsCurrent(items []list.Item, prs worktree.PRSet) bool`, `prsLoadedMsg{prs worktree.PRSet}`. Behavior is unchanged in this task.

- [ ] **Step 1: Branch** `git worktree add .worktrees/picker-remote-pr-tags -b fix/picker-remote-pr-tags main`.
- [ ] **Step 2: Capture the fixture.** Run (read-only) `gh pr list --repo ohanaverse/local-ai-setup --state all --limit 2 --json number,state,isDraft,headRefName,isCrossRepository,title,headRepository` and paste the output, with `"headRepository":{"id":...,"name":"local-ai-setup","nameWithOwner":"ohanaverse/local-ai-setup"}`, into the tests as the JSON fixture.
- [ ] **Step 3: Write the failing tests** in `prs_test.go`: `TestParsePRsReadsTheHeadRepoAsRepo` (fixture above gives `Repo == "ohanaverse/local-ai-setup"`, lower-cased from a mixed-case input), `TestParsePRsWithoutHeadRepositoryHasEmptyRepoButStillMapsBranches`, `TestMergePRsKeepsTheFirstNonEmptyRepo`. Convert the existing tests to the `PRSet` shape (`.ByBranch[...]`).
- [ ] **Step 4: Run** `cd wt && go test ./internal/worktree -run 'PR' -v`. Expected: FAIL (compile errors, then assertions).
- [ ] **Step 5: Implement** `PRSet` and the signature changes in `prs.go`, then update `app.go` and `worktree_list.go` to the new types without changing any rule. Convert the tui tests' `map[string]worktree.PR` literals to `worktree.PRSet{ByBranch: ...}`.
- [ ] **Step 6: Run** `cd wt && go build ./... && go vet ./... && go test ./internal/worktree ./internal/tui`. Expected: PASS with no behavior change.
- [ ] **Step 7: Commit** `refactor(wt): the picker's PR listing carries the repo gh listed (#388)`.

### Task 8: Remote repos from the repository's remotes

**Files:**
- Create: `wt/internal/worktree/remotes.go`, `wt/internal/worktree/remotes_test.go`
- Modify: `wt/internal/worktree/enumerate.go` (`Entry` struct, the remote-entry construction around line 288-294)
- Test: `wt/internal/worktree/enumerate_test.go`

**Interfaces:**
- Produces:
  - `func repoFromURL(url string) string`: `owner/repo`, lower-cased, `.git` stripped, from `https://host/o/r(.git)`, `ssh://git@host/o/r.git` and scp-style `git@host:o/r.git`; `""` when it cannot tell (a local path). The host is ignored (documented limit: an Enterprise host with the same owner/repo would match).
  - `func RemoteRepos(dir string) map[string]string`: remote name to `repoFromURL(url)`, using `git remote` then `git remote get-url <name>` (which applies `insteadOf`); a failed `git remote` gives an empty map; a failed `get-url` leaves just that remote out. `repoFromURL` returns "" for `file://` URLs and any URL with an empty host.
  - `func SplitRemoteBranch(remoteBranch string, remotes map[string]string) (remote, branch string, ok bool)`: the longest remote name `r` in `remotes` with prefix `r + "/"` wins; `ok=false` when none does (no first-slash guess).
  - `Entry` gains `RemoteRepo string` (the row's remote's `owner/repo`, `""` when not a remote row or unknown) and `RemoteBranch string` (the branch without its remote, `""` when unknown). `Enumerate` fills both for every remote-branch entry from one `RemoteRepos` call.
  - `PRBranch` is unchanged and stays the display rule and the local-row rule.

- [ ] **Step 1: Write the failing tests:** `TestRepoFromURLForms` (https, https with `.git`, ssh://, scp-style, mixed case, a local path `/srv/repo.git` gives `""`), `TestSplitRemoteBranchLongestRemoteWins` (remotes `up` and `up/stream`; `up/stream/feat/x` splits to `up/stream`, `feat/x`; unknown remote gives `ok=false`), `TestRemoteReposReadsEachRemoteURL` (temp repo with two remotes made like the other tests in this package, one with an `insteadOf` rewrite), `TestEnumerateFillsRemoteRepoAndBranchOnRemoteEntries` and `TestEnumerateLeavesAStaleRemoteRefUntagged` (a ref under a removed remote has empty `RemoteRepo` and `RemoteBranch`).
- [ ] **Step 2: Run** `cd wt && go test ./internal/worktree -run 'RepoFromURL|SplitRemote|RemoteRepos|EnumerateFills|StaleRemote' -v`. Expected: FAIL.
- [ ] **Step 3: Implement** `remotes.go` and the `Entry` fields and the `Enumerate` fill.
- [ ] **Step 4: Run** the same command and `go test ./internal/worktree`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(wt): resolve each remote branch's repository (#388)`.

### Task 9: Tag a remote row only for the repo gh listed

**Files:**
- Modify: `wt/internal/tui/worktree_list.go` (`entryItem`, `buildList` at line 274, `prFor`)
- Test: `wt/internal/tui/worktree_prs_test.go`
- Docs: `wt/CLAUDE.md:344`, `wt/docs/wt-agents/README.md:178-187`, `wt/docs/internals/packages.md:50`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes: Task 7's `PRSet`; Task 8's `Entry.RemoteRepo` and `Entry.RemoteBranch`.
- Produces: `entryItem` gains `repo string`. In `buildList`, a remote row gets `prBranch: e.RemoteBranch` and `repo: e.RemoteRepo`; other rows are as today. `prFor` returns `nil` for a remote row unless `prs.Repo != "" && strings.EqualFold(ei.repo, prs.Repo)`, then looks up `prs.ByBranch[ei.prBranch]` as before. `baseTitle` keeps using `worktree.PRBranch` for display, so a fork row's title is unchanged. `buildList`'s signature is unchanged.

- [ ] **Step 1: Write the failing tests** (build entries with `RemoteRepo`/`RemoteBranch` set, as the existing PR tests build theirs):
  - `TestRemoteRowsFromASecondRemoteAreNotTaggedWithTheBaseReposPR` (the issue's repro: `origin/feat-x` with `RemoteRepo = "o/base"` tagged, `keith/feat-x` with `RemoteRepo = "keith/fork"` not, `PRSet{Repo: "o/base"}`).
  - `TestRemoteRowUnknownRemoteIsUntagged` (empty `RemoteRepo`).
  - `TestRemoteRowsStayUntaggedUntilThePRsArriveThenTag` (`PRSet{}` first, then loaded).
  - `TestRemoteRepoMatchIgnoresCase`.
  - `TestForkRowKeepsItsDisplayTitle` (`keith/feat-x` renders `feat-x`).
  - `TestStaleFilterRunWithRemoteRowsKeepsTheTags` (adapt `TestStaleFilterRunAfterPRsKeepsTheTags` with a remote row) and `TestFilterByPRNumberDoesNotMatchAnUntaggedRemoteRow`.
  - Update `TestApplyPRsTagsEveryKindOfRow` and any other remote-row fixture to set `RemoteRepo`/`RemoteBranch` and `PRSet.Repo`.
- [ ] **Step 2: Run** `cd wt && go test ./internal/tui -run 'Remote|Fork|PR' -v`. Expected: the new tests FAIL.
- [ ] **Step 3: Implement** the `buildList` and `prFor` changes. `prsCurrent` needs no separate edit: it calls `prFor`, so tags and filter-staleness agree.
- [ ] **Step 4: Run** `cd wt && go build ./... && go vet ./... && go test ./...`. Expected: PASS.
- [ ] **Step 5: Docs.** `wt/CLAUDE.md:344` and `README.md:178-187`: a remote row is tagged only when its remote is the repository `gh pr list` reports, rows from other remotes (a fork you push to) are untagged until #387, local rows still match by branch name; `packages.md:50`: describe `PRSet`, `RemoteRepos`, `SplitRemoteBranch`. CHANGELOG `### Fixed` for #388. `make check-links`.
- [ ] **Step 6: Run `make test-all`** from the repo root. Expected: PASS. **Commit** `fix(wt): the picker tags a remote branch only with its own repo's PR (#388)`; push; open the PR.

---

## Self-Review

- **Spec coverage:** #398 (Task 1), #401 leaf records, hash not value, session wins, type conflict, legacy marker, pruning, arrays, null, docs (Tasks 2-3), #402 `config_defaults`, absent-only, claude-only, tier merge, validation, slot profiles excluded, doc limits (Tasks 4-6), #388 head-repo tagging, display kept, filter staleness, stale remote untagged, docs (Tasks 7-9). The spec's "key by (owner/repo, branch)" is replaced by `PRSet{Repo, ByBranch}` for the reason in the header.
- **Type consistency:** `leafRec`, `collectLeaves`, `collectAbsentLeaves`, `setLeaves`, `revertLeaves`, `encodeLeafRecs`/`decodeLeafRecs`, `PRSet`, `Loaded`, `RemoteRepos`, `SplitRemoteBranch`, `Entry.RemoteRepo`/`RemoteBranch` are defined once and used by the same names in later tasks. `snapshotAndWrite`'s changed signature is introduced in Task 3 and consumed in Task 6.
- **Known judgment calls for the executor:** exact `leafRec` JSON key names, file placement of `revertLeaves`, and the helper used to build temp repos in Task 8 are the executor's choice.
