# Issue-Backlog Waves Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Work the 36 open issues in the order agreed in the 2026-11-21 triage — verification debt and small bugs first, then session-end UX, the gateway line, test infra and cleanup polish — leaving every issue either fixed, scheduled behind a design gate, or registered as when-touched work.

**Architecture:** Waves 0–1 are fully specified here: two manual verification gates, then six small PRs. Waves 2–3 are design-first (their decisions are not this plan's to make) and each begins with a design session that produces its own spec + plan under `docs/superpowers/`. Waves 4–5 are concrete but not next; they get their own plan when started. The register lists when-touched items with their recorded rulings. One PR per task, `make pre-pr` before each.

**Tech Stack:** Go (the `wt/` module), bash, GitHub issues as the requirement source.

**Spec:** The open issues themselves (each task cites its issue, whose body carries the requirement and whose 2026-11-21 triage comment carries the verdict). The wave order below is the agreed approach from the 2026-11-21 triage session; the rejected alternatives were per-issue FIFO and severity-only ordering (both waste the natural groupings — shared files, shared design decisions, shared verification runs).

## Global Constraints

- **Gate:** `make pre-pr` before every PR (CI is off). Run it with `run_in_background` and read `GATE_EXIT` from the log; it takes ~12.5 minutes. Never merge on a log you have not read to the end.
- **PR bodies say `Refs #N`**, never a closing keyword followed by `#N` anywhere in the body. A PR that completes its issue may say so only with wording GitHub does not scan (or let the merge close it via a closing keyword used deliberately, on purpose, once — the default is Refs).
- **Frozen JSON contracts:** any change touching a `docs/contracts/` fixture moves with its Go change in the same PR (`cleanup-list`, `remove-result`, `rescue-result`, `restore-root-result`, `model-api`).
- **wt repo rules apply** (`wt/CLAUDE.md`): per-call test seams; every `TestMain` isolates what it reaches; every `Test*` has a what+why comment; no `os.Exit` inside `internal/tui`; cite `wt/docs/style-guide.md` rule IDs in review.
- **depcheck ratchet:** session packages stay off admin packages; the leaves (`modelping`, `modelmode`, `editorkit`, `modeview`, `cli`) stay off `modelclient`; allow-lists only shrink.
- **single-P list:** a package whose test newly waits on a goroutine joins `wt/Makefile`'s `single-p` target in the same change.
- **Commits end with the Co-Authored-By line; PR bodies end with the Generated-with line.** Merges are squash (`gh pr merge --squash --admin` — org ruleset wants an approval the author cannot give).
- **Worktrees never embed live model state**; dated or illustrative examples only.

## Review Focus

1. **#643's live launchd run acts on the real per-user domain** (`local.litellm.proxy`, `local.wt-model`) — it must run on a label and machine its runner can afford to disturb, and every finding becomes a filed issue, never a silent fix. Pinned by Task 1's checklist shape.
2. **#587's regrouping must leave the `--json` contract byte-identical** — only the plain-text grouping moves; the `cleanup-list` fixture and its contract test are the pin. Pinned by Task 7's contract step.
3. **#649 must not weaken classification:** a 4xx/5xx whose body is not a decodable `modelapi.APIError` still classifies `NotModelServerError`. Pinned by Task 3's second test case.
4. **#570's insulation must not change rescue's observable behavior** — only where the stat re-stamp lands (a scratch index copy); the `rootCheckout` column re-arms only after the insulation is in. Pinned by Task 6's ordering.
5. **#577 must keep keyboard ctrl+c semantics** (waits during a run, cancels a confirmation) while external signals stop ending wt. Pinned by Task 5's forwarded-signal test.

## The Waves (agreed order)

| Wave | Content | Gate before its PRs |
|---|---|---|
| 0 | #643, #533 (manual verification) → #641, #642; PR: #604 + #649 + #588 | none — start now |
| 1 | #577, #570, #587, #578, #558, #628 (six small PRs) | Task 1's findings (for nothing in wave 1 except shared context) |
| 2 | #569, #560, #543 (session-end UX) | design session → own spec + plan |
| 3 | #598, then #596 (+ #626), #597, #618 (gateway line) | #598 design session → own spec + plan |
| 4 | #565, #562 (drift guard), #582 (test infra) | own plan when started |
| 5 | #581 + #576; #586 + #579 + #580 (cleanup polish) | own plan when started |
| register | #621, #583, #584, #585, #544, #545, #548 | when-touched rulings (below) |

Every open issue appears exactly once above (36 total). Issues closed in the 2026-11-21 triage (#555, #557, #568, #599, #625, #650) are done and not listed.

---

# Wave 0 — verification debt + review smalls

## Task 1: Live launchd check of the Model server tab (user-run gate)

**Refs #643.** Manual; no code. The Setup tab's LaunchAgent actions have only ever run against a fake `Launchd`.

**Files:** none (findings become issues).

- [ ] **Step 1: Run the checklist on a disturbable label/machine.** The seven items are in the #643 body (Create happy path and refusals, Rotate incl. the `DATABASE_URL`-no-salt refusal, the wt-model agent bootout/bootstrap poll, `s`/`x`/`R`, `t` tighten, the Background Task Management notification note). This disturbs the real `local.litellm.proxy` and `local.wt-model` labels — user-driven, or a session with explicit go-ahead on this machine.
- [ ] **Step 2: File every finding as a new issue** (severity as observed), and comment the pass/fail per checklist item on #643. Close #643 if all seven pass; otherwise leave it open listing the filed issues.
- [ ] **Step 3: Re-scope Tasks 4 (the #641/#642 fixes) against the findings** — if a check misfired in reality, its fix follows the observed failure, not the issue's prediction.

## Task 2: Live origin-branch removal check (throwaway repo)

**Refs #533.** Manual but scriptable; no code expected unless it finds a defect.

- [ ] **Step 1: Create a throwaway GitHub repo** (fixture shape: a merged `feature` branch on `main`), then through a built wt: `wt remove origin/feature --remote --yes`, and verify: the `--force-with-lease` push is accepted; the restore line (`git push origin <sha>:refs/heads/feature`) restores it; a PR opened against the branch in the race window behaves as documented (GitHub closes it; Restore branch plus reopen recovers).
- [ ] **Step 2: Comment the evidence on #533** and close it (file an issue instead if anything misbehaved). Delete the throwaway repo.

## Task 3: PR — review smalls: docs exitcode, modelping APIError, dead creation line

One PR, three commits, `Refs #604`, `Refs #649`, `Refs #588`.

**Files:**
- Modify: `wt/CLAUDE.md:54`, `wt/docs/style-guide.md:89,90,97`, `wt/docs/internals/launch-flow.md:43,128` (#604)
- Modify: `wt/internal/modelping/ping.go` (+ `client.go:116` in `internal/modelclient`), Test: `wt/internal/modelping/ping_test.go` (#649)
- Modify: `wt/internal/launchwarn/check.go:41`, `wt/internal/worktree/create.go:79-80` (+ its resolution path), Test: `wt/internal/worktree/` and/or `wt/cmd/wt-session/` (#588)

**Interfaces:**
- Produces: `func DecodeAPIError(body []byte) (*modelapi.APIError, bool)` in `wt/internal/modelping` — later waves' model-server error paths may reuse it.

### Commit 3a — #604: docs name a type that no longer exists

- [ ] **Step 1: Replace every `exitCodeError` mention** in the three files with the real shape: `exitcode.New(code, err, quiet)` (`wt/internal/exitcode/exitcode.go:25`; the issue's `wt/cmd/wt/exitcode.go` path is stale). Reword each sentence that describes the old type; `wt/CLAUDE.md:54` and both `launch-flow.md` spots describe the TUI hand-off, `style-guide.md` C9/C10/J1 carry it inside frozen rule text — change only the type's name and the sentence around it, not the ruled behavior.
- [ ] **Step 2: Verify:** `grep -rn "exitCodeError" wt/ --include="*.md"` returns nothing outside `docs/superpowers/` (historical plans/specs stay as written).

### Commit 3b — #649: decode the server's APIError on 4xx/5xx

- [ ] **Step 1: Write the failing tests** in `wt/internal/modelping/ping_test.go`, extending `TestGetJSONPingsFirstThenDecodes` (ping_test.go:255) with two cases: a 500 whose body is a `modelapi.APIError` returns that error (carrying the server's code and message), and the status-table case at ping_test.go:~39 gains a 404 with a non-APIError body, which still classifies `*NotModelServerError`.
- [ ] **Step 2: Run to verify they fail.** Run: `go test ./internal/modelping -run 'TestGetJSON|TestCheckPing' -v` (from `wt/`). Expected: FAIL (new cases) on the old mapping.
- [ ] **Step 3: Implement `DecodeAPIError`** in `wt/internal/modelping` and use it in `GetJSON`'s `r.Status < 200 || r.Status > 299` branch (ping.go:~188): decode first, return the `*modelapi.APIError` when it parses, else `NotModelServerError` unchanged. Move the duplicate decode at `internal/modelclient/client.go:116` onto the same helper (that import direction is depcheck-legal; the reverse is not).
- [ ] **Step 4: Run to verify they pass**, then `go test ./internal/modelclient ./internal/modelping ./cmd/wt-session -count=1`.
- [ ] **Step 5: Commit** with `Refs #649`.

### Commit 3c — #588: delete the dead creation line; resolve `-W <remote>/HEAD`

- [ ] **Step 1: Write the failing test:** `-W origin/HEAD` on a gittest fixture whose default is `main` resolves like `-W origin/main` (the creation/reuse classification answers instead of git's raw "not a valid branch name"). Pin that the three live creation lines are unchanged.
- [ ] **Step 2: Run to verify it fails** (raw git error today).
- [ ] **Step 3: Implement:** in the `-W` resolution path (`wt/internal/worktree/create.go:79-80` names the current refusal), resolve `refs/remotes/<remote>/HEAD` via `git symbolic-ref` to `<remote>/<default>` before classification. Delete the dead fourth format (`launchwarn/check.go:41`) and the unreachable `NewBranch && Tracks` path that only it printed.
- [ ] **Step 4: Run to verify it passes:** `go test ./internal/worktree ./internal/launchwarn ./cmd/wt-session -count=1`.
- [ ] **Step 5: Commit** with `Refs #588`.

- [ ] **Step 6: PR + gate:** `make pre-pr` (backgrounded, read `GATE_EXIT`), `gh pr create` with a body that says `Refs #604`, `Refs #649`, `Refs #588` and ends with the Generated-with line.

## Task 4: PR — modelsetup false-failure and Apply race (gated on Task 1)

**Refs #641, Refs #642.** Re-scope both halves against Task 1's findings first.

**Files:**
- Modify: `wt/internal/modelsetup/apply.go` (`Apply`, `applyLiteLLMPlist`), `wt/internal/modelsetup/env.go` (`FS`), the `Net` seam
- Test: `wt/internal/modelsetup/apply_test.go`, `fakes_test.go`

- [ ] **Step 1: #641 — Create's listener-pid check:** add a `PPID` method to the `Net` seam (via `lsof -F pR`) and accept a listener whose parent pid is the launchd job's pid (or walk the process group). #642-style test first: a fixture `litellm` whose listener is a child of the job pid verifies (today it falsely fails).
- [ ] **Step 2: #641 — Rotate's key check:** on a 401 after `kickstart -k`, retry until `LiteLLMReadyTimeout` or until the listener pid changes, before declaring the key rejected. Test: an old process answering `/health/liveliness` first, then the new one accepting the key, ends `moved`, not `failed`.
- [ ] **Step 3: #642 — lock Apply:** hold `config.WithFileLock(<config home>/wt-model/setup.lock)` (`wt/internal/config/lock.go:31`) for the whole Apply. Test: two concurrent Applies — the loser gets the new-key-check failure and recovery text, never a mismatched pair.
- [ ] **Step 4: #642 — Create's absent-then-rename window:** write the plist to a temp file and `os.Link` it into place (fails if the target exists). Test: a plist appearing between the stat and the write is refused, not replaced.
- [ ] **Step 5: Run** `go test ./internal/modelsetup -count=1`, then the modelsetup-referencing suites in `cmd/wt-model`.
- [ ] **Step 6: PR + gate** (`make pre-pr`, `GATE_EXIT`), body `Refs #641`, `Refs #642`.

---

# Wave 1 — correctness and robustness smalls

## Task 5: PR — external signals no longer end wt during a picker removal

**Refs #577.**

**Files:**
- Modify: `wt/internal/tui/app.go:933` (the picker's `tea.NewProgram(initial, tea.WithAltScreen())`)
- Reuses: `forwardSignals` (`wt/internal/tui/cleanup_confirm.go:212`), the `guardedRun(forwardSignals(p.Send), p.Run)` shape (`wt/internal/tui/cleanup_list.go:567-568`)
- Test: `wt/internal/tui/` (the picker removal-run path)

- [ ] **Step 1: Write the failing test:** a SIGTERM/SIGHUP message arriving during the picker's removal run does not end the program — the run completes, the outcomes print (including any `git branch <name> <sha>` restore line). Keyboard ctrl+c keeps its documented behavior (waits during a run; cancels a confirmation).
- [ ] **Step 2: Run to verify it fails** (`go test ./internal/tui -run <the new test> -v`).
- [ ] **Step 3: Implement:** the picker program opts into `tea.WithoutSignalHandler()` and the same forwarder the cleanup list uses, held until the run ends. If the forwarder's messages need a home in the picker's update loop, route them to the run-wait state (do not print-and-exit).
- [ ] **Step 4: Run to verify it passes**, plus `go test ./internal/tui -count=1`.
- [ ] **Step 5: Manual spot-check:** one pty run at 80x24 (`wt` picker → `x` a removable row) surviving an external SIGTERM, per the repo's pty screen-check practice.
- [ ] **Step 6: Commit, PR (`Refs #577`), gate.**

## Task 6: PR — rescue's diff reads stop re-stamping the source index; rootCheckout re-arms

**Refs #570.** Optional riders, same PR, only if cheap and independently committable: #544 (test-only `writeRecord`, schema patterns), #545 (apply dirtsnap's parent-prefix rule to `verify` — decide in-commit by matching #542's `has_symlink_leading_path` shape), #548 (`Snapshot.RecordsErr` note line).

**Files:**
- Modify: `wt/internal/rescue/execute.go:169` (the `git diff --binary --full-index --no-renames` read), every other diff-shaped source read (`grep -rn -- '--binary' wt/internal/rescue wt/internal/rootmove wt/internal/gittest`)
- Modify: `wt/internal/rootmove/execute_test.go:52` (the `rootCheckout` raw-index column)
- Modify: the mechanism comments at `wt/internal/cleanup/git.go:135,211`, `wt/internal/gittest/gittest.go:206,372`, and rescue's current site (`grep -rn "only read" wt/internal/rescue`)
- Test: the six carry-window tests + the rootmove suite

- [ ] **Step 1: Insulate the reads:** run diff-shaped source reads against a scratch `GIT_INDEX_FILE` copy of the source index (copy `<gitdir>/index` to a temp path, point the read's env at it). Byte-diff test first: the source index is bit-identical before and after a read over an inode-replaced entry.
- [ ] **Step 2: Run the carry-window tests green** with the insulation in, before touching the column.
- [ ] **Step 3: Arm `rootCheckout`'s raw-index column** (`rootmove/execute_test.go:52`): with the helper insulated, "equal before and after" now fails only on a genuine engine write. The six carry-window tests stay green; a deliberately-injected engine write turns them red (one throwaway run to prove the detector lives, then revert the injection).
- [ ] **Step 4: Amend the mechanism comments** at the four current sites (and rescue's): say what actually happens — `GIT_OPTIONAL_LOCKS=0` suppresses `git status`'s optional write, not `git diff`'s stat refresh; diff-shaped reads run against a scratch index copy.
- [ ] **Step 5: Run** `go test ./internal/rescue ./internal/rootmove ./internal/cleanup ./internal/gittest -count=1`.
- [ ] **Step 6: Commit, PR (`Refs #570`), gate.**

## Task 7: PR — the default branch stops sitting under "LOOKS MERGED, LESS SURE"

**Refs #587.**

**Files:**
- Modify: `wt/internal/cleanup/present.go:45` (`Groups`), `:28-33` (`Heading`)
- Uses: `Base.Default` (`wt/internal/cleanup/base.go:19`), the `isDefault` test (`wt/internal/cleanup/classify.go:560`)
- Test: the plain-output tests + the untouched contract test (`cmd/wt-session` cleanup contract)

- [ ] **Step 1: Write the failing test:** in `--plain`, the default branch's local row and its `origin/<default>` remote row appear in their own group (e.g. `THE DEFAULT BRANCH`) with no verdict wording and no age/reason noise, apart from every removal-verdict group.
- [ ] **Step 2: Run to verify it fails.**
- [ ] **Step 3: Implement in `Groups`:** rows whose name is the default (local or the folded `upstream` remote ref) land in the new group; no `Row` field, status or `--json` key changes.
- [ ] **Step 4: Run to verify:** the plain test passes and the `cleanup-list` contract test is untouched and green (`go test ./internal/cleanup ./cmd/wt-session -count=1`).
- [ ] **Step 5: Commit, PR (`Refs #587`), gate.**

## Task 8: PR — one row builder for the picker and the post-run rebuild

**Refs #578.**

**Files:**
- Modify: `wt/internal/tui/picker_rows.go:139,150` (`dropStalePRTags` and its call), the picker's gather (`ProtectedPaths` input), `wt/cmd/wt-session/cleanup.go:188-232` (`cleanupReport` and its callers)
- Test: same-snapshot row-parity tests

- [ ] **Step 1: Write the failing test:** one snapshot yields identical rows from the picker load and the post-removal rebuild — a reused branch's ended-PR tag drops on both, and a row holding a protected artifact shows the `protected_artifact` tag in the picker too (pass `ProtectedPaths` into the picker's gather).
- [ ] **Step 2: Run to verify it fails** (the picker's rebuild path is the divergent one).
- [ ] **Step 3: Implement:** one row builder both paths call with the same inputs; `Row` tags stay derived from the report.
- [ ] **Step 4: Run** `go test ./internal/tui ./cmd/wt-session -count=1`.
- [ ] **Step 5: Commit, PR (`Refs #578`), gate.**

## Task 9: PR — ReleaseRoot's git writes run in their own process group

**Refs #558.**

**Files:**
- Modify: `wt/internal/cleanup` (`Git` gains `NewProcessGroup bool`, setting `SysProcAttr{Setpgid: true}`; unset for every existing caller), `wt/internal/removal/root.go:159,173` (`ReleaseRoot`, the `rg := rootGit(g, root)` used for `reset --hard` and `switch --no-overwrite-ignore`), `gitWrite`'s death-by-signal wording
- Test: a process-group test in the `TestLsofFinderTimesOut` fixture style (`wt/internal/removal/procs_test.go`)

- [ ] **Step 1: Write the failing test:** a SIGINT delivered to the foreground process group during the reset does not kill the git child — the reset completes and `ReleaseRoot` reports `done`, and a git child that does die by a signal is named ("killed by SIGTERM"), not "git exit -1".
- [ ] **Step 2: Run to verify it fails.**
- [ ] **Step 3: Implement** the field, set it only on `ReleaseRoot`'s write git; the timeout kill still targets the pid.
- [ ] **Step 4: Run** `go test ./internal/removal ./internal/cleanup -count=1`.
- [ ] **Step 5: Commit, PR (`Refs #558`), gate.**

## Task 10: PR — proxy-pair reads take a shared lock, and tolerate a read-only home

**Refs #628.**

**Files:**
- Modify: `wt/internal/config/lock.go:31` (a shared variant beside `WithFileLock`), `wt/internal/config/proxyhome.go` (`resolveProxyHome`)
- Test: beside `TestSetProxyNeverObservesATornPairUnderConcurrentSets`

- [ ] **Step 1: Write the failing tests:** (a) a pair read in a home the process cannot write succeeds without creating `config.toml.lock` (today: `EACCES` on the `O_CREATE` open); (b) concurrent readers do not queue behind one another (two reads interleave while a writer excludes both).
- [ ] **Step 2: Run to verify they fail.**
- [ ] **Step 3: Implement** the LOCK_SH variant used by `resolveProxyHome`: open the lock file `O_CREATE`; on an `EACCES` open, retry read-only and take the shared lock on what exists; if neither works, proceed unlocked with a comment (reads fail open — the pair's users already degrade through `(*Config).ProxyErr()`). The write path keeps the exclusive lock.
- [ ] **Step 4: Run** `go test ./internal/config -count=1` (the torn-pair test stays green untouched).
- [ ] **Step 5: Commit, PR (`Refs #628`), gate.**

---

# Wave 2 — session-end UX (design gate: session first, then its own spec + plan)

**Issues:** #569 (cleanup cannot remove the standing worktree), #560 (root's branch invisible in the picker; no removed-worktree detection), #543 (prune rescue records through `internal/removal`).

**Why here:** the most-felt daily pain (every "PR merged, clean up" ends inside the worktree being cleaned; #560's invisibility is user-reported), but all three need one design pass because they share the session/worktree lifecycle seam.

**Design questions to settle** (in `docs/superpowers/specs/2026-MM-DD-session-end-ux-design.md`):
- #569's direction: deferred-removal report (name the worktree and the command to run from elsewhere) vs exit-time removal (wt offers when the session ends) vs both — and which half belongs to the skill vs `wt`.
- #560: does the picker show the root's branch (marked root/held), and does wt detect "my worktree was removed" at launch instead of silently standing in the root? The guard ideas (hooks, pre-commit checks keyed on `--git-dir == --git-common-dir`) are a separate, cheaper slice — decide if they ride.
- #543: a row kind for `refs/wt/rescued/*` (source path, destination branch, age, whether the source still matches), deletion only through `internal/removal` (compare-and-swap `update-ref --no-deref -d`), and whether an age floor applies.

**PR shape:** likely 2–3 PRs after the design lands.

# Wave 3 — the gateway line (design-first; #598 first)

**Issues:** #598 (avoid the LiteLLM restart/drain), then #596 (+ #626 riding it), #597, #618.

**Why here:** highest user-facing value and the spike is already executed and recorded on #598 (runtime model management works: ~25 ms add, in-flight streams survive delete; needs `general_settings.store_model_in_db: true` + one restart — an owner deploy step; plus a startup reconcile, deterministic `model_info.id`s, and today's rewrite-and-restart as fallback). The remaining work is a design doc + a flag-gated hot-path PR, not research.

**Sequence:** #598 design session → spec + plan → PR(s). Then #596's exploration (slot profiles × LiteLLM aliases/groups; what stays in slot-profiles.toml) with #626 (always set context windows — live symptom: the manual `CLAUDE_CODE_MAX_CONTEXT_TOKENS` profile fix) riding its outcome or landing standalone if it bites first. #597 is research-first (LiteLLM subscription/OAuth passthrough + claude slot env). #618 starts by answering its design question 1 (can pi run the credential command itself? — if yes, the fix is documentation).

# Wave 4 — test infra (own plan when started)

**Issues:** #565 (gittest `Lineage` widening + carry-forwards — its 2026-11-21 checklist comment records what landed and what remains), #562 (the single-P drift guard; absorbed #568's deny-list option; current list verified clean), #582 (dedupe ~7 git-fixture copies into `internal/gittest`; check in the pty real-screen driver).

**Order inside the wave:** #565 first (its Lineage widening gates any later parallel-slice work), then the #562 guard, then #582. All three are concrete; none is urgent.

# Wave 5 — cleanup-screen polish (own plan when started)

**Issues and PR shape:** presentation PR = #581 (row-text polish: duplicate hold statement, the `held by session` literal shared as a constant, `Flow`'s lone-long-word cut) + #576 (name `d`/`o` and the picker's `x d o r R` in the key legends — needs the layout decision: a fourth width tier or a `?` help line; the mandated labels must stay whole at 80x24). Mechanics PR = #586 (unify the two fetch mechanisms; absorbs #579's double-fetch) + #579's cursor restore + #580's two hand-off edges (the hidden interrupted line; the stray fetch in the quit window — `refreshRowsChosenMsg` gated on `rows.done`).

# Register — when-touched (recorded rulings, no dedicated slot)

| Issue | Ruling |
|---|---|
| #621 | Per its scoping comment: no bulk migration; a seam's package converts its seams when touched. |
| #583 | Convert sessionclaim's liveness seams per-call when the package is next touched; fold `HoldLaunch`'s `others` through `cleanup.ResolvePath` when a real-path caller exists. |
| #584 | Restore-root detached-root support; flips `rootmove.Offered` (re-pin `TestOfferedDetachedRoot`). Naturally rides Wave 2's lifecycle design. |
| #585 | `pr_merged`/`upstream_gone` on typed/flag launches needs an async `gh` lookup design (never block a launch on it). Small design note, then a PR. |
| #544, #545, #548 | Rescue hygiene bundle; ride Task 6 when cheap, else the next rescue-touching change. |

---

## Self-review record

- **Spec coverage:** all 36 open issues appear exactly once (waves 0–5 + register); the six triage-closed issues are excluded. The manual gates (#643, #533) carry their checklists by reference, not copy.
- **Step scan:** every code step names its file and the test that pins it; no step leaves a body the signature and tests already determine. Task 4's steps are pre-shaped but explicitly gated on Task 1's findings (the issues predict the failures; the live check confirms which are real).
- **Type consistency:** `DecodeAPIError` introduced in Task 3b is the only new cross-task interface; nothing else in waves 0–1 consumes another task's product.
- **Review Focus:** each line names its pinning task's step (Tasks 1, 3, 5, 6, 7).
- **Proportion:** waves 0–1 carry full task granularity (they execute next); waves 2–5 are gates and scope, not transcripts — each produces its own plan at its gate, which keeps this document from rot.
