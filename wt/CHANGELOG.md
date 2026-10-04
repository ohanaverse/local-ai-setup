# Changelog

## Unreleased

### Changed

- `wt litellm sync` reconciles every route, not just the local ones (#179,
  "configured is exposed"). It routes every registry cloud model whose
  provider is non-native and has a LiteLLM mapping, plus the running local
  models (an ollama model counts while it is pulled, loaded or not); rewrites
  wt rows that drifted from the registry (prices, base URL, credentials); and
  removes the rows wt owns that are no longer desired. Every row wt writes
  carries `model_info.wt_managed: true`. wt touches a row only when it carries
  that marker or is named like a managed registry id (an unmarked row of a
  desired id is adopted once); hand-written rows are never removed or
  rewritten. A rebuilt row keeps the old row's hand-added `litellm_params`
  keys other than `model`/`api_base`/`api_key`. New `--dry-run` prints the
  plan (add/adopt/rewrite/remove/errors) and the probe warnings and writes
  nothing. `wt litellm list` prints a hand-written row as
  `id<TAB>(hand-written)` and its `--json` gains `rows: [{id, managed}]`.
  A `config.yaml` whose `model_list` is not a list is refused on every
  writing path.
- Stopping an ollama model no longer removes its LiteLLM route: a pulled
  model is still served on request (#179).
- Local models are discovered, not configured (#179 Phase B). A local row in
  the model picker, `wt start`, `wt smoke` and the non-TUI launch path now
  comes only from what wt's live probes find on disk or running; a registry
  entry is an optional overlay (family, tags, cost, `model_info`) matched to
  a found model by provider family and `model_name`. A registry local model
  that is not on disk is no longer listed (a failed probe still lists it, as
  `unknown`, and a stopped `mlx_lm_server` pairing is not listed either);
  pinning one (`-M`, `wt start`, `wt smoke`) says
  `<id> is not on disk — pull or download it first`.
- A local model with no registry entry is routed through LiteLLM like any
  other (#179 Phase B), under its discovered id `<family>/<artifact>`
  (`ollama/<name:tag>`, `omlx/<dir>`, `mtplx/<org>/<name>`): by the start
  hook when wt starts it, and by `wt litellm sync` while it runs (an ollama
  model while it is pulled). The row carries the marker and $0 pricing. A
  discovered route never replaces a hand-written (unmarked) row of the same
  name. **The first sync after upgrading adds a route for every pulled
  ollama model and every running omlx/mtplx model that has no registry
  entry** — additive, listed by `wt litellm sync --dry-run`, and removed
  again when the artifact goes. A local model started outside wt has no
  route until a sync runs or wt launches it (see Fixed, #192).
- Starting a model on a single-model provider (omlx, mtplx) clears that
  provider family's routes — every wt-marked row (discovered siblings
  included) and the family's registry-model rows, marked or legacy-unmarked
  — keeping only the started model's own route; stopping one clears them
  all (#179 Phase B). Hand-written rows are still never removed.
- `wt litellm sync` freezes every route of a local provider family it cannot
  vouch for — its discovered routes as well as its registry ids — when the
  family's probe ran and was neither OK nor refused, or when the registry
  references the family without a resolvable location (#179 Phase B). A
  family the registry does not reference at all is not frozen: its leftover
  marked routes are removed.
- Stopping several models at once (the post-exit stop picker or `wt stop`)
  restarts the LiteLLM proxy once at the end instead of once per model (#142).
- The post-session stale-pricing notice is no longer printed when no model
  takes its price from OpenRouter, e.g. a catalog of only ollama cloud models
  and native agents (#151).
- Native models (the agent's own subscription model) now always sort first in
  the model picker (#172). Before, a native model with no price data sorted
  as "no data", below every priced cloud model. **This also changes which
  model is selected by default.** The pickers that have no other default
  (`wt smoke`/`wt start`'s standalone picker, and `wt`'s no-rotation
  fallback) highlight the first row, so where a native model is present a
  bare Enter now picks it — launching the agent's own subscription model
  rather than the cheapest cloud model. Use `-M` to pin a model explicitly,
  or arrow down; rotation still decides the cursor in `wt` once a launch has
  been recorded for that agent.
- The model picker is now an aligned table whose header is rendered as the list
  title:
  `FAMILY  MODEL  LOC  STATUS  RUNNING  COST  1D  7D  30D  SURVEY`
  The table keeps the two ordering groups described below, now under the
  native-first rule above: rows sort cost-ascending (output price, then input
  price; local and subscription-only models count as $0, and a model with no
  price data sorts last within that group), then by 7-day usage ascending,
  then by id; non-running local models form a second group sorted by id. The
  previous compact one-liner
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
  N; Ctrl+C at the prompt aborts) and refuses when stdin is not a TTY. In the
  picker it covers only the pinned row; other start rows still show the
  replace dialog.
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

- Launching a running local model that wt did not start no longer fails with
  `Invalid model name` (#192). wt writes the model's LiteLLM route, if it is
  missing, before handing the model to an agent — on a `-M` pin, on rotation,
  in the picker and in `wt smoke` — and `wt start <id>` on a running model now
  repairs the route instead of only reporting `already running`. It prints
  `wt: LiteLLM route for <id> updated` when it wrote one. A hand-written row
  is never replaced, and nothing is written when the launch dials the provider
  directly.
- The start hook writes a model's route under the id the picker showed. An
  artifact whose name resembles a registry model's (`org/name` beside `name`)
  was routed under the registry model's id (#195).
- The model picker (TUI) fetches the agent's full catalog once and filters it
  in place via `cfg.EligibleModelsIn`, sharing a single traversal with
  `EligibleModels` instead of re-scanning the catalog to build a family-count
  map. (The map and its column are gone with the compact layout.)
- A configured local model now reads `unknown` rather than `absent` when the
  probe could not determine whether its artifact is present (a failed ollama
  probe). Previously a transient daemon hiccup made every configured
  ollama model unlaunchable in the TUI even though the same model launched
  fine through `-M` and the non-TUI path. (The `absent` status itself is
  gone since #179 Phase B: a model the provider answered for and does not
  have, and a non-running `mlx_lm_server` pairing, now have no row — see
  Changed and Removed.)
- pi can launch a discovered local model (on disk, no registry entry). The
  pre-launch sync of `~/.pi/agent/models.json` only wrote registry models, so
  a discovered launch target had no entry and pi silently ran its own default
  model instead (`pi: model "…" not configured for pi, using default model`).
  The launch target is now synced like a registry model (so, as for one,
  not in direct mode while pi has no `models.json` yet): under the
  `litellm` pi provider keyed by its discovered id when routing through
  LiteLLM, or under the pi provider named after its registry provider keyed
  by the artifact name when direct (#179 Phase B). A hand-written pi provider
  block holding that artifact stays the user's: the discovered target does
  not count toward the legacy "wt wrote every model in this block" ownership
  inference, so its `baseUrl`/`apiKey` are not resynced.
- pi's sync follows the launch's resolved route, not the LiteLLM toggle
  alone. With the toggle off, a model whose provider shares no protocol with
  pi is forced through LiteLLM and looked up as `litellm/<id>`, but the sync
  only wrote direct entries, so pi silently ran its default model. The forced
  launch target now gets its `litellm` entry and the gateway endpoint
  (creating `models.json` if needed), and that provider's own `secret_ref`
  can no longer fail the launch — LiteLLM holds the key.
- opencode's skip-permissions flag is `--auto`, passed as `--auto=true`. wt
  still passed `--dangerously-skip-permissions`, which opencode 1.18 does not
  have, so `wt --yolo -A opencode` and every opencode row of `wt smoke`
  (which forces yolo) ended in opencode's usage text and exit 1. The value is
  attached because a bare `--auto` in front of a subcommand swallows it
  (`opencode --auto run …` starts the TUI in a directory named `run`); with
  `=true` the flag leads every argv form — the interactive launch
  (`opencode --auto=true`), smoke's one-shot (`opencode --auto=true run
  <prompt>`) and a passthrough subcommand (`wt --yolo -A opencode -- run
  <prompt>`). Note `--auto` approves permissions that are "not explicitly denied":
  unlike an unconditional skip, a permission the opencode config denies
  stays denied.
- `wt smoke` no longer reports PASS for an agent that fell back to a
  different model. A row whose driver could not select the model under test
  is FAIL (`<agent> fell back to its default model instead of <id>: …`) and
  the agent is not run — the default model echoes the sentinel just as well,
  which is how the pi bug above passed smoke. A real launch still warns and
  continues.

### Removed

- `wt litellm expose` and `wt litellm unexpose` (#179), with their
  `--dry-run`/`--skip-ready-gate` flags. They remain as hidden stubs that exit
  1 and point to `wt litellm sync`, so a script calling them fails instead of
  silently doing nothing.
- The model picker's EXPOSED column (#179). Every configured native, cloud and
  local model is in the catalog (local: see the Phase B entry above — a row
  needs the model on disk or running); whether a model is routed is
  `wt litellm list`.
- Reading modelman's per-model state from `modelman.toml` (#179): first the
  `exposed` / legacy `litellm_exposed` keys, then (Phase B) `ready` and
  legacy `downloaded` too. wt now reads no `[model_state]` key at all — what
  is on disk and what is running come from its live probes.
- The ready gate (#179 Phase B): a local model is routed because a probe
  found it running (or pulled, for ollama), never because modelman flagged
  it downloaded.
- The "not in LiteLLM" refusal for discovered models (#179 Phase B): an
  unregistered local model is selectable with LiteLLM routing on, in the
  picker, with `-M` and in `wt smoke`. The refusal remains for a cloud model
  whose provider has no LiteLLM mapping.
- The `absent` picker status and the unselectable "not on disk" row (#179
  Phase B): a registry local model that is not on disk has no row.
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
