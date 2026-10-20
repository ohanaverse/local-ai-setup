# CLAUDE.md

Guidance for Claude Code when working in this repo.

## What this repo is

The `wt` binary (`cmd/wt/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--cwd` (the current directory — the one launch that does not move to a checkout root, and needs no git repository), `-W <name>` (named worktree), or the worktree picker. Outside a repository `-W` refuses (`not in a git repo`) and a launch without `--cwd` runs in the current directory too.
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
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`; cloud or running → launch; a non-running local starts (with a progress display); a blocked model errors with that row's reason. Without `-A`, prompts for the agent first, then validates the pin. `-M profile/<name>` pins a slot profile |
| `--replace` | With `-M`, start the model even if it means stopping a running one (same provider only) |
| `--others=keep\|stop` | When a non-TUI start (`-M`, `wt start`, `wt smoke`) loads a model beside other providers' models: keep them or stop the ones wt can stop, without asking. Unset: ask on a TTY; without one keep them and print one warning (#412) |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR) |
| `-F <family>`, `--family <family>` | Filter models by family (comma-delimited, OR) |
| `--cwd` | Launch in the current directory, inside a git repository or not; skip the worktree picker. The agent starts there, so its own `--continue` (after `--`) finds that directory's sessions. Takes no value: a first argument before `--` that names an existing directory is refused (#392), unless `shell`'s first word is a bare name found on `$PATH` |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed AGENTS.md + pointer files, then exit |
| `--version` | Print version and exit |
| `--check-guard` / `--no-guard` | Check / remove the `block-main-commit` guard |
| `--debug-worktrees` | List worktrees and branches (test helper) |

## Passthrough args

Extra args forward to the launched agent. `--` is required when any argument begins with a dash (`shell-wt -- ls -la`); other commands pass without it, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Post-exit flow

After the launched subprocess exits, the TUI and non-TUI paths run the same fixed sequence — [docs/wt-agents/README.md#post-exit-order](docs/wt-agents/README.md#post-exit-order). Change both paths together.

- `survey.ReleaseSession()` runs before the stop picker, so the model just used does not count as in use.
- The summary line (`agents.Summary`) is the single formatter for both paths and never affects the exit code.
- **wt exits with the agent's status on both paths, after the whole tail** (#412). Non-TUI: `os.Exit(ee.ExitCode())` in `runAgentCmd`/`runSlotAgentCmd`. TUI: `launchDoneMsg.err` (the run's error, or the profile error that kept the agent from starting) is kept on the model, `tui.Run` returns it (`runResult`), and `runTUI` turns an `*exec.ExitError` into a quiet `exitCodeError` (`agentExitStatus`; a signal is 255, what `os.Exit(-1)` gives) with cobra silenced, so `main` prints nothing more; a profile that fails to apply returns its plain error, which `main` prints once and exits 1, as `runAgentCmd`'s return does. Never `os.Exit` inside `internal/tui`.
- A batch of stops costs one LiteLLM restart and one ollama wait: `lifecycle.BeginStops` sends every ollama stop, the batch's `Stop` finishes each model under one shared 3-minute bound (#379), then one `lifecycle.SettleRoutes` (#142). A model ollama accepted and still lists at the bound is `*lifecycle.StillFinishingError`: the stop loop prints it as still finishing and does not count it as failed; every other caller gets an error.
- A slot-profile session has a shorter tail on both paths: release → stop picker → summary, with no survey and no price notice (`runSlotAgentCmd`; the `profile` branch of `printPendingSummaryAndSurvey`).

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md) before changing the summary line, the stop picker (`survey.Picker`, `chooseLabeled`, `stopEntries`), the stale-pricing notice, the session survey, session handling, or profiles.

## Session survey (Go)

**The survey is switched off** (`survey.Enabled = false`, `internal/survey/prompt.go`, #136). The code is kept and tested so it can return by flipping that value; `wt stats` and the picker's SURVEY column still report stored answers. The stop picker lives in `internal/survey` and is unaffected. Keep `drainTTYInput` after the last question: it stops a multi-line paste running as shell commands once wt exits. Details: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#session-survey-go).

## Docs

- `docs/configuration.md` — Claude Code / Codex CLI config
- `docs/wt-config.md` — `wt config` subcommands
- `docs/wt-agents/` — per-agent reference (one file per launcher); `README.md` there covers LiteLLM routes/proxy lifecycle and post-exit order
- `docs/wt-agents/profiles.md` — local-model launch profiles
- `docs/wt-agents/slot-profiles.md` — slot profiles: named multi-model launches for Claude Code
- `docs/wt-smoke.md`, `docs/wt-start-stop.md`, `docs/wt-stats.md`, `docs/wt-model.md`, `docs/wt-cloud-sync.md` — command references
- `docs/superpowers/specs/`, `docs/superpowers/plans/` — wt design specs and plans (newer cross-package specs live in the monorepo's [../docs/superpowers/](../docs/superpowers/))
- `../CLAUDE.md` — monorepo-wide commands, benchmark isolation helpers, shared config ownership (wt is the only writer of `registry.toml` — the `wt model` commands, the Models tab of `wt config` and `wt cloud-sync`; wt owns `~/.config/agent-wt/config.toml`)

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

- **Test seams** are package-level vars (`var x = realX`): production code calls the var, tests swap it. `internal/lifecycle` keeps its seams in one `env` struct (`defaultEnv()` / `testEnv()`).
- **No test reads the real mtplx pidfile or signals a process it did not start**: the `TestMain`s of `internal/lifecycle`, `internal/survey` and `cmd/wt` call `lifecycle.IsolateProcessesForTest()`.
- **Tests stay off the developer's machine.** The `TestMain` of each package that builds a model table (`cmd/wt`, `internal/tui`) calls `tiers.IsolateForTest` (`MODELTIERS_OUT` at a file that does not exist), so no test reads the real `tiers.toml`. The `TestMain` of each package that reaches `config.Dir()` calls `config.IsolateConfigHomeForTest` (throwaway `XDG_CONFIG_HOME`; `WT_REGISTRY` and `MODELMAN_REGISTRY` cleared); `cmd/wt` and `internal/tui` also stub the inventory probe, hard-fail model starts and no-op the route check, and no test runs `psql`. Tests name a scratch registry through `WT_REGISTRY`, and where no `TestMain` isolates also set `MODELMAN_REGISTRY` to `""`. A new package that reaches `config.Dir()` needs the same; a new launch path calls the route check through `stubEnsureRoute(t)`'s seam.
- **Git finds only repositories a test made**: the `TestMain` of every package whose tests can run git (itself, or via `internal/worktree`, `guard` or `smoke`) calls `gitenv.IsolateForTest()` (`TestEveryPackageThatRunsGitIsolatesIt`).
- **Assert on unexported functions directly** (e.g. `buildStatsRows`); parsing rendered lipgloss output flakes under forced-color ANSI.

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, runs git, starts a model, or touches routes — it lists every seam and what each `TestMain` stubs.

```bash
go test ./...
go test ./internal/agents -run TestOpenCodeOllamaPrefix   # one test
go vet ./...
make check   # shellcheck, shfmt, gofmt -l (wt-ci's gate); `make format` writes both
make -C .. test-all   # CI-equivalent: root lint + llmbench + wt
```

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,tomlw,tiers,cloudsync,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,gitenv,themes,tui,tuilayout,configeditor,ollamacheck,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke}`.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config and profiles.toml once) |
| `cmd/wt/commands.go` | `rotate` subcommand (debug helper) |
| `cmd/wt/commands_config.go` | `wt config` subcommand family; `runConfigEditor` opens the editor on a tab and runs the route sync the Models tab owes |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch from live `catalog` rows; a `-M` pin on a start row starts it |
| `cmd/wt/start.go` | `startForLaunch` — non-TUI start driver: stderr progress, Ctrl+C cancel, replace confirmation, `allowReplace`, the others question (`decideOthers`; its prompt, `--others` and warning are `start_others.go`); `startForLaunchOwed` — the same drive (`driveStart`) for a slot profile's local model: it carries an owed proxy restart and returns `started <id>; launch cancelled` for a Ctrl+C that lost to the start |
| `cmd/wt/start_others.go` | the others question (#412): `--others` (`othersMode`), the `[s/k/N]` prompt (`chooseOthers` seam, `askChoice`), the no-TTY warning |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers; TTY seams and picker-TTY errors |
| `cmd/wt/launch.go` | `launchFiltered`, `launchPassthroughImpl`, `runAgentCmd`; profile apply |
| `cmd/wt/stats.go` | `wt stats` — read-only report: the survey table over `survey.jsonl`, then the usage table |
| `cmd/wt/stats_usage.go` | `wt stats`' usage half: launches joined with LiteLLM spend; the `querySpend` and `stdoutWidth` seams |
| `cmd/wt/stats_usage_table.go` | `renderUsageTable` — the usage table's cells, laid out by `renderPlainTable` |
| `cmd/wt/plain_table.go` | `renderPlainTable` — the one borderless, width-aware text table (`wt stats`, `wt model list`) |
| `cmd/wt/stats_json.go` | `wt stats --json` — `buildStatsJSON`, one document built from the same rows as the tables |
| `cmd/wt/model_cmds.go` | `wt start` / `wt stop` |
| `cmd/wt/smoke.go` | `wt smoke` — one-shot model×agent smoke test |
| `cmd/wt/profile.go` | `wt profile list/show/status/on/off`; `setEnabledLine`'s surgical `enabled = ...` edit |
| `cmd/wt/slot_launch.go` | `launchSlotProfile` — the non-TUI slot-profile launch (ten steps, fail closed); `applySlotProfile`, `runSlotAgentCmd`, the `slotPin` sentinel |
| `cmd/wt/profile_slots.go` | the slot-profile half of `wt profile list` and `wt profile show`; `maskKey` |
| `cmd/wt/litellm.go` | `wt litellm ...` |
| `cmd/wt/model.go` | `wt model` group (bare: the Models tab of `wt config`); `wt model init` |
| `cmd/wt/model_write.go` | `wt model add`, `edit`, `rm` — each one `modeladmin` write, then one route sync |
| `cmd/wt/model_list.go` | `wt model list [--json]` — `modeladmin.Rows` over one probe; `fitModelList` drops PATH, then SIZE, on a narrow terminal |
| `cmd/wt/cloudsync.go` | `wt cloud-sync` — the command, the one confirmation, the one registry write both flows share, the route sync, the exit code; the openrouter flow's fetch |
| `cmd/wt/cloudsync_catalog.go` | the ollama flow: plan, gate (mass removal, removal digest), then pulls and removals after the registry write |
| `cmd/wt/cloudsync_ollama.go` | the ollama CLI for the ollama flow, through the `ollamaCLI` seam, pinned with `OLLAMA_HOST` to the registry's ollama origin |
| `cmd/wt/exitcode.go` | `exitCodeError` / `exitCodeOf` — how one command exits with a status other than 1; `agentExitStatus` (a picker launch's agent status), `reportExit` |
| `internal/config/` | config load/validate/save (agents + joined registry catalog), route resolution (`ResolveRoute`), migrations |
| `internal/tomlw/` | ordered TOML document and an emitter that reproduces tomli-w's layout byte for byte; never use the stock `toml.Encoder` on the registry |
| `internal/cloudsync/` | the pure core of `wt cloud-sync` (parsers, planners, `Apply`); no I/O, no clock: see [Cloud sync](#cloud-sync-wt-cloud-sync) |
| `internal/tiers/` | read-only reader of modeltiers' `tiers.toml` (`Path` follows modeltiers' rule, `MODELTIERS_OUT` first; `Load` never fails; `Index.Rating` by provider id + model name; `Entry.Cell` is the TIER cell, `Entry.Short` a slot's form on the slot line, `Entry.Notes` the mode line's long forms); `contract_test.go` parses `../docs/contracts/tiers.sample.toml` |
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection; `last-picked.state`, the picker's marker |
| `internal/usage/` | append-only JSONL launch history (1d/7d/30d), per model and per agent×model |
| `internal/refcount/` | live-session "in use" counts: JSONL keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point (`RecordAll`: one line per slot model of a profile session) |
| `internal/survey/` | post-session survey + stats; stop picker and stop loop (`Picker`, `PickerWith`, `ReadStopState`, `StopEntries`) |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, capabilities), picker catalog, drivers, `RunAndCleanup`, `BuildPassthroughCmd` |
| `internal/profiles/` | launch-profile overlays and slot profiles — see [Profiles](#profiles-internalprofiles) |
| `internal/smoke/` | `wt smoke`'s core: `Eligibility`/`Candidates` (rows per agent from one inventory snapshot), `RunRow` (PASS/FAIL/SKIP) |
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build`, `Find`, `MissingReason`, `Row.Action`; slot-profile rows (`ResolveSlotProfile`), opt-in; `SlotPrefixReserved`, the one predicate for the reserved `profile/` id prefix. No rendering, counts or sorting |
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/modeladmin/` | the core of model management, shared by the `wt model` commands and the Models tab, with no UI import: `Rows`, `Add`, `Edit`, `Remove` (each one `config.UpdateRegistry`, no route sync) |
| `internal/lifecycle/` | local-model start/stop engine — see [Lifecycle](#lifecycle-internallifecycle) |
| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management: entries and policy, `ApplyChange`, `Sync`/`PlanSync`, restart and readiness, `DatabaseURL` |
| `internal/spend/` | per-model request, token and cost totals from the proxy's spend table: one aggregated query through `psql`, typed failures, no Postgres driver; the connection string is never a `psql` argument (#282) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection, enumeration, creation; the picker's pull request tags (`PRs`, `prs.go`); `remotes.go`: remote name to owner/repo (`RemoteRepos`), used to tag remote picker rows |
| `internal/gitenv/` | `IsolateForTest`: git isolation for test binaries |
| `internal/initseed/` | `--init` seeding |
| `internal/ollamacheck/` | pre-launch `ollama list` availability check, pinned to the registry's ollama address; `StubListForTest` is its seam (`fortest.go`) |
| `internal/configeditor/` | the TUI behind `wt config`: the Agents tab (`config.toml`, saved with ctrl+s) and the Models tab (`registry.toml` through `internal/modeladmin`, each change written at once); `Run` reports `Result.RegistryChanged` |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell, pickers, launch, start-on-select flow (`start_flow.go`); `modelrows.go`, `modeltable.go`; `PickStartModel` — the standalone picker `wt start` and `wt smoke` use |
| `internal/tuilayout/` | what both TUIs fit a terminal with: `ListFrame`, `FitTo`, `Clip`, `WrapText`/`Flow`, `Columns` (a table that drops whole columns) |

Every function, seam and rule each row holds: [docs/internals/packages.md](docs/internals/packages.md).

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry; nothing in this file's write path touches them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields). `Save(cfg)` is for a `Config` known to be fresh (tests, one-shot tools).
- `migrateConfigSchema` runs self-extinguishing fixups on every `Load`.
- `wt config` launches even when `config.toml` fails validation, so the editor can repair it; other paths exit early.

Read [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md) before changing config load/save, migrations, the `[litellm]` table, registry loading, catalog membership, or location validation. `wt config` subcommands: [docs/wt-config.md](docs/wt-config.md).

## LiteLLM routing state (wt-owned)

The `[litellm]` table (`enabled`/`url`/`api_key`) in wt's `config.toml` decides whether non-native models route through the proxy (`Config.IsLitellm()`) or dial providers directly (when it is false). It is set with `wt litellm status|on|off|set` and read from nowhere else. Toggling is routing policy only: it never touches the proxy. Details: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#litellm-routing-state-wt-owned).

## Registry

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. wt loads it via `config.Load`, fail-closed, and joins it in memory with `config.toml`. `config.Load` never writes it; wt writes it through `config.UpdateRegistry` alone: `wt model init` (provider rows), `wt model add|edit|rm` and the Models tab of `wt config` (model rows, `internal/modeladmin`), and `wt cloud-sync` (cloud prices and the ollama cloud entries). Path precedence: `WT_REGISTRY` > `MODELMAN_REGISTRY` (the older name, kept as an alias) > `XDG_CONFIG_HOME` > `~/.config`. llmbench's `registry_path` uses the same order — keep the two in sync; each has a precedence test.

- **`ResolveLocation` is the one judge of a location**: every consumer keys off its error (`config.ErrLocation`), so catalog, inventory, sync and validation agree.
- **What is on disk and what is running come from live probes.** wt reads no per-model `[model_state]` key; whether a model is routed is `wt litellm list`.
- **`config.UpdateRegistry` is the only registry write path**: flock, symlink write-through, touched-row validation, no-op skip, re-check before rename. Its `apply` must be pure (it may run up to three times). `RegistryDoc`'s operations are patch-shaped — never round-trip a `config.Model` into the file.
- **An id on more than one model row is refused, never resolved to the first row** (`config.ErrModelAmbiguous`, from `RegistryDoc.modelRow`): `wt model edit` and `wt model rm` run on a registry that fails validation, and a duplicated id is one such registry. Find a row for a write through `RegistryDoc.Model`/`PatchModel`/`RemoveModel`, not a loop of your own.
- **A malformed `fetch` or `draft`, or a `cost.time_prices` row the validator refuses, reads as absent and never fails the load**; `Model.Malformed()` names it, and `wt model list` and the Models tab say so. A value of the wrong TOML type still fails the load. `TimePrice.Problem` asks `validateTimePrice` itself, so reader and writer cannot disagree; a window whose `end` is before its `start` runs past midnight and is read by wall clock (`TimePrice.holds`), never start plus hours. Get a zone through `config.loadZone`, not `time.LoadLocation`.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`, loaded by wt's Go tests (and, for the two `registry*.toml` fixtures, llmbench's). `tiers.sample.toml`, modeltiers' output, is parsed by `internal/tiers/contract_test.go`, which asserts known entries' codes and aligned scores. One pins an output instead: `model-list.sample.json` is the `wt model list --json` document, which modeltiers reads as its whole inventory and its tests read as theirs. A key added to or renamed in `modelListJSON` or `modelListRow` (`cmd/wt/model_list.go`) fails `cmd/wt/model_list_contract_test.go` until the fixture has it, and a changed fixture runs modeltiers-ci.
- A missing registry lets an *unconfigured* agent launch as a native passthrough (`agents.BuildPassthroughCmd`); a configured one fails on model resolution.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug** — e.g. models referencing `provider_id`s with `providers = []`. Fix with `wt model init` on that machine (it adds a default row for each provider that is installed, used by a model or listed by a configured agent, and a native row per configured agent; a provider it has no default row for is named in its output and is a hand edit of `registry.toml`), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/` and `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither variable, so a fixture that redirects the registry also names config.yaml (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load`/`Validate` fails with `unknown provider "agy"`.

Full rules (catalog membership, passthrough, decoded fields): [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry).

Adding a provider: the `adding-a-provider` skill (`.claude/skills/adding-a-provider/SKILL.md`).

## Local-model resolution

Every model row wt shows or resolves — the TUI picker, `wt start`/`wt smoke`'s picker, the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot. A row's action is **launch** (cloud, or a local model the probe reports running and loaded), **start** (a local row of ollama/omlx/omlx-6bit/mtplx that is not running, or that omlx is still loading), or **block**.

- Local rows come only from the live inventory, which probes servers and disk and reads no stored flag.
- **`config.Provider.Origin` is the one reading of a provider's address** (#348): the probes (`localmodels.FamilyOrigin`), a route's `api_base`, a direct route and pi's `models.json` all go through it, and so does any new reader. It is the `base_url` as written, except an `http` mtplx url with no port whose host reaches the server `wt start` runs (`localhost`, with or without the root dot, `127.0.0.1`, or the unspecified `0.0.0.0`/`::`), which is `config.MtplxPort` (8003); `wt start` refuses any other mtplx url with no port. A test never binds or dials 8003.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** — omlx's `/v1/models` lists its whole pool, loaded or not (#201).
- A `-M` pin is looked up among all rows, discovered ones included (`catalog.Find`); no row → `catalog.MissingReason`.
- Without `-M`, the non-TUI path picks only among launch rows; only a pin may start a model.
- **A slot-profile row is not a model row**: its `Model` holds only the id `profile/<name>` and `Row.Profile` holds the rest. Rows are emitted only for a caller that sets `catalog.Input.SlotProfiles` (the picker, `resolveModel`, `wt profile show`), never for `wt start` or `wt smoke`. Test `r.Profile != nil` before reading a `Model` field other than `ID`, calling `ResolveRoute`, or launching or starting `Model`. Its `Running` and `Loading` are its one local model's, on a blocked row too (`slotLocalState`); they feed the RUNNING cell only, and `ProfileRow.Verdict` alone decides launch, start or block.

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

The start/stop engine: `Start` (`Options.Out` takes every line a start prints, the proxy restart's included, in place of stderr; the model picker passes one), `Stop`, `BeginStops` (a `StopBatch`, whose `Stop` stops one model and writes its route removal) + `SettleRoutes`, `LoadingServer` + `StopLoading`; backends ollama (`Shared`), omlx (`Pool`: loads beside, `/unload` per model), mtplx (`Exclusive`). Used by the TUI start flow, `startForLaunch`, `wt start`, and `wt smoke`.

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`, `Occupants` lists them): an `Exclusive` occupant (mtplx) to be replaced, or the `Pool` victims (omlx) the eviction plan (`evictions.go`) predicts omlx will unload (`poolAdmissionMarginPct` margin, 15% of the ceiling; `reconcilePool` handles what omlx unloaded anyway, on failure too); undeterminable occupancy is `*OccupancyUnknownError`, never assumed empty. Other providers' models loaded beside the target (`residentOthers`, #412) need `Options.Others`: its zero value refuses with the same `*OccupiedError` (`Others`, `OthersUnknown`), and `OthersAnswer.Stop` stops the named ones before the replace and the start. The non-TUI driver asks after the same-family y/N (`[s/k/N]`, `cmd/wt/start_others.go`), or takes `--others=keep|stop`, or without a TTY keeps them with one warning line; `--replace` never answers it. The TUI still passes `Decided` with nothing to stop.
- **Route hook** (`routes.go`): a start/stop wt performs updates `config.yaml` through `internal/litellm`. A launch of an already-running model gets the Add-only launch-time check, `lifecycle.EnsureModelRoute` (#192), which never fails a launch.
- **A slot-profile launch uses its own route calls** (`profile_routes.go`): `EnsureModelRoutes` (several models, restart deferred, every failure returned; `Routable` is its predicate, which the catalog's resolved validation asks too), `Options.RestartOwed` on `Start` (one restart for the cloud routes and the started model; a refusal leaves it owed, `RestartStillOwed`), `RestartProxy`/`RestartProxyTo` and `ProbeProxy` (failures returned, not printed; every warning `litellm.RestartContext` returns is a failed restart, and a request cut off at the drain bound is a `litellm.Notice`, printed, never a failure; a route writer that is a `lifecycle.RestartNoticer` takes the notices itself, which is how the TUI shows the in-progress one on its waiting screens). `EnsureModelRoute` stays the single-model check that never fails a launch. `internal/lifecycle` remembers the outcome of the most recent proxy restart attempt in the wt process, and `ProbeProxy` refuses a profile launch with `the last LiteLLM proxy restart failed, so routes written since may not be live` until a later restart succeeds or a new wt process starts; both launch paths call `WaitPendingRoutes` immediately before that probe.
- **Call `WaitPendingRoutes()` on every process exit and before handing a model to an agent**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async.
- **A `Start` that returned nil is followed by one call, `SettleStart(ctx, out, cfg, target)`** (`confirm.go`, #343, #349): the wait for the proxy, then one probe of the provider's server, because another terminal's `wt stop` can land in that wait. A server that is gone is a `*StoppedError`, and the model's own route is removed. It drops the caller's cancellation itself, so callers pass the context they have. Each package has one seam over it that its `TestMain` stubs (`lifecycleSettleStart` in `cmd/wt`, `settleStart` in `internal/tui`); a new caller of `Start` makes the same one call.
- **The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose** — callers cancel as soon as the hook returns. Pinned by `TestRouteRestartSurvivesCallerCancelAfterReturn`.
- Route output goes through `routePrintf(ctx, …)`; a caller-supplied writer rides on the context.
- **A failed start is printed once, by `main`** (#344): `wt start` and `wt smoke` set `SilenceErrors`; the root command silences cobra only for an error that came out of a `-M` pin's start (`asStartError` in `resolveModel`, `printOnce` in `runLaunchPath`). A new command that can return a failed start needs one of the two, or the message and its log tail print twice.
- **An ollama stop ollama accepted and has not carried out is `*StillFinishingError`, never nil** (#379): strict by default, so a caller that needs the model gone fails on it; only the stop loop (`survey.stopEntries`) reads it as its own outcome (exit 0). A stop that waits past `stopNoticeAfter` (2s) gives one `StopNotice`; the caller takes it with `WithStopNotice`, else it is a route line. A screen that stops a model must take the notice itself: the TUI stops models only in the post-exit phase, after the alt screen is gone (`TestModelStopsRunOnlyAfterTheAltScreen`).
- **wt signals a process only after identifying it** (`mtplx_loading.go`): the one stop with no port to go through is an mtplx still loading, found by wt's pidfile. A pid is never enough: alive, the user's own, an mtplx server on the provider's port by exact argv, and the recorded start time when there is one, checked again before SIGKILL. The pidfile stays a bare pid (llmbench shares the path); wt's start record is `<pidfile>.wt`; both are removed only when the server they name is confirmed gone. Ctrl+C after a signal was sent is a `*StopInterruptedError`.

Read [docs/internals/local-models.md](docs/internals/local-models.md#lifecycle-internallifecycle) before changing start/stop, the route hook, the launch-time route check, restart or readiness waits, or replace.

## Rotation (Go)

Each successful single-model launch records its model id in `~/.config/agent-wt/rotation.state`; the picker cursor lands on the model after it (`FirstAfter`). `rotation.RecordFor(agent, id)` also writes `last-picked.state` and appends the usage event to `usage.jsonl`.

- **The marker and the pointer are two files.** `rotation.state` stays one bare registry id. `last-picked.state` holds the row the `> ` marker sits on (`LastPicked`, falling back to `Last`). A slot-profile launch calls `RecordProfile`, which writes the marker and the usage event and never `rotation.state`; a profile row never rotates.
- **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks. The bands are native rows, slot-profile rows in file order, then the rest.
- The last-launched marker is plain ASCII `> ` (Unicode shapes misalign CJK terminals).

Columns, sort rules and the pinned tests: [docs/internals/local-models.md](docs/internals/local-models.md#rotation-go). Debug helper: `go run ./cmd/wt rotate code`.

## Agents (Go)

Each agent registers a `Driver`. `BuildLaunchCmd(agent, m, worktreePath, yolo, cfg, extraArgs)` is the shared constructor for both launch paths: it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build` — every driver goes through it.

- **Protocols.** Agents declare wire protocols (claude `anthropic`; codex `openai-responses`; copilot, opencode, pi `openai-chat`; agy, shell none); providers declare what they serve. An empty intersection forces LiteLLM regardless of the toggle — so **codex always routes through LiteLLM**.
- **Model id contract** (`ResolveRoute`): litellm/forced routes use the registry id (`m.ID`); direct routes use the provider-side name (`m.ModelName`).
- **copilot uses the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters.
- **claude maps all four model slots on every non-native single-model launch**: `Build` sets `ANTHROPIC_DEFAULT_<SLOT>_MODEL` to the `--model` reference and `…_MODEL_NAME` to `m.ModelName`, and clears the inherited values. An applied automatic profile's `env` is appended later, so its value for one of them wins. The names come from `claudeSlotVars`, shared with the slot-profile build.
- agy is a native passthrough but not a command agent: `-A agy` without `-M` errors when more than one agy model is eligible.

Read [docs/internals/agents.md](docs/internals/agents.md) before changing a driver, `Route`, the optional capabilities (`Seeder`, `Syncer`, `ArgSetter`, `OneShotRunner`, `StateDirer`, `SlotConfigWarner`), or the pre-launch `ollamacheck.Check`. Per-agent env/args: [docs/wt-agents/](docs/wt-agents/). New driver: the `adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml`, resolved per agent × model (`profiles.Resolve`) and applied on both launch paths (`applyProfileForLaunch`) and by `wt smoke` (`applyResolvedProfile`). Operator reference: [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- Call the `config_content` cleanup explicitly after the run — `runAgentCmd`'s `os.Exit` skips defers. `config_defaults` (claude only: the same table shape, applied after `config_content` and only where the file has no leaf) rides on that one call, its leaves recorded and restored the same way.
- Apply order is env/args → `config_content` (with `config_defaults`) → wrapper; load/validate/apply errors degrade to an unprofiled launch with a warning.

**Slot profiles** are a second kind, from the hand-written `~/.config/agent-wt/slot-profiles.toml` (wt never writes it): a named entry that maps an agent's model slots to registry models, shown as a picker row and pinned with `-M profile/<name>`. Claude is the only agent with slots (`profiles.SlotCapable`). A rule about what a profile may hold that belongs to one agent goes on that interface and is asked through `profiles.SlotDrivers` (claude's `config_content.env must be a table` and `config_content.modelOverrides must be a table`: `CheckConfigContent`, shared with the slot build; a rule that only warns goes on the optional `agents.SlotConfigWarner`, never on `SlotDrivers`), never written into `internal/profiles` or `internal/catalog`, which do not import `internal/agents`. Operator reference: [docs/wt-agents/slot-profiles.md](docs/wt-agents/slot-profiles.md).

- **A slot profile is its own launch unit**: `launchSlotProfile` (`cmd/wt`) and `slot_flow.go` (`internal/tui`) run the same ten steps. Change both paths together.
- **The file's problems are warnings, never a stopped wt** (`slotWarnings`, `cmd/wt/slot_launch.go`): a parse failure (a wrong TOML type included, which fails the whole file), each key the schema does not name (`SlotStore.Unknown`; the file still loads), each profile with no usable name or agent, a registry that holds the `profile/` prefix (`catalog.SlotPrefixReserved`: a provider with id `profile`, or a model id beginning with `profile/`), and each driver warning about a passing profile's `config_content` (`slotContentNotes`, `agents.SlotConfigWarnings`: claude's `modelOverrides` targets that are not strings or not the profile's slot models). They are printed before the TUI and again after it.
- **Fail closed, always the proxy, no file written**: a profile that cannot be fully applied never launches; nothing on the path reads `cfg.IsLitellm()`; claude's `config_content` travels only in the inline `--settings` JSON, and the proxy token only in the process env.
- **Automatic profiles are never applied to a slot-profile launch**: it calls `applySlotProfile`, never `applyProfileForLaunch`.
- **A default may be an alias of the agent's** (`SlotCapable.DefaultAlias`; claude's `opusplan`, #391): it needs the slots in its `Requires` filled and rests on its `Rest` slot. Read the slot a session works on through `SlotCheck.RestSlot()` and list slots with `SlotCheck.FilledOrder()`, never `Profile.Default`, which may name no slot; only the slot build reads `Default` itself, to pass it as `--model`.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing apply order, confirmation, `SelfHeal`, or `wt profile`, and [its slot-profile section](docs/internals/launch-flow.md#slot-profile-launch) before changing a slot-profile launch step, its applier or its recording.

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

- **`ParsePricing` is the only code that knows the pricing page's HTML**, and every way the page can stop looking like a price table is a `*ParseError`, never a short catalog: a catalog that lost its rows would plan the removal of every ollama cloud entry.
- **The plan's text is the plan**: `CatalogPlan.Format` and `PricePlan.Format` are what the user approves and what the command compares with the re-plan made under the registry lock, so both planners are deterministic and neither reads the clock.
- **The `Apply` methods are pure and write a diff**: `config.UpdateRegistry` may run them up to three times, and nothing is written that the printed plan does not show.
- **Exit codes 2 to 5 are the ollama flow's and win over 1**; each means "the ollama flow changed nothing".
- **A flow whose provider the registry does not use is skipped, never an error** (one line on stdout, exit 0), and `wt cloud-sync` never seeds a provider row.

Read [docs/internals/cloud-sync.md](docs/internals/cloud-sync.md) before changing a planner or a parser, either `Format`, the `cost.time_prices` rows a flow writes, the removal digest, the registry write, the route sync, an exit code, or how the ollama CLI is run.

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`, the sole implementation of `config.yaml` route management. Subcommands `sync|list|providers|status|on|off|set|restart`; `sync` is the only CLI route write, and `restart` (`litellm.RestartAndAwait`, the restart `sync` makes, on its own, waiting for the proxy to answer even when it was down before; exit 1 on a failed restart or a proxy that does not answer) is what `bin/update/litellm` runs after an upgrade. Operator doc: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned). JSON shapes are pinned by [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json).

- **wt owns only its own rows**: each carries `model_info.wt_managed: true`. A hand-written row keeps everything except an empty ollama `api_base`, which every write fills (#202).
- **`sync` trusts only `ok` families**: a family whose probe is untrustworthy is frozen with a warning; one that refuses the connection has its local routes removed.
- **`sync` runs on a registry with a duplicated model id, so it never takes "the first row with the id"**: an inventory entry is paired with the row of its own provider (`registryRowOf`), and an id with more than one row to route — or one row to route beside a local row a failed probe froze — is left as it is, with a warning (`planSync`'s `ambiguous`).
- **An `os.environ/VAR` api_base stays as written**; "set for the proxy" comes from `litellm.ProxyEnv` (the LaunchAgent plist), which `os.Getenv` cannot answer.
- `status` shows the api key only as `api_key_set` / last 4 chars.
- Path `WT_LITELLM_CONFIG`, default `~/.config/litellm/config.yaml`; restart `WT_LITELLM_RESTART_CMD`, run as given; with none set, `litellm.RestartContext` lets a request in flight finish (#377): pid from `launchctl list`, one SIGTERM, a wait of up to 3 minutes (`proxyDrainTimeout`) for the process to exit, then a plain `launchctl kickstart`; at the bound wt SIGKILLs that pid itself (ESRCH: it exited in time, nothing was cut off) and runs the plain kickstart. `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy` is the forced restart (launchd kills after 5s): what wt runs without a pid or when the SIGKILL is refused, and the manual command. The proxy refuses connections while it drains, and every restarting command can block about 3.5 minutes. **Every restart waits for the proxy to answer again** (`litellm.Bounce.Run`, #382): one `Listening` probe before it, then `WaitReady` up to 30s only when that probe found a listener; a failed wait is a warning. `wt litellm sync` (and every command that syncs after a registry write) runs it in Apply's default restart before returning; the lifecycle hook runs it over its own seams on its async goroutine; an `Options.Restart` replaces it, wait included. The readiness seam is `bounce`; `cmd/wt`'s `TestMain` stubs it with `litellm.StubProxyReadinessForTest`. The process seam is `proxyProc`; it and `realRunShell` refuse inside a test binary. A redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`).
- The spend database is `litellm.DatabaseURL()` (`WT_LITELLM_DATABASE_URL`, legacy `MODELMAN_LITELLM_DATABASE_URL`, then `general_settings.database_url` in `config.yaml`): read-only, used only by `wt stats`, and its errors name variables and files, never a value.

Read [docs/internals/litellm-routes.md](docs/internals/litellm-routes.md) before changing sync's desired set, row ownership or adoption, the `api_base` repair, ollama-serve warnings, or `config.yaml` I/O.

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Sessions (Go)

wt launches a fresh agent every time and leaves session handling to the agent (#198, #204): `BuildLaunchCmd` appends passthrough args unchanged, so continuing a conversation is the agent's own flags after `--` (`claude-wt -- --continue`, `opencode-wt -- --session <id>`). The removed session lookup and why it stays removed: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#sessions-go).

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → launch, plus starting, routing, slot-profile, replace-confirm and ollama-warning screens. A picker is skipped when its selection is already resolved. Every picker uses `ThemedListDelegate`.

- **A view fits the terminal** (`internal/tuilayout`, shared with `wt config`): every list screen renders through a `listFrame`, sized only by `fitTo` in `Update`. Add a new line to the frame, and size lists through `fitTo`. Pinned by `TestEveryListPhaseFitsTheTerminal`. A status that ends in what to do is wrapped, not clipped; one too tall to share the terminal with the table has the screen until the next key (`statusAlone`).
- **The update goroutine never waits**: the launch-time route check uses the non-blocking `tryEnsureModelRoute`; waits happen in a command behind `phaseRouting`, and the start flow waits for the proxy restart and confirms the start (`settleStart`, #343) on the start's own goroutine (`runStart`). A server that is gone arrives as `startDoneMsg.err` and is a failed start. A start the user quit out of at the routing stage is waited for once the alt screen is gone (`settleQuitStart`), so wt never exits under that check's route removal and restart.
- **A slot-profile launch waits behind `phaseSlot`** (`slot_flow.go`): the route writes, the restart and the probe run in a command, the profile's local model starts through the existing start flow (`beginStartOwed`), and ctrl+c quits without launching or recording; `flushRouteNotesAfterRun` settles the stage and any restart still owed, on every way out: a stage in flight, the replace dialog, and a quit while the local model starts (`settleQuitSlot`, `settleQuitSlotStart`). A profile row is drawn from `Row.Profile`: `renderTable` makes no `ResolveRoute` call for it, and `slotLine` replaces the mode line while it is highlighted.
- **A start prints into the flow, not onto the screen** (#275): `runStart` passes `lifecycle.Options.Out` (a `startOutput`), and `finishStart` puts the engine's lines on the status and in `pendingRouteNotes`. From the routing stage on a start cannot be cancelled (`pastCancel`), and a row the picker just started gets the launch-time route check like any other.
- **A model table is priced once, when it is built** (`modelrows.go`): `buildRows` reads one clock and `tableRow.pricedAt` resolves each row's price in force. The cost sort, the COST cell, the `~` mark and the mode line read `tableRow.price()` / `timePriced()`, never `Model.Cost`'s flat fields and never a clock, so an open picker's rows do not move at a window boundary. The mark and the heading cost width: read the two notes in [docs/internals/tui.md](docs/internals/tui.md) before changing either.
- **The model table drops whole columns on a narrow terminal** (`tuilayout.Columns`, fitted by `tuilayout.FitTo`), keeping MODEL, STATUS and RUNNING.
- **TIER is modeltiers' advisory tier, read once per table** (`loadTiers` seam in `buildRows`, `internal/tiers`): `S44` (code + rounded aligned score, the digits right-aligned to the table's widest: `Q 9`), an unrated model's score alone with the code column blank (`  9`, `tiers.Entry.Cell`), `-` for no score and no tier, a profile row, or a missing or unparseable file — never an error or a message. Dropped after COST, before LOC, or first of all in a table where no row has a tier or a score (`unratedDropOrder`). The highlighted row's long form follows the mode line's price where it fits (`withTierNote`), and `slotLine` puts each rated slot model's short form (`tiers.Entry.Short`: `S44`) after its name. Display only: never sorted or routed on.
- A test that sets state by hand goes through `Update` before reading `View()`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`, `wt rotate`) skip the TUI. `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted.

Read [docs/internals/tui.md](docs/internals/tui.md) before changing layout or sizing, start-on-select (`start_flow.go`), the launch-time route check (`checkLaunchRoute`, `phaseRouting`), or the model table's columns.

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate` → worktrees / local branches / remote-only branches) and creation (`EnsureForName` for `-W`, `EnsureForBranch` for the picker). Every function takes `dir` (repo root) first for testability.

> **A launch that has a directory needs no repository.** The repo root handed to `runLaunchPath` gates the guard install and nothing else: rotation, usage and refcount state are keyed by model, agent and pid, never by a repository or branch (claude's `config_content` backup is keyed by the absolute settings path, which cannot collide). `--cwd` passes the checkout root when `RepoRoot` finds one and `""` when it does not (outside a repository, in a bare one, inside `.git`, or with no git on PATH), so it launches either way; only `-W` and the worktree picker refuse. Don't key new launch state on the repository without deciding what a launch outside one records. Details: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#launch-directory-and-the-repository).

> **`Enumerate` fetches before listing.** It runs `git fetch --all --prune` (5s timeout, every remote) before building the picker's groups. Failure — offline, no remotes, timeout — is silently ignored; the picker falls back to existing local refs. `-W`/`--cwd` skip `Enumerate` entirely, and `EnsureForName` never consults remotes.

> **The picker's pull request tags never hold it up** (`worktree.PRs`, `prs.go`, #384). Two `gh pr list --limit 200` calls per picker open, `--state open` and `--state all` (`worktree.PRListStates`), so merged and closed PRs never crowd an old open one out (5s timeout each, `GH_PROMPT_DISABLED=1`, no pager, stdin on the null device, stderr captured and dropped). Each runs beside `Enumerate` and the other in its own command (`loadPRsCmd(state)`); each answer (`prsLoadedMsg`) is merged into the tags so far (`worktree.MergePRs`: an open or draft PR beats a closed or merged one, else the higher number wins, so arrival order does not matter) and tags the list already on screen in place (`applyPRs`: cursor, page and filter kept) — the open PRs usually first, every state filled in after. Each row gets its branch's open PR, else its highest-numbered one (`preferPR`), looked up by head branch, fork PRs skipped. A local row matches by its branch name (`worktree.PRBranch`). A remote row is tagged only when its remote is the repository `gh pr list` reports (#388): `Enumerate` fills `Entry.RemoteRepo` (the remote URL's owner/repo, `worktree.RemoteRepos`) and `Entry.RemoteBranch` (split by the remote's real name, `worktree.SplitRemoteBranch`), and `prFor` compares `RemoteRepo` with `PRSet.Repo` without case, so a fork you push to stays untagged until #387; the URL's host is ignored (an Enterprise host with the same owner/repo matches), and a local-path or `file://` remote never matches. The display name is still `worktree.PRBranch`'s (`keith/feat-x` shows `feat-x`). A listing that fails adds nothing, with no message; both failing is no tags. The seam is `listPRs` (`worktree.StubPRListForTest`), stubbed to fail by the `TestMain`s of `internal/worktree` and `internal/tui`; the real call also refuses inside a test binary.

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
wt model add <provider> <name> --family F   # register a model (one route sync)
wt model add mlx_lm_server <target> --draft <draft> --family F   # a target+draft pairing; wt cannot start one
wt model edit <id> --tags code       # change family, tags, location or prices; nothing else in the row moves
wt model rm <id> [--yes]             # registry only; prints where the weights are
wt cloud-sync --dry-run              # plan a price and ollama-catalog refresh; changes nothing (fetches public pages, runs `ollama list`)
wt profile show -A <agent> -M <id>   # dry-run profile resolution
wt profile list                      # automatic profiles, then slot profiles with each one's static verdict
wt profile show -A claude -M profile/<name>   # a slot profile: the picker's verdict, the slots, the --settings payload (key masked)
wt stats [--window 7d] [--family F] [--json]  # survey table, then launches and LiteLLM spend per model
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix in both routing modes (flips `wt litellm off|on`, restoring config.toml; --modes current for one pass)
```

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
