# Changelog

## Unreleased

### Added

- `wt stats` reports launches and LiteLLM spend per model. Below the survey
  table it prints a second table for the same `--window`: MODEL, LAUNCHES,
  REQUESTS, PROMPT, COMPLETION, SPEND. Launches come from `usage.jsonl`;
  the rest is one query against the proxy's `"LiteLLM_SpendLogs"` table
  through `psql`. The database is found the way the proxy finds it, with
  nothing to export: `WT_LITELLM_DATABASE_URL` (legacy alias
  `MODELMAN_LITELLM_DATABASE_URL`), else `general_settings.database_url` in
  LiteLLM's `config.yaml`, else `DATABASE_URL` — an `os.environ/NAME` value
  and `DATABASE_URL` are read from wt's environment and then from the
  proxy's LaunchAgent plist. Without `psql`, a reachable database or a
  configured URL, the launch counts still print, the spend cells show `-`,
  one note on stderr says why (an unreachable database is named by host and
  port, and by nothing else from the connection string), and the exit code
  stays 0. Requests the proxy logged under the other spelling of a wt id —
  `mtplx/Org/Name` for `mtplx/Org--Name` — are added to that id's row, when
  the logged id is not itself a registry or launched id. `--family` narrows
  the new table; `--agent` narrows its launch counts and leaves spend out.
  `--family` is `stats`' own flag and takes one exact family: the root
  command's `-F` shorthand, which `wt stats` used to accept and ignore, is
  now an error there. This replaces `modelman usage report`, which still works until modelman
  is removed. Not carried over: `--days N`, the Markdown output, the
  Reconciliation sections and the "Last wt launch" line.
- `wt stats --json` prints both tables as one JSON document (`window`,
  `as_of`, `survey`, `usage`) on stdout; notes stay on stderr. A usage row
  lists in `also_logged_as` the other spellings whose requests it includes.

### Changed

