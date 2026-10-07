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
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md` — command references
- `docs/superpowers/specs/`, `docs/superpowers/plans/` — wt design specs and plans (newer cross-package specs live in the monorepo's [../docs/superpowers/](../docs/superpowers/))
- `../CLAUDE.md` — monorepo-wide commands, benchmark isolation helpers, shared config ownership (modelman owns `registry.toml`/`modelman.toml`; wt owns `~/.config/agent-wt/config.toml`)

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

- **Test seams** are package-level vars: production code calls the var, tests swap it. A new seam is a `var x = realX` plus a `realX` function. `internal/lifecycle` instead keeps every seam in one `env` struct (`defaultEnv()` / `testEnv()`).
- **Tests stay off the developer's machine.** The `TestMain`s of `cmd/wt`, `internal/tui`, `internal/litellm` and `internal/survey` point `XDG_CONFIG_HOME` at a throwaway directory, stub the inventory probe, hard-fail model starts, and no-op the route check. `cmd/wt`'s also replaces `querySpend`, and `internal/spend`'s fails its `lookPath`/`runPsql` seams, so no test runs `psql` or reaches the LiteLLM database. A new package whose tests reach `config.Dir()` needs the same setup; a new launch path calls the route check through `stubEnsureRoute(t)`'s seam.
- **Assert on unexported functions directly** (e.g. `buildStatsRows`); parsing rendered lipgloss output flakes under forced-color ANSI.

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, starts a model, or touches routes — it lists every seam and what each `TestMain` stubs.

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go test ./internal/agents -run TestOpenCodeOllamaPrefix -v   # one test
go vet ./...                         # static analysis
make check                           # shellcheck + shfmt check + go-format-check (gofmt -l gate wt-ci runs); `make format` writes both
```

