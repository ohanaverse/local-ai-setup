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
- **Tests stay off the developer's machine**: each package's `TestMain` isolates what it reaches — `lifecycle.IsolateProcessesForTest()` (every process family's pidfile, signals, the wired-limit read), `tiers.IsolateForTest` (model-table packages), `config.IsolateConfigHomeForTest` (anything reaching `config.Dir()`), `gitenv.IsolateForTest()` (anything that can run git; `TestEveryPackageThatRunsGitIsolatesIt`). A new package or launch path needs the same (route check via `stubEnsureRoute(t)`); no test runs `psql`; scratch registries go through `WT_REGISTRY`.
- **Assert on unexported functions directly**; parsing rendered lipgloss output flakes under forced-color ANSI.
- **A test that creates worktrees builds a fresh repo per case** (`t.TempDir()` + `gitInit`): one wrong success leaves `.worktrees/<name>`, and the path-collision error then masks every later case. Use a branch name like `topic`, not `feature`, when a case also needs `feature/<x>` (git forbids a ref that is both a leaf and a directory).
- **A test that depends on filesystem case-sensitivity probes and skips** (write `CaseProbe`, stat `caseprobe`), and asserts on `git for-each-ref` output, never `refExists`: on a case-insensitive volume `show-ref` resolves `Feature/x` through `feature/x`'s ref file and says yes.
- **A test's `WarmupSeconds` must outlast the helper server's first answer.** Each warmup request is capped at the time left in the window, so a 1 s budget on a loaded runner fails on the deadline, not on the response you meant to assert on. Use 3 s or more.

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, runs git, starts a model, or touches routes.

```bash
go test ./...
go test ./internal/agents -run TestOpenCodeOllamaPrefix   # one test
go vet ./...
make check   # shellcheck, shfmt, gofmt -l (wt-ci's gate); `make format` writes both
make -C .. test-all   # CI-equivalent: root lint + llmbench + wt
```

## Go module

Module root is `wt/` (`go.mod`: `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,provider,tomlw,tiers,candidates,slotedit,cloudsync,rotation,usage,refcount,survey,agents,profiles,guard,sessionclaim,rootclaim,worktree,initseed,gitenv,themes,tui,tuilayout,configeditor,ollamacheck,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke}`.

**Never use the stock `toml.Encoder` on the registry** (`internal/tomlw` reproduces tomli-w's layout). `internal/spend` never takes the connection string as a `psql` argument (#282).

Every file and package in full: [docs/internals/packages.md](docs/internals/packages.md).

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` table**; Providers/Models live in the registry and this file's write path never touches them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields); `Save(cfg)` only for a known-fresh `Config`. **Any read-modify-write of a wt-owned file flocks via `config.WithFileLock`** — the one helper (`WithLock` for config.toml, `UpdateRegistry` for registry.toml, `slotedit.Apply` for slot-profiles.toml); a new write path without it can silently lose a concurrent edit.
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
- **`serve_args` is mtplx-only extra argv, one value judge** (#432): `provider.FamilySpec.ServeArgs` says which family takes it, `config.ServeArgsProblems` is the one judge (the loader's `Model.Malformed()`, the writer's `validateModelRow`, `wt model add|edit`, and `lifecycle.serveArgsFor` at a start, which stops with `*ServeArgsError` before spawning), and `provider.ProcessSpec.SpawnArgs`/`OwnedFlags` build the argv and derive the refused flags from the spec. A malformed value reads as absent and never fails the load. A change applies from the next start (wt cannot see a running server's flags). It is not in `docs/contracts/model-list.sample.json` (an omitempty key, like `target`/`draft`; `TestModelListJSONOutputMatchesTheFixture` exercises it) and llmbench ignores it.
- **`llamacpp` is still a provider row** (`internal/provider/table.go`) — retired as a *backend* on 2026-09-07 (no LaunchAgent, no llmbench backend) but never removed from the table, so a registry model naming that provider still resolves. It has no `Seed`, so it is never created automatically; `docs/reference/provider-artifacts.md` has the retirement record.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`: a key added to or renamed in `modelListJSON`/`modelListRow` (`cmd/wt/model_list.go`) fails `cmd/wt/model_list_contract_test.go` until `model-list.sample.json` has it (modeltiers reads that document as its whole inventory).
- A missing registry lets an *unconfigured* agent launch as a native passthrough (`agents.BuildPassthroughCmd`); a configured one fails on model resolution.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug.** Fix with `wt model init` on that machine (a provider it has no default row for is named in its output: a hand edit of `registry.toml`), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/`, `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither, so a fixture that redirects the registry also names it (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load` fails with `unknown provider "agy"`.

Full rules: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry). Adding a provider: the `adding-a-provider` skill.

## Local-model resolution

Every model row wt shows or resolves is built by `internal/catalog` from the agent's eligible list plus one live `localmodels.Inventory` snapshot (no stored flag). A row's action is **launch** (cloud, or local and running), **start** (local ollama/omlx/omlx-6bit/mtplx/rec, not running or still loading) or **block**.

- **`config.Provider.Origin` is the one reading of a provider's address** (#348): probes, routes, direct routes and pi's `models.json` all use it, and so must any new reader. A test never binds or dials the mtplx default port 8003.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** (#201).
- A `-M` pin is looked up among all rows (`catalog.Find`, else `catalog.MissingReason`); without `-M`, the non-TUI path picks only launch rows — only a pin may start a model.
- **A slot-profile row is not a model row** (`Model` holds only `profile/<name>`, `Row.Profile` the rest): test `r.Profile != nil` before reading any other `Model` field, calling `ResolveRoute`, or launching/starting `Model`.

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

The start/stop engine (`Start`, `Stop`, `BeginStops` + `SettleRoutes`); backends ollama (`Shared`), omlx (`Pool`), every process-server family (mtplx, rec and sushi, `Exclusive`; one spec-driven `processServer` backend, a `Process` row in `internal/provider/table.go`). rec is `ScopeMachine` (one instance per machine, found through `rec status`) and `ServedOnly` (no artifacts: only its registered `rec/flint` is listed). sushi is `ScopePort` and `ServedOnly`: one model-less `sushi serve` on port 12345 that loads the pack on the first request naming it, so wt lists no artifacts for it either.

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`); unknown occupancy is `*OccupancyUnknownError`, never assumed empty. Other providers' models beside the target need `Options.Others` (#412); `--replace` never answers it.
- **A start/stop wt performs updates `config.yaml`** through `internal/litellm` (route hook); `lifecycle.EnsureModelRoute` (#192) never fails a launch, a slot-profile launch uses the failing `profile_routes.go` calls.
- **Call `WaitPendingRoutes()` on every process exit and before handing a model to an agent**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async. The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose.
- **A `Start` that returned nil is followed by one `SettleStart(ctx, out, cfg, target)`** (#343, #349); each package has one seam over it that its `TestMain` stubs (`lifecycleSettleStart` in `cmd/wt`, `settleStart` in `internal/tui`).
- **A failed start is printed once, by `main`** (#344): a new command that can return one needs `SilenceErrors` or `asStartError`/`printOnce`, or the message prints twice.
- **An ollama stop ollama accepted but has not carried out is `*StillFinishingError`, never nil** (#379); only the stop loop reads it as an outcome (exit 0). The TUI stops models only after the alt screen is gone.
- **wt signals a process only after identifying it** (`server_loading.go`): a pid is never enough (user's own, a process server's exact argv on the provider's port, recorded start time, rechecked before SIGKILL). The pidfile stays a bare pid (llmbench shares it).
- **An empty `Stop` means "signal the verified pid"** (sushi has no stop command, and running the bare binary is not a stop): `processServer.stop` calls `stopByPid`, which shares `signalVerifiedServer` with `stopLoadingServer` (SIGTERM to the group when wt recorded the start, else the pid alone; `stopGrace` 10 s; SIGKILL only if the same start time remains; the pidfile and `.wt` go once the process is gone; then `stopByPid` waits `stopTimeout` for the port to close). `loadingServer` refuses a pidfile pid <= 1 or wt's own. No verified pid: a port that does not answer is nothing to stop (nil), one that answers is `*NotStartedByWtError` ("… was not started by wt; stop it yourself"). `TestTableInvariants` requires `PidfileDefault`, `Subcommand` and `PortFlag` of a spec with an empty `Stop`. A sushi llmbench started has a bare-pid pidfile and no `.wt` record: `wt stop` still stops it when its argv matches, with no start-time check against pid reuse.
- **`ProcessSpec.WarmupSeconds` and `ReadyHTTP`.** `WarmupSeconds` (contract key `warmup_seconds`; mtplx 120, rec 120, sushi 400; required and positive, `warmupBudget` fails a start without one) is the warmup window and the cap on each chat request. `ReadyHTTP` (contract kind `http-get`, `Path "/health"`) is ready on exactly HTTP 200 and names no model: sushi answers it before any pack is loaded, so the warmup performs the cold load and then `confirmLoaded` requires the exact id among the loaded models.
- **The loaded-aware served list is opt-in per family** (`FamilySpec.LoadedOnly`, contract key `loaded_only`; today only sushi). `ServedIDs` (so `wt served` and the inventory's Running) reads such a family through `localmodels.FetchLoadedModelIDsKeyed`, which drops an entry only when its `loaded` is the JSON literal `false`; absent, null or non-bool keeps it. `confirmLoaded` always uses it. Every other call, including every other family's served list, the readiness poll and `requirePackListed` (exact id; an empty list skips the check; a failed fetch fails the start), goes through `FetchModelIDsKeyed`, which lists every id. omlx keeps its own pool branch.
- **A busy port holding a wt-started `ReadyHTTP` server is reused, not refused** (`processServer.start`): sushi answers `/health` with no pack loaded (or mid cold-load), so nothing shows Running and the port is busy. When `loadingServer` verifies the pidfile's process as wt's own, the start spawns nothing and signals nothing; it runs the pack check, the warmup (which loads the pack into that server) and `confirmLoaded`. A failure there does not kill it (it was not spawned by this start). A busy port with no verified pidfile is still `*PortBusyError`.
- **`config.Load` derives sushi for claude and pi in memory** when the registry holds a sushi model (`deriveSushiAgents`, `config.SushiAgents`): `wt model add` still writes it (`EnsureAgentProvider`), but a pack registered any other way works too. Removing sushi from an agent sticks only while the registry has no sushi model.
- **The wired-limit preflight** (`wiredlimit.go`, `wiredLimitFamilies` gates it by family id, today `sushi`) runs in `start` before the others question and before any stop. It reads `sysctl -n iogpu.wired_limit_mb hw.memsize` (0 means 75% of `hw.memsize`) and needs `floor(total *.safetensors bytes * 11 / 10 / 1048576)` MiB (`ngram_table.bin` excluded; 49,506,491,792 bytes, i.e. 47,213 MiB of bytes, is 51,934 MiB). Below that it fails with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot); wt never sets the sysctl. What it cannot measure prints `wt: skipping the memory check (<why>)` and the start goes on. llmbench applies the same integer rule, gated by the contract's `wired_limit` (a lifecycle test pins `wiredLimitFamilies` to it) and reading the pack directory from the contract's `model_dir` (the contract test pins it to `DefaultModelDir`). A failed sushi warmup gets the fix appended only for eight phrases (`memoryRefusalMarkers`; the bare word `iogpu` is not one), pinned by a test on each side. A warmup request is capped at the time left in the `WarmupSeconds` window, so the budget is the most a warmup can take.
- **A key the registry names goes with every probe of a process server** (`localmodels.FamilyAPIKeyErr`): a `secret_ref` that cannot be read ends a start before the spawn (`*KeyUnreadableError`); a refused key is `localmodels.ErrKeyRefused` (family `partial`, not `down`). rec's key (`exec:rec auth token`) also goes into its LiteLLM route. A `ScopeMachine` family's existing instance is adopted when it serves the target at the registry's address and asked about before it is replaced otherwise (details and known limits: `docs/internals/local-models.md`).

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
- **claude declares a gateway model's context window** (#405): `CLAUDE_CODE_MAX_CONTEXT_TOKENS` from the `context` of its `tiers.toml` entry **only when `context_basis` is `host`** (`claudeWindow`; #470: a `largest_row` window, or a file with no basis, is never declared; cloud models only, a local build has none). The single-model launch sets it unless the user's environment already has it; a slot profile sets the smallest window among the slots that have one, provided the default slot has one (`claudeSlotWindow`, #471; opusplan anchors on the smaller of opus and sonnet, none if either is unknown; a slot with no window is ignored, not a veto) unless the profile's env, `config_content.env` or the user's environment names it. The `loadTiers` seam is stubbed by tests and `tiers.IsolateForTest` closes the real file.
- agy is a native passthrough, not a command agent: `-A agy` without `-M` errors when more than one agy model is eligible.

Read [docs/internals/agents.md](docs/internals/agents.md) before changing a driver, `Route`, an optional capability (`Seeder`, `Syncer`, `ArgSetter`, …), or `ollamacheck.Check`. Per-agent env/args: [docs/wt-agents/](docs/wt-agents/). New driver: the `adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml` (`profiles.Resolve`), applied on both launch paths and by `wt smoke`; operator reference [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- Call the `config_content` cleanup explicitly after the run — `runAgentCmd`'s `os.Exit` skips defers.
- Apply order is env/args → `config_content` (with `config_defaults`) → wrapper; load/validate/apply errors degrade to an unprofiled launch with a warning.

**Slot profiles** (`~/.config/agent-wt/slot-profiles.toml`, hand-editable and written by wt only through `wt profile slots add|set|rm` (`internal/slotedit`: dated backup, validate under a `config.WithFileLock` flock, comment-preserving); [docs/wt-agents/slot-profiles.md](docs/wt-agents/slot-profiles.md)) map an agent's model slots to registry models and are pinned with `-M profile/<name>`; claude is the only slot agent. A rule that belongs to one agent goes on that interface and is asked through `profiles.SlotDrivers` (warn-only: `agents.SlotConfigWarner`), never into `internal/profiles` or `internal/catalog`, which do not import `internal/agents` (#418).

**No flag points a claude launch at another LiteLLM** (the URL comes only from `[litellm]` in `config.toml`). To A/B a proxy setting, run a scratch `litellm --config <scratch>.yaml --port 4001` and start `claude` directly with the env `claudeDriver.BuildSlots` builds (`internal/agents/claude_slots.go`: `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN`, per-slot `ANTHROPIC_DEFAULT_*_MODEL` plus `_NAME` in the `--settings` env JSON, `--model`). The scratch config holds the omlx key, so delete it afterwards.

- **A slot profile is its own launch unit**: `launchSlotProfile` (`cmd/wt`) and `slot_flow.go` (`internal/tui`) run the same ten steps. Change both paths together.
- **The file's problems are warnings, never a stopped wt** (`slotWarnings`; #418, #419).
- **Fail closed, always the proxy, no file written**: nothing reads `cfg.IsLitellm()`; `config_content` travels only in inline `--settings`, the proxy token only in the process env.
- **Automatic profiles are never applied to a slot-profile launch** (`applySlotProfile`, never `applyProfileForLaunch`).
- **A default may be an agent alias** (`SlotCapable.DefaultAlias`, #391): use `SlotCheck.RestSlot()`/`FilledOrder()`, never `Profile.Default`.
- **Read each input file once per command** (`wt profile slots usage|candidates`: `tiers.LoadWithFreshness`, `usage.AllActivity`): a second parse re-reads a file that may have changed between them, so a report can mix two generations — and the duplicated I/O buys nothing.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing profiles, or its [slot-profile section](docs/internals/launch-flow.md#slot-profile-launch) before changing a slot-profile launch step.

## Smoke test (`wt smoke`)

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model, built like a real launch (`agents.BuildLaunchCmdInfo`), and reports PASS/FAIL/SKIP ([docs/wt-smoke.md](docs/wt-smoke.md)). Each row runs in its own temporary git repo unless `--cwd`; permission checks are off (yolo forced on except codex) and the directory is not a sandbox (#193). A model fallback is FAIL; the default prompt never contains its sentinel verbatim. `--tools` is the tool-call check (a nonce planted in the row's directory, read with a tool) and `--hops N` chains N dependent reads; both are exclusive with `--prompt` and `--cwd`, and their combination is validated before the model is resolved.

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
- **`EnsureSettings` (`internal/litellm/configfile.go`) applies the launcher-required settings on every write:** `litellm_settings.drop_params` and `use_chat_completions_url_for_anthropic_messages` value-enforced to `true`, plus `general_settings.cancel_on_disconnect: true` only when absent (a hand-written `false` is the opt-out; merge keys are followed; an aliased or root-merged `general_settings` is skipped with a sync warning). Without it LiteLLM never cancels an abandoned non-streamed request upstream (#399); a cancelled one logs as a failed `499`. The first write to an older config adds the key and restarts the proxy once. **Adding a key here fails every "settled config needs no change" fixture** (`cmd/wt/litellm_test.go`, `internal/litellm/{discovered,service}_test.go`) until it carries the key; `go test ./...` lists them.
- The spend database (`litellm.DatabaseURL()`) is read-only, only for `wt stats`; errors never print a value.

Read [docs/internals/litellm-routes.md](docs/internals/litellm-routes.md) before changing sync's desired set, row ownership or adoption, the `api_base` repair, ollama-serve warnings, or `config.yaml` I/O.

## Guard, init seeding, sessions

- `internal/guard` manages the `block-main-commit` hook (`Install` is idempotent and appends; `git rev-parse --git-common-dir` covers all worktrees).
- `wt --init` seeds `AGENTS.md` and a pointer file (`cmd/wt/main.go`, before any agent-binary requirement); existing files are never overwritten.
- wt launches a fresh agent every time and leaves session handling to the agent (#198, #204): continue with the agent's own flags after `--` (`claude-wt -- --continue`). Why the session lookup stays removed: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#sessions-go).
- A launch in the repository root writes a claim under `<git-common-dir>/wt-sessions/<pid>` (`internal/sessionclaim`); a second live root launch gets the continue / new worktree / quit menu (`c`/`w`/`q`, Esc quits). A linked worktree (by `-W` or a row) skips the check — the check judges the resolved directory, so `-W <root's branch>` is checked — `--allow-shared-root` continues without asking, and with no terminal the launch refuses. A claims failure only prints a note. Where the check runs and when the claim is released: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#shared-root-check-internalsessionclaim); the file format `repo-cleanup` also reads: [docs/contracts/session-claims.md](../docs/contracts/session-claims.md).

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

> **Worktree paths derive from the full branch name** (`worktreeDirName`): path separators on any platform, colon, and ASCII control characters flatten to `-`, so `feature/x` → `.worktrees/feature-x` and `feature/x` + `bug/x` get distinct directories (once they raced for `.worktrees/x`). A name that flattens onto another's directory (`feature/x` vs `feature-x`) errors "already in use by branch …" on every creation path, `-W` and the picker alike (one `pathCollision` check); the check asks the filesystem (`os.SameFile`) so `Feature/x` vs `feature/x` collide on a case-insensitive volume (macOS default) and stay distinct on a case-sensitive one; a flattened name over 255 bytes is refused with a "shorten the branch name" error (git stores a long ref as nested directories, but the flattened name is one entry); a worktree from the old last-component layout (`.worktrees/x` holding `feature/x`) keeps resolving by branch name (the reuse loop matches branches, never paths). Derived `.`/`..` are rejected — `filepath.Join` would clean `..` to the repo root and `.` to the container.

> **Validate the derived path before running git** (`worktreePath`/`newWorktreePath`): `git worktree add -b` creates the branch first, so a path that then fails (too long, taken) leaves a stray branch. Every creation site goes through those two helpers; a new site must too.

> **A launch that has a directory needs no repository**: the repo root handed to `runLaunchPath` gates the guard install and nothing else (state is keyed by model, agent and pid). Only `-W` and the worktree picker refuse outside one; don't key new launch state on the repository. [Details](docs/internals/launch-flow.md#launch-directory-and-the-repository).

> **`Enumerate` fetches before listing** (`git fetch --all --prune`, 5s; failure silently ignored); `-W`/`--cwd` skip it, and `EnsureForName` never fetches (it reads only the remote-tracking refs already present).

> **The picker's pull request tags never hold it up** (`worktree.PRs`, #384, #387, #388, #390): `gh pr list` runs in its own command, a failed listing adds nothing and no message, PRs are keyed by head repository and branch (`prFor` is the one lookup, and forks no git: staleness is answered off the update goroutine, #451). Seam `listPRs` (`worktree.StubPRListForTest`). [Details](docs/internals/tui.md).

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and directories inside `.git` have no worktree; don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it when typed as the bare name or a remote-tracking form like `origin/<default>` or `<remote>/HEAD` (one `refuseDefaultBranch` guard; matched only while that ref lives under `refs/remotes/` and no local branch has the same name), and `-W origin/feature` resolves like the picker (a local `feature` tracking it). The picker skips bare default-branch rows.

## Cleanup classifier (`wt cleanup`)

`internal/cleanup` classifies every worktree, local branch and `origin` branch into proven / less_sure / unique_work / in_use from one read-only snapshot; `cmd/wt/cleanup.go` prints it (`--json`, `--plain`) or shows the tick list (`internal/tui/cleanup_list.go`). Design: [../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md](../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md); operator pointer: guide 08 §9.

- **Fail closed.** A git error, an unresolved default branch, an unknown PR state or an unavailable gate is Unknown, never proven. `LookupUnknown` is the zero value of `Lookup`, so a missing map entry reads Unknown.
- **No deletion code in `internal/cleanup` or `cmd/wt/cleanup*.go`**: `TestCleanupCmdContainsNoDeletion` scans both for a banlist.
- **The base is `origin/<default>`**; the local default is only a fallback, and then every verdict is capped below proven (`capFallback`).
- **Weak signals are not proofs**: `git cherry` and patch-id equivalence (`WeakSignals`) show as less_sure, never proven (they pass a squash that was later reverted).
- **Refs passed to `Prove`/`WeakSignals` are fully qualified** (`refs/heads/<b>`, `refs/remotes/<r>/<b>`) or a 40-hex id, so a tag never shadows a branch.
- **Min-age (`DefaultMinAge`, 10m) gates `same-commit` and `ancestor` evidence**: wt creates branches from a local main behind `origin/main`, so a new branch reads as ancestor-merged. An old branch whose reflog expired takes its age from `packed-refs`' mtime, so it reads fresh for 10 minutes after any packed-refs rewrite (clone, gc, a fetch that prunes a packed ref); accepted, it heals.
- **Evidence strings, reason and blocker codes are the JSON contract**: [../docs/contracts/cleanup-list.sample.json](../docs/contracts/cleanup-list.sample.json) + `cleanup-list.schema.json`, held by `cmd/wt/cleanup_contract_test.go`; change them only with the fixture.

**Before `wt cleanup` deletes anything** (PR B carries, settle first):

- a floor for `--min-age` (today any positive value, 1ns, turns the fresh-branch rule off), or a header line when it is lowered;
- a header note when the registry failed to load (protected `local_path` artifacts are then not protected);
- `config.ExpandHome` for protected paths (today only `~/` is expanded);
- extend the banlist test (refspec push `:topic`, `gh api -X DELETE`) as removal code arrives;
- a ticked less_sure or unique_work row goes through a confirmation that states what would be lost, with `-D` behind its own confirmation;
- a `remote` row is tickable only when proven;
- the stacked-PR `base_of_open_pr` check is blind under a failed or skipped fetch.

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
>   WT_MTPLX_PIDFILE="$D/mtplx.pid" WT_REC_PIDFILE="$D/rec.pid" WT_SUSHI_PIDFILE="$D/sushi.pid" "$D/wt" model list
> ```
>
> This redirects files, not servers: a start, stop or probe still reaches whatever answers at the registry's `base_url`s, and `wt cloud-sync` still runs the real `ollama`. `cmd/wt`'s `TestMain` (through `lifecycle.IsolateProcessesForTest`) sets `WT_SUSHI_PIDFILE`, like every other process family's pidfile variable, to a path that does not exist.
