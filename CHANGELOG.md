# Changelog

## Unreleased

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

### Notes

- Rotation state-file scheme is unchanged in this PR. The per-slot rotation
  refactor lands in PR 3.
- TUI behavior is unchanged in this PR. The model picker will honor `-T`/`-F`
  in PR 3.
