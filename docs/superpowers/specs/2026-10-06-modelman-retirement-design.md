# Retiring modelman into wt — design

Date: 2026-10-06
Issues: #259 and #258 (pre-work); #194, #260, #266, #267, #268 (closed by this work, see Step 6)

## Problem

Two tools manage the same models. modelman (Python, about 21,700 lines and 1,788 tests) owns `registry.toml`, the model TUI, downloads, prices, the ollama catalog mirror, the usage report and the benchmarks. wt (Go) launches agents, owns LiteLLM routing and local-model start and stop, and reads the registry read-only.

The split costs more each month:

- **Bridge code.** modelman drives wt through a subprocess for LiteLLM and for omlx start and stop. #260, #267 and #268 are all defects of that bridge.
- **State wt ignores.** modelman stores `ready`, `disk_path`, `size_bytes` and `running` per model in `modelman.toml`. wt takes all four from live probes and reads none of them.
- **Two lifecycle engines** with different policies, on the same ports and pidfiles.

## Goal

wt is the one tool for models, routes, prices and usage. The benchmarks live on as a standalone Python tool. modelman is deleted.

**Success criteria**

1. `modelman/` no longer exists, and no guide, skill, `CLAUDE.md` or wt message tells anyone to run `modelman`.
2. wt is the only writer of `registry.toml`. A wt write preserves every key wt does not model, and a no-op write leaves the file byte-identical.
3. Every modelman command has a named replacement or is listed as dropped (see the mapping table in Step 6).
4. `llmbench` runs the three benchmarks and provider isolation with no import of modelman, and its moved tests pass.
5. `make test-all` passes at the end of every step, with modelman still working until Step 6.

## Decisions

These were settled with the owner and are fixed for every step.

| Topic | Decision |
|---|---|
| Benchmarks | Stay in Python as a standalone package, `llmbench`, with provider isolation and the mlx_lm_server backend |
| Model management | CLI verbs under `wt model`, plus a Models tab in `wt config` beside the Agents tab |
| Hugging Face downloads | Not in wt. wt discovers what is on disk |
| Removing a model | Registry only. wt never deletes weights; it prints the path |
| Prices and catalog | OpenRouter refresh and the full ollama catalog mirror under one command, `wt cloud-sync` |
| Usage report | Folded into `wt stats`; spend is read by shelling out to `psql` |
| Transition | Additive. modelman keeps working, frozen to bug fixes, and is deleted in the last step |
| Price refresh | Manual only. The stale-price notice fires after 7 days |
| Pairing start | `llmbench provider isolate --solo` stays as the way to start an mlx_lm_server pairing beside other models |

**Not ported:** `modelman migrate` (legacy YAML import), the retired llamacpp provider, the apply-on-exit pending-changes queue, stored per-model state in `modelman.toml`, `[[families]]` management and `delete-family`, the ready toggle (download and delete), theme persistence in `settings.yaml`, the time-price resolver `price_at` (no production caller).

## End state

| Thing | Owner |
|---|---|
| `~/.config/local-ai/registry.toml` | wt writes; llmbench reads |
| Model add, edit, remove, list; Models tab | wt |
| OpenRouter prices, ollama catalog mirror | wt (`wt cloud-sync`) |
| Usage and spend | wt (`wt stats`) |
| Benchmarks, benchmark isolation, mlx_lm_server backend | llmbench |
| `~/.config/local-ai/benchmarks/` and `latest.toml` | llmbench |
| `modelman.toml`, `settings.yaml` | read by nothing; removed by hand |

## Order

Each step gets its own implementation plan and PR series. This document is the design for all of them; a step's plan is written when the step starts.

| Step | Work | Needs |
|---|---|---|
| 0 | Pre-work in wt: #259, #258, wording-pin test | nothing |
| 1 | Carve `llmbench` out of modelman | nothing |
| 2 | wt becomes a registry writer | 1 (three readers share the env name) |
| 3 | `wt model` CLI and the Models tab | 2 |
| 4 | `wt cloud-sync` | 2 |
| 5 | Spend in `wt stats` | nothing |
| 6 | Delete modelman; docs, skills, CI, strings | 1 to 5 |

