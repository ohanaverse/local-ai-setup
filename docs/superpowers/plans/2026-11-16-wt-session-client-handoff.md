# wt-session client (Phase 2): handoff

Written 2026-11-16, when work was stopped on request after Task 20. This file is
the recovery map for resuming the plan. The SDD ledger that tracked the work
(`.superpowers/sdd/2026-11-15-wt-session-client/progress.md`) is git-ignored and
dies with the worktree, so everything that matters from it is copied here.

- Plan: `docs/superpowers/plans/2026-11-15-wt-session-client.md` (22 tasks, rulings R1-R18, Global Constraints, Review Focus)
- Spec: `docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md`
- Umbrella issue: #599 (stays OPEN: every PR uses `Refs #599`, never a closing keyword)

## 1. Where things stand

| Piece | State |
|---|---|
| PR-A (#605) Tasks 1-4, wt-model additions | MERGED (`8cab46a1`) |
| PR-B (#606) Tasks 5-9, client foundations | MERGED (`759b6ddc`) |
| PR-C (#607) Tasks 10-17, the client path | MERGED (`651ae7a9`) |
| PR-D (#616) Tasks 18-19, the flip and the deletion | OPEN, MERGEABLE, BLOCKED on approval (head `2465a4c8`, branch `feat/wt-session-d-flip`, base `main`) |
| Task 20, binary rename (`cmd/wt` to `cmd/wt-session`) | DONE and reviewed (opus: approved, no Critical or Important). Commit `d586da39` on the working branch `feat/wt-session-client`. **Not pushed, no PR yet.** |
| Task 21, llmbench message | NOT STARTED |
| Task 22, cross-cutting docs, guides, spec | NOT STARTED |
| PR-E (Tasks 20-22) | NOT OPENED |
| Final whole-branch review and `finishing-a-development-branch` | NOT DONE |

Working branch: `feat/wt-session-client` in the worktree `.worktrees/wt-session-client`, HEAD `d586da39` (plus this handoff doc if it has been committed; see `git log`). It has no upstream. The two commits that matter on top of the pushed PR-D head `2465a4c8`: `d586da39` (Task 20) and the handoff doc.

The user merges PRs (the org ruleset wants one approval and the author cannot self-approve, so the user runs `gh pr merge --squash --admin`). **Never merge.** Standing authorization from the user ("create all prs when ready"): push batch branches and open PRs once a batch's full gate is green. Pushes go with `git push origin HEAD:refs/heads/<batch-branch>`; PR bodies via `gh pr create --body-file`, ending with the 🤖 footer; commits end with `Co-Authored-By: Claude <model> <noreply@anthropic.com>`.

## 2. How to resume (first 10 minutes)

1. `cd .worktrees/wt-session-client`; `git status`, `git log --oneline -5`, `git fetch origin main`.
2. Check PR #616: `gh pr view 616 --json state,mergeCommit,mergeStateStatus`.
   - **If #616 is merged** (squash): the working branch holds the same content under different SHAs. Rebase before anything else, exactly as was done after #605/#606/#607: `git rebase --onto origin/main 2465a4c8 HEAD` (2465a4c8 = the PR-D head the squash was made from). `git rebase ... HEAD` detaches, so afterwards run `git branch -f feat/wt-session-client $(git rev-parse HEAD) && git checkout feat/wt-session-client`.
   - **Expect conflicts.** After every earlier PR the user (or a review) pushed fixes onto the PR branch before merging, so `main` held changes beyond what I had pushed (after #605: 7 server files; after #607: 12 files in `cmd/wt` and `internal/tui`). Resolve by keeping both the new `main` fixes and the later task's changes; rebuild and run the full gate afterwards. If #616 got such fixes, the Task 20 rename will conflict on any file under `cmd/wt/` that #616's review touched: git follows renames, but verify.
   - **If #616 is still open**: leave the working branch alone; PR-E stacks on it (see section 5).
3. Restore the ledger if the worktree is new: this doc plus `~/tmp/wt-session-client-sdd/` (a copy of the ledger directory, taken at handoff time; see section 8) hold everything.
4. Read the plan's Task 21 and Task 22 text and sections 4-6 below, then continue with SDD (section 3).

## 3. The process used (subagent-driven development)

Skill: `superpowers:subagent-driven-development` (plugin path `~/.claude/plugins/cache/claude-plugins-official/superpowers/7.0.2/skills/subagent-driven-development/`; scripts `scripts/{task-brief,review-package,sdd-workspace}`). Rules that were followed and should be kept:

- No pausing between tasks. A fresh implementer subagent per task, then a fresh reviewer per task, a fix loop for Critical/Important findings (rounds resume the same implementer via SendMessage, each round ends in a scoped re-review), a ledger as the recovery map.
- `scripts/task-brief PLAN N` writes the brief file; `scripts/review-package PLAN BASE HEAD` writes the review diff file (BASE = the commit recorded before dispatching, never `HEAD~1`). The implementer writes `task-N-report.md`; the controller keeps only the short contract in context.
- Always set the model on every subagent. Used: sonnet for standard/mechanical implementation and for scoped re-reviews of small diffs; opus for concurrency/security-sensitive implementation (Tasks 7, 11, 13, 16, 18, 19) and for all full task reviews.
- **Large diffs (Task 18 at 481 KB, Task 19 at 198 files): use the `Workflow` tool** for a split-area review, one opus reviewer per area, an adversarial opus skeptic per Critical/Important finding, then triage. The scripts are in `~/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup/131a01da-9180-4554-8092-56f76ecba808/workflows/scripts/review-task18-flip-wf_*.js` and `review-task19-delete-wf_*.js` (reuse as templates; the session id directory may be gone, so the script bodies are the pattern: a `COMMON` context string, an `AREAS` array, `pipeline(AREAS, review, verify)`).
- The controller never fixes findings itself; it dispatches the fix. Rulings (decisions the plan did not settle) go in the ledger as `Ruling: ... — why — cost if wrong`.
- Gates before pushing a batch: `cd wt && go build ./... && go vet ./... && gofmt -l .` (prints nothing); the full `go test ./...` in the background with an absolute log path and the exit code captured INSIDE the log (`{ ... ; echo "GATE_EXIT=$?"; } > /abs/log 2>&1`, no inner `&`; the full suite takes ~10 min); `make -C wt check`; `bin/check-links`; `make lint-skills`; `make lint` from the root. `internal/config` `TestResolveSecretExecDistinctRefsRunConcurrently` is a known 500 ms timing flake under full-suite load: rerun that package alone and say so.
- Test conventions every task enforced: every `Test*` has a top-level `//` comment (what and why); per-call seams; no fixed ports; no real config home; no sleep-based synchronization; RED evidence before GREEN.
- Never use bare `git stash`. Subagents edit only inside the worktree. Never put closing keywords (`close`/`fix`/`resolve` + `#N`) in PR bodies.

## 4. Remaining work

### Task 21: llmbench keeps its own routes (small)
Plan text: `### Task 21`. Change only the `litellm_credentials` error text in `llmbench/src/llmbench/benchmark/_routes.py` to end `launch a wt pi session first`; update the failing test `test_litellm_credentials_message_names_a_wt_pi_session`; `make -C llmbench test`. Commit `fix(llmbench): the pi credentials message names the wt pi session`.

**Scope extension (ruling, 2026-11-16, from the Task 18/19 reviews):**
- `llmbench/src/llmbench/wt_bridge.py` (~line 121) unloads omlx models with `wt stop omlx/<name> --yes`. After the flip a model-id stop always goes through the model server, so on a host with no `[model-server]` configured every unload is refused (exit 1, "nothing was stopped") and benchmark isolation fails. Not fixed in code by design (a server is required for any model stop); make `wt_bridge` print a clear hint naming `[model-server]` in `~/.config/agent-wt/config.toml` / `WT_MODEL_URL` + `WT_MODEL_TOKEN_FILE` when the refusal is the not-configured one, with a test.
- `llmbench/src/llmbench/benchmark/runner.py` ~217 fallback message still says "launch a wt pi session in litellm mode" (`wt litellm on` is retired): reword.
- `benchmarks/mtplx-profile-matrix` ~194 launches `wt --cwd -A claude -M "$SLOT_PROFILE"`, which now refuses without a model server: add a prerequisite note/check.
- Guide 09: the llmbench pi harness reads the key from pi's `models.json` `litellm` block; that block now holds the gateway origin and the client token (not the LiteLLM key). A llmbench run therefore needs a reachable `wt-model serve`. Say so (guide 09 already has a first note; verify).

### Task 22: cross-cutting docs, guides, the spec
Plan text: `### Task 22`. Also route here (all verified by the Task 18/19/20 reviews):
- Docs still describing `wt litellm on|off` or `[litellm] enabled` as live: guides 06, 07, 08 (also the modelman migration table ~498), 09; `wt/docs/wt-agents/{README,profiles,litellm-troubleshooting}.md`; `wt/docs/internals/config-and-registry.md`; `docs/reference/glm-5.3-flash-openrouter.md` (also lines 257/365 show `agents-smoke.sh --modes current`, a mode that no longer exists).
- Task 12: the plan wording promised an `IN USE` column header on the picker; it was ruled unnecessary (a headerless leftmost digit, detail says `N client session(s)`): amend the plan/spec wording.
- Task 14 deferral: `wt start`'s picker opens with a nil cfg, so blocked rows are not greyed out (refused by the start job instead): documented in `wt-start-stop.md`; a wire-aware start picker is a follow-up.
- Doc nits from Task 20: `wt/README.md:26` names only `bin/wt` (name both `wt-session` and `wt`, plus `wt-model`); `docs/guides/06-wt-agents-and-models.md:13` hand-build line refreshes only one binary (point at `make -C wt install`); `docs/guides/08-maintenance-and-troubleshooting.md:348` says `make install` builds `wt/bin/wt` and does not mention the one-time manual install step for a pre-split install.
- `docs/guides/00-config-map.md`: a note that existing installs may still have `refcount.jsonl` / `refcount.jsonl.lock` files (the code no longer reads them).
- The R17 interim limit (host commands and the Models tab still read registry/config.yaml until Phase 4) and the R18 terminal-close limit (a killed wt cannot call DELETE; its client session ends by the server's 60 s TTL) belong in `wt/docs/wt-session.md` (already written in Task 18; verify) and the spec.
- The spec edits Task 22 names; keep guides state-independent (no live model counts/snapshots).

### PR-E cleanup commit (fold the parked minors from Task 20 and any from 21/22)
- `bin/update/wt:59-60`: a pre-split install (only `~/.local/bin/wt`) is reported `not-installed` with "no binary at .../wt or .../wt-session", which the update-common contract defines as "not on this machine". Treat a missing `wt-session` as stale (fall through to `make install`, a stale note under `--check`) or at least name the missing file.
- `wt/scripts/agents-smoke.sh` keeps `--modes`/`validate_modes`/`expand_modes`/`xfail:<mode>` for a single value (`gateway`): leftover structure.
- `wt/internal/rootmove/guard_test.go:62` does not assert that the `cmd/wt-session` half of its scan finds files (`scanned < 3` is met by `internal/rootmove` alone).

### Then: PR-E, the final review, finishing
1. Full gate (section 3) on the rebased working branch.
2. Push: `git push origin HEAD:refs/heads/feat/wt-session-e-binaries`. PR body (`--body-file`, `Refs #599`, 🤖 footer) must list: the rename and the two installed binary names; `bin/update/wt` now needs one manual `make -C wt install` on a pre-split install; the agents-smoke rewrite (gateway mode only, needs `WT_MODEL_URL` + `WT_MODEL_TOKEN_FILE`); the llmbench message and hint; the docs sweep; the **not-run manual live check** from Task 17 step 5 (below).
3. Final whole-branch review per `references/final-review.md` of the SDD skill: dispatch it on the most capable model; its single fix wave; then `superpowers:finishing-a-development-branch` (present the options, wait for the user: do not merge).

## 5. Branch and PR mechanics that bit during this work

- Pushed batch heads: `feat/wt-session-a-server` (deleted after merge), `feat/wt-session-b-client`, `feat/wt-session-c-client`, `feat/wt-session-d-flip` (PR #616, head `2465a4c8`).
- The working branch is the single line of development; each batch branch is a push of `HEAD` at a batch boundary. After the user squash-merges a PR the batch SHAs vanish, so rebase the working branch with `git rebase --onto origin/main <pushed head of the merged batch> HEAD`.
- Do not rebase or switch branches in the worktree while an implementer subagent is running in it.
- There is a stray worktree `.worktrees/feat-wt-session-a-server` (not created by this work; someone else's) and old ones may remain: leave them alone. The `wt-repo-cleanup` skill / `wt worktrees` is the tool for tidying merged worktrees, on the user's say.
- If PR-E is opened while #616 is still open, base it on `feat/wt-session-d-flip` (not `main`), and rebase when #616 merges, as was done for #606.

## 6. Rulings made (the plan did not settle these)

- Task 8: honour the wire. An unmapped cloud row is `block` with the server's reason; the session has no direct-launch path. (`catalog.FromWire` never overrides the server's action; the parity test asserts this one row diverges from the pre-server `Build` row.)
- Task 10: `openModels` wraps every failure with `modelclient.Still`; callers never re-wrap it. `resolveModelRemote` errors carry no still clause: the caller adds it.
- Task 13: without a terminal, `-A X` with no `-M` and several launchable models follows legacy P5: fail with `pickerNeedsTTYError` naming the flag, no silent rotation. With a terminal it opens the API picker with the rotation-next row selected.
- Task 17: a local model a client session registers (including `wt smoke`, even if already running) is stopped by the server after the idle grace unless another client session uses it; docs say benchmarks must not smoke a model they are about to use. After one smoke row finds the gateway down, the remaining rows SKIP at once; a vanished server (unreachable/unauthorized/skew) stops the run with exit 1.
- Task 18: the N7 scan (`TestUserTextQualifiesSession`) checks user-facing text only; a not-configured server refuses every model-needing path before anything starts (including git work under `-W`/`--root`).
- Task 19: the host `wt stop <provider>|--all` no longer asks "in use by N" (only the server knows client sessions); the unreachable inUse branches stay with a comment as a Phase 3/4 follow-up. The four registry-picker behaviors that are gone (single-row auto-launch, native-first default cursor, last-picked-profile cursor, route-back when an agent's only row is a profile) were dead after the flip, not regressions. llmbench's `wt stop <model>` dependence on a server is a documented consequence, not a code fix in Task 19.
- Plan task count is 22 (an early summary said 17; stale).

## 7. Known issues, risks and deferred items

- **Task 17 step 5 live check was NOT run.** It needs a real `wt-model serve` with LiteLLM: run `wt smoke --tools --hops 5 --json` for one agent while another terminal starts a model (the route restart returns gateway 503) and note which agents retry. Record the result in the PR-E body (or a follow-up issue). This also covers #598.
- `bin/check-config-dirs-untouched` fails on a developer machine that has run wt (it is meant for clean CI runners); not caused by this work.
- The scan in `boundary_test.go` matches by name with no call graph: a future helper in an unscanned `cmd/wt` file called from a session file would not be flagged (today the only such call is the sanctioned `protectedPaths`).
- A configured but unreachable server is found only at `launchFilteredRemote`'s ping, after `-W <name>` has already created its worktree and branch. A known limit; also `wt -W <new>` with no agent and no terminal creates the worktree then refuses (pre-existing).
- `endpointConfigured()` was deleted in Task 18; `wt stats` with an unreadable `config.toml` prints a note.
- Left on purpose for Phases 3/4: `lifecycle`/`localmodels`/`provider` still linked into the session binary; dead non-gateway driver branches; `config.EligibleModels`/`ModelsWithTag`; dead lifecycle route calls (`EnsureModelRoutes`, `RestartProxy`, `ProbeProxy`, `EnsureModelRoute`, `TryEnsureModelRouteTo`); pi's gateway sync leaves a previously written `ollama` block (and its LiteLLM key) alone.
- Phase 3 (untangle `internal/tui` and the import graph) and Phase 4 (move admin commands and the Models tab to `wt-model`, `wt-model litellm set`, `wt-model check`, the session `litellm list|status`, the `wt-cloud-sync` skill rewrite) are out of scope here; issues #596 (slot profiles over LiteLLM aliases), #597 (subscription mixed slots), #598 (restart/drain: R12 is the whole client-side answer here) stay open.
- Two real bugs found by rebuilt tests, both fixed in PR-D: `Client.Spend` sent the window to the second (now RFC3339Nano), and a slot-profile TUI session ran the survey.

## 8. What lives where

- Code: `wt/internal/{modelapi,modelserver,modeljobs,modelgateway,sessionreg,modelclient,tokenfile,exitcode}` (server and client halves); `wt/cmd/wt-session/` (the session binary, renamed in Task 20; `wt/cmd/wt-model/` is the server binary); `wt/internal/catalog/wire.go`, `wt/internal/agents/gateway.go`, `wt/internal/tui/{models_client,remote_start,slot_flow}.go`.
- Docs written along the way: `wt/docs/wt-session.md`, `wt/docs/wt-job.md`, `wt/docs/wt-model-server.md`, `wt/docs/wt-start-stop.md`, `wt/docs/wt-smoke.md`, `wt/docs/wt-agents/slot-profiles.md`, style guide T1 amendment.
- Fixtures: `docs/contracts/{model-api,job,model-list.server,litellm-cli}.sample.json`; `model-list.sample.json` is untouched on purpose (modeltiers reads it).
- The git-ignored SDD workspace: `.worktrees/wt-session-client/.superpowers/sdd/2026-11-15-wt-session-client/` holds `progress.md` (the ledger), `task-N-brief.md`, `task-N-report.md` (implementer reports with RED/GREEN evidence), `review-*.diff`, gate logs, and the two split-review results `review-task18-workflow.json`, `review-task19-workflow.json`. A copy was taken at handoff time to `~/tmp/wt-session-client-sdd/` (outside the repo; removing the worktree destroys the original).
- Agent memory (`~/.claude/projects/-Users-keith-github-ohanaverse-local-ai-setup/memory/`) has an entry pointing at this file.
