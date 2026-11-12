# worktrunk vs wt — worktree & branch feature comparison

> **Status note (2026-11-07).** This is a research snapshot of 2026-11-04. wt has since gained `wt cleanup`, `wt remove` (including `--remote` for origin branches) and `wt rescue`, with JSON contracts and a removal engine that checks dirty state, merge proof and age (see [wt/docs/wt-cleanup.md](../../wt/docs/wt-cleanup.md)). Where the text below says wt has no removal or cleanup, that gap is closed; the comparison of what each tool does is otherwise as of 2026-11-04.

**Research date:** 2026-11-04
**Scope:** worktree/branch management. wt's model/provider stack (registry, LiteLLM, slot profiles, smoke) appears only as *switch cost* — what replacing wt's worktree layer would put at risk.
**Method:** read-only docs + source audit of both tools (worktrunk at `main` `3bf9166`, release v0.81.0-era; wt at `9b702c0f`), each report adversarially cross-checked against primary sources by a second reader prompted to refute. Neither tool was executed — see [Gaps](#gaps-not-verified). Evidence tiers: **verified** (adversarially confirmed), **reported** (grounded, single-source), **unverified** (static reading only).

---

## TL;DR

- The two tools overlap only at the **worktree-mechanics layer**. [worktrunk](https://github.com/max-sixty/worktrunk) is a general git-worktree lifecycle manager (create → list → merge → remove) built for parallel AI-agent workflows. Local `wt` ([wt/](../../wt/CLAUDE.md)) is an **agent launcher** whose unit of work happens to be a worktree (create-or-reuse → pick agent → pick model → launch), with no removal, merge, or cleanup at all at the snapshot (removal and cleanup have since shipped, see the status note).
- **They collide on the binary name `wt`** (worktrunk's default command name is `wt`; on Windows it also ships `git-wt`). Installing worktrunk on this machine shadows or is shadowed by local wt, and worktrunk's Claude Code plugin hooks literally invoke `wt switch --create` — PATH order would silently route those to the wrong tool. Any coexistence requires renaming one of them (worktrunk supports `--cmd <name>`).
- **Recommendation: don't replace wt; port selected lifecycle ideas** (see [Recommendation](#recommendation)). Replacing wt means rebuilding its entire model/agent stack on top of worktrunk (worktrunk launches arbitrary programs via `-x` but knows nothing about models), and adopting a solo-maintainer 0.x dependency that ships breaking changes frequently.
- The highest-value ideas to bring into wt, ranked: **full-branch-name worktree paths** (fixes a real collision block), **a removal/prune lifecycle with worktrunk's safety kernel**, **copy-ignored (reflink) seeding of build caches**, **JSON output discipline** (versioned schemas, stable outcome vocabularies).
- Verification found one real wt bug: **`wt -W origin/main` escaped the default-branch guard** (fixed 2026-11-04, [PR #516](https://github.com/ohanaverse/local-ai-setup/pull/516); see [Findings](#findings-from-verification)).

---

## The two tools

**worktrunk** (Rust, MIT OR Apache-2.0, https://worktrunk.dev) — "a CLI for Git worktree management, designed for parallel AI agent workflows." Philosophy: *"worktrees as easy as branches"* — worktrees are addressed by branch name and their paths are **computed** from a configurable minijinja template (default `{{ repo_path }}/../{{ repo }}.{{ branch | sanitize }}`, i.e. a sibling dir like `~/code/myproject.feature-auth`), never stored. Four core commands (`switch`, `list`, `merge`, `remove`) plus building blocks (`wt step …`), a ten-type hook system, aliases, and `wt-<name>` custom subcommands. Repo created 2025-10-17; 140 releases as of the research date.

**wt** ([wt/](../../wt/CLAUDE.md), Go) — a worktree **agent launcher**: choose *where* (`-W <name>`, `--cwd`, or the worktree picker), *what* (an agent CLI — claude, codex, copilot, pi, agy, opencode — or `shell`), and *which model* (the rotating picker over the local model stack), then launch. Its worktree surface is **create, reuse, and enumerate** only. There is deliberately no removal, no prune, no merge/rebase/push, no stacking, no dirty detection (see [Deliberately absent](#deliberately-absent-in-wt)). At the snapshot, cleanup lived outside wt (plain git and the `repo-cleanup` skill); `wt cleanup`/`wt remove` now cover it. Its reason to exist — the model registry, LiteLLM routing, slot profiles, smoke testing, shared-root session claims — has no worktrunk equivalent.

---

## Headline asymmetries

| | worktrunk | wt |
|---|---|---|
| **Binary name** | `wt` (Windows also `git-wt`) | `wt` — direct collision |
| **Lifecycle** | full: create, list, merge, remove, prune, backup/recovery | create-or-reuse + enumerate only |
| **Agent/model launch** | thin: `-x <program>` runs anything; no model concepts | the core: agent pickers, model pickers, slot profiles, claims |
| **Shared-root multi-session** | not a concept (each agent gets its own worktree) | session/root claims with a conflict menu |
| **Ignored files on delete** | die with the worktree (dirty gate exempts gitignored files) — mitigated by `copy-ignored` for caches | die with the worktree — no mitigation (the SDD-ledger loss is the recorded incident) |

---

## Feature comparison

### Addressing & creation

| Concern | worktrunk | wt |
|---|---|---|
| Address a worktree by | branch name, path, shortcuts `^` `@` `-`, `pr:{N}`/`mr:{N}`, PR/MR web URL | `-W <name>` (branch as typed), TUI picker rows |
| Path derivation | minijinja `worktree-path` template; default sanitizes the **full** branch name (`feature/foo` → `myrepo.feature-foo`) | `<repo>/.worktrees/<branch's last component>` (`feature/x` → `.worktrees/x`) |
| Branches sharing a last component | distinct paths (`repo.feature-x` / `repo.bug-x`) ✓ | **blocked** — second create errors `worktree path … is already in use by branch …` (safe, but the worktree cannot exist) |
| Sanitize-equivalent names (`feature/x` vs `feature-x`) | both map to the same template path — collision behavior **unverified** | n/a |
| Re-use of an existing checkout | path computed from the branch; one checkout per branch by design | `EnsureForName` is idempotent: reuses any existing checkout of the branch, **including the root** |
| Default branch | primary worktree special-cased (never pruned; `promote` swaps into it); no linked-worktree refusal documented | **refused** in both creation functions and, since [PR #516](https://github.com/ohanaverse/local-ai-setup/pull/516), in remote forms too — the old `-W origin/main` guard escape is [fixed](#findings-from-verification) |
| Remote branches | `origin/foo` resolves to local `foo`; fork PRs via `refs/pull/N/head` + `pushRemote` to the fork | picker: `origin/foo` → new local `foo` tracking it (refuses if local exists); `-W` resolves `origin/<name>` to the local name (picker parity, merged 2026-11-04) but never fetches |
| Placement escapes | `--path` (experimental) + `wt step relocate` to restore the template | none (one convention) |

### Listing & picker

| Concern | worktrunk | wt |
|---|---|---|
| Overview | `wt list`: dense status matrix (staged/modified/untracked, operation state, ahead/behind default+upstream, line diffs, CI+review color, dev-server URL, LLM summaries, markers), adaptive columns, progressive rendering | TUI picker: worktrees + local branches + remote-tracking rows |
| Remote/PR data | forge CLI (`gh`/`glab`/`tea`/`az`), CI status cached 30–60 s, review states, PR/MR pages & comment threads in-picker | two `gh pr list` calls per open (5 s, non-blocking); rows tagged `#n open|draft|merged|closed`; stale tags answered off the update goroutine |
| Interactivity | picker with **8 preview tabs** (net diff, working, committed, log, remote, LLM summary, PR, comments), fuzzy filter over branch/path/PR | 3-stage picker (worktree → agent → model); keys `n`/`enter`/`q` |
| Fetch policy | none in `list`/`switch` ("never fetches" is a stated invariant of merge; switch fetches only PR refs explicitly) | `git fetch --all --prune` capped at 5 s before every picker open (silently ignored on failure) |
| Statusline | `wt list statusline` (Claude Code / tmux; model, context usage, rate-limit pace) | none |

### Lifecycle (the core gap)

| Concern | worktrunk | wt |
|---|---|---|
| Remove | `wt remove`: dirty gate (`git status --porcelain` incl. untracked; **gitignored files exempt**), `git worktree lock` immune even with `--force`, two force scopes (`--force` = dirty worktree, `-D` = unmerged branch), background by default | **none** at the snapshot (now `wt remove`, see the status note) |
| Branch deletion safety | only when integrated, via 6 checks (same commit, ancestor, empty 3-dot diff, trees match, simulated merge adds nothing, **patch-id match** — squash-aware; walk capped at 500 commits) | `repo-cleanup` classification (STALE / OK TO DELETE / MERGED / DIRTY / ACTIVE), `git branch -d` discipline |
| Bulk cleanup | `wt step prune`: integrated worktrees+branches + stale registrations; `--min-age` 1d guard; skips locked/dirty/main; concurrent | `wt cleanup` + `wt remove` (shipped after this snapshot, see the status note) |
| Merge | `wt merge`: commit → squash (swept changes backed up to `refs/wt-backup/<branch>` with a printed recovery command) → rebase → pre-merge hooks → fast-forward → pre-remove → cleanup | none (agents own their git workflow) |
| Local integrate/push | `wt step push` (fast-forward the target ref **locally only** — "no commits leave the repository"), `step rebase`, `step squash`, `step diff` (net diff since merge-base) | none |
| Undo/recovery | `refs/wt-backup/<branch>` survives even a remove | none |
| Ignored-state tooling | `wt step copy-ignored`: reflink copy-on-write (APFS/btrfs/XFS/ReFS) seeding of gitignored files between worktrees, `.worktreeinclude` (gitignore-style, shared with Claude Code desktop), built-in excludes for VCS/tool state; `wt step promote` swaps branches **and** ignored files into the primary worktree | none |
| Process teardown | `wt step tether` (kill a command's process group when the worktree goes; survives removal by other tools), `wt remove --reap` (SIGTERM→SIGKILL by cwd) | n/a |

### Safety & concurrency

| Concern | worktrunk | wt |
|---|---|---|
| Commit guard | none (local CI via `[[pre-merge]]` hooks) | `block-main-commit` pre-commit hook in `<git-common-dir>/hooks/` (covers all worktrees; preserves the user's hook; auto-installed) |
| Dirty detection | remove/prune only | **none anywhere** |
| Self-concurrency | trash-drain lock (one deleter per repo), CAS branch deletion (`git update-ref -d` expected-tip → `retained_raced`), TOCTOU revalidation after hooks, semaphore (32 git commands), atomic no-overwrite renames; **concurrent hooks documented as racy** | n/a (few mutating operations) |
| Multi-session coordination | none (agents isolated in separate worktrees by design) | session claims (`<git-common-dir>/wt-sessions/<pid>`, pid + start-time liveness, flock on scan+write, ancestor rule, fail-open notes, a held-session warning with one default-yes question, and the picker's shared-root menu `c`/`w`/`q`); contract read by `repo-cleanup` ([session-claims.md](../contracts/session-claims.md)) |

### Automation & extension

| Concern | worktrunk | wt |
|---|---|---|
| Hooks | 10 types (pre/post × switch, start, commit, merge, remove), user + project (approval-gated store), minijinja templating, `[[hook]]` pipelines, interactive foreground hooks (v0.81.0) | the commit guard only |
| Extension | aliases (templated, multi-step), custom subcommands `wt-<name>` on PATH (git's pattern; `worktrunk-sync`, `workz`, `wt-tui` community examples), `wt step for-each`, `wt step eval` | `bin/*-wt` shims (`claude-wt` → `wt --agent claude`) |
| LLM features | commit messages (pluggable command, prompt templates), branch summaries in `wt list --full`/picker | none (the launched agent writes commits) |

### Agent integration

| Concern | worktrunk | wt |
|---|---|---|
| Role | **companion** to agent CLIs: plugins for Claude Code, Codex, OpenCode 2, pi, oh-my-pi, Gemini CLI route agent-created worktrees through `wt switch --create` / `wt remove` (WorktreeCreate/WorktreeRemove hooks) | **is** the integration: launches the agent as a subprocess with `cmd.Dir` = the worktree |
| Session model | agent's own; activity markers (🤖/💬) in `wt list` | deliberately none — no session lookup/resume (#198/#204 "Don't bring the lookup back"); continuation via the agent's own flags (`claude-wt -- --continue` works because wt launched in the worktree) |
| Extras | `/wt-switch-create` skill, `/wt-diff` pane, Claude Code statusline installer, `PermissionRequest` auto-approve for entering template-path worktrees | shared-root claims, `--replace/--others/--yolo` launch modes |

### Machine-consumable contracts

| Concern | worktrunk | wt |
|---|---|---|
| JSON | `--format json` on every command; "one structured result even on nonzero exit"; `list` schema 2 envelope with **published** JSON Schema (worktrunk.dev/schema/list-v2.json); absent-vs-`null` distinguishes "nothing" from "not determined"; `branch_outcome` stable enum (`deleted/deferred/not_attempted/retained_*`) | `--json` on model/profile/smoke/stats (with contract fixtures in [docs/contracts/](../contracts/)); worktree side has only the hidden `--debug-worktrees` |
| Exit codes | **undocumented** (readable from `src/main.rs`; a known gap) | `cmd/wt/exitcode.go` + tests (not compared — gap) |

### Portability & distribution

| Concern | worktrunk | wt |
|---|---|---|
| Platform | macOS, Linux, Windows (Git Bash required for hooks), Git ≥ 2.43, Rust MSRV 1.98 | Go 1.26.7; worktree mechanics portable-ish but the launcher stack is macOS/Apple-Silicon-specific (LaunchAgents, omlx/mtplx); portability floor unexamined (gap) |
| Install | Homebrew, crates.io, Winget, pacman, conda-forge, nix, curl\|sh; signed `packslip.sigstore.json` releases | local `make install` |

### Maintenance & adoption risk (as of 2026-11-04)

| | worktrunk | wt |
|---|---|---|
| Popularity | ★ 10,949; self-described "the most popular git worktree manager" | private tool |
| Bus factor | **1 human** — max-sixty 4,132 commits of ~5,300 (rest: worktrunk-bot 807, dependabot 305, small drive-bys) | local |
| Cadence | 140 releases since 2025-10-17 (~biweekly); pushed the day of research | merged via PRs into this repo |
| Tracker | 41 open issues; recurring themes: picker UX, remove safety, hooks, Windows, Claude Code hook latency | open items in [issues.md](../../issues.md) |
| Stability | 0.x semver; **v0.81.0 alone shipped many breaking changes** (hook stdin JSON removed, MiniJinja 3 rendering, `list` JSON symbol order, concurrent prune hooks, Ctrl-C/job-group semantics, commit-stage hook ordering) — with a maintained CHANGELOG and deprecation shims (`wt select`, `--no-verify`, state subcommands) | full control |

---

## Findings from verification

1. **REFUTED (wt) — `wt -W origin/main` escapes the default-branch guard.** `EnsureForName` (the `-W` / TUI new-worktree path) checks only `name == defaultBranch` ([create.go:31-33](../../wt/internal/worktree/create.go)), so a remote-form name passes and falls through to `git worktree add -b origin/main .worktrees/main` — the outcome is now verified: git accepts the command and creates `.worktrees/main` holding a local branch literally named `origin/main` (reproduced by a failing test, 2026-11-04). The remote-form hard refusal (`isDefaultBranchSelection`) exists only in `EnsureForBranch`, the picker path ([create.go:111-113, 158-163](../../wt/internal/worktree/create.go)); wt's own comments agree ([enumerate.go:305-308](../../wt/internal/worktree/enumerate.go)). This violates wt's invariant "the default branch is never a linked worktree." Fix candidate: route `EnsureForName` through the same remote-form check. **Fixed 2026-11-04** in [PR #516](https://github.com/ohanaverse/local-ai-setup/pull/516), with follow-up commits on the same PR consolidating one shared guard, `<remote>/HEAD` handling, and `-W origin/<name>` resolution. (The `create.go` line numbers above describe the pre-fix code.)
2. **CONFIRMED — worktrunk's docs and source agree across the checked surface.** Every documented command/flag resolves in `src/`; docs are auto-generated from clap help (`wt <cmd> --help-page`), which structurally limits drift. Hidden-but-implemented: `_delete-branch-if-safe`, `_drain-trash`, `wt hook run-pipeline`, `wt config plugins claude hook`, `--help-page`.
3. **Recorded doc/behavior discrepancies (worktrunk):** branch markers outliving their branch (max-sixty/worktrunk#4683, open at research time) vs docs claiming markers are cleared on branch delete; `wt merge` pre-remove hooks skipped (max-sixty/worktrunk#4624, closed fixed); "three core commands" README wording vs four.

---

## Deliberately absent in wt

Design choices, not gaps — do not "fix" these by porting:

- **No session resume/lookup** — removed on purpose (#198, #204; `docs/internals/launch-flow.md`: "Don't bring the lookup back").
- **No per-repository or per-branch launch state** — nothing per-worktree to clean up on a worktree removal (the one exception: a profile's `config_content` backup, restored after the run).
- **The default branch is never a linked worktree** (the [finding](#findings-from-verification) that once broke this was corrected in [PR #516](https://github.com/ohanaverse/local-ai-setup/pull/516)).
- **`-W` never fetches** — it resolves `origin/<name>` remote-tracking names to their local name since 2026-11-04 (picker parity), but only the picker fetches (5 s cap, failure ignored).
- **Claims fail open** — a claims failure is a `wt: note:`, never a refusal.

---

## Recommendation

**Do not replace wt with worktrunk.** Three reasons, in order of weight:

1. **The overlap is only the worktree-mechanics layer** — the smaller half of wt. Worktrunk has no model registry, no LiteLLM routing, no slot profiles, no model picker, no smoke; its `-x` runs arbitrary programs. "Using worktrunk instead" means keeping wt's entire launch/model stack and swapping ~`internal/worktree` for a foreign CLI — with a binary-name collision to resolve first.
2. **Adoption risk is real**: one human maintainer, 0.x, a release history that includes many breaking changes in a single release. Fine for a dev tool you can pin and fork (MIT/Apache-2.0); poor for the base your agent launcher runs on.
3. **wt's philosophy is coherent without it**: wt treats worktrees as disposable launch targets and deliberately owns no git workflow state; worktrunk treats worktrees as long-lived parallel change management. Stacking both philosophies in one binary is scope creep.

**Port, don't adopt.** Ranked by value-per-effort:

1. **Full-branch-name worktree paths** — derive the directory from the whole branch name (e.g. `.worktrees/{{ branch | sanitize }}` style, with re-use of an existing basename dir for compat). Today `feature/x` and `bug/x` cannot both have worktrees; worktrunk's default template is the existence proof that full-name derivation works.
2. **A removal/prune command with worktrunk's safety kernel** — dirty gate (`--porcelain` incl. untracked, gitignored exempt), integrated-only branch deletion via the 6 checks (especially the squash-aware patch-id match), two force scopes, background trash-rename + single drain, and a stable JSON `branch_outcome` vocabulary. This closes the largest asymmetry and the recorded ignored-file death hazard. Keep it narrow: wt should delete worktrees, not become a merge tool.
3. **copy-ignored semantics** — reflink (APFS) copy-on-write seeding of gitignored build caches into new worktrees, with a `.worktreeinclude`-style filter. Smallest useful version: seed known cache dirs at create time.
4. **JSON output discipline** — versioned envelope schemas, absent-vs-`null` distinction, published schemas, one structured result even on nonzero exit, stable outcome enums. wt serves agent CLIs; this is the contract layer they need.
5. **Addressing shortcuts** (`-` previous, `^` default-branch-as-root, `pr:{N}`) for `-W` and the picker — small, high-QoL.
6. **(Only if removal lands)** `tether`-style process-group teardown for processes bound to a worktree.

Not worth porting: the hook/alias subsystem (scope creep for a launcher), the merge/squash/rebase pipeline (the launched agents own git workflows; only the integrated-check kernel belongs in a `remove`), the 8-tab picker preview (heavy; wt's picker is deliberately thin).

**Coexistence is possible but second-best.** Worktrunk can be installed under a renamed command (`wt config shell install --cmd git-wt`) and its `worktree-path` template can match wt's layout (`{{ repo }}/.worktrees/{{ branch | basename }}`), so it could serve as a companion `remove`/`prune` tool while porting is pending — at the cost of two tools' worth of state and conventions. Prefer a spike (below) to decide.

---

## Decision (2026-11-04)

**Accepted: wt stays; the ranked ideas are ported as a six-PR series, not
adopted.** The per-PR scopes, shared vocabulary, and the deliberately
not-ported list live in the series design,
[docs/superpowers/specs/2026-11-04-worktrunk-ports-design.md](../superpowers/specs/2026-11-04-worktrunk-ports-design.md):

| PR | Port | Status at this decision |
|---|---|---|
| 1 | full-branch-name worktree paths | [PR #517](https://github.com/ohanaverse/local-ai-setup/pull/517), in review |
| 2 | `wt remove` + JSON contract | built (`wt remove`, `wt cleanup`, `wt rescue`; see status note) |
| 3 | integrated-only branch deletion + `wt prune` | spec'd, not built |
| 4 | copy-ignored (reflink) seeding | spec'd, not built |
| 5 | tether / process teardown (standalone PR) | spec'd, not built |
| 6 | addressing shortcuts (`-`, `^`, `pr:{N}`, PR URL) | spec'd, not built |

Decided with the packaging (2026-11-04): `-D` follows **git parity** — the
integrated-check kernel gates the default path and `-D` force-deletes behind
its own confirmation — and a scratch-directory worktrunk spike (never on
PATH, the binary collision above) runs before any remove/prune schema is
pinned. Worktrunk itself is not installed.

---

## Gaps (not verified)

Static research only — **neither tool was executed**. The most decision-relevant unknowns:

- **Hands-on behavior** — create/reuse/remove/merge on the same scratch repo in both tools; error paths are exactly where docs and code diverge (one such divergence already surfaced without running anything). A safe spike would install worktrunk into a scratch directory (never on PATH — the `wt` collision) and run both tools against a throwaway clone.
- **worktrunk's test suite** (`tests/`, `benches/`) unread — its dirty-gate/remove-race/prune edge cases and the only performance evidence.
- **wt's tests** unread as a behavior spec (prod code and docs matched in the read).
- **Exit-code contracts** not compared (worktrunk's is answerable from `src/main.rs`).
- **wt's portability floor** — CI OS, minimum git version, macOS-only assumptions unexamined.
- **Git-feature interactions** untested both sides: submodules, sparse-checkout, LFS, cross-filesystem worktrees (reflink vs plain copy).
- **Real concurrency** untested: wt's fail-open claims under parallel launches; worktrunk's documented-racy concurrent hooks.
- **Network surface** not compared in depth (worktrunk: version check + forge CLI calls with 30–60 s caches; wt: `gh pr list` for PR tags).
- **wt issue references** (#198, #204, #392) cited from docs, issues not opened during research.
- Critic-flagged gaps already answered by the full reports: wt's branch-name collision behavior (errors safely — in the table above) and the repo-cleanup workflow (covered in [Lifecycle](#lifecycle-the-core-gap)).

---

## Sources

Method: seven-agent research fan-out (worktrunk docs, worktrunk source audit, wt source audit), each with a refute-by-default cross-check, plus a completeness critique (2026-11-04). Full agent reports were staged under `/tmp/worktrunk-vs-wt-research/` (ephemeral).

- worktrunk: [repository](https://github.com/max-sixty/worktrunk), [README](https://github.com/max-sixty/worktrunk/blob/main/README.md), [docs site](https://worktrunk.dev), [CHANGELOG](https://github.com/max-sixty/worktrunk/blob/main/CHANGELOG.md), [releases](https://github.com/max-sixty/worktrunk/releases), [issue #4683](https://github.com/max-sixty/worktrunk/issues/4683), [issue #4624](https://github.com/max-sixty/worktrunk/issues/4624), [list JSON Schema](https://worktrunk.dev/schema/list-v2.json) (all verified live 2026-11-04)
- worktrunk source audit: `src/cli/mod.rs`, `src/cli/step.rs`, `src/cli/hook.rs`, `src/config/expansion.rs`, `src/git/remove.rs`, `src/git/ref_deletion.rs`, `src/git/repository/integration.rs`, `src/commands/step/prune.rs`, `src/commands/worktree/switch.rs`, `src/output/handlers.rs` (clone at `3bf9166`)
- wt: [wt/CLAUDE.md](../../wt/CLAUDE.md), [launch-flow.md](../../wt/docs/internals/launch-flow.md), [tui.md](../../wt/docs/internals/tui.md), [session-claims.md](../contracts/session-claims.md), `wt/cmd/wt/{main,commands,launchwarn}.go`, `wt/internal/worktree/{create,enumerate,remotes,prs}.go`, `wt/internal/{sessionclaim,rootclaim,guard}/`, `wt/internal/agents/agents.go`
- ecosystem: `repo-cleanup` skill (`~/.claude/skills/repo-cleanup/SKILL.md`)
- repo health (stars, contributors, issues): `gh api` / `gh issue list`, 2026-11-04