PR #165 (`cc-session-transcripts`) touches root `CLAUDE.md` and the `Makefile`. Merge or close it before Step 6.

## Step 0 — pre-work

**#259: loading is not loaded.** `localmodels.Entry` and `catalog.Row` gain `Loading bool`, true when the omlx pool reports the model with `Loading && !Loaded`. `Running` stays true for a loading model, so it still occupies the pool, keeps its route and is still offered by `wt stop`. (omlx 0.7.0 refuses to unload a model mid-load, so that stop fails with omlx's answer; `wt stop omlx` is what calls a load off.) What changes: `wt start` on a loading model waits for the load instead of printing "already running", and a loading model is a start row in the pickers, not a launch row. The fallback pool reading has no loading signal and keeps today's behaviour.

**#258: show what a start unloaded.** The TUI start flow passes `Options.OnUnloaded` and puts "omlx unloaded X to make room" on the picker status line.

**Wording pin.** #267 is left unfixed, so one Go test asserts the exact text of the two `wt stop` refusals modelman matches by substring (`model "<id>" is not running`, `unknown model "<id>"`) and the registry-redirected refusal. The test is deleted with modelman.

## Step 1 — carve out llmbench

**Move.** `git mv` `modelman/src/modelman/benchmark/**` and `providers/lifecycle/**` to a new uv package at `llmbench/`, keeping the sub-package shape (`llmbench.benchmark.*`, `llmbench.providers.lifecycle.*`) so relative imports and repo-relative path depths are unchanged. 575 tests move with them (351 benchmark, 224 lifecycle). A scratch prototype of this move passed all 575 with `import modelman` blocked.

**Support modules** (about 330 lines, copied at the same relative positions): a read-only registry reader, `local_process` (three names), `_toml_io` (atomic write), `wt_bridge` (`warm`, `ensure_wt`, and the exception classes), the mtplx constants.

**Registry reader.** About 110 lines; reads only what the benchmarks use: model `id`, `family`, `provider_id`, `model_name`, `location`, `fetch.repo`, `fetch.local_path`, `draft.repo`, `draft.local_path`, and provider `id` and `location`. It tolerates unknown top-level keys, because it never writes. It must resolve the same path as modelman's loader until Step 6, because the moved mtplx backend loads the registry itself and `modelman start <mtplx model>` reads through it; one test asserts the two resolvers agree under `XDG_CONFIG_HOME`, with and without a file there.

**modelman rewiring.** modelman takes an editable path dependency on llmbench. `local_control.py` imports the lifecycle from llmbench; `modelman benchmark` and `modelman provider` stay mounted from llmbench until Step 6; modelman's `eval` extra forwards to `llmbench[eval]`. modelman re-exports `ProcessResult` and the bridge exception classes from llmbench, so there is one of each.

**Commands.** `llmbench run|list-workloads|show-results`, `llmbench agent …`, `llmbench eval …`, `llmbench provider isolate|stop|stop-all|restore|list`. Flags, exit codes and the provider `--json` envelope are unchanged. `--solo` is kept permanently.

**Latest-run pointers.** The four keys move from `modelman.toml`'s `[benchmarks]` table to `~/.config/local-ai/benchmarks/latest.toml` (override: `LLMBENCH_LATEST`). When `latest.toml` is absent, llmbench reads the old keys once as a fallback.

**Env.** `LLMBENCH_WORKLOAD` and `LLMBENCH_AGENT_DEBUG`, with the `MODELMAN_BENCHMARK_WORKLOAD` and `MODELMAN_AGENT_DEBUG` names kept as aliases. The `LLM_ISOLATE_*` names are unchanged.

**Tests and CI.** llmbench gets its own conftest with autouse guards: the subprocess allow-list, the urlopen block, the `os.kill` no-op, and redirects for the two real-home inputs the moved code reads (the LiteLLM LaunchAgent plist and `~/.pi/agent/models.json`). Its pytest and coverage config is written for it, not copied (no `asyncio_mode`; coverage source `src/llmbench`). A new `llmbench-ci` workflow runs `make check && make test`; `modelman-ci` also triggers on `llmbench/**`. `make install` and `make test-all` gain an llmbench step.

