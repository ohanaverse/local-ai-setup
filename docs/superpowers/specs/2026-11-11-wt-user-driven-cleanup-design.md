# wt: user-driven repo maintenance (simplify cleanup)

Status: draft for review. Date: 2026-11-11. Supersedes the gating and automatic-behavior parts of `2026-11-04-wt-cleanup-design.md`, `2026-11-08-wt-restore-root-design.md` and the root-launch gating from #523/#524. The cleanup snapshot/classify pipeline, the removal engine, `rescue` and `restore-root` stay.

## Intent

wt has to expect the unexpected. Agents switch the root checkout to other branches, sessions end badly, PRs merge while a worktree is open. wt cannot prevent that, and every attempt to do so (gates, refusals, hidden rows) has made wt harder to use without stopping it. The user knows which worktrees and branches they want. wt should show the true state of the repository, warn where something may go wrong, offer maintenance actions beside each item, and then do what the user chooses.

Why now: after the recent cleanup PRs the picker hides rows the user used to see (the root, the default branch, any branch held by a worktree), the launch flow has several separate gates, and agents still move the root anyway.

## Goals

1. **One list, two entry points.** A single worktree/branch list is the selector at `wt` launch and the whole of a new `wt` sub-command (working name `wt worktrees`; builder may pick a better name, keeping `wt cleanup` as an alias). It always offers maintenance actions.
2. **Show everything local.** Every worktree (the root included, always), every local branch, and remote branches in a separate group. Branch-to-worktree association is explicit on each row: a branch held by a worktree is one row, not two.
3. **Per-row status**, computed by the existing `internal/cleanup` pipeline: uncommitted changes (not gitignored), local commits not pushed, upstream deleted, PR association and state (open/merged/closed), held by a live wt session, and a verdict "safe to clean up / not, and why".
4. **Warn, don't block.** Choosing any row launches it. Risky choices (root, a row held by another session, merged PR, deleted upstream, default branch) show a warning in the list and a one-line confirm at launch, never a refusal.
5. **Actions beside each item**, available from the launch selector and the sub-command alike: remove worktree, delete local branch, delete remote branch (proven or confirmed), rescue, restore root, refresh (`git fetch --prune`).
6. **Root drift is visible and fixable.** The root row always shows its live branch. When it is not the default branch it carries a badge ("root is on X, not main") and a restore action.
7. **Agent-callable.** Every action the TUI offers is reachable non-interactively with `--json` (already true for list, remove, rescue and restore-root); the repo-cleanup skill drives the same commands.

## Non-goals

- Preventing branch switches in the root. wt does not install watchers, git hooks that block checkout, or anything that tries to make the root unswitchable.
- Automatic cleanup of any kind (see "Removed" below).
- New data-loss protections. Existing ones for real data loss stay.
- Changing the JSON contracts' shape beyond what showing more rows needs.

## Anti-goals (a failure even if it works)

- A row that exists in git but is missing from the list, for any reason other than an explicit user-chosen filter.
- A refusal the user cannot override at launch, other than the cases listed under "Hard stops".
- Two interfaces (picker vs cleanup) that classify the same item differently.
- wt silently changing repository state (pruning, fast-forwarding, creating branches or worktrees, installing hooks) as a side effect of opening the list.

## Design

### The list

One row model, built from `internal/cleanup` Gather+Classify, rendered by one TUI component used by both entry points. Groups, in order: **Worktrees** (root first, labelled `root`, then linked worktrees), **Local branches without a worktree**, **Remote branches** (collapsed by default; a remote branch with a same-named local branch is folded into that row with a `+remote` marker rather than dropped). Default-branch rows are shown like any other.

Columns/tags per row: name, branch, path (worktrees), PR tag (`#N open|merged|closed`), status tags (`dirty`, `unpushed`, `upstream gone`, `held by session <id>`, `locked`), and a verdict (`clean-up safe` or the reasons it is not). Verdicts reuse today's classification (proven / less sure / unique work / in use), but "in use" is no longer a reason to hide or block anything, only to label.

The refresh action runs `git fetch --prune origin` (all remotes, as today's cleanup does) on request. Opening the list does **not** fetch or prune. [My call.] The list header shows when remote state was last refreshed and offers `r` to refresh; the sub-command accepts `--fetch` for the non-interactive path, and `wt cleanup`'s current fetch-first behavior stays for its `--json`/agent use unless `--no-fetch`.

### Warnings at launch

Selecting a row always proceeds after at most one confirmation line listing every warning that applies (root; held by another session; PR merged; upstream gone; default branch; dirty and held). Default answer is yes for warnings, with no typed phrases. Non-TTY launches print the warnings and continue. This replaces `gateRoot`, `refuseRootBranch`, `confirmCreate`, `confirmSharedRoot` and the `--cwd`-in-root refusal as separate gates with one `launch warnings` function. Bare-branch rows still create a worktree on selection, but state it in the confirmation line.

`-W <branch>` follows the same rule: naming the root's branch or the default branch launches in the root (or reuses the existing worktree) with a warning instead of refusing. Creating a linked worktree for the default branch is allowed when asked for explicitly. [My call; the original refusal exists because git forbids two worktrees on one branch, so the builder should keep git's own error for that case.]

### Session tracking

Keep claims, but change what they do: they feed the "held by session" tag and the launch warning only. They no longer block launch, `restore-root` preflight or cleanup classification; a held row is labelled in the list and the remove/restore-root confirmation names the holder. Claims cover every launch (root, linked worktree, branch), not only root launches, so a session working in a worktree is visible. Stale claims (dead pid) are ignored in display and pruned on refresh. [My call on extending claims to linked worktrees; builder to confirm the cost.]

### Root drift

