# Changelog

## Unreleased

### Changed

- The model picker is now an aligned table whose header is rendered as the list
  title:
  `FAMILY  MODEL  LOC  STATUS  EXPOSED  RUNNING  COST  1D  7D  30D  SURVEY`
  Rows are sorted cost-ascending (output price, then input price; local and
  subscription-only models count as $0, and a model with no price data sorts
  last within that group), then by 7-day usage ascending, then by id;
  non-running local models form a second group sorted by id. The previous
  compact one-liner
  (`family  <fam-30d>  <provider/model>  <location>  <1d/7d/30d> [tags]`) and
  its family divider header rows are gone — including the per-family 30-day
  count column, so there are no inline `[tags]` either. Navigation indices are
  dense (0..n-1); up at the first row wraps to the last and down at the last
  wraps to the first.

### Breaking changes

- `-w` short flag for `--worktree` has been removed. Use `-W` or `--worktree`.
  Running `wt -w foo` now errors with: `-w is removed; use -W or --worktree`.

### Added

- `-A`/`--agent` short flag (alias for `--agent`).
- `-M`/`--model` flag to pin a model as `<provider>/<name>`. Resolved from
  live rows: a cloud or running local model launches; a non-running local
  model is started first (timestamped progress on stderr, Ctrl+C cancels, a
  second Ctrl+C exits); a model that cannot be used errors with the row's own
  reason. The pin is looked up among all rows, including models discovered on
  disk that have no registry entry.
- `--replace` flag: with `-M`, start the model even if it means stopping a
  running one. Without it, an occupied provider prompts y/N on a TTY (default
  N) and refuses when stdin is not a TTY.
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

- The model picker (TUI) fetches the agent's full catalog once and filters it
  in place via `cfg.EligibleModelsIn`, sharing a single traversal with
  `EligibleModels` instead of re-scanning the catalog to build a family-count
  map. (The map and its column are gone with the compact layout.)
- A configured local model now reads `unknown` rather than `absent` when the
  probe could not determine whether its artifact is present — a failed ollama
  probe, or any non-running `mlx_lm_server` row, whose target+draft pairing is
  not discoverable. `absent` is reserved for a provider that answered and does
  not have the model. Previously a transient daemon hiccup made every configured
  ollama model unlaunchable in the TUI even though the same model launched
  fine through `-M` and the non-TUI path.

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