**Callers and docs.** `benchmarks/lib/benchmark-common.sh` switches to `uv run --directory llmbench llmbench provider`. Guides 05, 09 and 11, `docs/reference/provider-artifacts.md`, root `CLAUDE.md` and the `adding-a-benchmark-backend` skill switch to the `llmbench` spellings. llmbench gets its own `CLAUDE.md`, taking the provider-lifecycle and benchmark sections from modelman's.

**PR slices:** (1) the move, support modules, rewiring, CI; (2) callers and docs.

## Step 2 — wt writes the registry

### Document model

New package `wt/internal/tomlw`: decode with the pinned BurntSushi/toml into an ordered table tree, with key order recovered from `MetaData.Keys()`, and emit with an in-house emitter that reproduces tomli-w's layout. A prototype was byte-identical on the owner's real 791-line registry and on every modelman-written input tried.

The stock encoder is not used: it rewrites every line (sorted keys), and it formats local date and time values in UTC, which moves a bare date back a day on a machine east of UTC.

Known gap to close before merge: for an inline array of inline tables, the decoder's key list has no element boundaries, so per-element key order is not recovered. The emitter must recover the boundaries (a key already consumed, or absent from the current element, starts the next) and a golden test covers two inline rows with different key order.

### One write path

`config.UpdateRegistry(apply func(*RegistryDoc) error) (changed bool, err error)` is the only way wt writes the registry.

