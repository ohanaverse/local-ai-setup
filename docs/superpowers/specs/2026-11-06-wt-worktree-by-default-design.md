# `wt` opens in a worktree by default — Design

**Status:** approved and built (status note 2026-11-07): L1 #523, L2 #524, L3 #526; cleanup PR C #528 and PR D #529 also merged; #522 open. Originally a draft for approval (2026-11-06). Revises the scope of #521 and #522
and adds two launch changes. Reads with `2026-11-04-wt-cleanup-design.md`
(PR C and PR D there are unchanged).

## Intent

Parallel agent sessions on one repository go wrong when one of them leaves the
repo root on a feature branch. With a single session it is harmless: the PR
merges and the root goes back to the default branch. With several, one
session's branch switch confuses the others, and every `wt` worktree assumes
the root sits on the default.

Today `wt` lets the first session into the root without asking anything; only a
second live session gets the continue / new worktree / quit menu
(`internal/sessionclaim`). The fix is prevention: **`wt` does not work in the
root unless the user said so**, and every ordinary session gets its own
worktree. Working in `main` stays possible, because it is occasionally the right
thing, but it is a deliberate act. A worktree that was opened and never used
should not linger, so on exit `wt` offers to remove it (#521).

This replaces #522's premise ("repair the root afterwards") as the main answer.
A repair path is still wanted for roots that are already off the default; it is
demoted, not dropped.

## Goals

- A bare `wt` never lands in the repository root. Its default is a new
  worktree.
- Choosing the root is explicit, short to type, and does not depend on knowing
  the root's branch name (`main`, `master`, `feat`, …).
- A typo in `-W <name>` cannot silently create a branch and a worktree.
- A worktree this launch created, and the user did nothing in, can be removed
  at exit with one keypress, through the same engine as `wt remove`.

## Non-goals

- Stopping an agent that was started *outside* `wt` from running `git checkout`
  in the root. (Extending the `block-main-commit` hook to checkouts is a
  separate idea; not here.)
- Automatic removal of anything. Every removal is a keypress by the user.
- Changing the claim file format (`docs/contracts/session-claims.md`).
- The repair path for an off-default root (#522), beyond keeping the door open
  for it. See "What happens to #522".

## Anti-goals (a failure even if it "works")

- **A silent root launch.** Any route into the root that does not show the user
  they are choosing it.
- **An exit prompt that removes work.** Offering removal for a worktree that
  holds anything not provably nowhere-else-needed.
- **A prompt that cannot be skipped in scripts.** Non-terminal and `--json`
  runs never prompt and never remove.

## The launch changes

### Where a launch goes

| Entry | Resolves to | Root handling |
|---|---|---|
| bare `wt` (picker) | a worktree row, or "new worktree" | **the root row is dropped**; the picker has no way to choose the root |
| `-W <name>` | the worktree for `<name>` | see below |
| `--cwd` | the current directory | standing in the root: asks on a terminal; no terminal: refuses and names `--root` |
| `--root` (new) | the primary checkout, found from anywhere | the user's consent is the flag itself: no prompt |

- `--root`, `--cwd` and `-W` are **mutually exclusive**. Combining any two is a
  usage error.
- `--root` finds the root by the checkout (`worktree.PrimaryOf`, the primary entry
  of `git worktree list`, from the root, a linked worktree or a subdirectory;
  `RootOf` is the yes/no form for a given directory), never by branch name. It refuses outside a repository and in a bare repository (no root).
- `--root` waives the *root* prompt only. If another live `wt` session holds
  the root, the continue / new worktree / quit menu still appears;
  `--allow-shared-root` still pre-answers it. Consent to work in the root is not
  consent to share it.
- A launch from a subdirectory of the root is not the root and is not checked,
  as today (documented limit).
- `-W <name>` naming a branch the **root has checked out** is refused with a
  message pointing at `--root`. This holds for any root branch, default or not.
  Before L1 it silently resolved to the root, including `-W main` while the root
  is on `main`: `EnsureForName` returns an already-checked-out branch's path
  before its default-branch refusal (#516), which therefore only covers the
  not-checked-out forms (`origin/main`, `<remote>/HEAD`).

- **No terminal.** A bare `wt` (and the `<agent>-wt` shims, which are
  `wt --agent <agent>`) already needs a terminal for its pickers
  (`wt/CLAUDE.md`, "TTY required"), so it cannot reach the root implicitly
  today and still cannot. Nothing changes for scripts: they name a place with
  `-W`, `--cwd` or `--root`. The root gate sits on the resolved directory, so
  every entry above passes through it.
- **`--cwd` details.** "Standing in the root" means the launch directory
  resolves to the primary checkout's top level (`worktree.RootOf`). A linked
  worktree or a subdirectory is unaffected. The prompt defaults to no. If
  another live session also holds the root, only the shared-root menu is shown
  (its `c` choice is the consent, and it also offers a new worktree), so the
  user is never asked twice. A launch under a `wt` session that already holds the root (a
  parent process's claim, as `shell-wt --cwd -- npm test` run by an agent at the
  root) is not gated — and only while that claim is live: its recorded start
  time must still match the pid, so a stale claim left by a killed wt, its pid
  since recycled, exempts nothing (`session-claims.md`'s liveness rule).
  `wt smoke --cwd` passes through the same gate before its
  model is resolved; its refusal names smoke's own remedies, since `--root` is
  not smoke's flag.

### Creating a worktree by typing a name

`-W <name>` that matches no existing branch or worktree is a *creation*.

- On a terminal, ask: `no branch or worktree named "<name>"; create it
  <start>? [y/N]`. Default no. `<start>` is `from <base>`, or `as an empty
  orphan branch with no shared history (this repository has no commits yet)`
  in a repository with no commits — what git actually creates there (amended
  2026-11-06 after review).
- With no terminal, refuse unless `--new` is passed.
- A name that already exists is reused with no question, as today. `--new` is
  valid only with `-W`; with an existing name it is a no-op, and with `--cwd`,
  `--root` or no `-W` it is a usage error.
- The base is whatever creation uses today (`EnsureForName`); this design does
  not change it, and the prompt names how the branch starts: `from <current
  branch>`, `from detached HEAD <short sha>`, or the empty-orphan phrase above
  on an unborn HEAD (git infers `--orphan`, never the unborn branch).
- The picker's explicit "new worktree" row already asks for a name on purpose;
  it is unchanged.

### Exit prompt (#521)

When the launched agent exits, if the worktree is eligible, ask whether to keep
or remove it.

**Eligible** means all of:

1. This launch **created the branch and its worktree** (not one the user made
   earlier and came back to; not `--cwd`, not `--root`; not a new worktree on
   a branch that already existed, whose history this launch did not make).
2. Nothing happened in it: the branch tip still equals the tip recorded when
   the launch created it, the removal engine's preview shows no losses and no
   running processes, no hard blocker, and the row's only soft reasons are
   `fresh_branch` (and `base_is_local` when the repository has no remote).
   The branch's reflog must also still have the entries it had at creation, so
   a commit made and then undone by `git reset --hard` counts as touched (the
   tree is clean and the tip is the creation tip, but the reflog is all that
   still reaches that commit and deleting the branch deletes it); an unreadable
   or disabled reflog fails closed. Any other reason, a moved tip, dirt or an
   ignored file makes it ineligible.
3. A terminal is attached.

If it is not eligible, the worktree is kept silently.

**Ruling: how this fits the engine's min-age rule** (decided 2026-11-06 after
running the engine on a just-created worktree). The classifier files a
worktree whose branch is at the base commit but under 10 minutes old as "looks
merged, less sure" with the single reason `fresh_branch`; `wt remove` then
needs `--yes --accept-loss`, and `-D` to delete the branch (otherwise it is
kept as `retained_unmerged`). The min-age floor cannot be lowered, and this
design does not change the classifier or add stored state. Instead the exit
prompt's *eligibility* is the narrow predicate above over the engine's own
output, with the creation tip held **in memory** by the launch that made the
branch (no stored record). On "y" it removes the worktree **and its branch**
through the engine with the `Confirm` and branch-delete approvals (the same
sealed approvals `wt remove --accept-loss -D` uses), so the engine's rescan,
compare-and-swap and hard blockers still apply: anything that moved since the
prompt was built is skipped, never forced.

A session longer than the 10-minute floor is different: an untouched branch at
or behind the base then classifies **proven** with no reasons, and the engine
deletes its branch under a plain confirmation (the preview's `CanDeleteBranch`
is false because there is nothing unproven to opt in to). The prompt offers
these too, removing through `Confirmed(p.Needs)` and requiring the plan to
delete the branch; every other condition above is unchanged. Without this the
prompt would only appear for sessions under ten minutes.

**Behaviour**

- "Created by this launch" is a fact every creation route must report to the
  post-exit step: the picker's new-worktree row, `-W` after a "y", and `-W
  --new`. A route that cannot report it makes the worktree ineligible (kept).
- A worktree launch writes no session claim, so a claim cannot make it "in
  use"; the agent has exited by the time the prompt runs. The engine's own
  re-checks still decide.
- Prompt is one line; Enter keeps. Removal goes through `internal/removal`
  (re-checks, hard blockers, compare-and-swap branch delete) exactly as
  `wt remove <path>` does. A refusal by the engine is reported and the worktree
  is kept.
- A new setting chooses `ask` (default) or `keep` (never prompt). **There is no
  `remove` setting**: nothing is removed without a keypress.
- The prompt runs in the existing post-exit sequence on both launch paths (TUI
  and non-TUI), once, and does not change the exit status. Order relative to
  the stop picker and summary line must be the same on both paths.

## What happens to #522

Kept open, demoted. Its job becomes the **repair** for a root that is already
off the default (a session started outside `wt`, or an old mess): move the
root's branch and its uncommitted changes into a worktree and restore the
default in the root, refusing whenever work could be lost (in-progress
rebase/merge, conflicts, detached HEAD with nothing to keep). The root-guard
message should still suggest it. It is no longer the main fix and is not
required by anything above. It overlaps PR D (move work to a new worktree) and
should reuse it where possible.

## Effect on the wt-cleanup plan

PR C (origin removal) and PR D (move local-only work) are unchanged. New work,
in my suggested order (my call; any order works, PR C is independent):

1. **L1 — root gate:** `--root`, picker root row dropped, `--cwd` in the root
   asks, `-W <root's branch>` refused. Docs: `wt/CLAUDE.md`,
   `wt/docs/internals/launch-flow.md`, README flag table.
2. **L2 — creation confirmation:** `-W` of a missing name asks; `--new`.
3. **L3 — exit prompt (#521):** setting plus the post-exit step.
4. PR C, PR D, then #522 as the repair path.

## Decisions I made (check these)

- The flag is `--root`, and it replaces the separate `--allow-root` I first
  suggested; its own use is the consent.
- `--root` does not waive the shared-root menu.
- The exit prompt is offered only for worktrees **this launch created**.
- The setting offers `ask` and `keep` only; no auto-remove.
- `--new` as the non-terminal override name, valid only with `-W`.
- With another live session on the root, only the shared-root menu is shown,
  not also the `--cwd` root prompt.
- The `--cwd` root prompt defaults to no.
- Order L1 → L2 → L3.

## Left to the builder

- Where the exit-prompt setting lives. Settled as built: `[launch] exit_prompt = "ask"|"keep"` in `config.toml` (`wt/internal/config/launch.go`, `wt/docs/configuration.md` "Exit prompt setting").
- Wording of the prompts and error messages.
- Whether a claude launch (which writes and restores
  `<worktree>/.claude/settings.local.json`) leaves a worktree that the engine
  still calls clean. **Checked 2026-11-07 on the built binary with a real
  claude (no model call):** yes, once wt has restored the file at a normal
  exit, which happens before the prompt; only an automatic profile on a
  non-native model writes the file (`claude/native` and slot profiles never
  do), it leaves an empty `.claude/` directory git does not see, and a
  killed wt leaves the file behind so the engine correctly refuses the
  worktree. A repository that *tracks* the file gets it reformatted by the
  restore and so is not offered (#530); the predicate was not loosened.
- Test strategy; the pty driver for the TUI screens (`memory/tui-screens-via-pty`).
