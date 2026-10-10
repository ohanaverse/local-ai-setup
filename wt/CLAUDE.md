# CLAUDE.md

Guidance for Claude Code when working in this repo.

## What this repo is

The `wt` binary (`cmd/wt/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--cwd` (the current directory — the one launch that does not move to a checkout root), `-W <name>` (named worktree), or the worktree picker.
2. **What** — `-A <name>`: an LLM agent, or `shell` for a plain command. **Never defaulted**: omitting `-A` always shows the agent/command picker.
3. **Which model** — `-M <provider>/<name>`, filtered by `-T <tags>` / `-F <family>`, or picked (eligible models rotate on successive launches).

`bin/*-wt` are shims that forward to `wt` (`claude-wt` → `wt --agent claude`), so `wt` must be on `$PATH`. All logic lives in Go.

## Installation

```bash
make install      # builds bin/wt and copies it + the shims to ~/.local/bin/
```

Requires Go 1.26.7 (see `go.mod`).

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`). A drifted linker signature is rejected by AMFI with "Taskgated Invalid Signature" and SIGKILLs the binary (exit 137).

Run `make help` for the full target list. `make test` requires `make install` first — it exercises the installed binary, not just the build.

## Key flags

Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; missing flags come from pickers or defaults. The agent is never defaulted.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use/create the worktree for `<name>`; skip the worktree picker |
| `-A <name>`, `--agent <name>` | Pin the agent or `shell`; never defaulted |
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`; cloud or running → launch; a non-running local starts (with a progress display); a blocked model errors with that row's reason. Without `-A`, prompts for the agent first, then validates the pin |
| `--replace` | With `-M`, start the model even if it means stopping a running one |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR) |
| `-F <family>`, `--family <family>` | Filter models by family (comma-delimited, OR) |
| `--cwd` | Launch in the current directory; skip the worktree picker. The agent starts there, so its own `--continue` (after `--`) finds that directory's sessions |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed AGENTS.md + pointer files, then exit |
| `--version` | Print version and exit |
| `--check-guard` / `--no-guard` | Check / remove the `block-main-commit` guard |
| `--debug-worktrees` | List worktrees and branches (test helper) |

## Passthrough args

Extra args forward to the launched agent. `--` is only required when the command starts with a flag-like token, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Post-exit flow

