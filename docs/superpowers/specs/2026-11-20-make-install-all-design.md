# `make install-all`: one install for the whole wt stack

Status: approved by the user in conversation, 2026-11-20 (short design for a small change).

## Intent

`wt` is now several components: the `wt`/`wt-session` session binary, the `wt-model` host binary, and the `wt-repo-cleanup` skill that agents use. Setting up a machine meant `make install` plus up to three separate `make install-skill-*` commands, installing a skill even for agents the machine does not have. One target should do the whole install and skip agents that are not there.

## Goals

- `make install-all` installs everything this machine can use: `make install` (wt, wt-session, wt-model, shims, llmbench and modeltiers venvs), then the `wt-repo-cleanup` skill for each of claude, pi and codex whose command is on `PATH`.
- It says what it did: one `skipped <agent>: not on PATH` line per absent agent and a final `skills: installed: ...; skipped: ...` line.

## Non-goals

- No new agents. Only the three that have a known global skills directory today (`~/.claude/skills`, `~/.agents/skills` for Pi, `~/.codex/skills`). copilot, opencode and agy stay out until someone has verified their skill directories.
- No other skills. `wt-repo-cleanup` is the only global one; the other `wt-*` skills are repo-local.
- `install`, `install-skill-*` and `uninstall-skill-*` do not change.

## Anti-goals

- Installing a skill for an agent that is not installed.
- Touching anything outside the three skill directories, or the real home in tests.

## Approaches

1. **Make-only (chosen).** `install-all` runs `install`, then `install-skills` runs the existing `install-skill-*` targets for the agents found. It reuses the tested copy logic.
2. A `bin/install-skills` script doing detection and copying: a second place that knows the skill paths, which could drift from the Makefile.
3. Rewrite the three targets as one parameterised rule: tidier, but changes targets other docs name.

## Design

- Detection is `command -v <agent>` on `PATH` (user's call: the same test `wt` itself uses to launch an agent).
- `install-skills` loops over `claude pi codex`; a failing `install-skill-*` fails the target.
- `install-all` is `install` followed by `install-skills`. No agent found is not an error (exit 0, the binaries installed fine) and the summary says so (my call).
- Target name `install-all` (my call; matches `test-all`).
- A test, `bin/test-install-skills`, runs `make install-skills` in a scratch `HOME` with a scratch `PATH` holding only fake agent commands, and checks that exactly the present agents get the skill, the absent ones get nothing, and no other directory appears. It runs in `make test-all` (so in `make pre-pr`).
- Docs: root `CLAUDE.md` (Commands and the skills line), guide 08 §9, `wt/CLAUDE.md` Installation, and the README's Makefile tree comment.
- The test unsets make's own variables (`MAKEFLAGS`, `MAKELEVEL`) and runs with a scratch `PATH` of fake agents plus symlinks to `sh`, `cp`, `rm` and `mkdir` only, so neither a command-line `HOME=` nor an agent in `/usr/bin` can change the result; it also covers a failing copy (non-zero exit, no summary line).
