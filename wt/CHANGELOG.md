# Changelog

## Unreleased

### Added

- `wt cloud-sync` gains its second flow, `ollama`, replacing
  `modelman ollama-catalog sync` (which still works): it mirrors
  <https://ollama.com/pricing> into the registry's ollama cloud entries
  (prices, off-peak included), into ollama (`ollama pull` for new cloud
  models, `ollama rm` for retired ones, both pinned to the registry's ollama
  address) and, through one route sync, into LiteLLM. Both flows run unless
  `--only openrouter` or `--only ollama` picks one, and neither stops the
  other. `--yes` never deletes on its own: a plan that removes anything
  needs `--approve-removals` with the digest a dry run printed (the same
  digest modelman prints), and a plan that removes more than half the cloud
  entries needs `--force` as well. `--html FILE` reads a saved pricing page
  instead of fetching it (cloud tags are still looked up on
  ollama.com/library); an ollama-flow flag together with `--only openrouter` is a
  usage error. Exit codes 2 to 5 say why the ollama flow changed nothing
  (inputs unreadable, page shape changed with its HTML saved, mass removal
  refused, removals not approved or the registry changed after the plan was
  printed); every other wt command still exits 1 on an error. A failed
  `ollama pull` or `ollama rm` is one error line (ollama's last line, without
  its progress output) followed by what is left and what to run next, and a
  command wt stopped at its own time limit says so. An ollama provider row
  whose `base_url` names no daemon (`/v1`, a blank, no `http://`) stops the
  flow before anything is fetched (exit 2), since ollama would otherwise
  fall back to its default daemon. A registry with no `ollama` provider row has no catalog to
  mirror: the flow prints one line and is skipped with exit 0, also when it
  was asked for by name (`--only ollama` or one of its flags), so with
  neither an ollama row nor an OpenRouter-priced model the command fetches,
  asks and writes nothing. An ollama cloud entry the plan would change or
  remove whose id is in the registry twice is refused before the plan, in a
  dry run too (exit 1). Reference: `docs/wt-cloud-sync.md`.
- `docs/wt-cloud-sync.md`, the reference for `wt cloud-sync`: the two flows,
  what a run does in order, every flag and exit code with each of its
  causes, what is skipped and when, what it reads and writes, and how to
  recover from a failed pull, a failed removal or an interrupted run. The
  `cloud-sync` skill (`.claude/skills/cloud-sync/`) is the same command as a
  procedure for an agent, including the parser repair after an exit 3; it
  replaces modelman's `ollama-catalog` skill, which moved here.
- `wt cloud-sync [--only openrouter] [--dry-run] [--yes]` refreshes the per-token
  prices of the registry's OpenRouter-priced models from OpenRouter's public
  model list, replacing `modelman refresh-prices` (which still works). It
  prints its plan as `id: old -> new` first; `--dry-run` stops there, and
  otherwise it asks once on the terminal unless `--yes` is given. Input and
  output prices take OpenRouter's value; a cache price is replaced only when
  OpenRouter reports one, and a subscription or time-windowed price is never
  touched (but for the flow's own `openrouter` rows on a model priced by
  time of day: the entry under Fixed). Every matched model is stamped, changed or not, which is what the
  stale-pricing notice reads. The LiteLLM routes are synced once when a
  price changed, and not at all when none did. With no OpenRouter-priced
  model in the registry it says so and exits 0 without fetching, asking or
  writing anything. `--only` given with an empty value is a usage error, not
  "every flow", and a model id that is in the registry twice is refused
  before the plan, in a dry run too. Nothing refreshes prices automatically.
