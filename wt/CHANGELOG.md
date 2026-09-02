# Changelog

## Unreleased

### Changed

- Model picker rows are now compact one-liners: each row renders as
  `family  <fam-30d>  <provider/model>  <location>  <1d/7d/30d> [tags]`,
  with the family name leading every row. The multi-line card layout and
  the family divider header rows are gone — family context is inline on
  each row. Typing a family name (or any part of a model ID) into the
  picker's `/` filter narrows the list. Navigation indices are dense
  (0..n-1); up at the first row wraps to the last and down at the last
  wraps to the first.

### Breaking changes

- `-w` short flag for `--worktree` has been removed. Use `-W` or `--worktree`.
  Running `wt -w foo` now errors with: `-w is removed; use -W or --worktree`.

### Added

- `-A`/`--agent` short flag (alias for `--agent`).
- `-M`/`--model` flag to pin a model as `<provider>/<name>`. Verified against
  the eligible model list for the chosen agent.
- `-T`/`--tags` flag to filter models by tag (comma-delimited, OR within flag).
- `-F`/`--family` flag to filter models by family (comma-delimited, OR within flag).
- `internal/config.EligibleModels(agent, tags, family)` returns the models
  usable by an agent after applying tag and family filters.
- `-A` accepts command agents (currently `shell`); commands skip the model
  picker and launch directly with no model, no yolo, no session resume.
- New `phaseAgent` picker screen after the worktree picker: lists agents
  and commands, `enter` on an agent transitions to the model picker,
  `enter` on a command launches immediately.
- Global rotation replaces the legacy per-tag rotation. State is kept in a
  single file, `~/.config/agent-wt/rotation.state`, holding one bare model
  id; the next picker entry lands on the model after the last-launched one
  within the current `-T`/`-F` eligible set. Legacy `rotation-*.state` files
  are folded in once on first launch and then removed.
- Model picker now honors `-T` and `-F` filters from the CLI: only
  models matching the agent + tag set + family set are eligible.
- When the eligible list contains exactly one model, the model picker is
  skipped and the agent launches (or the resume prompt appears, if a
  prior session exists). Reuses the existing session-check and
  rotation-recording flow, so cancelling the resume prompt leaves
  rotation untouched.

### Fixed

- Family 30-day counts in the compact model picker are now aggregated
  from the agent's full catalog (`cfg.ModelsForAgent`) instead of only the
  `-T`/`-F`-filtered eligible subset. This restores the invariant that a
  family's usage total includes launches of models currently hidden by
  tag/family filters, which the previous family-divider layout guaranteed.
- The empty (unnamed "other") family's 30-day count is shown alongside its
  `-` family column, matching the family sort key instead of a hardcoded 0.
- The model picker (TUI) now fetches the agent's full catalog once and
  filters it in place via `cfg.EligibleModelsIn`, sharing a single traversal
  with `EligibleModels` instead of re-scanning the catalog to build the
  family-count map.

### Removed

- The `d` keybinding in the model picker has been removed. Tag groups
  are now selected via the `-T` flag instead of an in-picker toggle.

### Changed

- Worktree picker now always shows at least the default branch plus the
  `+ New worktree…` sentinel, even from inside a worktree. Rows are
  ordered: sentinel → local branches and worktrees alphabetical →
  remote-only branches alphabetical with a separator.
- Picker is skipped when `-W`/`--worktree`, `--cwd`, or non-git-repo
  conditions hold.

### Notes

- All PRs 1-4 of the wt flow cleanup plan are bundled in this release;
  see `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md`
  for the unified design.
- `CLAUDE.md` and `docs/wt-agents/{README.md,shell-wt.md}` were re-synced
  to the new flag surface and three-input mental model (directory /
  agent-or-command / model). The legacy `-w` short flag references were
  removed; `-W`/`-A`/`-M`/`-T`/`-F` are now documented everywhere they
  apply. The Rotation (Go) section reflects the global rotation introduced
  in PR 3.
