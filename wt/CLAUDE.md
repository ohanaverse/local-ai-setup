# CLAUDE.md

Rules and gotchas live here; mechanisms, function lists and step-by-step detail live in [docs/internals/](docs/internals/) (each section names its page; [packages.md](docs/internals/packages.md) lists every file and package).

## What this repo is

The `wt` binary (`cmd/wt/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--cwd` (the current directory; no git repository needed), `-W <name>` (named worktree), or the worktree picker. Outside a repository `-W` refuses (`not in a git repo`).
2. **What** — `-A <name>`: an LLM agent, or `shell` for a plain command. **Never defaulted**: omitting `-A` always shows the agent/command picker.
3. **Which model** — `-M <provider>/<name>`, filtered by `-T <tags>` / `-F <family>`, or picked (eligible models rotate on successive launches).

`bin/*-wt` are shims that forward to `wt` (`claude-wt` → `wt --agent claude`), so `wt` must be on `$PATH`. All logic lives in Go.

## Installation

`make install` builds `bin/wt` and copies it and the shims to `~/.local/bin/` (Go 1.26.7, see `go.mod`; `make help` lists targets). `make test` needs `make install` first — it exercises the installed binary.

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`); a drifted one is rejected by AMFI ("Taskgated Invalid Signature") and SIGKILLs the binary (exit 137).

## Key flags

Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; missing flags come from pickers or defaults. The agent is never defaulted.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use/create the worktree for `<name>`; skip the worktree picker |
| `-A <name>`, `--agent <name>` | Pin the agent or `shell`; never defaulted |
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`: cloud or running → launch; a non-running local starts; a blocked model errors with that row's reason. Without `-A`, the agent is prompted first. `-M profile/<name>` pins a slot profile |
| `--replace` | With `-M`, start the model even if it means stopping a running one (same provider only) |
| `--others=keep\|stop` | Answer without asking when a start loads a model beside other providers' models (picker: `-M` row only). Unset: ask; with no TTY keep them and warn once (#412) |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR) |
| `-F <family>`, `--family <family>` | Filter models by family (comma-delimited, OR) |
| `--cwd` | Launch in the current directory, repository or not. Takes no value: a first argument before `--` naming an existing directory is refused (#392) unless `shell`'s first word is a bare name on `$PATH` |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` / `--check-guard` / `--no-guard` | Seed AGENTS.md + pointer files then exit / check / remove the `block-main-commit` guard |

## Passthrough args

Extra args forward to the launched agent; `--` is required when any argument begins with a dash. Agents append them to the command; `shell` (`ArgSetter`) takes them as argv (`d.args[0]` = binary).

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

## Post-exit flow

After the launched subprocess exits, the TUI and non-TUI paths run the same fixed sequence — [docs/wt-agents/README.md#post-exit-order](docs/wt-agents/README.md#post-exit-order). Change both paths together.

- `survey.ReleaseSession()` runs before the stop picker; the summary line (`agents.Summary`) never affects the exit code.
- **wt exits with the agent's status on both paths, after the whole tail** (#412): non-TUI `os.Exit(ee.ExitCode())`; TUI `tui.Run` returns the error and `runTUI` makes an `exitCodeError`. **Never `os.Exit` inside `internal/tui`.**
- A batch of stops costs one LiteLLM restart and one ollama wait (#379, #142); a stop ollama accepted and still lists at the bound is `*lifecycle.StillFinishingError`, not a failure.
- A slot-profile session's tail is release → stop picker → summary (no survey, no price notice).

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md) before changing the summary line, the stop picker, the stale-pricing notice, the survey, session handling, or profiles.

## Session survey (Go)

**The survey is switched off** (`survey.Enabled = false`, #136); the code is kept and tested, and `wt stats` still reports stored answers. Keep `drainTTYInput` after the last question. [Details](docs/internals/launch-flow.md#session-survey-go).

## Docs

`docs/`: `wt-*.md` command references, `wt-agents/` per-agent reference (`README.md`: LiteLLM routes and post-exit order; `profiles.md`, `slot-profiles.md`), `configuration.md`, `internals/` (code-level notes, one page per section below), `superpowers/` (specs and plans). `../CLAUDE.md`: monorepo commands and config ownership (wt is the only writer of `registry.toml`, and owns `~/.config/agent-wt/config.toml`).

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters.

- **Test seams** are package-level vars (`var x = realX`) that tests swap; `internal/lifecycle` keeps its seams in one `env` struct.
- **Tests stay off the developer's machine**: each package's `TestMain` isolates what it reaches — `lifecycle.IsolateProcessesForTest()` (mtplx pidfile, signals), `tiers.IsolateForTest` (model-table packages), `config.IsolateConfigHomeForTest` (anything reaching `config.Dir()`), `gitenv.IsolateForTest()` (anything that can run git; `TestEveryPackageThatRunsGitIsolatesIt`). A new package or launch path needs the same (route check via `stubEnsureRoute(t)`); no test runs `psql`; scratch registries go through `WT_REGISTRY`.
- **Assert on unexported functions directly**; parsing rendered lipgloss output flakes under forced-color ANSI.

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, runs git, starts a model, or touches routes.

```bash
go test ./...
go test ./internal/agents -run TestOpenCodeOllamaPrefix   # one test
go vet ./...
make check   # shellcheck, shfmt, gofmt -l (wt-ci's gate); `make format` writes both
make -C .. test-all   # CI-equivalent: root lint + llmbench + wt
```

## Go module

Module root is `wt/` (`go.mod`: `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,tomlw,tiers,cloudsync,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,gitenv,themes,tui,tuilayout,configeditor,ollamacheck,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke}`.

**Never use the stock `toml.Encoder` on the registry** (`internal/tomlw` reproduces tomli-w's layout). `internal/spend` never takes the connection string as a `psql` argument (#282).

Every file and package in full: [docs/internals/packages.md](docs/internals/packages.md).

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` table**; Providers/Models live in the registry and this file's write path never touches them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields); `Save(cfg)` only for a known-fresh `Config`.
- `wt config` launches even when `config.toml` fails validation, so the editor can repair it.

Read [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md) before changing config load/save, migrations, the `[litellm]` table, registry loading, catalog membership, or location validation.

## LiteLLM routing state (wt-owned)

The `[litellm]` table (`enabled`/`url`/`api_key`) decides whether non-native models route through the proxy (`Config.IsLitellm()`); set with `wt litellm status|on|off|set`, read nowhere else, routing policy only (never touches the proxy): [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#litellm-routing-state-wt-owned).

## Registry

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. `config.Load` reads it fail-closed and never writes it; wt writes it through `config.UpdateRegistry` alone. Path precedence: `WT_REGISTRY` > `MODELMAN_REGISTRY` (legacy alias) > `XDG_CONFIG_HOME` > `~/.config`; llmbench's `registry_path` uses the same order — keep the two in sync (each has a precedence test).

- **`ResolveLocation` is the one judge of a location** (`config.ErrLocation`).
- **On-disk and running state come from live probes**; no per-model state key exists, and whether a model is routed is `wt litellm list`.
- **`config.UpdateRegistry` is the only write path**; its `apply` must be pure (it may run up to three times). `RegistryDoc` operations are patch-shaped — never round-trip a `config.Model` into the file.
- **An id on more than one model row is refused, never resolved to the first row** (`config.ErrModelAmbiguous`); find a row through `RegistryDoc.Model`/`PatchModel`/`RemoveModel`.
- **A malformed `fetch`/`draft` or `cost.time_prices` row reads as absent and never fails the load** (`Model.Malformed()`). Get a zone through `config.loadZone`, not `time.LoadLocation`.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`: a key added to or renamed in `modelListJSON`/`modelListRow` (`cmd/wt/model_list.go`) fails `cmd/wt/model_list_contract_test.go` until `model-list.sample.json` has it (modeltiers reads that document as its whole inventory).
- A missing registry lets an *unconfigured* agent launch as a native passthrough (`agents.BuildPassthroughCmd`); a configured one fails on model resolution.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug.** Fix with `wt model init` on that machine (a provider it has no default row for is named in its output: a hand edit of `registry.toml`), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/`, `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither, so a fixture that redirects the registry also names it (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load` fails with `unknown provider "agy"`.

Full rules: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry). Adding a provider: the `adding-a-provider` skill.

## Local-model resolution

Every model row wt shows or resolves is built by `internal/catalog` from the agent's eligible list plus one live `localmodels.Inventory` snapshot (no stored flag). A row's action is **launch** (cloud, or local and running), **start** (local ollama/omlx/omlx-6bit/mtplx, not running or still loading) or **block**.

- **`config.Provider.Origin` is the one reading of a provider's address** (#348): probes, routes, direct routes and pi's `models.json` all use it, and so must any new reader. A test never binds or dials the mtplx default port 8003.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** (#201).
- A `-M` pin is looked up among all rows (`catalog.Find`, else `catalog.MissingReason`); without `-M`, the non-TUI path picks only launch rows — only a pin may start a model.
- **A slot-profile row is not a model row** (`Model` holds only `profile/<name>`, `Row.Profile` the rest): test `r.Profile != nil` before reading any other `Model` field, calling `ResolveRoute`, or launching/starting `Model`.

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

The start/stop engine (`Start`, `Stop`, `BeginStops` + `SettleRoutes`); backends ollama (`Shared`), omlx (`Pool`), mtplx (`Exclusive`).

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`); unknown occupancy is `*OccupancyUnknownError`, never assumed empty. Other providers' models beside the target need `Options.Others` (#412); `--replace` never answers it.
- **A start/stop wt performs updates `config.yaml`** through `internal/litellm` (route hook); `lifecycle.EnsureModelRoute` (#192) never fails a launch, a slot-profile launch uses the failing `profile_routes.go` calls.
- **Call `WaitPendingRoutes()` on every process exit and before handing a model to an agent**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async. The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose.
- **A `Start` that returned nil is followed by one `SettleStart(ctx, out, cfg, target)`** (#343, #349); each package has one seam over it that its `TestMain` stubs (`lifecycleSettleStart` in `cmd/wt`, `settleStart` in `internal/tui`).
- **A failed start is printed once, by `main`** (#344): a new command that can return one needs `SilenceErrors` or `asStartError`/`printOnce`, or the message prints twice.
- **An ollama stop ollama accepted but has not carried out is `*StillFinishingError`, never nil** (#379); only the stop loop reads it as an outcome (exit 0). The TUI stops models only after the alt screen is gone.
- **wt signals a process only after identifying it** (`mtplx_loading.go`): a pid is never enough (user's own, exact argv on the provider's port, recorded start time, rechecked before SIGKILL). The pidfile stays a bare pid (llmbench shares it).

Read [docs/internals/local-models.md](docs/internals/local-models.md#lifecycle-internallifecycle) before changing start/stop, the route hook, the launch-time route check, restart or readiness waits, or replace.

## Rotation (Go)

Each successful single-model launch records its id in `~/.config/agent-wt/rotation.state` (`rotation.RecordFor`, which also writes `last-picked.state` and `usage.jsonl`); the picker cursor lands on the model after it (`FirstAfter`).

- **Marker and pointer are two files**: `rotation.state` stays one bare registry id; `last-picked.state` holds the `> ` marker's row. A slot-profile launch calls `RecordProfile` (never `rotation.state`); a profile row never rotates.
- **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks. The marker is plain ASCII `> ` (Unicode misaligns CJK terminals).

Details and pinned tests: [docs/internals/local-models.md](docs/internals/local-models.md#rotation-go).

## Agents (Go)

Each agent registers a `Driver`; `BuildLaunchCmd(agent, m, worktreePath, yolo, cfg, extraArgs)` is the shared constructor for both launch paths (it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))`).

- An empty agent/provider wire-protocol intersection forces LiteLLM regardless of the toggle — so **codex always routes through LiteLLM**. Litellm/forced routes use the registry id (`m.ID`); direct routes the provider-side name (`m.ModelName`).
- **copilot uses the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters.
- **claude maps all four model slots on every non-native single-model launch** (`claudeSlotVars`); an applied profile's `env` is appended later and wins.
- agy is a native passthrough, not a command agent: `-A agy` without `-M` errors when more than one agy model is eligible.

Read [docs/internals/agents.md](docs/internals/agents.md) before changing a driver, `Route`, an optional capability (`Seeder`, `Syncer`, `ArgSetter`, …), or `ollamacheck.Check`. Per-agent env/args: [docs/wt-agents/](docs/wt-agents/). New driver: the `adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml` (`profiles.Resolve`), applied on both launch paths and by `wt smoke`; operator reference [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- Call the `config_content` cleanup explicitly after the run — `runAgentCmd`'s `os.Exit` skips defers.
- Apply order is env/args → `config_content` (with `config_defaults`) → wrapper; load/validate/apply errors degrade to an unprofiled launch with a warning.

**Slot profiles** (hand-written `~/.config/agent-wt/slot-profiles.toml`, never written by wt; [docs/wt-agents/slot-profiles.md](docs/wt-agents/slot-profiles.md)) map an agent's model slots to registry models and are pinned with `-M profile/<name>`; claude is the only slot agent. A rule that belongs to one agent goes on that interface and is asked through `profiles.SlotDrivers` (warn-only: `agents.SlotConfigWarner`), never into `internal/profiles` or `internal/catalog`, which do not import `internal/agents` (#418).

- **A slot profile is its own launch unit**: `launchSlotProfile` (`cmd/wt`) and `slot_flow.go` (`internal/tui`) run the same ten steps. Change both paths together.
- **The file's problems are warnings, never a stopped wt** (`slotWarnings`; #418, #419).
- **Fail closed, always the proxy, no file written**: nothing reads `cfg.IsLitellm()`; `config_content` travels only in inline `--settings`, the proxy token only in the process env.
- **Automatic profiles are never applied to a slot-profile launch** (`applySlotProfile`, never `applyProfileForLaunch`).
- **A default may be an agent alias** (`SlotCapable.DefaultAlias`, #391): use `SlotCheck.RestSlot()`/`FilledOrder()`, never `Profile.Default`.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing profiles, or its [slot-profile section](docs/internals/launch-flow.md#slot-profile-launch) before changing a slot-profile launch step.

## Smoke test (`wt smoke`)

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model, built like a real launch (`agents.BuildLaunchCmdInfo`), and reports PASS/FAIL/SKIP ([docs/wt-smoke.md](docs/wt-smoke.md)). Each row runs in its own temporary git repo unless `--cwd`; permission checks are off (yolo forced on except codex) and the directory is not a sandbox (#193). A model fallback is FAIL; the default prompt never contains its sentinel verbatim.

Read [docs/internals/smoke.md](docs/internals/smoke.md) before changing row directories, yolo, process-group cleanup, env stripping, Ctrl+C, or timeouts (180s cloud, 900s local).

## Start/stop (`wt start`, `wt stop`)

`cmd/wt/model_cmds.go`; [docs/wt-start-stop.md](docs/wt-start-stop.md); code notes: [docs/internals/local-models.md](docs/internals/local-models.md#startstop-wt-start-wt-stop).

## Cloud sync (`wt cloud-sync`)

`internal/cloudsync` decides what a sync changes (no I/O, no clock); `cmd/wt/cloudsync*.go` owns fetches, the ollama CLI, the confirmation, the one registry write, the route sync and exit codes. Behavior: [docs/wt-cloud-sync.md](docs/wt-cloud-sync.md); operator procedure (parser repair after exit 3): the `cloud-sync` skill. **Change the page and the skill with the behavior** — both quote its messages.

- **`ParsePricing` is the only code that knows the pricing page's HTML**; anything that stops looking like a price table is a `*ParseError`, never a short catalog (it would plan removing every ollama cloud entry).
- **The plan's text is the plan** (`CatalogPlan.Format`/`PricePlan.Format`): deterministic, no clock, compared with the re-plan under the registry lock; `Apply` methods are pure and write only the shown diff.
- **Exit codes 2 to 5 name a flow that changed nothing and win over 1** (2 and 3 are ollama's; 4 and 5 either flow's). **`source = "manual"` keeps a hand-added cloud model through a sync until its source lists it** (`wt model add` sets it, `wt model edit --source` marks older entries); a vanished OpenRouter model is removed only after approval. A flow whose provider the registry does not use is skipped (exit 0); cloud-sync never seeds a provider row.

Read [docs/internals/cloud-sync.md](docs/internals/cloud-sync.md) before changing a planner or parser, either `Format`, `cost.time_prices` rows, the removal digest, the registry write, an exit code, or how the ollama CLI is run.

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`, the sole implementation of `config.yaml` route management. Subcommands `sync|list|providers|status|on|off|set|restart`; `sync` is the only CLI route write, `restart` is what `bin/update/litellm` runs. Operator doc: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned); JSON shapes: [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json).

- **wt owns only its own rows** (`model_info.wt_managed: true`); a hand-written row keeps everything except an empty ollama `api_base` (#202).
- **`sync` trusts only `ok` families** (an untrustworthy probe freezes the family with a warning) and **never takes "the first row with the id"** on a duplicated id.
- **An `os.environ/VAR` api_base stays as written**; "set for the proxy" comes from `litellm.ProxyEnv`, not `os.Getenv`. `status` shows the api key only as `api_key_set` / last 4 chars.
- Paths: `WT_LITELLM_CONFIG` (default `~/.config/litellm/config.yaml`), `WT_LITELLM_RESTART_CMD`; a redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`). **A restarting command can block about 3.5 minutes** (in-flight requests drain, #377); every restart waits for the proxy to answer (#382). Manual forced restart: `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. Test seams `bounce` (`litellm.StubProxyReadinessForTest`) and `proxyProc` refuse inside a test binary.
- The spend database (`litellm.DatabaseURL()`) is read-only, only for `wt stats`; errors never print a value.

Read [docs/internals/litellm-routes.md](docs/internals/litellm-routes.md) before changing sync's desired set, row ownership or adoption, the `api_base` repair, ollama-serve warnings, or `config.yaml` I/O.

## Guard, init seeding, sessions

- `internal/guard` manages the `block-main-commit` hook (`Install` is idempotent and appends; `git rev-parse --git-common-dir` covers all worktrees).
- `wt --init` seeds `AGENTS.md` and a pointer file (`cmd/wt/main.go`, before any agent-binary requirement); existing files are never overwritten.
- wt launches a fresh agent every time and leaves session handling to the agent (#198, #204): continue with the agent's own flags after `--` (`claude-wt -- --continue`). Why the session lookup stays removed: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#sessions-go).

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`): worktree → agent+command → model picker → launch, plus starting, routing, slot-profile, replace-confirm and ollama-warning screens; a picker is skipped when its selection is resolved.

- **A view fits the terminal** (`internal/tuilayout`): every list screen renders through a `listFrame`, sized only by `fitTo` in `Update` (`TestEveryListPhaseFitsTheTerminal`).
- **The update goroutine never waits**: use non-blocking `tryEnsureModelRoute`; waits run in a command behind `phaseRouting` (`settleStart`, #343).
- **A start prints into the flow, not onto the screen** (#275) and cannot be cancelled from the routing stage on (`pastCancel`).
- **A slot-profile launch waits behind `phaseSlot`**; `flushRouteNotesAfterRun` settles any restart still owed on every way out.
- **A model table is priced once, when built** (`modelrows.go`): read `tableRow.price()`/`timePriced()`, never `Model.Cost`'s flat fields or a clock.
- **TIER's score orders the picker's model rows** (descending, within the running-local and other bands) but is never routed on; a missing or unparseable `tiers.toml` is `-` and unscored, never an error.
- A test that sets state by hand goes through `Update` before reading `View()`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`) skip the TUI; `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted.

Read [docs/internals/tui.md](docs/internals/tui.md) before changing layout, start-on-select, the launch-time route check, or the model table.

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate`) and creation (`EnsureForName` for `-W`, `EnsureForBranch` for the picker). Every function takes `dir` (repo root) first for testability.

> **A launch that has a directory needs no repository**: the repo root handed to `runLaunchPath` gates the guard install and nothing else (state is keyed by model, agent and pid). Only `-W` and the worktree picker refuse outside one; don't key new launch state on the repository. [Details](docs/internals/launch-flow.md#launch-directory-and-the-repository).

> **`Enumerate` fetches before listing** (`git fetch --all --prune`, 5s; failure silently ignored); `-W`/`--cwd` skip it, and `EnsureForName` never consults remotes.

> **The picker's pull request tags never hold it up** (`worktree.PRs`, #384, #387, #388, #390): `gh pr list` runs in its own command, a failed listing adds nothing and no message, PRs are keyed by head repository and branch (`prFor` is the one lookup). Seam `listPRs` (`worktree.StubPRListForTest`). [Details](docs/internals/tui.md).

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and directories inside `.git` have no worktree; don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it, and the picker skips bare default-branch rows.

## Verification commands

Run the Go test block above before any commit, then exercise the installed binary:

```bash
wt -W my-feature -A claude           # named worktree + launch
wt model list [--json]               # every registry model and local model found, with live status
wt cloud-sync --dry-run              # plan a price and ollama-catalog refresh; changes nothing
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix, both routing modes (flips `wt litellm off|on`)
```

The full command list (`wt served`, `wt warm`, `wt model add|edit|rm|init`, `wt profile list`, `wt stats`, `wt profile list|show`, `wt start|stop`, `wt litellm`, …): [docs/internals/testing.md](docs/internals/testing.md#verification-commands).

> Verifying a branch's behavior requires building it first (`D=$(mktemp -d)`, then `go build -o "$D/wt" ./cmd/wt`) — `~/.local/bin/wt` is whatever was last `make install`ed and may predate the branch.

> **A built wt that must not touch this machine** runs with every path redirected. Any one left out reaches the real file:
>
> ```bash
> env -i PATH="$PATH" HOME="$D/home" WT_REGISTRY="$D/registry.toml" \
>   WT_LITELLM_CONFIG="$D/config.yaml" WT_LITELLM_RESTART_CMD=true \
>   WT_MTPLX_PIDFILE="$D/mtplx.pid" "$D/wt" model list
> ```
>
> This redirects files, not servers: a start, stop or probe still reaches whatever answers at the registry's `base_url`s, and `wt cloud-sync` still runs the real `ollama`.