- omlx is handled as the multi-model pool it is (#213). `wt start` loads an
  omlx model beside the ones already loaded instead of stopping the service
  first, and asks only when the model does not fit, naming what omlx is
  expected to unload. `wt stop <model>` unloads that model and leaves the
  others up; `wt stop omlx` stops the service. Routes follow each model's
  loaded state, so two loaded omlx models are both routed. `wt start --json`
  and `--plan` give scripted callers the plan and the result. On an omlx
  that wants its API key for load and unload but not for inference, and with
  no `auth.secret_ref` in the registry, a start still loads the model through
  a keyless chat request, and a model stop fails with the two ways to proceed.

### Fixed

- The `wt` picker says which omlx models a start unloaded (#258). The line
  was printed only on stderr, which the full-screen picker hides, so a model
  another session was using could go without a word. It is now on the
  picker's status line after a failed or cancelled start, and above the
  agent's output after a successful one (and on the status line again if
  the agent then fails to launch).
- An omlx model that is still loading is no longer treated as loaded (#259).
  `wt start` on it waits for the load instead of printing `already running`,
  and the pickers show `load` in the RUNNING column and start it on Enter
  instead of launching an agent on a model that cannot answer yet. It still
  counts as occupying the pool and keeps its route. `wt stop <id>` on it
  fails as before — omlx does not unload a model mid-load — but now says
  that omlx is still loading it and that `wt stop omlx` stops the service,
  instead of `omlx still has <id> loaded`.
- The omlx scan's single-model fallback matches omlx (#263). A model
  directory that is itself a LoRA adapter, or that holds a Hugging Face cache
  entry, is no longer listed as a model omlx does not serve. wt and modelman
  are tested against one shared fixture of directory trees.
- omlx models inside an organization folder are found (#213). wt now scans
  omlx's model directory two levels deep, as omlx does, so a model at
  `<model dir>/mlx-community/<model>` is listed under the name omlx serves
  it by and can be started and stopped. The folder itself is no longer
  listed as a model, and neither is a LoRA adapter or a directory with no
  `config.json`.
- `wt start` can start a model on an omlx that has an API key (#256). The
  warmup request now carries the key the registry's omlx provider names
  (`auth.secret_ref`), as the "what is loaded" probe already did; it used to
  go keyless, be refused, and be retried for the whole ten-minute warmup
  budget. A warmup the server refuses (401/403) now fails at once, for every
  provider, with an error that says whether a key is missing or was refused.

### Added

- `wt warm <provider> <model>` loads a model into a running omlx server with
  one keyed request — the warmup step of `wt start` by itself, with nothing
  started, stopped or routed. `modelman start` asks it when omlx refuses its
  keyless warmup.

### Changed

- wt refuses to write LiteLLM routes when the registry is redirected and
  nothing names `config.yaml`. The registry path follows `MODELMAN_REGISTRY`
  and `XDG_CONFIG_HOME`; `config.yaml` follows neither, so a run that
  redirected only the registry (an ad-hoc `modelman migrate` against a
  throwaway registry) synced it onto the real `config.yaml`: every route that
  registry lacked was removed and the live proxy restarted. `wt litellm sync`
  (and `--dry-run`) and the start, stop and launch route updates now answer
  `LiteLLM routes not touched: the registry is … but config.yaml is the
  default …`. Set `WT_LITELLM_CONFIG` to the `config.yaml` that registry
  belongs to — the default path is a valid answer. **If `XDG_CONFIG_HOME` is
  part of your everyday environment, set `WT_LITELLM_CONFIG` once**, or no
  route is written.
- The post-session survey is switched off (#136). wt no longer asks "did it
  work?", speed, quality and task after a session, and prints no
  after-survey stats block. The code is kept behind `survey.Enabled`
  (`false`) so it can be brought back. `wt stats` and the picker's SURVEY
  column still show the answers already recorded, which drop out as they
  pass the 30-day window. The stop prompt is unaffected.
- The stop prompt (`wt stop` with no argument, and the one after an agent
  exits) is one line, applied on Enter (#139). Type the numbers to stop,
  separated by spaces (`1 3`), or `a` / `all` for every listed model; Enter
  on an empty line stops nothing. It used to be a checkbox list: a number
  ticked a line and a second Enter confirmed. `none` is gone (Enter does
  that), and a line with anything unreadable on it now stops nothing and
  asks again, where it used to act on the numbers it could read.
- `wt litellm sync` and `sync --dry-run` now warn about an ollama row whose
  `api_base` is an `os.environ/VAR` reference to a variable that is not set
  for the proxy (#211). LiteLLM resolves the reference before deciding
  whether to start its own `ollama serve`, and an unset variable counts as no
  `api_base` — the second-server problem of #202, which wt neither repaired
  nor reported for this spelling. "Set for the proxy" is read from the proxy
  LaunchAgent's `EnvironmentVariables`
  (`~/Library/LaunchAgents/local.litellm.proxy.plist`, or `WT_LITELLM_PLIST`),
  not from the shell wt runs in; wt's own environment is used only when
  `WT_LITELLM_RESTART_CMD` is set or the plist cannot be read, and the
  warning names which it consulted. The row is never rewritten. A bare
  `os.environ/` with no variable name is reported in its own words (#218).
- `wt config` now says where to fix a mistyped `location` (#209). The editor
  opens on an invalid config so it can be repaired, but a location error is
  in `registry.toml`, which the editor cannot edit, and its status line
  showed only the raw error. It now ends with the same `(fix the entry in
  <path to registry.toml>)` hint the other commands print. The status line
  also wraps to the terminal's width: it was drawn as one line and cut off at
  the right edge, so on an 80-column terminal the end of a long error —
  where the hint is — was never visible.
- An omlx model is "running" only when omlx has it loaded (#201). omlx's
  `/v1/models` lists every model in its model directory, loaded or not, and
  wt read that list as running: with the omlx service up, every omlx model on
  disk showed as running in the picker, launched without being started, was
  routed by `wt litellm sync` and offered by the stop picker; with two models
  on disk, sync and `wt start` kept undoing each other's routes, restarting
  the LiteLLM proxy each time; and a start into an idle server asked to
  replace a model that was not loaded. wt now asks omlx what is loaded. A
  model that is on disk but not loaded is a start row, and Enter or `-M`
  loads it as before.
  - No configuration is needed when omlx has nothing loaded or everything
    loaded: its `/health` answers that without a key. Its counts answer
    "everything" only when the list carries the whole pool — a hidden model
    is counted without being listed — so that case asks the status endpoint,
    which wants the key when one is set.
  - A 503 `/health` answer is not an outage: omlx 503s while its pinned
    models preload, and the counts ride the 503 body. With zero built there,
    wt still asks the status endpoint, because loaded_count cannot see a
    model that is mid-load — so a sync inside the preload window routes the
    models omlx is loading, instead of unrouting everything.
  - When only some models are loaded, wt needs omlx's `/v1/models/status`,
    which wants the server's API key if one is set. Give it through
    `auth.secret_ref` on the registry's omlx provider. Without it wt cannot
    tell which model is loaded: the family's running state is untrusted, so
    `wt litellm sync` leaves omlx routes as they are, with a warning that
    carries the reason and the repair, and a start asks before replacing.
  - wt still treats omlx as holding one model: a start replaces what is
    loaded and a stop stops the service.
- Follow-ups to the ollama `api_base` repair (#206):
  - `wt litellm sync` and `sync --dry-run` now warn about a row LiteLLM starts
    its own `ollama serve` for that wt does not repair. LiteLLM's test is the
    word `ollama` anywhere in the model with no `api_base` (`openai/ollama-proxy`),
    wider than the `ollama/` and `ollama_chat/` rows wt fills in; wt leaves
    such a row alone, since the ollama address would be wrong for it, and
    prints `warning: row "<name>" (model <model>) has no api_base: …` (JSON:
    in `warnings`) until the row is given one.
  - When the registry's ollama provider has no `base_url`, or there is no
    ollama provider, wt now uses `http://localhost:11434` — the address it
    already dials in that case — both for the repair and for the ollama rows
    it writes itself. It used to repair nothing and write its own ollama rows
    with no `api_base`, each of which made LiteLLM start a second server.
  - A repaired row that has no `model_name` is reported as
    `(no model_name: <model>)` instead of with an empty id (`: api_base set`,
    `{"id": ""}`).
  - A launch prints `wt: LiteLLM route for <id> updated` only when that
    model's own route was added or rewritten. It used to print it whenever
    the write changed `config.yaml`, so a launch that only filled another
    row's `api_base` or restored a `litellm_settings` key claimed a route
    change, and a model whose row could not be built got `updated` beside
    its own `not updated`. Such a launch now prints the line for what it did
    (`… <other id>: api_base set`), or `wt: LiteLLM config.yaml updated` when
    there is nothing more specific; the proxy restart is unchanged.
- A `location` that is neither `local` nor `cloud` is now rejected (#200).
  `"Local"` or `"Cloud"` used to pass as a third kind of location: the model
  was offered, `wt litellm sync` put it in neither its local nor its cloud set
  and never routed it, and the launch failed at the proxy with `Invalid model
  name`. A mistyped value is now a validation error that fails the launch:
  `wt`, `wt start`, `wt stop` and `wt smoke` stop with `config error: provider
  "<id>" has location "<value>"; expected "local" or "cloud"` (or the same
  with `model "<id>"` when a model's own value is mistyped) until
  `registry.toml` is fixed. The model is not offered, and `wt litellm sync`
  keeps its existing row until the registry is repaired; its family is frozen,
  and the run warns naming the gap, only when the family could not be probed —
  always the case for a mistyped provider entry, since the inventory probes
  only an exact `local`. Unlike a location left out, which a model may
  inherit from its provider, a value that is set but wrong never falls back.
  **One mistyped entry stops every launch**, as a missing location already
  did. The message names the registry file to edit; `wt config` cannot repair
  it.
- wt no longer resumes an agent session by itself, on any launch path
  (#198, #204). Every launch starts the agent fresh: no session lookup, no
  resume flag, and the picker's "Resume previous session?" prompt is gone.
  To continue a conversation, pass the agent's own flags after `--`, which
  wt hands over unchanged, in the picker too: `claude-wt -- --continue`
  (or `-- --resume <id>`, or `-- --continue --fork-session` to fork) and
  `opencode-wt -- --continue` (or `-- --session <id>`, or
  `-- --continue --fork`). The automatic resume appended a one-shot run
  (`-- -p "..."`) to whichever conversation was newest, including one another
  process was using (#204), and made an opencode launch fail when the resumed
  session had stored a different model (#198). The `--debug-session <agent>`
  flag is removed. Native models are no longer special-cased for resume:
  whether a resumed session's stored model overrides the chosen one is the
  agent's behaviour, and wt does not guard against it.
- `--cwd` launches the agent in the directory you typed the command in, not
  at the root of the current checkout. A relative path after `--` then means
  what you meant. `-W` and the worktree picker still start at a worktree's
  root. The agent looks for its sessions in the directory it starts in, so
  `-- --continue` finds a session started at the repo root only when you
  launch from the root.
- `wt smoke` runs each agent in its own fresh temporary git repository, removed
  when the row ends, instead of the current directory (#193). Smoke runs
  agents with permission checks off, and a model's stray tool call had written
  a junk file into a real repo. Pass `--cwd` to run in the current directory
  as before.
- `wt smoke`'s default prompt no longer contains the text the row looks for
  (#193). It gives the text in lower case and asks for it in upper case. An
  agent that prints the prompt back, as codex does, used to pass the row on
  that echo whatever the model replied; a model that cannot answer now FAILs.
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
- On a terminal too narrow for the whole model table, the model picker (and
  the picker `wt start` and `wt smoke` use) now drops whole columns —
  survey first, then usage (30D, 7D, 1D), cost, location and family — and
  always keeps the model id, status and running columns. The list used to cut
  a row wider than itself wherever its own width fell, ending it in `…`,
  which on a long model id hid whether the model was running. The filter still matches the dropped columns' text, and
  widening the terminal brings the columns back. Only when the three kept
  columns alone do not fit is a row cut at the edge.
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

- `wt served <provider> [--json]` prints the model ids an `omlx`, `mtplx` or
  `mlx_lm_server` server is serving now — the probe `wt start`, `wt stop` and
  the pickers already act on. For omlx that is the models loaded or loading,
  asked with the key the registry's omlx provider names (`auth.secret_ref`)
  when the server has one. It exits 1 rather than print nothing when the
  server gives no usable answer. modelman asks it for a keyed omlx with a
  partly loaded pool, where it used to leave a stale `running` flag in place.
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
  skipped and the agent launches. Reuses the picker's own launch and
  rotation-recording flow.

### Fixed

- Launching a registry cloud model through LiteLLM (or smoke-testing one)
  writes its route when `config.yaml` has no row for it. Only local models
  were repaired at launch, so a `config.yaml` that had lost its cloud rows
  answered every cloud launch with `Invalid model name` until someone ran
  `wt litellm sync`. A cloud row that is there is left as it is: keeping it
  current stays with `wt litellm sync`.
- A local ollama model no longer stays loaded after wt stops it (#202). The
  cause was a second Ollama server: LiteLLM runs `ollama serve` itself at
  proxy startup for every ollama row in `config.yaml` that has no `api_base`,
  and with Ollama already running that server shares port 11434, bound to
  `127.0.0.1` beside the first one's wildcard socket. Clients then reached
  one or the other depending on how `localhost` resolved, a model was loaded
  in both, and wt unloaded it in the one it could see and reported `done`.
  wt now gives an ollama row with no `api_base` the registry's ollama address
  whenever it writes `config.yaml` — **on hand-written rows too**, that one
  field, and only when it is empty — and reports it (`<id>: api_base set`;
  `would set api_base` in a dry run; `wt: LiteLLM route for <id>: api_base
  set` when a start, stop or launch makes the write). A row that names its
  own address, or inherits one through a YAML merge key, is left alone.
- wt prints a note when an argument after `--` is a relative path that
  resolves differently for the agent than for you. An agent starts in the
  worktree wt launches it in, so `opencode-wt -- ../../other`, typed in a
  subdirectory, reached opencode where that path named nothing and failed
  with `Failed to change directory`. The note names the directory the agent
  starts in and the absolute path to pass. The argument is not rewritten.
  There is no note for the same path in the worktree you launched
  (`shell-wt -W feat -- cat README.md` from the repo root), nor for the
  program name `shell-wt` runs.
- A provider entry whose `location` is mistyped (`"Local"`, say) no longer
  costs its models their LiteLLM routes (#195). The inventory probes only an
  exact `local`, so such a family went unprobed, and `wt litellm sync` then
  removed its marked routes with no warning. Any location that is neither
  `local` nor `cloud` is now a registry gap like a missing one: the routes are
  kept, and the warning names the value. That warning no longer needs a
  discovered model's route to be present: a family whose only routes are its
  registry models' own was kept with no warning at all.
- `wt litellm sync`'s "could not be probed" warning says which registry gap
  it found — no location, an invalid location, or models with no provider
  entry — instead of "no resolvable location" for all of them (#195).
- A stopped provider whose only routes belong to discovered models is warned
  about like any other when `wt litellm sync` removes those routes as stale;
  it used to remove them silently (#195).
- A route removal that removed nothing is no longer recorded as an outcome.
  Stopping a model on a single-model provider recorded every registry model
  of that provider as `unrouted` in the route writer's result, whether or not
  it had a route (#195). Nothing printed those entries, so no command's output
  changes.
- Launching a running local model that wt did not start no longer fails with
  `Invalid model name` (#192). wt writes the model's LiteLLM route, if it is
  missing, before handing the model to an agent — on a `-M` pin, on rotation,
  in the picker and in `wt smoke` — and `wt start <id>` on a running model now
  repairs the route instead of only reporting `already running`. It prints
  `wt: LiteLLM route for <id> updated` when it wrote one. In the picker, a
  launch that had to write the route shows an "Updating the LiteLLM route"
  progress screen while the proxy restarts instead of freezing, and that line
  and any route warning are printed on the terminal once the picker releases
  it (when the agent starts, or after wt exits). A hand-written row
  whose name is not a registry model id is never replaced (one named like a
  registry id is adopted, as `wt litellm sync` does — guide 04,
  `docs/guides/04-litellm-config.md`, Gotchas), and nothing is written when
  the launch dials the provider directly.
- The model picker's agent/tag header and its status line are on screen
  again. The picker's view was taller than the terminal — the list took the
  window height minus two under six to eight lines of the picker's own — and
  the terminal UI drops a too-tall view's top lines, so the header and every
  status the picker set (a failed start, `cancelled`, a resume warning) were
  pushed off the top. The list is now sized to leave room for them, and
  re-sized when a status appears or clears, the list's help is expanded with
  `?`, or the terminal is resized. The agent picker's `directory:` line was
  lost the same way and is fixed the same way; the ollama availability prompt
  now follows a window resize. A terminal too short for the whole model picker
  gives up its blank margin, then the header, then the mode line and key
  hints; the agent picker gives up its `directory:` line (below 14 lines when
  a status is showing).
- `loading worktrees...` no longer stays on the picker after the worktrees
  have loaded. It was the picker's initial status and nothing cleared it; it
  went unnoticed on the agent and model pickers only because their status
  line was off the top of the screen.
- No picker screen is wider than the terminal. The model picker's table was
  sized two columns past the right edge, and a long status line widened the
  whole screen; lists are now sized to the columns their screen leaves, and
  status lines, paths and key hints end at the edge.
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