- `wt model add mlx_lm_server <target> --draft <draft> --family F` registers a
  target+draft pairing, each side a Hugging Face repo or a local path, and
  prints the command that starts it. A side is a local path when it starts
  with `/`, `~`, `./` or `../`; a `./` or `../` path is stored as the
  absolute path it names, and a side registered as a repo that is also a
  directory in the working directory gets a `note:` saying so. Two pairings
  are the same when their sides are, not their names — `~/x` and the
  absolute path of the same directory are one side. wt cannot start a pairing: where it
  used to say `modelman start <id>`, it now names `llmbench provider isolate
  --solo mlx_lm_server <target> --draft <draft>` with the pairing's own
  target and draft. In the launcher that message is the agent picker's
  status, which is now wrapped to the terminal's width instead of cut at its
  edge, so the command is on screen. The Models tab lists a pairing
  (`STATUS -`), shows its target and draft under the table and edits its
  family, tags and prices; it does not create one, and its add form names
  the command that does while the cursor is on Provider. Whether a pairing row reads as running is still
  decided as before, and can name the wrong pairing
  ([#299](https://github.com/ohanaverse/local-ai-setup/issues/299)).
- `wt config` has a second tab, Models (`Tab` switches; `wt model` opens the
  editor on it): every registry model and every local model found, with
  status and running state. `d` removes the selected model from the registry
  after a prompt that shows where its weights are; `r` probes again; `/`
  filters. A change is written at once, and the LiteLLM routes are synced
  once when the editor closes. The selected row's detail names a malformed
  `fetch` or `draft` (`fetch is not a table; read as absent`). Removing an id
  that more than one registry row carries is refused with `wt model rm`'s
  message, and nothing is written.
- The Models tab adds and edits models: `n` opens a form for a new model,
  `Enter` edits the selected one or registers a `new` row under the id it
  already has. The form has the fields `wt model add` takes (the provider,
  location and subscription period are choices changed with `←`/`→`), offers
  the registry's families as suggestions (`→` at the end of the text takes
  one), and saves with `Ctrl+S` — straight to `registry.toml`, with the
  routes synced when the editor closes. An edit writes only the fields that
  changed. A refused save keeps the form open with the cursor on the field
  at fault; an edit of an id the registry holds twice, or of a row whose
  `fetch` is malformed, is refused with nothing written. On a short terminal
  the fields scroll (`↑ N more` / `↓ N more`).
- `wt model add <provider> <name> --family F`, `wt model edit <id>` and
  `wt model rm <id>...` register, change and remove models in the registry.
  Each makes one locked write and then syncs the LiteLLM routes once. `add`
  derives the id (the discovered id for a local model, `/` as `--` for a
  cloud one; `--id` overrides), seeds a missing default provider row —
  openrouter's too, which `wt model init` now also adds for a model that
  references it — and for an ollama model records what `ollama show` says it
  supports. `edit` changes only the named fields; an edit of a price also
  moves a row out of modelman's old cost layout. `rm` removes registry rows
  only and prints where the weights are. An id that more than one registry
  row carries is refused by `edit` and `rm`, with nothing written: `model
  "<id>" is in the registry twice (providers A, B); wt cannot tell which one
  you mean — fix the entry in <registry path>`. Reference: `docs/wt-model.md`.
- `wt litellm sync` warns when a registry model names a provider that has no
  `[[providers]]` row; it used to leave such a model unrouted without a word.
- `wt model list [--json]` lists every model in the registry and every local
  model the providers have that the registry does not, with live status
  (`ok`, `missing`, `unknown`, `new`, `-`) and running state (`run`, `load`,
  blank, `?`). The text table has no borders and fits the terminal; `--json`
  adds each model's size and path. A model whose `fetch` or `draft` is
  malformed in the registry is still listed, with that value read as absent,
  and is named on stderr (`<id>: fetch is not a table; wt reads it as absent
  (fix the entry in <registry path>)`) and in the model's `malformed` array in
  `--json`. Reference: `docs/wt-model.md`.
- `wt stop --all [--yes]` stops every running local model and then halts the
  omlx service. It asks once when a live wt session is using one of them,
  keeps going when one stop fails (and then exits 1), and takes no argument.
  Ctrl+C ends it where it is: a run interrupted while it stops the models does
  not go on to halt omlx.
- `WT_REGISTRY` names the model registry file. It outranks `MODELMAN_REGISTRY`,
  which keeps working as an alias; modelman and llmbench read the same name.
- `wt model init [--json]` creates the model registry when it is missing and
  adds the provider rows it lacks: ollama, omlx and mtplx when installed, used
  by a model or listed by a configured agent; mlx_lm_server when used by a
  model or listed by an agent; openrouter when used by a model or listed by an agent, with
  `auth.secret_ref = "OPENROUTER_API_KEY"` (the variable's name, never a key,
  so a missing key is an error instead of an empty `api_key` in the route);
  and a native row for each configured agent. It never changes a row that
  exists, and it names any provider an agent lists that it has no default row
  for. A `config.toml` that cannot be read is a warning, since no agent gets a
  row until it is fixed. The "model registry not found" error now names this
  command, once, where it named `modelman migrate` twice.
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
  stays 0. The connection string reaches `psql` in its environment, never
  on its command line where `ps` would show the password to other users
  (#282). Requests the proxy logged under the other spelling of a wt id —
  `mtplx/Org/Name` for `mtplx/Org--Name` — are added to that id's row, when
  the logged id is not itself a registry or launched id. `--family` narrows
  the new table; `--agent` narrows its launch counts and leaves spend out;
  launches and spend are counted over one window ending at one instant, so a
  launch dated after it is not counted. The window starts just after
  `as_of - window` and includes `as_of`, for launches, requests and survey
  answers alike (#298): a request logged exactly at the start is left out,
  as a launch at that instant is, where it used to be counted while the
  launch was not.
  `--family` is `stats`' own flag and takes one exact family: the root
  command's `-F` shorthand, which `wt stats` used to accept and ignore, is
  now an error there. This replaces `modelman usage report`, which still works until modelman
  is removed. Not carried over: `--days N`, the Markdown output, the
  Reconciliation sections and the "Last wt launch" line.
- `wt stats --json` prints both tables as one JSON document (`window`,
  `as_of`, `survey`, `usage`) on stdout; notes stay on stderr. A usage row
  lists in `also_logged_as` the other spellings whose requests it includes.

### Changed

- What follows a start that succeeded is one call into the engine,
  `lifecycle.SettleStart` (#349): the wait for the LiteLLM proxy restart, the
  check that the model's server is still there (#343), and, when it is not,
  the wait for the restart that removing its route starts. The sequence was
  written out twice, in the non-TUI start driver and in the model picker's
  start, and the two had drifted. What wt prints, and in what order, is
  unchanged. One thing moves: after a `wt start`, `wt smoke` or `-M` launch
  whose model was stopped during the proxy wait, the wait for the proxy to
  restart without the removed route is now part of the start, as it already
  was in the picker, instead of the wait wt makes as it exits. A first
  Ctrl+C during that wait is therefore taken by the start, as during the
  rest of it, and no longer ends wt mid-restart; a second one still does.
  The check drops the caller's
  cancellation inside the engine, so no caller has to, and the check without
  its waits (`ConfirmStarted`, `ConfirmStartedTo`) is no longer exported.
- The model picker shows the price in force now. For a model with
  `cost.time_prices` rows (ollama's off-peak row, the rows `wt cloud-sync`
  stores for a model OpenRouter prices by time of day, or rows written by
  hand), the COST column shows, and the cost sort uses, the prices of the
  row whose timezone and windows hold the current instant, and the model's
  own price when none does. It used to show the model's own price at every
  hour, which for an ollama cloud model is the peak price (and for a model
  OpenRouter prices by time of day would be its dearest level). Such a
  price is marked `~`, and the column heading then reads `COST (~ varies
  by time)`.
  The mark and the heading take room: at a few widths a table with a
  time-priced model in it shows one usage column fewer (1D, 7D, 30D or
  SURVEY) than it would without one, and at three widths, where COST is
  the last column there is room for, it shows no COST column where a table
  of single prices has one.
  The line under the launcher's table now names the highlighted model's
  price beside the LiteLLM mode, as `cost~ 0.66/0.022/1.98`, whole or not
  at all: on a terminal too narrow for both (at 40 columns, a price with
  many digits, and nearly every price when LiteLLM is off) the price takes
  the line and the mode gives way. The COST column itself is dropped on a
  narrow terminal, as before. Because the sort uses the
  current price, a time-priced model's place in the list, and so the first
  row, can differ from one hour to the next. The price is read when the
  picker opens, not while it is open. A row that breaks the registry's
  rules (an unknown timezone; a time that is not `HH:MM`; a negative
  price) is not applied, and `wt model list` now names it on stderr and in
  `--json`'s `malformed` array, as it does a malformed `fetch`. Not
  changed: LiteLLM's route, and any spend computed from it, uses the
  model's own price at every hour; `wt model list` and `wt config`'s
  Models tab show no price, and the model form edits the model's own
  price. Reference: `docs/wt-cloud-sync.md` ("The price the picker
  shows").
- A window of a `cost.time_prices` row may run past midnight. With its
  `end` before its `start` (`start = "22:00"`, `end = "06:00"`) it is one
  window, from `start` on each listed day to `end` on the next day, on the
  clock of the row's timezone; the picker applies it. The registry used to
  refuse such a row (`start must be before end`), so a model that carried
  one could not be edited or re-priced until the window was split in two.
  A window whose `start` equals its `end` is still refused (`start and end
  must differ`; a whole day is `00:00` to `24:00`). A wt built before this
  change still loads a row with a window past midnight but refuses to
  write its model (`start must be before end`), so write one only once the
  installed wt has this change. Reference: `docs/wt-cloud-sync.md` ("A row
  you write by hand").
- **Breaking:** the provider key `openrouter_priced` is no longer read, and
  the rule it overrode is simpler: a model takes its price from OpenRouter
  when its `provider_id` is `openrouter`, and never otherwise. That one rule
  decides what `wt cloud-sync`'s openrouter flow refreshes and what the
  stale-pricing notice watches. Until now a model of any cloud provider that
  is not an agent's native one counted too, and the key could put a
  provider's models in (`true`) or take them out (`false`); it dates from
  when ollama's cloud models had no published prices. A registry that still
  has the key loads as before, nothing warns about it, and wt keeps the key
  when it writes the file; it just decides nothing. So a model under a cloud
  provider other than `openrouter` (a gateway that serves OpenRouter ids,
  say) is no longer refreshed, and no longer watched by the notice, whether
  the key was `true` or was never set. It keeps the price it has. To have
  it refreshed, register the model under the `openrouter` provider; to keep
  it where it is, set its price yourself with `wt model edit <id>
  --input-price … --output-price …`. The line a run prints when no model
  could be refreshed no longer names the key: it ends `the warnings above
  say why for each model`. Reference: `docs/wt-cloud-sync.md`.
- **Breaking:** `wt cloud-sync`'s two flows are named for the provider each
  one syncs: `prices` is now `openrouter` and `catalog` is now `ollama`.
  The names changed everywhere they appear: `--only openrouter`, `--only
  ollama`, each flow's output lines (`openrouter:`, `ollama:`; the route
  sync's `routes:` lines keep their prefix), the last error line (`wt:
  cloud-sync: the ollama flow changed nothing: …`) and `--help`. There are no aliases: `--only prices` and `--only catalog` are
  unknown flows (exit 1, `--only: unknown flow "prices" (valid: openrouter,
  ollama)`), so a script that names one stops instead of running something
  else. Two lines were reworded with the rename: `ollama: ollama at
  <address>` read badly and is now `ollama: daemon at <address>`, and the
  refusal both gates print ends `Nothing was changed by the ollama flow.`
  where it said `Nothing was changed for the catalog.` What each flow does
  has not changed. Reference: `docs/wt-cloud-sync.md`.
- **Breaking:** wt no longer reads `~/.config/local-ai/modelman.toml`. Its
  `[litellm]` table was a fallback for routing state (on/off, proxy URL, API
  key); wt has kept that state in its own `~/.config/agent-wt/config.toml`
  since 2026-09-21, and copied the old table there the first time it ran
  with a `config.toml` that had none. A machine where that copy was made is
  unaffected. On a machine that has no `config.toml`, or where wt has not
  run since that date, routing now reads as off and unset
  (`wt litellm status`), agents dial providers directly, and a launch that
  needs the proxy (codex, always) stops with `litellm routing is required
  for this model but no URL is configured`. Nothing warns about it. To
  restore it, take the URL and key from the `[litellm]` table of the old
  file and run `wt litellm set --url <url> --api-key <key>`, then
  `wt litellm on`; both write `config.toml` (0600), creating it if needed.
  If wt then stops with `config error: agent "agy": unknown provider "agy"`,
  run `wt model init`: the restore created `config.toml`, and that command
  adds the provider row it needs to `registry.toml`.
  A malformed or unreadable `modelman.toml` no longer stops every wt command
  with `parse modelman.toml: …`.
- The stale-pricing notice wt prints after a launch takes its date from
  `registry.toml`: the newest `pricing_updated_at` among the models priced by
  OpenRouter. It now speaks when that is more than 7 days old, or when no
  such model was ever refreshed, and it names `wt cloud-sync`; it used to
  speak whenever `modelman.toml`'s `price_refresh_last_run` was not today,
  and name `modelman refresh-prices`. wt no longer reads that key. Either
  tool's refresh still clears the notice for a registry in which it matches
  at least one model, since both stamp the models they match; when a refresh
  matches none, `wt cloud-sync` says that the notice stays.
- omlx is handled as the multi-model pool it is (#213). `wt start` loads an
  omlx model beside the ones already loaded instead of stopping the service
  first, and asks only when the model does not fit, naming what omlx is
  expected to unload. `wt stop <model>` unloads that model and leaves the
  others up; `wt stop omlx` stops the service. Routes follow each model's
  loaded state, so two loaded omlx models are both routed. On an omlx that
  wants its API key for load and unload but not for inference, and with
  no `auth.secret_ref` in the registry, a start still loads the model through
  a keyless chat request, and a model stop fails with the two ways to proceed.
- **Removed:** `wt start --json` and `wt start --plan`. modelman, their only
  caller, is deleted. Either flag is now `unknown flag`. `wt model list --json`
  shows what is on disk and running before a `wt start <id> --replace`.

### Fixed

- `wt start` of an mtplx model no longer fails when the registry's mtplx
  `base_url` names no port (#348). With `base_url = "http://127.0.0.1/v1"`
  wt started mtplx on its default port, 8003, waited for it there, and then
  looked for it on port 80, the url as written: every start ended in
  `<id> is not running: it started, and was stopped while wt updated the
  LiteLLM routes (mtplx no longer answers at http://127.0.0.1)`, exit 1,
  with the model serving and its route removed. The same reading was behind
  every other use of that address: `wt model list` and the pickers showed a
  serving model as not running, `wt stop` and `wt served mtplx` found
  nothing, a start did not see the model it was about to replace, the
  LiteLLM route's `api_base` and a direct launch pointed at port 80, and
  `wt litellm sync` removed the family's routes as if the server were down.
  An `http` url for the mtplx provider on this machine (`localhost`,
  `127.0.0.1`, `[::1]`, or `0.0.0.0`) with no port now means 8003 everywhere,
  the port `wt start` serves mtplx on. **If such a url really meant port 80**
  (an mtplx you run yourself on 80, or something on 80 in front of it),
  write the port: `base_url = "http://127.0.0.1:80/v1"`. Until you do, wt
  looks on 8003, shows the model as not running and drops its route on the
  next sync. A host name that only resolves to this machine (an `/etc/hosts`
  alias) is not recognised, so that url has to name its port too.
  Nothing else changes: a `base_url` that names
  a port is that port; an mtplx url that is `https` or names another host,
  and every omlx, ollama and mlx_lm_server url, is read as written (no port
  meaning the scheme's own), because wt passes those servers no port. A
  registry whose mtplx `base_url` names its port, as the row `wt model init`
  writes does, is unaffected.
- A failed start is printed once (#344). `wt start`, `wt smoke` and a launch
  whose `-M` pin had to be started printed the whole message twice, as
  `Error: failed to start ...` and again as `wt: failed to start ...`, each
  with the server's multi-line log tail. The `wt:` line is the one that
  remains; exit codes are unchanged. Every other error of `wt start` and
  `wt smoke` is printed once as well, and so is a start the launch refused
  or that was cancelled.
- The log tail a failed mtplx start shows begins at the start of a line. It
  was cut at 512 bytes wherever that fell, so it could begin mid-word.
- `wt start` no longer prints `<id> is running` and exits 0 for a model that
  was stopped while it waited for the LiteLLM proxy (#343). A start reports
  success as soon as the route is written; the wait for the proxy that
  follows takes about ten seconds, and a `wt stop` from another terminal in
  that time left the first one announcing a server that was gone. After the
  wait wt now asks the provider's server once whether the model is still
  there, and a model that is gone is one error line and exit 1:
  `<id> is not running: it started, and was stopped while wt updated the
  LiteLLM routes (...)`. The route the start wrote is removed, so a stop
  that ran just before the start's route write does not leave a route to a
  dead port. Gone means: an mtplx whose port refuses the connection or that
  no longer serves the model, an omlx that refuses or no longer has it
  loaded, an ollama daemon that refuses. An ollama model that was only
  unloaded still counts as running, because ollama loads it on the next
  request. `wt smoke`, a `-M` launch that starts its model and the model
  picker's start make the same check before they use the model. In the
  picker a model that is gone is a failed start: the picker comes back with
  that line on its status, under whatever the start printed, the agent is
  not launched, and when the start printed anything those lines are printed
  again, with that line under them, when wt exits. The check is
  made behind the start screen's `updating LiteLLM routes`, which keeps
  answering keys meanwhile, and it is made for a start whose cancel came too
  late as well, which then reads `is not running` instead of `started <id>;
  launch cancelled`. Quitting wt with ctrl+c at `updating LiteLLM routes`
  no longer leaves before that check is done: wt waits for the proxy and
  the check (`wt: waiting for the LiteLLM proxy restart…`), so a model that
  was stopped meanwhile does not keep its route, and the proxy is restarted
  after the route is removed.
- `wt stop` of a serving mtplx removes the pidfile and wt's start record
  (#343). Only the stop of a server that was still loading did; the ordinary
  stop left both in `/tmp` naming a dead pid. They are removed only when the
  pidfile named the server that was stopped, verified before the stop as a
  loading server is (the user's own regular file, no symlink, the user's own
  mtplx server on that port, the recorded start time), and only once that
  process is gone. A pidfile naming any other process is left alone.
- The model picker shows what a start printed. A start from the picker ran
  with nowhere to print but stderr, which the full-screen picker hides: a
  model started while `config.yaml` could not be written launched its agent
  into `Invalid model name` with the line that explains it,
  `LiteLLM route not updated: …`, lost, as were the proxy restart's
  warnings and the `(not predicted)` on a model omlx unloaded. The picker
  now takes every line the start prints: above the agent's output after a
  start that succeeds, and on the status above the failure, and again on
  the terminal when wt exits, after one that fails or is cancelled (a start
  that printed nothing leaves its failure on the status only). The
  picker's status is wrapped to the terminal's width instead of cut at its
  edge, and one too tall for the terminal has the screen to itself until the
  next key. A row the picker just started now gets the launch-time route
  check like any other, so a route the start could not write is tried once
  more before the agent launches; when it still cannot be written the agent
  is launched as before, with the lines that say why above its output.
  After a failed or cancelled start that had already unloaded a model, the
  start screen now stays up until the proxy has restarted. Once a start
  shows `updating LiteLLM routes` it can no longer be cancelled (the model
  is loaded): esc does nothing there and ctrl+c quits wt, where an esc used
  to end in `cancelled` with the model running. Quitting during a start
  prints what it had printed. On a terminal too narrow for the start
  screen's line the stage is on a line of its own instead of cut off (#275).
- `wt cloud-sync` no longer stores, for a model OpenRouter prices by time of
  day, whichever price was in force when the sync ran (#322). Such a model
  used to flip between its rates from one sync to the next, each flip
  reported as a price update, written to LiteLLM's routes and able to
  restart the proxy. The openrouter flow now reads the model's whole
  schedule from OpenRouter's list (the entries of `pricing.overrides` that
  have `utc_start`, `utc_end` or `utc_days`) and never the price at the top
  of the entry, and it reads no clock. It stores the dearest level (the one
  with the highest output price) as the model's price, and each other
  level as a `[[models.cost.time_prices]]` row labelled `openrouter`, in
  UTC, each day's hours under that day (a level that runs past midnight is
  two windows). The plan line shows them: `<id>: <old> -> <price>
  (openrouter <in>/<cached>/<out> <windows>)`. The model picker, which
  already applied `cost.time_prices` rows, now shows and sorts by the level
  OpenRouter charges at the moment it opens, marked `~`; LiteLLM's route
  carries the model's price, the dearest level, at every hour.
  What to expect: the first sync after this change lists one update for
  each such model, and a sync run later in another window lists none. When
  only the rows are new (the stored price already was the dearest level,
  `<price> -> <price> (openrouter …)`), the update is still listed and
  counted and the routes are synced, but `config.yaml` is left byte for
  byte and the proxy is not restarted.
  The label is the sync's: on a model of the `openrouter` provider a row
  labelled `openrouter` is replaced or removed, one you wrote yourself
  included (the plan line shows it as found, with `timezone="…"` or `+keys`
  where it differs from the sync's own), and the rows are left exactly
  where they stand when they already hold the schedule. Rows under any
  other label are kept as they are. A price that depends on prompt size
  (`min_prompt_tokens`) is not a schedule: it is ignored and the model's
  listed price is stored, as before. A schedule wt cannot read with
  certainty is a `warning: Could not use OpenRouter's time-of-day pricing
  for <id>: <reason>`; the model is left as it is and not stamped, and wt
  does not fall back to the listed price. A model whose `pricing.overrides`
  is not a list, or holds an entry that is not an object, is refused the
  same way (`its overrides are not a list`, `an overrides entry is not an
  object`), where it used to be priced from the top of its entry.
  Not fixed here: only a few of OpenRouter's models are priced by time of
  day, and a model that several providers serve can still show a different
  price from one sync to the next, because OpenRouter lists the price of
  one of them and that price moves
  ([#337](https://github.com/ohanaverse/local-ai-setup/issues/337)).
  That is left as it is, since wt stores what is listed, and is described
  in `docs/wt-cloud-sync.md` ("A listed price that moves between syncs").
  Reference: `docs/wt-cloud-sync.md` ("A model OpenRouter prices by time of
  day").
- `wt cloud-sync`'s mass-removal refusal (exit 4) prints the count its gate
  uses. `<n> of <m> ollama cloud entries would be removed` printed the
  number of removal lines, a re-tagged entry included, while the gate that
  decides the refusal leaves re-tagged entries out, since they come straight
  back under another tag: a plan with two entries gone and one re-tagged
  read `3 of 3`. It now reads `2 of 3 ollama cloud entries would be removed
  (re-tagged entries are not counted)`; the clause is printed only when the
  plan re-tags something, where `<n>` is smaller than the count heading the
  plan's removal list. Which plans are refused, the printed plan and the
  removal digest are unchanged (#320).
- `wt stop --all` and `wt stop mtplx` stop mtplx unless its port refuses the
  connection, also when wt cannot tell what it has loaded — a server that
  answered with an error, with nothing loaded, or not at all within the
  probe's 2 seconds. They used to print `wt: no running local models` /
  `wt: nothing running on mtplx` and exit 0 with the server still up (#308).
  A stop that leaves the port answering prints `failed` and exits 1.
- `wt stop --all` and `wt stop mtplx` stop an mtplx that is still loading
  (#308). mtplx reads its weights before it opens its port, so a load in
  progress refuses the connection and both commands reported nothing running
  and exited 0. They now read the pidfile wt writes when it starts mtplx and
  stop the process it names — `Stopping mtplx (still loading, pid N)... done`
  — after the in-use question when a live wt session is on mtplx
  (`mtplx is still loading (pid N) and is in use by ...; stop anyway?`). The
  pid alone is never enough: the process must be alive, the current user's,
  an mtplx server on the provider's port by its exact arguments, and, when
  wt recorded its start time (a new file beside the pidfile, written by
  `wt start` from now on), the process that started then. Anything else is
  left alone, with a line saying what the pidfile names; if the process
  table cannot be read the commands exit 1 with
  `cannot tell whether mtplx is still loading`. wt sends SIGTERM, waits 10
  seconds, checks the process is still the same one, and only then sends
  SIGKILL; a process that is already exiting by then (a large server takes
  a moment to free its memory) is waited for and not killed. A stop it
  cannot confirm prints `failed` and exits 1. Ctrl+C during the wait prints
  `cancelled` and says the server was already sent SIGTERM, instead of
  `mtplx was not stopped`. A port that
  refuses with no such process behind it is still "nothing running", and so
  is a pidfile that is a symlink or that another user owns.
  `wt stop <mtplx model>` during a load stops nothing and names
  `wt stop mtplx`; a pidfile naming a process wt could not verify, or one
  that is not an mtplx server, puts that line in the error in place of the
  bare "is not running". Bare `wt stop` has no picker row for a loading
  mtplx and stops none: it prints
  `wt: mtplx is still loading (pid N) — "wt stop mtplx" or "wt stop --all" stops it`
  where it used to print `wt: no running local models`. An mtplx started by
  hand has no pidfile and is still not seen while it loads.
- `wt stop omlx`, `wt stop --all` and the mtplx stop above ask the in-use
  question whenever a live wt session uses any model of the provider they are
  about to stop as a whole, counted across the provider and not from the
  models wt could see running. With omlx's probe untrusted (an omlx that
  wants its API key with no `auth.secret_ref` on the registry's omlx row and
  a partly loaded pool, or a `/health` that did not answer in time), or with
  the session on a model that was not loaded or has no registry entry, the
  service was halted under the other terminal's session with no question
  (#307). When wt could not tell what the provider has loaded the question
  says so (`..., and wt could not tell what it has loaded; stop anyway?`).
- `wt: nothing running on <provider>` and `wt: no running local models` are
  printed only when that is known. When ollama's probe gives no usable answer
  and its port does not refuse, `wt stop ollama` and `wt stop --all` exit 1
  with `cannot tell what is running on ollama` (`--all` after stopping
  everything else), and bare `wt stop` with nothing to list exits 1 naming
  the provider it could not read. `wt stop <model>` of such a provider exits 1
  the same way, instead of reporting the model not running: a model id stops
  nothing there, and the model may be loaded. Each used to report nothing
  running and exit 0. When the picker does list models, bare `wt stop` adds
  a line naming a provider it could not read (`wt: could not tell what is
  running on mtplx — "wt stop mtplx" or "wt stop --all" stops mtplx`); the
  command it names is given only for mtplx and omlx, since no command stops
  an ollama wt cannot read. None of these failures prints the usage text:
  `wt stop` now shows it only for an argument mistake, as `wt stop --all`
  already did.
- `wt stop --all` waits for the LiteLLM proxy restart one halted provider
  started before it halts the next (mtplx, then omlx), as it already did
  between the model stops and the first halt.
- `wt config`, Models tab: a refusal too long for a short terminal even with
  the screen to itself (a registry path of 160 characters or more at 40x12)
  no longer ends, unmarked, wherever the terminal does — which first dropped
  the "press a key to go back to the form" line and then the end of the
  registry's path, the file the message tells the user to fix. The key hint
  is always the last line; the message keeps its first and its last lines,
  and one line between them says how many are not shown (`… 9 lines not
  shown …`). The same holds for the table's status (a refused removal), which
  now always has the key hints under it. The tests of these two screens no
  longer pass or fail with the length of `TMPDIR` (#318).
- The check wt makes before launching an ollama model directly (not through
  LiteLLM) now runs `ollama list` against the ollama address in
  `registry.toml`, as `wt stop`, `wt model add` and `wt cloud-sync` already
  did. It used to ask whichever daemon the shell's `OLLAMA_HOST` named, so
  with that variable pointing elsewhere a model the registry's daemon has
  was reported as "not available". When the registry has no ollama provider
  row, or the row's `base_url` is not an `http://host:port` address, the
  check now says so (`ollama check failed: ...`, as the picker's status or
  the launch's error; the status names what `base_url` must be within 80
  columns) instead of asking ollama's default daemon. The check also stops
  `ollama list` after 30 seconds, the limit `wt cloud-sync` already gives the
  same command, so a daemon that takes the connection and never answers fails
  the launch instead of freezing the picker on the update goroutine.
- wt's own tests no longer run the `ollama` on PATH: `go test ./...` made
  three `ollama list` calls from `internal/tui`, and
  `TestOllamaWarnShownWhenUnavailable` failed whenever that command exited
  non-zero. The lookup and the list are a seam
  (`ollamacheck.StubListForTest`) that `internal/tui` and `cmd/wt` fail
  closed (#317).
- `wt litellm sync` on a registry with one model id on two rows (which every
  launch refuses, and sync does not) routes the id from the row of the
  provider that is serving it. It used to take the first row with the id for
  its checks and the last one for the route, so the route could name a server
  that was not serving the model. When more than one of the rows is to be
  routed — both providers serve it, or a cloud model shares the id — or a
  cloud model shares the id with a local model whose provider's probe did not
  succeed, sync leaves the id's route as it is and warns once: `model "<id>" is in the
  registry twice (providers A, B); its route is left as it is — fix the entry
  in <registry path>`. The sync after a `wt model` write does the same. Such
  an id no longer brings on the `refused the probe connection ... its local
  routes are treated as stale` warning for a stopped provider that holds one
  of its rows: the route was not that provider's to lose.
- Ctrl+C during `wt stop omlx` is reported as `cancelled`, with an error that
  says the service was not stopped. It printed `failed` and `context canceled`,
  as if the provider were broken.
- `wt config`: `/` on the Agents tab filters the list (it showed `Filter: …`
  above every agent), and `Esc` on the list no longer ends the editor without
  the unsaved-changes prompt. Only `q` and `Ctrl+C` quit.
- `wt litellm sync`, `status`, `on`, `off` and `set` name the repair that
  works when wt's configuration does not load (#291). Each ended its refusal
  with "run `wt config` to repair" whatever had failed, so a missing registry
  was given two repairs (`wt model init`, then `wt config`) and a registry
  link that leads nowhere was sent to an editor that cannot touch it. They now
  say what `wt start` and `wt stop` say: nothing more for a missing registry,
  `fix the link or move it aside` for a broken link, and `wt config` only for
  a problem in `config.toml`. Every command also stops sending a registry.toml
  that does not parse, cannot be read, or sits at a path wt cannot examine (a
  file where its directory should be) to `wt config`: the hint is `fix that
  file by hand`, which `wt model init` now adds too, for a registry it cannot
  parse or read and for a top-level key it will not write. A provider or
  model row that fails validation (an empty or repeated id, no `model_name`,
  a model whose provider has no row, no location) gets `fix the entry in
  <registry path>`, as a mistyped location already did, from `wt`, `wt
  start`, `wt stop`, `wt smoke` and the `wt config` editor; the `model_name`
  error no longer carries its own `(add model_name to this registry.toml
  entry)` beside it. The `--family` note of `wt stats` follows the same rule
  and no longer names `modelman migrate`.
- A registry path that is a symlink to a file that is not there is reported
  as a broken link, naming the link and its target, instead of `model
  registry not found` (#248). An unconfigured agent no longer launches with no
  model routing in that case, and the hint no longer says to seed a registry
  that exists behind the link. The same goes for a registry whose directory
  is the broken link (`~/.config/local-ai` linked into a checkout or a volume
  that is not there).
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