From the monorepo root, `make test-all` runs the CI-equivalent sweep (root lint + llmbench + modelman + wt build/vet/test).

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,spend,smoke}`.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config and profiles.toml once) |
| `cmd/wt/commands.go` | `rotate` subcommand (debug helper) |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch from live `catalog` rows; a `-M` pin on a start row starts it |
| `cmd/wt/start.go` | `startForLaunch` — non-TUI start driver: stderr progress, Ctrl+C cancel, replace confirmation, `allowReplace` |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers; TTY seams and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `launchFiltered` (`launchFilteredImpl`), `launchPassthroughImpl`, `runAgentCmd`; profile apply (`applyProfileForLaunch`, `applyResolvedProfile`) |
| `cmd/wt/stats.go` | `wt stats` — read-only report: the survey table over `survey.jsonl`, then the usage table |
| `cmd/wt/stats_usage.go` | `wt stats`' usage half: `collectUsage` (launches from `usage.jsonl` joined with LiteLLM spend), `buildUsageRows`, the `querySpend` and `stdoutWidth` seams |
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the borderless, width-aware usage table (an id is never truncated) |
| `cmd/wt/model_cmds.go` | `wt start` / `wt stop` |
| `cmd/wt/smoke.go` | `wt smoke` — one-shot model×agent smoke test |
| `cmd/wt/profile.go` | `wt profile list/show/status/on/off`; `setEnabledLine`'s surgical `enabled = ...` edit |
| `cmd/wt/litellm.go` | `wt litellm ...` |
| `internal/config/` | config load/validate/save (agents + joined registry catalog), route resolution (`ResolveRoute`), migrations |
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection |
| `internal/usage/` | append-only JSONL launch history (1d/7d/30d); `RecordFor` tags the agent; `CountsForAgent` per agent×model, legacy agent-less lines count toward `Counts` only; `AllCounts(agent)` enumerates every model in the file (for `wt stats`) |
| `internal/refcount/` | live-session "in use" counts: JSONL keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point |
| `internal/survey/` | post-session survey + stats; stop picker and stop loop (`Picker`, `PickerWith`, `StopCandidates`, `StopEntries`) |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, capabilities), picker catalog, drivers, `RunAndCleanup` (shared apply-run-cleanup core for both launch paths), `BuildPassthroughCmd` |
| `internal/profiles/` | launch-profile overlays — see [Profiles](#profiles-internalprofiles) |
| `internal/smoke/` | `wt smoke`'s core: `Eligibility`/`Candidates` (rows per agent from one inventory snapshot — call `Eligibility` directly when you need both the model union and per-model agent lists, to avoid two probe rounds), `RunRow` (PASS/FAIL/SKIP via the `buildAndRun` seam) |
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build` (local rows only from the inventory), `Find` (includes discovered rows), `MissingReason` (why a pinned registry id has no row), `Row.Action` (launch/start/block), `Row.BlockReason`, presence status (`ok`/`unknown`; `new` for discovered, including discovered models handed in via `Input.Models`). No rendering, counts or sorting |
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/lifecycle/` | local-model start/stop engine — see [Lifecycle](#lifecycle-internallifecycle) |
| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management (replaced modelman's Python writer): `configfile.go`, `entry.go`/`policy.go` (entries, provider mappings), `service.go` (`ApplyChange` — the targeted route writer the lifecycle hook uses, its unit `Change` with `RemoveFamilies`; `DiscoveredModel`, `RowFamily`; `Sync`/`PlanSync`), `restart.go` (`RestartContext`, `Listening`, `WaitReady`), `dburl.go` (`DatabaseURL` — the spend database's connection string; reads, never writes) |
| `internal/spend/` | per-model request, token and cost totals from the proxy's `"LiteLLM_SpendLogs"` table: one aggregated query through `psql` (`Query`), typed failures (`ErrNoConnectionString` for a blank string, refused before `psql` is looked for; `ErrNoPsql`, `ErrUnreachable`, `ErrQuery`), no Postgres driver |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection, enumeration, creation |
| `internal/initseed/` | `--init` seeding |
| `internal/ollamacheck/` | pre-launch `ollama list` availability check |
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell, pickers, launch, start-on-select flow (`start_flow.go`); `modelrows.go` (rows + sort), `modeltable.go` (`buildTable`, `renderTable`); `PickStartModel` — standalone picker without launch-route gating, used by `wt start` and `wt smoke` (`PickModel`, the route-gated variant, currently has no production caller) |

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry; wt never writes them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields). `Save(cfg)` is for a `Config` known to be fresh (tests, one-shot tools).
- `migrateConfigSchema` runs self-extinguishing fixups on every `Load`.
- `wt config` launches even when `config.toml` fails validation, so the editor can repair it; other paths exit early.

Read [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md) before changing config load/save, migrations, the `[litellm]` table, registry loading, catalog membership, or location validation. `wt config` subcommands: [docs/wt-config.md](docs/wt-config.md).

## LiteLLM routing state (wt-owned)

The `[litellm]` table (`enabled`/`url`/`api_key`) in wt's `config.toml` decides whether non-native models route through the proxy (`Config.IsLitellm()`) or dial providers directly (`Config.IsDirect()`). `modelman.toml`'s `[litellm]` is a legacy read-only fallback, copied in once. Toggling is routing policy only — it never touches the proxy, which reads `config.yaml` only at startup ([docs/wt-agents/README.md#litellm-proxy-lifecycle](docs/wt-agents/README.md#litellm-proxy-lifecycle)). Details: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#litellm-routing-state-wt-owned).

## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. wt loads it read-only via `config.Load`, fail-closed, and joins it in memory with `config.toml`. Path precedence matches modelman's: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` — keep the two in sync.

- **`ResolveLocation` is the one judge of a location**: every consumer keys off its error (`config.ErrLocation`), so catalog, inventory, sync and validation agree.
- **What is on disk and what is running come from live probes.** wt reads no per-model `[model_state]` key; whether a model is routed is `wt litellm list`.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`, loaded by `internal/config` tests and modelman's `tests/contracts/` — a schema change updates both sides.
- A missing registry lets an *unconfigured* agent launch as a native passthrough (`agents.BuildPassthroughCmd`); a configured one fails on model resolution.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug** — e.g. models referencing `provider_id`s with `providers = []`. Fix with `modelman sync`/`modelman migrate` on that machine (`sync` recreates default provider entries), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/` and `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither variable, so a fixture that redirects the registry also names config.yaml (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load`/`Validate` fails with `unknown provider "agy"`.

Full rules (catalog membership, passthrough, decoded fields): [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry-modelman-owned).