1. Take a `flock` on `<registry path>.lock`, the path as named, not a symlink's target.
2. Resolve the path. A dangling symlink is `ErrRegistryLink`, never "missing". A resolving symlink is written through, so a dotfiles link survives (#248).
3. Read and decode. An unknown top-level key refuses the write and names the keys (#247). wt never adds a top-level key.
4. Run `apply`. It must be pure: it may run more than once, and must not print, call ollama or mutate caller state.
5. Validate only the rows `apply` touched, against wt's typed decode and modelman's required-field and cost rules. A bad row elsewhere does not block the write.
6. Skip the write when the bytes are unchanged. Otherwise re-check that the file has not changed since step 3 (retry `apply` up to three times, then `ErrRegistryBusy`) and rename atomically.

The existing `ErrRegistryRedirected` rule is unchanged: a registry write under a redirected registry succeeds, and the route sync that follows is refused unless `WT_LITELLM_CONFIG` is set.

### Operations

`RegistryDoc` is patch-shaped. Nothing writes a whole typed struct, so a key wt does not model is never touched, and adding a field to `config.Model` cannot change what a write touches (a test pins this).

| Operation | Behaviour |
|---|---|
| `PatchModel(id, set, unset)` | Set or delete named keys on one model row; new keys are inserted at their schema position. `ErrModelNotFound` |
| `AddModel(table)` | Append a model row. `ErrModelExists` |
| `CloneModel(fromID, overrides)` | Copy a row with overrides (catalog re-tags) |
| `RemoveModel(id)` | Delete the row and return it, so the caller can print the weights path |
| `SetTimePrices(id, rows)` | Replace `cost.time_prices` |
| `AddProvider(table)` | Append a provider row; used only by seeding |
| `Models()`, `Providers()` | Read views for planners |

`model_info` is opaque: written only when a caller passes it. A price key is written only when its value changes, so an integer price is not rewritten as a float.

### Seeding

`config.SeedRegistryDefaults` is the Go port of modelman's default-provider logic: add the default row for ollama, omlx, mtplx or mlx_lm_server when a model references it, or (for the first three) when its command is on PATH; add a native provider row for each configured agent. It runs inside one `UpdateRegistry` and creates the file when it is missing. It has two callers: `wt model init [--json]`, and `wt model add` (Step 3) in the same locked write. Reads never seed.

The "seed the registry with `modelman migrate`" hints in `cmd/wt/helpers.go` and `internal/config/registry.go` change to name `wt model init`.

### Env name

`WT_REGISTRY` > `MODELMAN_REGISTRY` > `$XDG_CONFIG_HOME/local-ai/registry.toml` > `~/.config/local-ai/registry.toml`. One PR adds it to all three readers (wt `RegistryPath`, modelman `_default_registry_path`, llmbench `registry_path`), each with a precedence test, and the test helpers in all three clear both names.

### Interim: two writers

modelman's registry lock is in-process only and its TUI saves a whole snapshot, so an open modelman TUI would revert a wt edit on its next save. The one modelman change in this migration: `load_registry` records the file's `mtime_ns` and size, and `_write_registry` raises `RegistryError("registry.toml changed on disk; reload")` when they differ; a successful write updates the recorded values. This must merge before Step 3's first writing PR.

### Contract fixture

`docs/contracts/registry.written.sample.toml`: one file in tomli-w form with unknown keys at every level, ints and floats, an empty array, long and short arrays of tables, `time_prices`, and offset and local datetimes. Go asserts decode-then-emit reproduces the bytes. modelman (until Step 6) asserts `tomli_w` reproduces them too. llmbench asserts its reader loads the file; `llmbench-ci` triggers on `docs/contracts/registry*.toml`.

### Test guard

A wt test binary must never write the developer's real registry. The guard is a package-level seam set by `IsolateConfigHomeForTest` and `TestMain`, not an `import "testing"` in shipped code.

**PR slices:** (1) symlink resolution and `ErrRegistryLink` on the read path, lock helper refactor; (2) `WT_REGISTRY` in three readers; (3) `tomlw` and the fixture; (4) `RegistryDoc` and `UpdateRegistry`; (5) seeding, `wt model init`, hint text; (6) modelman's stale-snapshot guard.

## Step 3 — model CLI and the Models tab

### Core

`wt/internal/modeladmin`, with no UI imports, shared by the CLI and the tab:

- **Rows.** Every registry model plus discovered inventory entries, from one live probe. `localmodels.Entry` gains `Path` and `Size`, filled from data the probe already touches (omlx and mtplx directory, ollama size from `/api/tags`); a row with `fetch.local_path` takes its path from that key and its presence from a stat.
- **Status.** `ok`, `missing`, `unknown` (provider unreachable), `new` (discovered, unregistered), `-` (mlx_lm_server pairing, which cannot be enumerated). Running is `run`, `load` (from Step 0), blank, or `?`.
- **Validation and ids.** A local add takes the artifact name exactly as the provider lists it; the id is `config.DiscoveredModelID(provider, name)`, the id wt already routes. A cloud id keeps modelman's rule (`/` in the name becomes `--`), so ids agree across tools and history keeps matching; `--id` overrides.
- **Immutable after creation:** id, provider, model name (usage history, rotation and profiles key on the id). **Editable:** family, tags, location, the three per-token prices, subscription price and period.
- **Ollama capabilities.** Adding an ollama model runs a best-effort `ollama show`, pinned to the provider row's address, and records `model_info.supports_function_calling` and `supports_vision`, which wt copies into the LiteLLM route. A failed lookup adds the model without them and says so.

### CLI

| Command | Behaviour |
|---|---|
| `wt model` | Opens `wt config` on the Models tab; needs a TTY |
| `wt model list [--json]` | All rows. Text is borderless and width-aware, tested at 80 columns; size and path that do not fit are left to `--json` |
| `wt model add <provider> <name> --family F [--tags a,b] [--location] [--input-price --cache-price --output-price] [--subscription-price --subscription-period] [--id] [--draft]` | Adds a model; seeds a missing default provider row in the same write |
| `wt model edit <id> [--family] [--tags] [--location] [price flags]` | Patches the named fields; no flags is a usage error |
| `wt model rm <id>… [--yes]` | Registry only; prints where the weights are |
| `wt model init [--json]` | From Step 2 |
| `wt stop --all [--yes]` | Stops every running local model with the existing in-use confirmation, then halts pool services |

Each writing verb does one `UpdateRegistry` and then one route sync. Exit 0 when the write succeeds, even if the sync only warned; exit 1 on a validation error, an unknown or duplicate id, an unreadable registry, or a declined removal.

`local_path` entries (the `bin/mlx-quantize` workflow) stay a hand edit of `registry.toml`, as today. wt preserves the keys and shows the path.

### Models tab

`wt config` gains a tab bar: Agents, Models. The Models tab follows the Agents tab's interaction.

- **Keys:** `enter` edit (register, on a `new` row), `n` add, `d` remove, `r` re-probe, `/` filter, `tab` switch tab.
- **Form:** hand-built on `bubbles/textinput` like the agent form. Fields: provider (choice; mlx_lm_server excluded), model name, family (suggestions from existing families; the accept key fires only with the cursor at the end of the text), tags, location, three per-token prices, subscription price and period. `ctrl+s` saves, `esc` cancels.
- **Remove:** a y/N prompt that shows the weights path; the path is repeated on the status line afterwards.
- **Saving.** Each saved form and each confirmed removal writes the registry at once. The Agents tab keeps its buffer and `ctrl+s`; its quit prompt concerns agent edits only.
- **Routes.** The tab does not sync routes per change. It marks routes pending on the status line and runs one sync when the editor exits, only if the registry changed. If the editor dies first, the next `wt start`, `wt stop` or launch through LiteLLM repairs the routes, as it does today. Requirement, verified in this step: a sync that leaves `config.yaml` byte-identical does not restart the proxy.
- **Probing** runs in a `tea.Cmd`, never on the update loop.
- **Layout.** The wide-table sizing helpers (`fitTo`, `listFrame`, the column-dropping table) move from `internal/tui` to a package both `tui` and `configeditor` import; `tableColumns` is generalised from fixed arrays to slices with a per-table drop order. The tab is added to the fit tests at 40/80/120 by 12/24/50, with assertions that the header row and the focused form field are present, and real screens are captured through the pty driver at 80x24 before the form PR merges.

### mlx_lm_server pairings

Created with `wt model add mlx_lm_server <target> --draft <draft>`; shown as rows; metadata editable in the tab. wt cannot start one. `catalog.MissingReason` and `BlockReason` name `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>`.

**PR slices:** (A) `wt stop --all`; (B) `Entry.Path`/`Size`, rows, `wt model list`; (C) add, edit, rm on the Step 2 writer; (D) layout extraction and `tableColumns` refactor; (E) tab bar and Models tab list, remove, exit sync; (F) the form; (G) pairings and their hints.

## Step 4 — wt cloud-sync

```
wt cloud-sync [--only prices,catalog] [--dry-run] [--html FILE] [--yes]
              [--approve-removals DIGEST] [--force]
```

### Flows

New pure package `wt/internal/cloudsync`.

- **Prices.** Fetch `openrouter.ai/api/v1/models`, match OpenRouter-priced registry models on `model_name`, plan price updates. `--dry-run` lists `id: old -> new`.
- **Catalog.** Port of the pricing-page parser (with `golang.org/x/net/html`'s tokenizer), cloud-tag resolution against `ollama.com/library`, the planner, the mass-removal guard and the removal digest. The parser keeps every fail-loud check and saves the raw HTML on a shape change.

**Sequence:** fetch and plan both flows with no lock held; print both plans; gate (confirmation on `/dev/tty`, digest, `--force`); apply registry changes in one `UpdateRegistry`, re-planning under the lock and refusing if the plan changed; run `ollama pull` and `ollama rm` through the CLI pinned with `OLLAMA_HOST` to the provider row's address; one route sync.

A registry entry is removed in the registry write and its tag removed afterwards. A failed `ollama rm` leaves a stray tag the next run removes. A tag is removed only when no remaining registry ollama entry names it.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | Every selected flow finished or had nothing to do |
| 1 | A step failed in either flow, or a usage error |
| 2 | Catalog changed nothing: page fetch failed, `--html` unreadable, ollama tags unreadable, or no cloud tag resolved |
| 3 | Catalog changed nothing: page shape changed; HTML saved |
| 4 | Catalog changed nothing: mass removal refused without `--force` |
| 5 | Catalog changed nothing: removals not approved |

Codes 2 to 5 describe the catalog flow only and take precedence over 1. The flows are independent: a refused catalog plan does not stop the prices flow, and output lines are prefixed `prices:` or `catalog:`. `--html`, `--approve-removals` or `--force` with `--only prices` is a usage error. A typed exit-code error in `cmd/wt` carries the code; every other command keeps exit 1.

### Last-refresh date

Nothing new is stored. The stale-price notice derives the date from the newest `pricing_updated_at` among OpenRouter-priced models.

- The prices flow stamps every model it matched, including those whose price did not change.
- `wt model edit` does not stamp `pricing_updated_at`.
- The route sync runs only when a price or the model set changed, so a stamp-only run does not restart the proxy.
- The notice fires when the date is more than 7 days old, or absent, and names `wt cloud-sync`.

`config.PriceRefreshLastRun` and its read of `modelman.toml` are removed in this step.

### Time-windowed prices

`cost.time_prices` rows are still written by the catalog flow and preserved by every other write. Nothing applies them, as today.

### Dependency

`golang.org/x/net` is added as a direct dependency. Before it is accepted, `go mod tidy`, `go build ./...` and the full wt test suite run on a branch, and the resulting `go.mod` diff is recorded in the PR.

### Skill

`modelman/.claude/skills/ollama-catalog` is moved to `wt/.claude/skills/cloud-sync` and rewritten: dry run, summarise both plans, get the go-ahead, apply with the digest, check `wt litellm list`. The root `CLAUDE.md` pointer changes in the same PR.

**PR slices:** (1) the pure core and the dependency; (2) `--only prices` end to end, exit-code plumbing, the derived notice; (3) the catalog flow; (4) skill and docs.

## Step 5 — spend in wt stats

`wt stats` keeps its survey table and prints a second per-model table for the same `--window`: MODEL, LAUNCHES, REQUESTS, PROMPT, COMPLETION, SPEND.

- **Launches:** `usage.StoreImpl.AllCounts(agent)` enumerates every model in `usage.jsonl`.
- **Connection string:** `WT_LITELLM_DATABASE_URL`, then `MODELMAN_LITELLM_DATABASE_URL`, then `general_settings.database_url` in LiteLLM's `config.yaml`; an `os.environ/NAME` value is looked up in wt's own environment and then in the LiteLLM LaunchAgent plist's `EnvironmentVariables` (the existing `litellm.LoadProxyEnv`), and when `config.yaml` names no `database_url`, `DATABASE_URL` is looked up the same two ways (LiteLLM's own fallback); a blank value from any source counts as unset.
- **Query:** one aggregated query over `"LiteLLM_SpendLogs"` through `psql -X -w -q -At -v ON_ERROR_STOP=1`, returning one JSON document, with `PGCONNECT_TIMEOUT=3` and a 10-second deadline.
- **Degradation:** with no `psql`, no reachable database or no configured URL, the launch counts print, spend cells show `-`, one note goes to stderr and the exit code is 0. Requests with no model are counted in a stderr note.
- **Flags:** `--family` (new; usage table only), `--json` (new; one document with `window`, `as_of`, `survey`, `usage`). `--agent` filters launches and skips spend with a note.

**Dropped from `modelman usage report`:** arbitrary `--days N`, the Markdown output, the Reconciliation sections, the "Last wt launch" line, and the reverse-index fallback for rows with an empty model group.

Tests stub the query through a package-level seam and never reach a database. The table is measured at 80 columns.

**PR slices:** (1) `AllCounts`, `litellm.DatabaseURL`, the `spend` package; (2) the table, `--family`, notes; (3) `--json`.

## Step 6 — retirement

No `modelman` shim. `modelman` is reachable only through `uv run --directory modelman modelman`, which fails by itself once the directory is gone.

### Mapping table (guide 08)

| modelman | Replacement |
|---|---|
| TUI (`a`, `e`, `d` keys) | `wt config` Models tab, or `wt model add\|edit\|rm` |
| `start` with no argument | `wt model list` |
| `start <id>`, `stop <id>`, `stop --all` | `wt start`, `wt stop`, `wt stop --all` |
| `start <mlx_lm_server pairing>` | `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` |
| `sync` | `wt model init` for provider rows; nothing for state |
| `refresh-prices`, `ollama-catalog sync` | `wt cloud-sync` |
| `usage report` | `wt stats` |
| `litellm …` | `wt litellm …` |
| `benchmark …`, `provider …` | `llmbench …`, `llmbench provider …` |
| `migrate`, `delete-family`, TUI `r` (download, delete), `l`, `s` | dropped; `wt litellm on\|off`, `wt start`, `wt stop` cover the last two |

### PR slices

1. **Docs and skills.** Guides 00 to 11, root `README.md` and `CLAUDE.md`, `litellm-session-logs/CLAUDE.md`; `adding-a-provider` rewritten for wt and moved to `wt/.claude/skills/`; `adding-a-tui-screen` deleted; `mlx-lm-quantization` updated (hand-edit `local_path`, or place the output in the provider's model directory and `wt model add` it by name).
2. **wt strings sweep.** Comments and stragglers only; each user-facing hint already flipped in the step that shipped its replacement. Ends with a one-time grep for `modelman` in non-test Go.
3. **The deletion.** `git rm -r modelman/`; `git mv modelman/docs/superpowers` to `docs/superpowers/modelman/`; remove `modelman-ci`; root `Makefile` `install` and `test-all`; llmbench drops its registry-path parity with modelman, the retired llamacpp backend entry and `LLM_ISOLATE_LLAMACPP_MODEL`; delete the wording-pin test from Step 0.
4. **Remove `wt start --json` and `--plan`,** `start_json.go`, its test and `docs/contracts/wt-start-cli.sample.json`.
5. **wt stops reading `modelman.toml`.** Delete `internal/config/modelman.go`, the legacy `[litellm]` fallback, and `docs/contracts/modelman.sample.toml`.
6. **Optional, mechanical.** Move Go tests from `MODELMAN_REGISTRY` to `WT_REGISTRY`, leaving one alias-precedence test.

### What stays

- `wt warm` (llmbench's omlx backend calls it for a keyed omlx) and `wt served`.
- Env aliases, permanently, each read after its `WT_` name: `MODELMAN_REGISTRY`, `MODELMAN_LITELLM_CONFIG`, `MODELMAN_LITELLM_RESTART_CMD`, `MODELMAN_LITELLM_DATABASE_URL`.
- Contract fixtures: `registry.sample.toml` and `registry.written.sample.toml` stay cross-language (Go and llmbench). The others become Go-only, with their headers corrected.

### Left on disk

`~/.config/local-ai/modelman.toml` (its legacy `[litellm]` table holds an API key), its `.bak-*` copies, `settings.yaml`, legacy `config.yaml` and `families/` are read by nothing. wt never deletes them. Guide 08 documents the manual removal.

### Issues

Close #259 and #258 by their fixes in Step 0. Before closing #194, refile its one live item (mlx_lm_server pairing identity: lenient name matching and the sole-registered fallback can read the wrong pairing as running) as its own issue against llmbench's backend and wt's pairing detection. Close #194, #260, #266, #267 and #268 as obsolete when modelman is deleted.

## Testing

- **Ported behaviour is written test-first in Go** from the Python tests that encode a numbered fix: #247 and #248 for the registry, the discovered-id rules, the catalog safety net, the price merge rules. Tests of mechanisms that disappear (running flags, the queue, the bridge, download progress) are not ported.
- **Scratch-registry safety.** Every wt command that writes the registry has a test under a redirected registry with and without `WT_LITELLM_CONFIG`. `bin/check-config-dirs-untouched` stays the CI backstop in `wt-ci` and `llmbench-ci`.
- **Byte stability.** Decode-then-emit is a fixed point on the written fixture; an unchanged `PatchModel` leaves the bytes identical on a row with integer prices, no tags and a `model_info` table.
- **Screens.** Fit tests at every supported size, plus pty captures at 80x24.
- **No live calls in tests.** ollama, OpenRouter, `ollama.com` and `psql` are reached through seams.
- **Live checks before merge,** done by hand: one `wt cloud-sync` against the real services from a scratch registry, and one `wt model add`/`rm` round trip.

## Risks

- **Lost Linux coverage.** `modelman-ci` runs on Linux and `wt-ci` on macOS only, so ported code loses its Linux run. Accepted; llmbench keeps a Linux job.
- **Scraped pages.** `ollama.com/pricing` has no API contract. The fail-loud parser and the removal gates are the protection against deleting entries after a page change, and they are ported in full.
- **Interim double writer.** Covered by wt's lock and re-check plus modelman's stale-snapshot guard. A window of a few instructions remains on a single-user machine.
- **Form size.** modelman's form is about 850 lines with 100 tests. The wt form is smaller (no Hugging Face parsing, no dual-model inputs) but is the largest new UI in wt and must fit 80x24.
- **Dependency bump.** `golang.org/x/net` raises two indirect dependencies under Bubble Tea; verified on a branch before acceptance.