After the launched subprocess exits, the TUI and non-TUI paths run the same fixed sequence — [docs/wt-agents/README.md#post-exit-order](docs/wt-agents/README.md#post-exit-order). Change both paths together.

- `survey.ReleaseSession()` runs before the stop picker, so the model just used does not count as in use.
- The summary line (`agents.Summary`) is the single formatter for both paths and never affects the exit code.
- A batch of stops costs one LiteLLM restart: `lifecycle.StopModelDeferred` per model, then one `lifecycle.SettleRoutes` (#142).

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md) before changing the summary line, the stop picker (`survey.Picker`, `chooseLabeled`, `stopEntries`), the stale-pricing notice, the session survey, session handling, or profiles.

## Session survey (Go)

**The survey is switched off** (`survey.Enabled = false`, `internal/survey/prompt.go`, #136). The code is kept and tested so it can return by flipping that value; `wt stats` and the picker's SURVEY column still report stored answers. The stop picker lives in `internal/survey` and is unaffected. Keep `drainTTYInput` after the last question: it stops a multi-line paste running as shell commands once wt exits. Details: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#session-survey-go).

## Docs

- `docs/configuration.md` — Claude Code / Codex CLI config
- `docs/wt-config.md` — `wt config` subcommands
- `docs/wt-agents/` — per-agent reference (one file per launcher); `README.md` there covers LiteLLM routes/proxy lifecycle and post-exit order
- `docs/wt-agents/profiles.md` — local-model launch profiles
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md`, `docs/wt-model.md`, `docs/wt-cloud-sync.md` — command references
- `docs/superpowers/specs/`, `docs/superpowers/plans/` — wt design specs and plans (newer cross-package specs live in the monorepo's [../docs/superpowers/](../docs/superpowers/))
- `../CLAUDE.md` — monorepo-wide commands, benchmark isolation helpers, shared config ownership (wt is the only writer of `registry.toml` — the `wt model` commands, the Models tab of `wt config` and `wt cloud-sync`; wt owns `~/.config/agent-wt/config.toml`)

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

- **Test seams** are package-level vars: production code calls the var, tests swap it. A new seam is a `var x = realX` plus a `realX` function. `internal/lifecycle` instead keeps every seam in one `env` struct (`defaultEnv()` / `testEnv()`).
- **No test reads the real mtplx pidfile or signals a process it did not start**: the `TestMain`s of `internal/lifecycle`, `internal/survey` and `cmd/wt` call `lifecycle.IsolateProcessesForTest()`.
- **Tests stay off the developer's machine.** The `TestMain`s of `cmd/wt`, `internal/tui`, `internal/config`, `internal/modeladmin`, `internal/configeditor` and `internal/cloudsync` call `config.IsolateConfigHomeForTest`, which points `XDG_CONFIG_HOME` at a throwaway directory and clears `WT_REGISTRY` and `MODELMAN_REGISTRY`; `cmd/wt` and `internal/tui` also stub the inventory probe, hard-fail model starts, and no-op the route check. `cmd/wt`'s also replaces `querySpend`, and `internal/spend`'s fails its `lookPath`/`runPsql` seams, so no test runs `psql` or reaches the LiteLLM database. Tests name a scratch registry through `WT_REGISTRY`. In a package with no isolating `TestMain`, a test that sets it also sets `MODELMAN_REGISTRY` to `""`: the alias is read after `WT_REGISTRY`, permanently, so a test that clears only one name can still inherit the other. The alias's own tests are in `internal/config/registry_env_test.go`. A new package whose tests reach `config.Dir()` needs the same setup; a new launch path calls the route check through `stubEnsureRoute(t)`'s seam.
- **Assert on unexported functions directly** (e.g. `buildStatsRows`); parsing rendered lipgloss output flakes under forced-color ANSI.

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, starts a model, or touches routes — it lists every seam and what each `TestMain` stubs.

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go test ./internal/agents -run TestOpenCodeOllamaPrefix -v   # one test
go vet ./...                         # static analysis
make check                           # shellcheck + shfmt check + go-format-check (gofmt -l gate wt-ci runs); `make format` writes both
```

From the monorepo root, `make test-all` runs the CI-equivalent sweep (root lint + llmbench + wt build/vet/test).

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,tomlw,cloudsync,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,tuilayout,configeditor,ollamacheck,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke}`.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config and profiles.toml once) |
| `cmd/wt/commands.go` | `rotate` subcommand (debug helper) |
| `cmd/wt/commands_config.go` | `wt config` subcommand family; `runConfigEditor` opens the editor on a tab (bare `wt config`: Agents; bare `wt model`: Models) and runs the one route sync the Models tab owes |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch from live `catalog` rows; a `-M` pin on a start row starts it |
| `cmd/wt/start.go` | `startForLaunch` — non-TUI start driver: stderr progress, Ctrl+C cancel, replace confirmation, `allowReplace` |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers; TTY seams and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `launchFiltered` (`launchFilteredImpl`), `launchPassthroughImpl`, `runAgentCmd`; profile apply (`applyProfileForLaunch`, `applyResolvedProfile`) |
| `cmd/wt/stats.go` | `wt stats` — read-only report: the survey table over `survey.jsonl`, then the usage table |
| `cmd/wt/stats_usage.go` | `wt stats`' usage half: `collectUsage` (launches from `usage.jsonl` joined with LiteLLM spend), `buildUsageRows`, the `querySpend` and `stdoutWidth` seams |
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the usage table's cells, laid out by `renderPlainTable` |
| `cmd/wt/plain_table.go` | `renderPlainTable` — the one borderless, width-aware text table (`wt stats`, `wt model list`): the first column is the row's key and is never truncated |
| `cmd/wt/stats_json.go` | `wt stats --json` — `buildStatsJSON`, one document built from the same rows as the tables |
| `cmd/wt/model_cmds.go` | `wt start` / `wt stop` |
| `cmd/wt/smoke.go` | `wt smoke` — one-shot model×agent smoke test |
| `cmd/wt/profile.go` | `wt profile list/show/status/on/off`; `setEnabledLine`'s surgical `enabled = ...` edit |
| `cmd/wt/litellm.go` | `wt litellm ...` |
| `cmd/wt/model.go` | `wt model` group — bare, it opens the Models tab of `wt config` (needs a terminal); `wt model init [--json]` — creates the registry and seeds provider rows, then one route sync (`syncRoutesAfterWrite`) |
| `cmd/wt/model_write.go` | `wt model add`, `edit`, `rm` — each one `modeladmin` write, then one route sync (`syncAndWarn`); the `ollamaCaps` seam, and `confirmRemove`, which is `wt stop`'s `promptStop` (it opens the terminal through `openTTY`) |
| `cmd/wt/model_list.go` | `wt model list [--json]` — `modeladmin.Rows` over one probe; `fitModelList` drops PATH, then SIZE, on a narrow terminal |
| `cmd/wt/cloudsync.go` | `wt cloud-sync` — the command, both flows' plans printed, the one confirmation, the one `config.UpdateRegistry` both flows share (`writeCloudSync`: each plan is made again under the lock; a flow whose plan changed is marked stale and not applied, and when no flow is left the `apply` returns `errPlanChanged`, so nothing is written and a missing registry is not created), the route sync, the exit code (`cloudSyncOutcome.err`); the openrouter flow's fetch (`planOpenRouterFlow`); seams `cloudFetch`, `confirmCloudSync`, `cloudSyncNow` (the prompt opens the terminal through `openTTY`) |
| `cmd/wt/cloudsync_catalog.go` | the ollama flow: `planOllamaFlow` (the skip on a registry with no ollama provider row, page or `--html`, `ollama list`, tag resolution, the duplicated-id refusal, the printed plan; exit 2 and 3), `ollamaGate` (mass removal, the removal digest; exit 4 and 5), `runOllamaWork` (the daemon named once, then pulls, then removals, after the registry write), `saveFailedHTML` (always a new 0600 file) |
| `cmd/wt/cloudsync_ollama.go` | the ollama CLI for the ollama flow: `ollamaTags` (`ollama list`, cloud stubs included), `ollamaPull`, `ollamaRemove` (ollama's "model '<tag>' not found" is done; the answer must name the tag), all through the `ollamaCLI` seam, pinned with `OLLAMA_HOST` to the registry's ollama origin; `ollamaFailure` (one line of stderr, escapes stripped), `ollamaTimedOut` (wt's own limit, said as a timeout), `cmd.WaitDelay` so a wrapper script's child cannot hold a stopped command open |
| `cmd/wt/exitcode.go` | `exitCodeError` / `exitCodeOf` — how one command exits with a status other than 1 |
| `internal/config/` | config load/validate/save (agents + joined registry catalog), route resolution (`ResolveRoute`), migrations |
| `internal/tomlw/` | ordered TOML document (`Decode`, `Table`) and an emitter (`Encode`) that reproduces tomli-w's layout byte for byte — what lets a wt write leave every line it did not change as it was. Imports nothing from wt; never use the stock `toml.Encoder` on the registry (it sorts keys and shifts local dates) |
| `internal/cloudsync/` | the pure core of `wt cloud-sync`: `ParsePricing` (the only code that knows ollama.com/pricing's HTML; `golang.org/x/net/html` tokenizer, fail-loud `*ParseError`), `ResolveCloudTags`/`VerifiedTags`, `PlanCatalog` (+ `EntriesGone`, the one count `MassRemoval` and the exit-4 line both use, `RemovalDigest`, `Format`), `ParseOpenRouter`/`PlanPrices`, and the two `Apply` methods that change a `config.RegistryDoc`. No I/O, no clock: see [Cloud sync](#cloud-sync-wt-cloud-sync) |
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection |
| `internal/usage/` | append-only JSONL launch history (1d/7d/30d); `RecordFor` tags the agent; `CountsForAgent` per agent×model, legacy agent-less lines count toward `Counts` only; `(*StoreImpl).AllCounts(agent, asOf)` enumerates every model in the file, bucketed against the caller's instant (for `wt stats`; not on the `Store` interface) |
| `internal/refcount/` | live-session "in use" counts: JSONL keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point |
| `internal/survey/` | post-session survey + stats; stop picker and stop loop (`Picker`, `PickerWith`, `ReadStopState`, `StopEntries`) |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, capabilities), picker catalog, drivers, `RunAndCleanup` (shared apply-run-cleanup core for both launch paths), `BuildPassthroughCmd` |
| `internal/profiles/` | launch-profile overlays — see [Profiles](#profiles-internalprofiles) |
| `internal/smoke/` | `wt smoke`'s core: `Eligibility`/`Candidates` (rows per agent from one inventory snapshot — call `Eligibility` directly when you need both the model union and per-model agent lists, to avoid two probe rounds), `RunRow` (PASS/FAIL/SKIP via the `buildAndRun` seam) |
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build` (local rows only from the inventory), `Find` (includes discovered rows), `MissingReason` (why a pinned registry id has no row), `Row.Action` (launch/start/block), `Row.BlockReason`, presence status (`ok`/`unknown`; `new` for discovered, including discovered models handed in via `Input.Models`). No rendering, counts or sorting |
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/modeladmin/` | the core of model management, shared by the `wt model` commands and the Models tab, with no UI import: `Rows` (every registry model and every discovered one, with status, running, path and size — nothing hidden, unlike `catalog.Build`), `WeightsNote`, `FormatSize`; `Add`, `Edit`, `Remove` (each one `config.UpdateRegistry`, no route sync), `CheckAdd` (what an add refuses without the registry, for a caller with a slow lookup to run first), `Fields` and `FieldError` (the editable fields as typed, and which one is wrong), `DeriveID`, `OllamaCapabilities` |
| `internal/lifecycle/` | local-model start/stop engine — see [Lifecycle](#lifecycle-internallifecycle) |
| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management: `configfile.go`, `entry.go`/`policy.go` (entries, provider mappings), `service.go` (`ApplyChange` — the targeted route writer the lifecycle hook uses, its unit `Change` with `RemoveFamilies`; `DiscoveredModel`, `RowFamily`; `Sync`/`PlanSync`), `restart.go` (`RestartContext`, `Listening`, `WaitReady`), `dburl.go` (`DatabaseURL` — the spend database's connection string; reads, never writes) |
| `internal/spend/` | per-model request, token and cost totals from the proxy's `"LiteLLM_SpendLogs"` table: one aggregated query through `psql` (`Query`) over the window `(start, end]` (`InWindow` — the launch counts' rule, and what a test's spend stub filters with), typed failures (`ErrNoConnectionString` for a blank string and `ErrConnectionString` for one that cannot be handed over, both refused before `psql` is looked for; `ErrNoPsql`, `ErrUnreachable`, `ErrQuery`), no Postgres driver. `conn.go` turns the connection string into libpq's `PG*` environment variables — it is never a `psql` argument (#282), and `psql` inherits no `PG*` variable from wt |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection, enumeration, creation |
| `internal/initseed/` | `--init` seeding |
| `internal/ollamacheck/` | pre-launch `ollama list` availability check, pinned to the registry's ollama address; `StubListForTest` is its seam (`fortest.go`) |
| `internal/configeditor/` | the TUI behind `wt config`: the Agents tab (`config.toml`; edits buffered, saved with ctrl+s) and the Models tab (`registry.toml` through `internal/modeladmin`: a table of `modeladmin.Rows`, `d` to remove, and the add / register / edit form of `models_form.go` on `n` and `enter`, hand-built on `bubbles/textinput` like the agent form; each change written at once from a `tea.Cmd`, the probe and the form's ollama lookup in a `tea.Cmd` too). A refused save stays on the form — the cursor on the field a `modeladmin.FieldError` names, any other refusal (`config.ErrModelAmbiguous`, a row the writer will not write back) shown as the save's error with nothing written. `Run` reports `Result.RegistryChanged`, and `cmd/wt`'s `runConfigEditor` then runs the one route sync the tab owes; a quit asked for while a write is in flight waits for it (`leave`, `quitPending`), so the result is never "unchanged" for a registry that changed, and is then an ordinary quit: agent edits made while it waited get the unsaved-changes prompt |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell, pickers, launch, start-on-select flow (`start_flow.go`); `modelrows.go` (rows + sort), `modeltable.go` (`buildTable`, `renderTable`); `PickStartModel` — standalone picker without launch-route gating, used by `wt start` and `wt smoke` (`PickModel`, the route-gated variant, currently has no production caller) |
| `internal/tuilayout/` | what both TUIs fit a terminal with: `ListFrame`, `FitTo`, `DrawnFrame`, `Clip` (a list-backed screen is never taller or wider than the terminal), `WrapText`/`Flow` (free text wrapped between words, for a status that must be read whole) and `Columns` (a table that drops whole columns, in an order each table names; a list item joins one through `TableItem`; `StyleTableTitle` is the one title-bar reset its `TitleRoom` counts on, called by every table-backed list) |

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry; nothing in this file's write path touches them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields). `Save(cfg)` is for a `Config` known to be fresh (tests, one-shot tools).
- `migrateConfigSchema` runs self-extinguishing fixups on every `Load`.
- `wt config` launches even when `config.toml` fails validation, so the editor can repair it; other paths exit early.

Read [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md) before changing config load/save, migrations, the `[litellm]` table, registry loading, catalog membership, or location validation. `wt config` subcommands: [docs/wt-config.md](docs/wt-config.md).

## LiteLLM routing state (wt-owned)

The `[litellm]` table (`enabled`/`url`/`api_key`) in wt's `config.toml` decides whether non-native models route through the proxy (`Config.IsLitellm()`) or dial providers directly (`Config.IsDirect()`). It is set with `wt litellm status|on|off|set` and read from nowhere else. Toggling is routing policy only — it never touches the proxy, which reads `config.yaml` only at startup ([docs/wt-agents/README.md#litellm-proxy-lifecycle](docs/wt-agents/README.md#litellm-proxy-lifecycle)). Details: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#litellm-routing-state-wt-owned).

## Registry

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. wt loads it via `config.Load`, fail-closed, and joins it in memory with `config.toml`. `config.Load` never writes it; wt writes it through `config.UpdateRegistry` alone: `wt model init` (provider rows, `config.SeedRegistryDefaults`), `wt model add|edit|rm` and the Models tab of `wt config` (model rows, both through `internal/modeladmin`), and `wt cloud-sync` (cloud prices and their `pricing_updated_at` stamps, and the ollama cloud entries it mirrors from ollama.com/pricing — added, re-priced and removed; `internal/cloudsync`'s `Apply` methods). Path precedence: `WT_REGISTRY` > `MODELMAN_REGISTRY` (the older name, kept as an alias) > `XDG_CONFIG_HOME` > `~/.config`. llmbench's `registry_path` uses the same order — keep the two in sync; each has a precedence test.

- **`ResolveLocation` is the one judge of a location**: every consumer keys off its error (`config.ErrLocation`), so catalog, inventory, sync and validation agree.
- **What is on disk and what is running come from live probes.** wt reads no per-model `[model_state]` key; whether a model is routed is `wt litellm list`.
- **`config.UpdateRegistry` is the only registry write path**: flock, symlink write-through, touched-row validation, no-op skip, re-check before rename. Its `apply` must be pure (it may run up to three times). `RegistryDoc`'s operations are patch-shaped — never round-trip a `config.Model` into the file.
- **An id on more than one model row is refused, never resolved to the first row** (`config.ErrModelAmbiguous`, from `RegistryDoc.modelRow`): `wt model edit` and `wt model rm` run on a registry that fails validation, and a duplicated id is one such registry. Find a row for a write through `RegistryDoc.Model`/`PatchModel`/`RemoveModel`, not a loop of your own.
- **A malformed `fetch` or `draft` reads as absent and never fails the load** (`ModelArtifact.UnmarshalTOML`); `wt model list` says so (a stderr line per row and `malformed` in `--json`, from `Model.Malformed()`), the Models tab says so for the selected row (`Row.Malformed`), and the writer's touched-row check names it. `Model.Malformed()` also names a `cost.time_prices` row the validator refuses (`TimePrice.Problem`: an unknown timezone, a time that is not `HH:MM`, a window whose start is its end, a negative price); such a row loads, `ModelCost.PriceAt` never applies it, and a model with no other row is not `TimePriced`. Keep `Problem` asking `validateTimePrice` itself, so the reader and the writer cannot come to disagree about a rule. One rule has two homes and they change together: a window whose `end` is before its `start` runs past midnight, which `validateWindow` accepts and `TimePrice.holds` reads by wall clock (at or after `start` on a listed day, or before `end` on the day after one; never start plus a number of hours, which is wrong on the two nights a year a zone's clock jumps: `TestPriceAtAWindowPastMidnightFollowsDaylightSavingTime`). A value of the wrong TOML type in a row still fails the load (`TestLoadStopsOnATimePricesValueOfTheWrongType`). Get a zone through `config.loadZone` (read from disk once per run), not `time.LoadLocation`: both callers run for every row while a picker is built.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`, loaded by wt's Go tests (and, for the two `registry*.toml` fixtures, llmbench's).
- A missing registry lets an *unconfigured* agent launch as a native passthrough (`agents.BuildPassthroughCmd`); a configured one fails on model resolution.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug** — e.g. models referencing `provider_id`s with `providers = []`. Fix with `wt model init` on that machine (it adds a default row for each provider that is installed, used by a model or listed by a configured agent, and a native row per configured agent; a provider it has no default row for is named in its output and is a hand edit of `registry.toml`), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/` and `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither variable, so a fixture that redirects the registry also names config.yaml (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load`/`Validate` fails with `unknown provider "agy"`.

Full rules (catalog membership, passthrough, decoded fields): [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry).

Adding a provider: the `adding-a-provider` skill (`.claude/skills/adding-a-provider/SKILL.md`).

## Local-model resolution

Every model row wt shows or resolves — the TUI picker, `wt start`/`wt smoke`'s picker, the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot. A row's action is **launch** (cloud, or a local model the probe reports running and loaded), **start** (a local row of ollama/omlx/omlx-6bit/mtplx that is not running, or that omlx is still loading), or **block**.

- Local rows come only from the live inventory, which probes servers and disk and reads no stored flag.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** — omlx's `/v1/models` lists its whole pool, loaded or not (#201).
- A `-M` pin is looked up among all rows, discovered ones included (`catalog.Find`); no row → `catalog.MissingReason`.
- Without `-M`, the non-TUI path picks only among launch rows; only a pin may start a model.

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

The start/stop engine: `Start` (`Options.Out` takes every line a start prints, the proxy restart's included, in place of stderr; the model picker passes one), `Stop`, `StopModelDeferred` + `SettleRoutes`, `LoadingServer` + `StopLoading`; backends ollama (`Shared`), omlx (`Pool`: loads beside, `/unload` per model), mtplx (`Exclusive`). Used by the TUI start flow, `startForLaunch`, `wt start`, and `wt smoke`.

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`, `Occupants` lists them): an `Exclusive` occupant (mtplx) to be replaced, or the `Pool` victims (omlx) the eviction plan (`evictions.go`) predicts omlx will unload (`poolAdmissionMarginPct` margin, 15% of the ceiling; `reconcilePool` handles what omlx unloaded anyway, on failure too); undeterminable occupancy is `*OccupancyUnknownError`, never assumed empty.
- **Route hook** (`routes.go`): a start/stop wt performs updates `config.yaml` through `internal/litellm`. A launch of an already-running model gets the Add-only launch-time check, `lifecycle.EnsureModelRoute` (#192), which never fails a launch.
- **Call `WaitPendingRoutes()` on every process exit and before handing a model to an agent**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async.
- **The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose** — callers cancel as soon as the hook returns. Pinned by `TestRouteRestartSurvivesCallerCancelAfterReturn`.
- Route output goes through `routePrintf(ctx, …)`; a caller-supplied writer rides on the context.
- **wt signals a process only after identifying it** (`mtplx_loading.go`): the one stop with no port to go through is an mtplx still loading, found by wt's pidfile. A pid is never enough — alive, the user's own, an mtplx server on the provider's port by exact argv, and the recorded start time when there is one, checked again before SIGKILL. A process that is exiting (`kern.procargs2` gives `EINVAL` while the kernel frees its memory) has no argv: it is never identified, never signalled, and a stop waits for it instead of killing it. The pidfile stays a bare pid (llmbench shares the path); wt's start record is `<pidfile>.wt`; both are read only as the user's own regular files (`readOwnFile`). Ctrl+C after a signal was sent is a `*StopInterruptedError`, which `wt stop` reports as "was sent SIGTERM", never "was not stopped".

Read [docs/internals/local-models.md](docs/internals/local-models.md#lifecycle-internallifecycle) before changing start/stop, the route hook, the launch-time route check, restart or readiness waits, or replace.

## Rotation (Go)

Each successful launch records one model id in `~/.config/agent-wt/rotation.state`; the picker cursor lands on the model after it (`FirstAfter`). `rotation.RecordFor(agent, id)` also appends the usage event to `usage.jsonl`.

- **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks.
- The last-launched marker is plain ASCII `> ` (Unicode shapes misalign CJK terminals).

Columns, sort rules and the pinned tests: [docs/internals/local-models.md](docs/internals/local-models.md#rotation-go). Debug helper: `go run ./cmd/wt rotate code`.

## Agents (Go)

Each agent registers a `Driver`. `BuildLaunchCmd(agent, m, worktreePath, yolo, cfg, extraArgs)` is the shared constructor for both launch paths: it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build` — every driver goes through it.

- **Protocols.** Agents declare wire protocols (claude `anthropic`; codex `openai-responses`; copilot, opencode, pi `openai-chat`; agy, shell none); providers declare what they serve. An empty intersection forces LiteLLM regardless of the toggle — so **codex always routes through LiteLLM**.
- **Model id contract** (`ResolveRoute`): litellm/forced routes use the registry id (`m.ID`); direct routes use the provider-side name (`m.ModelName`).
- **copilot uses the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters.
- agy is a native passthrough but not a command agent: `-A agy` without `-M` errors when more than one agy model is eligible.

Read [docs/internals/agents.md](docs/internals/agents.md) before changing a driver, `Route`, the optional capabilities (`Seeder`, `Syncer`, `ArgSetter`, `OneShotRunner`, `StateDirer`), or the pre-launch `ollamacheck.Check`. Per-agent env/args: [docs/wt-agents/](docs/wt-agents/). New driver: the `adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml`, resolved per agent × model (`profiles.Resolve`) and applied on both launch paths (`applyProfileForLaunch`) and by `wt smoke` (`applyResolvedProfile`). Operator reference: [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- Call the `config_content` cleanup explicitly after the run — `runAgentCmd`'s `os.Exit` skips defers.
- Apply order is env/args → `config_content` → wrapper; load/validate/apply errors degrade to an unprofiled launch with a warning.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing apply order, confirmation, `SelfHeal`, or `wt profile`.

## Smoke test (`wt smoke`)

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model, using the same command construction as a real launch (`agents.BuildLaunchCmdInfo`), and reports PASS/FAIL/SKIP. Behavior: [docs/wt-smoke.md](docs/wt-smoke.md).

- Each row runs in its own temporary git repository unless `--cwd` is passed; agents run with permission checks off (yolo forced on for every agent except codex), and the directory is not a sandbox (#193).
- A model fallback is FAIL; the default prompt never contains its sentinel verbatim.
- Timeout defaults are location-based: 180s cloud, 900s local.

Read [docs/internals/smoke.md](docs/internals/smoke.md) before changing row directories, yolo handling, process-group cleanup, env stripping (`GIT_*`, `PWD`), Ctrl+C, or timeouts — and when diagnosing a slow local row.

## Start/stop (`wt start`, `wt stop`)

`cmd/wt/model_cmds.go`; user-facing behavior in [docs/wt-start-stop.md](docs/wt-start-stop.md); code notes in [docs/internals/local-models.md](docs/internals/local-models.md#startstop-wt-start-wt-stop).

## Cloud sync (`wt cloud-sync`)

`internal/cloudsync` holds everything that decides what a sync changes, with no I/O and no clock. The command (`cmd/wt/cloudsync*.go`) owns the fetches, the ollama CLI, the confirmation, the one registry write, the route sync and the exit codes. What the command does for a user — the two flows, the order of a run, every flag, exit code and message, failure and recovery — is [docs/wt-cloud-sync.md](docs/wt-cloud-sync.md); the operator's procedure, including the parser repair after an exit 3, is the `cloud-sync` skill (`.claude/skills/cloud-sync/`). **Change the page and the skill with the behavior**: both quote the command's messages.

- **A flow whose provider the registry does not use is skipped, never an error, however it was asked for**: one line on stdout, nothing fetched, exit 0. openrouter: no OpenRouter-priced model (`cloudsync.OpenRouterPriced`, the same rule as `config.Config.OpenRouterPriced`: a model whose `provider_id` is `openrouter`, and no other — the models, not an `openrouter` row; the provider key `openrouter_priced` was removed in #322 and is ignored where a registry still has it). ollama: no `ollama` provider row, `--only ollama` and the ollama flow's flags included. With neither, nothing is asked, locked, written or synced (`TestCloudSyncWithNeitherProviderDoesNothing`, `TestCloudSyncSkipsOllamaHoweverItIsAskedFor`). `wt cloud-sync` never seeds a provider row.
- **`ParsePricing` is the only code that knows the pricing page's HTML.** Every way the page can stop looking like a price table is a `*ParseError`, never a short catalog: a catalog that lost its rows would plan the removal of every ollama cloud entry. When ollama changes the page, fix it there and replace `internal/cloudsync/testdata/ollama_pricing.html`.
- **The plan's text is the plan.** `CatalogPlan.Format` and `PricePlan.Format` are what the user approves and what the command compares with the re-plan made under the registry lock; both planners are deterministic. A change to either `Format` changes what counts as "the same plan".
- **`RemovalDigest` is a fixed format** (first 12 hex digits of SHA-256 over the sorted removals, then the sorted `rm <tag>` lines; the vectors are pinned in `internal/cloudsync/catalog_test.go`), so a digest printed by one run approves the same plan in the next.
- **The `Apply` methods are pure and write a diff**: `config.UpdateRegistry` may run them up to three times, and `costPatch` sets or deletes only the cost keys the plan changed, so nothing is written that the printed plan does not show (`TestApplyWritesOnlyWhatThePlanShows`). Of the `cost.time_prices` rows, the `off-peak` one is the ollama flow's (`OffpeakLabel`, on ollama cloud entries) and the `openrouter` ones are the openrouter flow's (`OpenRouterLabel`, `withOpenRouterRows` in `timeofday_rows.go`: one row per level of a schedule other than the flat price, on models of the `openrouter` provider); every other row, the subscription and any key wt does not model are kept. No row is ever in both flows' scope (`cloudsync.OpenRouterPriced` is `provider_id == "openrouter"`, the ollama planner addresses only `provider_id == "ollama"`; `TestTheTwoFlowsNeverShareAModel`), which is what lets the one shared write apply two plans made from the same reading of the registry. Keep it true when either rule changes.
- **A model OpenRouter prices by time of day is planned from its schedule, never from its top-level price or the clock** (#322). The top-level `prompt`/`completion`/`input_cache_read` of such a model are those of the window in force when the list was fetched. `parseSchedule` (`internal/cloudsync/timeofday.go`) reads the `pricing.overrides` entries that have a `utc_` key into price levels over the UTC week, by painting each entry onto the week's 10,080 minutes in list order (a later entry wins, a minute left unpainted makes the schedule unusable); `mainRate` is the one place that says which level becomes the flat price (the dearest: highest output price, then input, then cached input), and the others become the `openrouter` rows. An entry with no `utc_` key (a `min_prompt_tokens` tier) is ignored. A schedule that cannot be read with certainty is a warning that leaves the model alone and unstamped, never a fall back to the top-level price; so is an `overrides` value that is not a list, or an entry of it that is not an object, because neither can be said to hold no window. `TestPlanPricesDoesNotFollowTheClock` holds the plan to one text across a week of fetch times for one published schedule; a planner that read `time.Now` would also fail the compare under the lock. A schedule that appears or goes between two syncs is a real update (`TestPlanPricesReportsAScheduleThatComesAndGoes`).
- **The flow's rows are written one day at a time and moved as little as possible** (`timeofday_rows.go`). `rateRow` reads a level back from the painted week, so the bytes do not depend on how OpenRouter orders, groups or wraps its entries; a level that runs past midnight is two windows (to `24:00`, from `00:00`), though the registry would accept one. `withOpenRouterRows` returns the list it was given when the flow's rows already are the schedule's, wherever they stand, and otherwise replaces them where the first stood (rows resolve first match wins, so a user's row is never moved past one of the flow's on a plan line that reads the same on both sides). Rows are compared whole, so `formatOpenRouterRow` prints whatever can make a row differ (`timezone="…"`, `+keys`, `?`): keep the two in step, or an update becomes invisible. A rows-only update is a price update (listed, counted, routes synced), and a route's `model_info` is priced from the flat price only (`TestPricingInfoReadsOnlyTheFlatPrices`), so that sync rewrites no route and restarts nothing (`TestCloudSyncRowsOnlyUpdateLeavesTheRoutesAlone`). The whole chain, from OpenRouter's list to the price the picker resolves at every minute of the week, is `TestThePickerShowsThePriceOpenRouterChargesNow` (`cmd/wt/cloudsync_picker_test.go`), whose expectation is painted by the test itself from OpenRouter's rules: do not make it share code with `parseSchedule`.
- **Exit codes 2 to 5 are the ollama flow's and win over 1** (`cloudSyncOutcome.err`); each means "the ollama flow changed nothing". A new stop gets one of them only when it fits that code's documented meaning; otherwise it is exit 1, like the duplicated-id refusal, no terminal, and a refused write. An ollama-flow flag with `--only openrouter` is a usage error: ignored in silence, `--force` or a digest would be a gate the user believes was passed.
- **One registry write, both flows** (`writeCloudSync`), except on `config.ErrRegistryInvalid` with both pending, when it is called once per flow so a row that would not load holds up only the flow that owns it (`TestCloudSyncWritesEachFlowAloneWhenARowWouldNotLoad`).
- **A plan that changed under the lock stops only its own flow** (openrouter: exit 1; ollama: exit 5, with `cloudSyncOutcome.ollamaWhy` as the reason). An ollama plan is also stale when the ollama provider row is gone, whatever it prints; that, with an openrouter plan always reading differently from an empty file, is why a registry removed after the plan is never created (`TestCloudSyncRefusedPlansDoNotCreateARegistry`). Keep it true when a flow is added.
- **The route sync runs when a price or the model set changed, or ollama had work** (`CatalogApplied.Pulls`/`Removes`): a run that finishes an interrupted one changes no registry row and must still sync (`TestCloudSyncFinishesAnInterruptedRun`). A stamp-only run never syncs.
- **A registry entry goes in the registry write; its tag goes afterwards**, and only when it is a cloud tag, `ollama list` showed it and no remaining ollama entry names it (`CatalogApplied.Removes`).
- **Every ollama command is pinned to `localmodels.FamilyOrigin(cfg, "ollama")`**, whatever `OLLAMA_HOST` the shell exports. It is the one derivation of that address; add no other. A `base_url` that gives no `http(s)://host` stops the ollama flow before any fetch (exit 2), because ollama reads an unusable `OLLAMA_HOST` as its default daemon (`TestCloudSyncOllamaRefusesABaseURLThatNamesNoDaemon`). The pre-launch `ollamacheck.Check` pins its `ollama list` to the same address and, for such a `base_url` or a registry with no ollama row, reports an error without running anything (`TestCheckCannotTellWithoutADaemonAddress`).
- **Every line of a run keeps its flow prefix**, an ollama failure included: `ollamaFailure` reduces ollama's stderr (progress, cursor escapes) to its last line that says anything, and a failed pull or rm gets a second line saying what is left and what to run next.

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`, the sole implementation of `config.yaml` route management. Subcommands `sync|list|providers|status|on|off|set`; `sync` is the only CLI route write. Operator doc: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned). JSON shapes are pinned by [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json).

- **wt owns only its own rows**: each carries `model_info.wt_managed: true`. A hand-written row keeps everything except an empty ollama `api_base`, which every write fills (#202).
- **`sync` trusts only `ok` families**: a family whose probe is untrustworthy is frozen with a warning; one that refuses the connection has its local routes removed.
- **`sync` runs on a registry with a duplicated model id, so it never takes "the first row with the id"**: an inventory entry is paired with the row of its own provider (`registryRowOf`), and an id with more than one row to route — or one row to route beside a local row a failed probe froze — is left as it is, with a warning (`planSync`'s `ambiguous`).
- **An `os.environ/VAR` api_base stays as written**; "set for the proxy" comes from `litellm.ProxyEnv` (the LaunchAgent plist), which `os.Getenv` cannot answer.
- `status` shows the api key only as `api_key_set` / last 4 chars.
- Path `WT_LITELLM_CONFIG`, default `~/.config/litellm/config.yaml`; restart `WT_LITELLM_RESTART_CMD`, else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. A redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`).
- The spend database is `litellm.DatabaseURL()`: `WT_LITELLM_DATABASE_URL`, legacy `MODELMAN_LITELLM_DATABASE_URL`, then `general_settings.database_url` in `config.yaml`. An `os.environ/NAME` value — and `DATABASE_URL`, when `config.yaml` names no `database_url` — is looked up in wt's own environment and then in the proxy's (`ProxyEnv.Lookup` over `LoadProxyEnv`, the LaunchAgent plist); wt's own wins, and a blank value is unset everywhere. Read-only, used only by `wt stats`; under a redirected registry with `config.yaml` unnamed it answers `ErrNoDatabase` rather than read the default file or the plist. Its errors name variables and files, never a value; a `spend.ErrUnreachable` names the host and port `psql` tried and nothing else from the connection string.

Read [docs/internals/litellm-routes.md](docs/internals/litellm-routes.md) before changing sync's desired set, row ownership or adoption, the `api_base` repair, ollama-serve warnings, or `config.yaml` I/O.

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Sessions (Go)

wt launches a fresh agent every time and leaves session handling to the agent (#198, #204): `BuildLaunchCmd` appends passthrough args unchanged, so continuing a conversation is the agent's own flags after `--` (`claude-wt -- --continue`, `opencode-wt -- --session <id>`). The removed session lookup and why it stays removed: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#sessions-go).

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → launch, plus starting, routing, replace-confirm and ollama-warning screens. A picker is skipped when its selection is already resolved. Every picker uses `ThemedListDelegate`.

- **A view fits the terminal** (`internal/tuilayout`, shared with `wt config`; `layout.go` holds the launcher's frames): every list screen renders through a `listFrame`, sized only by `fitTo` in `Update`. Add a new line to the frame, and size lists through `fitTo`. Pinned by `TestEveryListPhaseFitsTheTerminal`. A status that ends in what to do is wrapped, not clipped (the agent picker's, `agentFrames`, and the model picker's, `modelFrames`): a clip cuts it before the command or the reason. A model-picker status too tall to share the terminal with the table has the screen until the next key (`statusAlone`).
- **The update goroutine never waits**: the launch-time route check uses the non-blocking `tryEnsureModelRoute`; waits happen in a command behind `phaseRouting`, and the start flow waits for the proxy restart on the start's own goroutine (`runStart`), before it reports.
- **A start prints into the flow, not onto the screen** (#275): `runStart` passes `lifecycle.Options.Out` (a `startOutput`), and `finishStart` puts the engine's lines on the status and in `pendingRouteNotes`. From the routing stage on a start cannot be cancelled (`pastCancel`), and a row the picker just started gets the launch-time route check like any other.
- **A model table is priced once, when it is built** (`modelrows.go`): `buildRows` reads one clock (`pickerNow`, or `tableInput.now`) and `tableRow.pricedAt` resolves each row's price in force (`config.ModelCost.PriceAt` over its `cost.time_prices` rows). The cost sort, the COST cell, the `~` mark and the mode line's price read `tableRow.price()` / `timePriced()`, never `Model.Cost`'s flat fields and never a clock, so an open picker's rows do not move at a window boundary. The rows are the ollama flow's `off-peak` one, the openrouter flow's `openrouter` ones (one per level of a schedule other than the dearest, which is the flat price) and any written by hand. The mark and the heading `COST (~ varies by time)` cost width (one usage column at one list width each; the COST column at three), and the mode line's price is whole or absent (`modeLine`): read the two notes in [docs/internals/tui.md](docs/internals/tui.md) before changing either. LiteLLM's route carries the flat price at every hour.
- **The model table drops whole columns on a narrow terminal** (`tuilayout.Columns`, fitted by `tuilayout.FitTo`), keeping MODEL, STATUS and RUNNING.
- A test that sets state by hand goes through `Update` before reading `View()`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`, `wt rotate`) skip the TUI. `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted.

Read [docs/internals/tui.md](docs/internals/tui.md) before changing layout or sizing, start-on-select (`start_flow.go`), the launch-time route check (`checkLaunchRoute`, `phaseRouting`), or the model table's columns.

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate` → worktrees / local branches / remote-only branches) and creation (`EnsureForName` for `-W`, `EnsureForBranch` for the picker). Every function takes `dir` (repo root) first for testability.

> **`Enumerate` fetches before listing.** It runs `git fetch --all --prune` (5s timeout, every remote) before building the picker's groups. Failure — offline, no remotes, timeout — is silently ignored; the picker falls back to existing local refs. `-W`/`--cwd` skip `Enumerate` entirely, and `EnsureForName` never consults remotes.

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and directories inside `.git` have no worktree, so `--show-toplevel` fails there; `--git-dir` succeeds. Don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it, and the picker skips bare default-branch rows.

## Verification commands

Run the Go test block above before any commit, then exercise the installed binary:

```bash
wt                                   # interactive TUI (needs TTY)
wt -W my-feature -A claude           # named worktree + launch
wt --cwd -A codex                    # current directory
claude-wt --cwd                      # shim forwards to wt
wt --init                            # seed agent instruction files
wt start [<id>] / wt stop [<id>|<provider>|--all]   # local-model lifecycle (routes follow automatically)
wt served <provider> [--json]        # ids an omlx/mtplx/mlx_lm_server server is serving now
wt warm omlx <model>                 # load a model into a running omlx (keyed warmup; llmbench's omlx backend calls it)
wt litellm list / sync / status      # routed ids, reconcile cloud + running local routes, routing state
wt model init [--json]               # create registry.toml if missing; add default provider rows (safe to re-run)
wt model                             # `wt config` on its Models tab (needs TTY): n add, enter edit/register, d remove
wt model list [--json]               # every registry model and every local model found, with live status
wt model add <provider> <name> --family F   # register a model (seeds a missing default provider row; one route sync)
wt model add mlx_lm_server <target> --draft <draft> --family F   # register a target+draft pairing; wt cannot start one and prints the llmbench command that does
wt model edit <id> --tags code       # change family, tags, location or prices; nothing else in the row moves
wt model rm <id> [--yes]             # registry only; prints where the weights are
wt cloud-sync --dry-run              # plan a refresh of OpenRouter prices and the ollama cloud catalog; changes nothing (fetches public pages, runs `ollama list`)
wt profile show -A <agent> -M <id>   # dry-run profile resolution
wt stats [--window 7d] [--family F] [--json]  # survey table, then launches and LiteLLM spend per model
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix in both routing modes
                                     # (flips routing via `wt litellm off|on`, snapshotting and restoring wt's config.toml; --modes current for one pass)
```

> Verifying a branch's behavior against live data requires building it first (`go build -o /tmp/wt-verify ./cmd/wt`) — `~/.local/bin/wt` is whatever was last `make install`ed and may predate the branch.
