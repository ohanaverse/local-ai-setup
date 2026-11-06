# `wt` opens in a worktree by default — Design

**Status:** draft for approval (2026-11-06). Revises the scope of #521 and #522
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
  Today it silently resolves to the root, including `-W main` while the root
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
  root) is not gated.

### Creating a worktree by typing a name

`-W <name>` that matches no existing branch or worktree is a *creation*.

- On a terminal, ask: `no branch or worktree named "<name>"; create it from
  <base>? [y/N]`. Default no.
- With no terminal, refuse unless `--new` is passed.
- A name that already exists is reused with no question, as today. `--new` is
  valid only with `-W`; with an existing name it is a no-op, and with `--cwd`,
  `--root` or no `-W` it is a usage error.
- The base is whatever creation uses today (`EnsureForName`); this design does
  not change it, and the prompt names it.
- The picker's explicit "new worktree" row already asks for a name on purpose;
  it is unchanged.

### Exit prompt (#521)

When the launched agent exits, if the worktree is eligible, ask whether to keep
or remove it.

**Eligible** means all of:

1. This launch **created** the worktree (not one the user made earlier and came
   back to; not `--cwd`, not `--root`).
2. The removal engine classifies it as removable without any approval: no
   uncommitted changes, no commits beyond the base, nothing unpushed, not in
   use, no hard blocker.
3. A terminal is attached and `--json` is not in effect.

If it is not eligible, the worktree is kept silently.

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

- Where the exit-prompt setting lives (`config.toml` is documented as holding
  only Agents, DefaultTag and `[litellm]`; this may need a documented
  exception, or a different file).
- Wording of the prompts and error messages.
- How to treat a freshly created branch at the base commit under the removal
  engine's "same commit as base is excluded when the branch is new" and
  min-age floor: the exit prompt must work for a worktree created minutes ago
  and left untouched. This needs a ruling in the engine, not a bypass of it.
- Test strategy; the pty driver for the TUI screens (`memory/tui-screens-via-pty`).