## Local-model resolution

Every model row wt shows or resolves — the TUI picker, `wt start`/`wt smoke`'s picker, the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot. A row's action is **launch** (cloud, or a local model the probe reports running and loaded), **start** (a local row of ollama/omlx/omlx-6bit/mtplx that is not running, or that omlx is still loading), or **block**.

- Local rows come only from the live inventory, which probes servers and disk and ignores modelman's `running` flag.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** — omlx's `/v1/models` lists its whole pool, loaded or not (#201).
- A `-M` pin is looked up among all rows, discovered ones included (`catalog.Find`); no row → `catalog.MissingReason`.
- Without `-M`, the non-TUI path picks only among launch rows; only a pin may start a model.

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

Go port of modelman's start/warmup: `Start`, `Stop`, `StopModelDeferred` + `SettleRoutes`, `Evictions`; backends ollama (`Shared`), omlx (`Pool`: loads beside, `/unload` per model), mtplx (`Exclusive`). Writes nothing to modelman state. Used by the TUI start flow, `startForLaunch`, `wt start`, and `wt smoke`.

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`, `Occupants` lists them): an `Exclusive` occupant (mtplx) to be replaced, or the `Pool` victims (omlx) `Evictions` predicts omlx will unload (`poolAdmissionMarginPct` margin, 15% of the ceiling; `reconcilePool` handles what omlx unloaded anyway, on failure too); undeterminable occupancy is `*OccupancyUnknownError`, never assumed empty.
- **Route hook** (`routes.go`): a start/stop wt performs updates `config.yaml` through `internal/litellm`. A launch of an already-running model gets the Add-only launch-time check, `lifecycle.EnsureModelRoute` (#192), which never fails a launch.
- **Call `WaitPendingRoutes()` on every process exit and before handing a model to an agent**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async.
- **The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose** — callers cancel as soon as the hook returns. Pinned by `TestRouteRestartSurvivesCallerCancelAfterReturn`.
- Route output goes through `routePrintf(ctx, …)`; a caller-supplied writer rides on the context.

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

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`, the sole implementation of `config.yaml` route management. Subcommands `sync|list|providers|status|on|off|set`; `sync` is the only CLI route write. Operator doc: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned). JSON shapes are pinned by [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json) (modelman parses them).

- **wt owns only its own rows**: each carries `model_info.wt_managed: true`. A hand-written row keeps everything except an empty ollama `api_base`, which every write fills (#202).
- **`sync` trusts only `ok` families**: a family whose probe is untrustworthy is frozen with a warning; one that refuses the connection has its local routes removed.
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

- **A view fits the terminal** (`layout.go`): every list screen renders through a `listFrame`, sized only by `fitTo` in `Update`. Add a new line to the frame, and size lists through `fitTo`. Pinned by `TestEveryListPhaseFitsTheTerminal`.
- **The update goroutine never waits**: the launch-time route check uses the non-blocking `tryEnsureModelRoute`; waits happen in a command behind `phaseRouting`.
- **The model table drops whole columns on a narrow terminal** (`tableColumns`, `fitTableColumns`), keeping MODEL, STATUS and RUNNING.
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
wt start [<id>] / wt stop [<id>|<provider>]   # local-model lifecycle (routes follow automatically)
wt served <provider> [--json]        # ids an omlx/mtplx/mlx_lm_server server is serving now
wt start <id> --plan --json          # dry run: what a start would unload (status running|fits|would_unload|unknown); changes nothing
wt warm omlx <model>                 # load a model into a running omlx (keyed warmup; modelman's fallback)
wt litellm list / sync / status      # routed ids, reconcile cloud + running local routes, routing state
wt profile show -A <agent> -M <id>   # dry-run profile resolution
wt stats [--window 7d] [--family F]  # survey table, then launches and LiteLLM spend per model
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix in both routing modes
                                     # (flips routing via `wt litellm off|on`, snapshotting and restoring wt's config.toml; --modes current for one pass)
```

> Verifying a branch's behavior against live data requires building it first (`go build -o /tmp/wt-verify ./cmd/wt`) — `~/.local/bin/wt` is whatever was last `make install`ed and may predate the branch.