The root row is built from live `git` state every time the list is built, never cached. If the root's branch is not the default branch, or HEAD is detached, the row carries the drift badge and `restore-root` appears as the first action. The same badge is shown in the `wt` launch header even when the user selected something else. `restore-root` itself stays as designed (rescue-verified move of the branch and its work into a worktree, then root back on the default branch in sync with origin), but `root_held` stops being a refusal: it becomes a confirmation that names the holding session.

### Cleanup actions

All removal goes through `internal/removal` as today (re-check before acting, compare-and-swap). Approval is simplified to: proven items need one confirm; anything with unpushed commits, uncommitted changes or unique work needs the typed word `delete` (today's `delete` / `delete origin`). The separate `--yes`, `--accept-loss`, `--force`, `-D` ladder on `wt remove` collapses to `--yes` (proven) and `--accept-loss` (anything at risk), with `--remote` still explicit and never implied. [My call; the builder should keep the old flags as accepted aliases for one release.]

### Hard stops (the only refusals)

These stay because proceeding would destroy work or corrupt git: removing the cwd (or an ancestor of it), removing the primary checkout, a rebase/merge/cherry-pick in progress, a locked worktree, and the engine's existing integrity refusals (submodules, non-sha1, case-colliding branch names, missing directory). They surface as a labelled reason on the row, not as a hidden row.

### Removed or reduced

- Picker row filters (`SkipInPicker`, root drop, `!used[b]` branch drop).
- Fetch+prune on every picker open.
- The exit prompt (offer to remove a launch-created worktree at session end) and its `gh` fan-out. Cleanup is something the user starts. [My call: this is the only unrequested removal path; removing it also drops the `[launch] exit_prompt` setting. Needs the user's confirmation.]
- Auto-named `session-<pid>-<time>` branch creation from the shared-root menu: the shared-root menu goes away with the gate; use the normal selector.
- Per-gate TTY/no-TTY refusal paths.

### Kept as they are

- The `block-main-commit` pre-commit hook. It is the user's explicit protection for commits to `main`; it only blocks commits, never branch switches, and `--no-guard` / `WT_SKIP_MAIN_BLOCK=1` remain. [My call: keep, but wt prints that it installed it, and does not reinstall silently on every launch.]
- `rescue`, `restore-root`, the cleanup JSON contracts, `--no-fetch`, reuse-first `-W`.
- The repo-cleanup skill's commands, which now simply call the same sub-command with `--json`.

## Constraints

- Go module under `wt/`; follow `wt/CLAUDE.md`. Contract fixtures under `docs/contracts/` change in the same commit as any JSON key change.
- Guides do not embed live state (`docs/guides/` rule); update `08-maintenance-and-troubleshooting.md` §9 and `wt/docs/wt-cleanup.md`.
- TUI changes need real-screen checks at 80x24 (pty driver, per project practice).
- Ship as stacked, reviewable PRs rather than one large one.

## Left to the builder

Sub-command name and flag spelling; exact keybindings and layout of the list; PR slicing and order (suggested: 1 unify list and drop filters, 2 warnings replace gates, 3 drop prune-on-open and exit prompt, 4 claims for all launches, 5 approval simplification and docs); how the non-interactive form lists remote branches; whether remote branches load lazily.

## Row model and JSON (author's calls, from builder review)

- **One row model, one classifier.** The TUI list is a view over the `internal/cleanup` rows; there is no second display type. `cleanup --json` includes the root, default branch and held rows too, so the agent path sees what the user sees. Each row carries its verdict plus a `hard_stop` reason where one applies.
- **Verdict floor.** The root, the default branch, the current worktree and any held row can never be `proven`. `wt remove` keeps refusing the primary checkout and the cwd with no override; a held or default-branch item needs `--accept-loss`. So an agent sweeping `proven` rows cannot touch them.
- **One row per worktree or branch.** A remote ref folded into a local row is a `remote` field on that row (upstream name, gone or present), not a second row. Actions name the row; `--remote` still chooses whether the origin ref goes too. A remote-only row is its own row; selecting it at launch creates a local tracking branch and a worktree, and the confirmation line says so.
- **PR lookup is asynchronous.** The list renders at once from git state; PR tags fill in as `gh` answers, cached for the life of the list. When PR state is unknown (no `gh`, offline, still loading) the row shows `PR ?` and its verdict is never `proven`.
- **`wt cleanup` and fetching.** Fetching is decided by the command, not the entry point: `--json` and non-TTY runs fetch first unless `--no-fetch`, exactly as today; the interactive list never fetches on open and refreshes on `r` or `--fetch`.
- **`wt remove` flags are not changed.** The `--yes / --accept-loss / --force / -D / --remote` set and its exit codes stay as they are; only the TUI confirmations are simplified to one confirm for proven items and a typed `delete` for anything at risk. This replaces the earlier idea of collapsing the flags.
- **Claims.** Keyed by the worktree path (written after a bare-branch launch has created its worktree), recording the pid and its start time so a reused pid is not mistaken for the session. They end when the process does; stale ones are pruned on refresh.
- **Config.** `[launch] exit_prompt` is tolerated and ignored with a one-time warning, not a validation error.
- **`restore-root` and a held root.** `root_held` becomes an acknowledged warning: interactively a confirm, non-interactively covered by `--yes`; the result contract reports it as a warning, with its fixture updated.
- **Launch confirmation.** A pick with no warning (including a bare branch that will get a worktree) proceeds with one informational line and no prompt; a pick with any warning prompts once, default yes.

## Decided by the user

- The exit prompt is removed.
- The `block-main-commit` hook stays; wt announces when it installs it and stops reinstalling silently on every launch.
- Launches without a terminal (agents, scripts) in the root or on the default branch warn and proceed; no opt-in flag.
