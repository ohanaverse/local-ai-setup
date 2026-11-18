# CLAUDE.md

Rules and gotchas live here; mechanisms, function lists and step-by-step detail live in [docs/internals/](docs/internals/) (each section names its page; [packages.md](docs/internals/packages.md) lists every file and package).

## What this repo is

The `wt` binary (`cmd/wt-session/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--root` (the primary checkout, from anywhere), `--cwd` (the current directory; no git repository needed), `-W <name>` (named worktree; a new name is created and said so, `--new` is a no-op), or the worktree picker (internal/cleanup's rows: the root, the default branch and held rows included, each launchable). Outside a repository `-W` refuses (`not in a git repo`).
2. **What** — `-A <name>`: an LLM agent, or `shell` for a plain command. **Never defaulted**: omitting `-A` always shows the agent/command picker.
3. **Which model** — `-M <provider>/<name>`, filtered by `-T <tags>` / `-F <family>`, or picked (eligible models rotate on successive launches).

`bin/*-wt` are shims that forward to `wt` (`claude-wt` → `wt --agent claude`), so `wt` must be on `$PATH`. All logic lives in Go.

## Installation

`make install` builds `bin/wt-session` and `bin/wt` (the same binary under two names: `wt` for the commands you type, `wt-session` for the session client's name beside `wt-model`; both print `wt <version>`) and copies them and the shims to `~/.local/bin/` (Go 1.26.7, see `go.mod`; `make help` lists targets). `make test` needs `make install` first — it exercises the installed binary.

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`); a drifted one is rejected by AMFI ("Taskgated Invalid Signature") and SIGKILLs the binary (exit 137).

## Key flags

Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; missing flags come from pickers or defaults. The agent is never defaulted.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use the worktree for `<name>`; skip the worktree picker. An existing worktree is reused with no question of its own (one `launching in …` line when nothing warns). A name that creates anything is announced first by its creation line, one shape per case: `creating branch <b> and worktree <p>` (a new branch), `creating worktree <p> for branch <b>` (a worktree for an existing branch), `creating worktree <p> for branch <b> tracking <r>` (a local branch tracking a remote-tracking name). The one launch question is asked before it only when a warning also applies (on a terminal). The root's own branch names the root and launches there with the root warning |
| `--new` | Accepted and ignored: a name that matches nothing is created and said so either way (kept for old scripts). A usage error without `-W` or with `--cwd`/`--root`. The picker's "+ New worktree…" prompt never asks |
| `-A <name>`, `--agent <name>` | Pin the agent or `shell`; never defaulted |
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`: cloud or running → launch; a non-running local starts; a blocked model errors with that row's reason. Without `-A`, the agent is prompted first. `-M profile/<name>` pins a slot profile |
| `--replace` | With `-M`, start the model even if it means stopping a running one (same provider only) |
| `--others=keep\|stop` | Answer without asking when a start loads a model beside other providers' models (picker: `-M` row only). Unset: ask; with no TTY keep them and warn once (#412) |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR) |
| `-F <family>`, `--family <family>` | Filter models by family (comma-delimited, OR) |
| `--root` | Launch in the repository's primary checkout, found from anywhere (a linked worktree or subdirectory too); the flag consents to the root warning, which is then not shown (any other warning still asks). Exclusive with `--cwd` and `-W`; refuses outside a repository and in a bare one |
| `--cwd` | Launch in the current directory, repository or not. Standing in the repository root it warns and proceeds, terminal or not (a terminal with any warning is asked the one launch question first). Takes no value: a first argument before `--` naming an existing directory is refused (#392) unless `shell`'s first word is a bare name on `$PATH` |
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

- The client session ends first (`runTail.End`, the TUI's `exitTail.end`); there is no stop picker (the model server stops an idle model after its grace; `survey.Picker`, `ReleaseSession` and `internal/refcount` were deleted with Task 19). The summary line (`agents.Summary`) never affects the exit code.
- **wt exits with the agent's status on both paths, after the whole tail** (#412): non-TUI `os.Exit(ee.ExitCode())`; TUI `tui.Run` returns the error and `runTUI` makes an `exitCodeError`. **Never `os.Exit` inside `internal/tui`.**
- A batch of host stops (`wt stop <provider>`/`--all`) costs one LiteLLM restart and one ollama wait (#379, #142); a stop ollama accepted and still lists at the bound is `*lifecycle.StillFinishingError`, not a failure.
- A slot-profile session's tail is end → summary (no survey, no price notice).
- **There is no post-exit prompt.** `internal/exitprompt` (its `Offer`) is **deleted** (Task 304): nothing is asked when the agent exits and an untouched session worktree is never offered for removal — cleanup is user-driven (`wt worktrees`, `wt remove`). The agent's exit status (above) is the tail's only outcome. A `[launch] exit_prompt` key is tolerated and ignored (`config.LaunchState` round-trips it; no value is validated or consulted) and a non-empty value prints one note per process: `wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored`.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md) before changing the summary line, the stale-pricing notice, the survey, session handling, or profiles.

## Session survey (Go)

**The survey is switched off** (`survey.Enabled = false`, #136); the code is kept and tested, and `wt stats` still reports stored answers. Keep `drainTTYInput` after the last question. [Details](docs/internals/launch-flow.md#session-survey-go).

## Docs

`docs/`: [style-guide.md](docs/style-guide.md) (**the CLI and TUI standard for every wt tool** — message prefixes, prompts, exit codes, JSON, layout, keys, look; read it before adding a command, a message or a screen, and cite rule IDs in review), `wt-*.md` command references, `wt-agents/` per-agent reference (`README.md`: LiteLLM routes and post-exit order; `profiles.md`, `slot-profiles.md`), `configuration.md`, `internals/` (code-level notes, one page per section below), `superpowers/` (specs and plans). `../CLAUDE.md`: monorepo commands and config ownership (wt is the only writer of `registry.toml`, and owns `~/.config/agent-wt/config.toml`).

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters.

- **Test seams are per-call values, not package globals** — pass hooks and inputs where the call happens (`rootmove.Engine.Hooks`, `cleanup.GatherInputs.PRList`/`PRBaseList`, `internal/lifecycle`'s `env` struct), so parallel tests cannot leak stub state into each other. A package-var seam (`var x = realX`, swapped and restored around the test) is the migrating exception, not the rule: convert it to per-call when you touch its package.
- **Tests stay off the developer's machine**: each package's `TestMain` isolates what it reaches — `lifecycle.IsolateProcessesForTest()` (every process family's pidfile, signals, the wired-limit read), `tiers.IsolateForTest` (model-table packages), `config.IsolateConfigHomeForTest` (anything reaching `config.Dir()`), `gitenv.IsolateForTest()` (anything that can run git; `TestEveryPackageThatRunsGitIsolatesIt`). A new package needs the same; a session path runs against a fake model server (`cmd/wt-session/boundary_test.go` keeps it off the registry and the lifecycle); no test runs `psql`; scratch registries go through `WT_REGISTRY`.
- **Assert on unexported functions directly**; parsing rendered lipgloss output flakes under forced-color ANSI.
- **A test that creates worktrees builds a fresh repo per case** (`t.TempDir()` + `gitInit`): one wrong success leaves `.worktrees/<name>`, and the path-collision error then masks every later case. Use a branch name like `topic`, not `feature`, when a case also needs `feature/<x>` (git forbids a ref that is both a leaf and a directory).
- **A test that depends on filesystem case-sensitivity probes and skips** (write `CaseProbe`, stat `caseprobe`), and asserts on `git for-each-ref` output, never `refExists`: on a case-insensitive volume `show-ref` resolves `Feature/x` through `feature/x`'s ref file and says yes.
- **A test's `WarmupSeconds` must outlast the helper server's first answer.** Each warmup request is capped at the time left in the window, so a 1 s budget on a loaded runner fails on the deadline, not on the response you meant to assert on. Use 3 s or more.
- **The full suite is slow** (`go test ./...` ~10 min: `internal/removal` ~6 min, `rescue` ~3 min, `tui` ~2 min, `cleanup` ~2 min): run it with `run_in_background` or a long timeout, and scope with `-run` while iterating.
- **A renamed function or changed launch note is quoted in prose**: before finishing, `grep -rn <name>` over `CLAUDE.md`, `docs/`, `../docs/contracts/` and test comments (comments there still said `holdRoot` after the rename).

Read [docs/internals/testing.md](docs/internals/testing.md) before adding a seam, a `TestMain`, or a test that launches, runs git, starts a model, or touches routes.

```bash
go test ./...
go test ./internal/agents -run TestOpenCodeOllamaPrefix   # one test
go vet ./...
make check   # shellcheck, shfmt, gofmt -l (wt-ci's gate); `make format` writes both
make -C .. test-all   # CI-equivalent: root lint + llmbench + modeltiers + wt
```

## Go module

Module root is `wt/` (`go.mod`: `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt-session`, `cmd/wt-model` plus `internal/{config,provider,tomlw,tiers,candidates,slotedit,cloudsync,rotation,usage,hoststop,survey,agents,profiles,guard,sessionclaim,rootclaim,worktree,initseed,gitenv,gittest,scratchdir,launchwarn,themes,tui,tuilayout,configeditor,catalog,localmodels,modeladmin,lifecycle,litellm,spend,smoke,cleanup,dirtsnap,removal,rescue,rootmove,modelapi,modelclient,tokenfile,sessionreg,modeljobs,modelgateway,modelserver}`.

**Never use the stock `toml.Encoder` on the registry** (`internal/tomlw` reproduces tomli-w's layout). `internal/spend` never takes the connection string as a `psql` argument (#282).

Every file and package in full: [docs/internals/packages.md](docs/internals/packages.md).

## Config (Go)

`~/.config/agent-wt/config.toml` is wt-owned and holds **only Agents, DefaultTag, the hand-edited `[model-server]` table (`config.ModelServerState`: url, token_file — a session that asks for a model needs it), the `[litellm]` table (the read-only fallback for the proxy pair the model server reads — the pair lives in the `~/.config/wt-model/` home, written by `wt-model litellm set --url URL --api-key KEY`; `enabled` is ignored) and the `[launch]` table** (`config.LaunchState`; its `exit_prompt` key is tolerated and ignored — parsed and round-tripped by a save, validated and consulted by nothing, and a non-empty value prints one note per process: `wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored`); Providers/Models live in the registry and this file's write path never touches them.

- **Writers go through `(*Config).PatchSave` or `lockedApply`** (flock, re-read fresh, mutate only their own fields); `Save(cfg)` only for a known-fresh `Config`. **Any read-modify-write of a wt-owned file flocks via `config.WithFileLock`** — the one helper (`WithLock` for config.toml, `SetProxy` for the `~/.config/wt-model/` home, `UpdateRegistry` for registry.toml, `slotedit.Apply` for slot-profiles.toml); a new write path without it can silently lose a concurrent edit.
- `wt config` launches even when `config.toml` fails validation, so the editor can repair it.

Read [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md) before changing config load/save, migrations, the proxy pair (`internal/config/proxyhome.go`), registry loading, catalog membership, or location validation.

## LiteLLM routing state (wt-owned)

Since the flip (Task 18) every session needs a model server (`[model-server]` url + token_file in `config.toml`, or `WT_MODEL_*`): a model-needing path with none refuses with `*modelclient.NotConfiguredError` before anything starts (exit 1), except that a launch asking for no model (no `-M`/`-T`/`-F`) with *neither* url nor token set runs the agent natively (#615: `nativeFallback`, `launchNative`, `agents.NativeModel`, the TUI's nil client; one bare `wt:` line (not a `note:`, style guide M4), said after the launch gate in the picker, zero route, no client session, no rotation or usage record, no profile overlay; a half-configured server still refuses); host commands read the registry through `app.hostConfig()` and session paths read `app.sessionCfg()` (there is no `app.cfg`). The proxy pair (`url`/`api_key`) is host state `wt-model serve` reads — the `~/.config/wt-model/` home first, the `[litellm]` table of `config.toml` its read-only fallback — set with `wt-model litellm set --url URL --api-key KEY` (the `wt litellm set` twin writes the same home) and shown by `wt litellm status`; the table's `enabled` is retired (one note per process, kept on save) and `wt litellm on|off` are hidden stubs exiting 1. Reference: [docs/wt-session.md](docs/wt-session.md).

## Registry

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. `config.Load` reads it fail-closed and never writes it; wt writes it through `config.UpdateRegistry` alone. Path precedence: `WT_REGISTRY` > `MODELMAN_REGISTRY` (legacy alias) > `XDG_CONFIG_HOME` > `~/.config`; llmbench's `registry_path` uses the same order — keep the two in sync (each has a precedence test).

- **`ResolveLocation` is the one judge of a location** (`config.ErrLocation`).
- **On-disk and running state come from live probes**; no per-model state key exists, and whether a model is routed is `wt litellm list`.
- **`config.UpdateRegistry` is the only write path**; its `apply` must be pure (it may run up to three times). `RegistryDoc` operations are patch-shaped — never round-trip a `config.Model` into the file.
- **An id on more than one model row is refused, never resolved to the first row** (`config.ErrModelAmbiguous`); find a row through `RegistryDoc.Model`/`PatchModel`/`RemoveModel`.
- **A malformed `fetch`/`draft` or `cost.time_prices` row reads as absent and never fails the load** (`Model.Malformed()`). Get a zone through `config.loadZone`, not `time.LoadLocation`.
- **`serve_args` is mtplx-only extra argv, one value judge** (#432): `provider.FamilySpec.ServeArgs` says which family takes it, `config.ServeArgsProblems` is the one judge (the loader's `Model.Malformed()`, the writer's `validateModelRow`, `wt model add|edit`, and `lifecycle.serveArgsFor` at a start, which stops with `*ServeArgsError` before spawning), and `provider.ProcessSpec.SpawnArgs`/`OwnedFlags` build the argv and derive the refused flags from the spec. A malformed value reads as absent and never fails the load. A change applies from the next start (wt cannot see a running server's flags). It is not in `docs/contracts/model-list.sample.json` (an omitempty key, like `target`/`draft`; `TestModelListJSONOutputMatchesTheFixture` exercises it) and llmbench ignores it.
- **`llamacpp` is still a provider row** (`internal/provider/table.go`) — retired as a *backend* on 2026-09-07 (no LaunchAgent, no llmbench backend) but never removed from the table, so a registry model naming that provider still resolves. It has no `Seed`, so it is never created automatically; `docs/reference/provider-artifacts.md` has the retirement record.
- **Read-side schemas are pinned by contract fixtures** in `../docs/contracts/`: a key added to or renamed in `modelListJSON`/`modelListRow` (`cmd/wt-session/model_list.go`) fails `cmd/wt-session/model_list_contract_test.go` until `model-list.sample.json` has it (modeltiers reads that document as its whole inventory).
- A launch reads no registry (its models come from the model server); the old bare passthrough for an agent with no `config.toml` entry is unreachable since the flip (R13: a session synthesizes every model-driven driver's agent); the native launch (#615) is a different path with the zero route.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug.** Fix with `wt model init` on that machine (a provider it has no default row for is named in its output: a hand edit of `registry.toml`), not a code change here.

> **Fixture gotcha.** `config.toml` lives in `$XDG_CONFIG_HOME/agent-wt/`, `registry.toml` in `$XDG_CONFIG_HOME/local-ai/` — fixtures populate both. LiteLLM's `config.yaml` follows neither, so a fixture that redirects the registry also names it (`WT_LITELLM_CONFIG` or `litellm.Options.Path`), or route writes refuse with `litellm.ErrRegistryRedirected`. `migrateConfigSchema` always ensures an `agy` agent, so a registry fixture with agents needs an `agy` provider or `Load` fails with `unknown provider "agy"`.

Full rules: [docs/internals/config-and-registry.md](docs/internals/config-and-registry.md#registry). Adding a provider: the `wt-adding-a-provider` skill.

## Local-model resolution

Every model row wt shows or resolves is built by `localmodels.CatalogRows` from the agent's eligible list plus one live `localmodels.Inventory` snapshot (no stored flag), or by `catalog.ModelRows` where no probe is read (the standalone picker); `internal/catalog` owns the `Row` and what a row does. A row's action is **launch** (cloud, or local and running), **start** (local ollama/omlx/omlx-6bit/mtplx/rec/sushi, not running or still loading) or **block**.

- **`config.Provider.Origin` is the one reading of a provider's address** (#348): the probes and the LiteLLM routes use it, and so must any new reader. A test never binds or dials the mtplx default port 8003.
- **`localmodels.ServedIDs` is the one answer to "what is this server serving"** (#201).
- A `-M` pin is looked up among all rows (`catalog.Find`, else `localmodels.MissingReason`, on the model server); without `-M`, the non-TUI path picks only launch rows of the server's catalog — only a pin may start a model.
- **A slot-profile row is not a model row** (`Model` holds only `profile/<name>`, `Row.Profile` the rest): test `r.Profile != nil` before reading any other `Model` field, or launching/starting `Model`. A session resolves profiles against the model server's catalog only (`catalog.ResolveSlotProfileWire`).

Read [docs/internals/local-models.md](docs/internals/local-models.md) before changing row policy, the inventory probes, omlx loaded-state detection, pin resolution, rotation, or `wt start`/`wt stop`.

## Lifecycle (`internal/lifecycle`)

The start/stop engine (`Start`, `Stop`, `BeginStops` + `SettleRoutes`); backends ollama (`Shared`), omlx (`Pool`), every process-server family (mtplx, rec and sushi, `Exclusive`; one spec-driven `processServer` backend, a `Process` row in `internal/provider/table.go`). rec is `ScopeMachine` (one instance per machine, found through `rec status`) and `ServedOnly` (no artifacts: only its registered `rec/flint` is listed). sushi is `ScopePort` and `ServedOnly`: one model-less `sushi serve` on port 12345 that loads the pack on the first request naming it, so wt lists no artifacts for it either.

- **Anything that displaces a model needs `AllowReplace`** (`*OccupiedError`); unknown occupancy is `*OccupancyUnknownError`, never assumed empty. Other providers' models beside the target need `Options.Others` (#412); `--replace` never answers it.
- **A start/stop wt performs updates `config.yaml`** through `internal/litellm` (route hook). Only the host runs the engine (the model server's jobs, `wt stop <provider>`/`--all`); a session never starts, stops or routes a model in process.
- **Call `WaitPendingRoutes()` on every host process exit and before handing a model to a client**: the `config.yaml` write is synchronous, the proxy restart and readiness wait are async. The restart goroutine runs on `context.WithoutCancel(ctx)` on purpose.
- **A `Start` that returned nil is followed by one `SettleStart(ctx, out, cfg, target)`** (#343, #349), on the model server's start job.
- **A failed start is printed once, by `main`** (#344): the root command silences cobra's `Error:` line for the whole tree, and `silenceUsageOnError` (the walk at the end of `rootCmd`) silences usage on every runnable subcommand except one carrying `usageOnMistakeAnnotation` (`stop`, `smoke`, which print usage themselves on a usage mistake; `TestEveryRunnableCommandSilencesUsage` pins the set). A new command needs nothing; `TestOneErrorLineThroughTheRealTree` shows the one-line shape.
- **An ollama stop ollama accepted but has not carried out is `*StillFinishingError`, never nil** (#379); only the stop loop reads it as an outcome (exit 0). The TUI stops no model (`TestTUIStopsNoModel`).
- **wt signals a process only after identifying it** (`server_loading.go`): a pid is never enough (user's own, a process server's exact argv on the provider's port, recorded start time, rechecked before SIGKILL). The pidfile stays a bare pid (llmbench shares it).
- **An empty `Stop` means "signal the verified pid"** (sushi has no stop command, and running the bare binary is not a stop): `processServer.stop` calls `stopByPid`, which shares `signalVerifiedServer` with `stopLoadingServer` (SIGTERM to the group when wt recorded the start, else the pid alone; `stopGrace` 10 s; SIGKILL only if the same start time remains; the pidfile and `.wt` go once the process is gone; then `stopByPid` waits `stopTimeout` for the port to close). `loadingServer` refuses a pidfile pid <= 1 or wt's own. No verified pid: a port that does not answer is nothing to stop (nil), one that answers is `*NotStartedByWtError` ("… was not started by wt; stop it yourself"). `TestTableInvariants` requires `PidfileDefault`, `Subcommand` and `PortFlag` of a spec with an empty `Stop`. A sushi llmbench started has a bare-pid pidfile and no `.wt` record: `wt stop` still stops it when its argv matches, with no start-time check against pid reuse.
- **`ProcessSpec.WarmupSeconds` and `ReadyHTTP`.** `WarmupSeconds` (contract key `warmup_seconds`; mtplx 120, rec 120, sushi 400; required and positive, `warmupBudget` fails a start without one) is the warmup window and the cap on each chat request. `ReadyHTTP` (contract kind `http-get`, `Path "/health"`) is ready on exactly HTTP 200 and names no model: sushi answers it before any pack is loaded, so the warmup performs the cold load and then `confirmLoaded` requires the exact id among the loaded models.
- **The loaded-aware served list is opt-in per family** (`FamilySpec.LoadedOnly`, contract key `loaded_only`; today only sushi). `ServedIDs` (so `wt served` and the inventory's Running) reads such a family through `localmodels.FetchLoadedModelIDsKeyed`, which drops an entry only when its `loaded` is the JSON literal `false`; absent, null or non-bool keeps it. `confirmLoaded` always uses it. Every other call, including every other family's served list, the readiness poll and `requirePackListed` (exact id; an empty list skips the check; a failed fetch fails the start), goes through `FetchModelIDsKeyed`, which lists every id. omlx keeps its own pool branch.
- **A busy port holding a wt-started `ReadyHTTP` server is reused, not refused** (`processServer.start`): sushi answers `/health` with no pack loaded (or mid cold-load), so nothing shows Running and the port is busy. When `loadingServer` verifies the pidfile's process as wt's own, the start spawns nothing and signals nothing; it runs the pack check, the warmup (which loads the pack into that server) and `confirmLoaded`. A failure there does not kill it (it was not spawned by this start). A busy port with no verified pidfile is still `*PortBusyError`.
- **`config.Load` derives sushi for claude and pi in memory** when the registry holds a sushi model (`deriveSushiAgents`, `config.SushiAgents`): `wt model add` still writes it (`EnsureAgentProvider`), but a pack registered any other way works too. Removing sushi from an agent sticks only while the registry has no sushi model.
- **The wired-limit preflight** (`wiredlimit.go`, `wiredLimitFamilies` gates it by family id, today `sushi`) runs in `start` before the others question and before any stop. It reads `sysctl -n iogpu.wired_limit_mb hw.memsize` (0 means 75% of `hw.memsize`) and needs `floor(total *.safetensors bytes * 11 / 10 / 1048576)` MiB (`ngram_table.bin` excluded; 49,506,491,792 bytes, i.e. 47,213 MiB of bytes, is 51,934 MiB). Below that it fails with `sudo sysctl iogpu.wired_limit_mb=59000` (resets on reboot); wt never sets the sysctl. What it cannot measure prints `wt: skipping the memory check (<why>)` and the start goes on. llmbench applies the same integer rule, gated by the contract's `wired_limit` (a lifecycle test pins `wiredLimitFamilies` to it) and reading the pack directory from the contract's `model_dir` (the contract test pins it to `DefaultModelDir`). A failed sushi warmup gets the fix appended only for eight phrases (`memoryRefusalMarkers`; the bare word `iogpu` is not one), pinned by a test on each side. A warmup request is capped at the time left in the `WarmupSeconds` window, so the budget is the most a warmup can take.
- **A key the registry names goes with every probe of a process server** (`localmodels.FamilyAPIKeyErr`): a `secret_ref` that cannot be read ends a start before the spawn (`*KeyUnreadableError`); a refused key is `localmodels.ErrKeyRefused` (family `partial`, not `down`). rec's key (`exec:rec auth token`) also goes into its LiteLLM route. A `ScopeMachine` family's existing instance is adopted when it serves the target at the registry's address and asked about before it is replaced otherwise (details and known limits: `docs/internals/local-models.md`).

Read [docs/internals/local-models.md](docs/internals/local-models.md#lifecycle-internallifecycle) before changing start/stop, the route hook, restart or readiness waits, or replace.

## Rotation (Go)

Each successful single-model launch records its id in `~/.config/agent-wt/rotation.state` (`rotation.RecordFor`, which also writes `last-picked.state` and `usage.jsonl`); the picker cursor lands on the model after it (`FirstAfter`).

- **Marker and pointer are two files**: `rotation.state` stays one bare registry id; `last-picked.state` holds the `> ` marker's row. A slot-profile launch calls `RecordProfile` (never `rotation.state`); a profile row never rotates.
- **The sort order is also the default selection** — changing `sortRows` changes what a bare Enter picks. The marker is plain ASCII `> ` (Unicode misaligns CJK terminals).

Details and pinned tests: [docs/internals/local-models.md](docs/internals/local-models.md#rotation-go).

## Agents (Go)

Each agent registers a `Driver`; `BuildLaunchCmdGateway(agent, m, worktreePath, yolo, gw, models, extraArgs)` is the one launch constructor for both launch paths and `wt smoke` (its route is `agents.GatewayRoute(gw, m)`).

- **The route is always the model server's gateway**: there are no direct routes (`config.ResolveRoute`, the protocol intersection and `IsLitellm` were deleted with Task 19). `Route.ModelRef` is the gateway id (`m.GatewayID`, else `m.ID`); a native model gets the zero `Route` and never sees the gateway token.
- **copilot uses the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters.
- **claude maps all four model slots on every non-native single-model launch** (`claudeSlotVars`); an applied profile's `env` is appended later and wins.
- **claude declares a gateway model's context window** (#405): `CLAUDE_CODE_MAX_CONTEXT_TOKENS` from the model's `HostContext` — the `context` of its host `tiers.toml` entry, which the model server reports **only when `context_basis` is `host`** (`claudeWindow`; #470: a `largest_row` window, or a file with no basis, is never declared; cloud models only, a local build has none). The single-model launch sets it unless the user's environment already has it; a slot profile sets the smallest window among the slots that have one, provided the default slot has one (`claudeSlotWindow`, #471; opusplan anchors on the smaller of opus and sonnet, none if either is unknown; a slot with no window is ignored, not a veto) unless the profile's env, `config_content.env` or the user's environment names it. The driver reads no `tiers.toml` (tests set `HostContext`).
- agy is a native passthrough, not a command agent: `-A agy` without `-M` errors when more than one agy model is eligible.

Read [docs/internals/agents.md](docs/internals/agents.md) before changing a driver, `Route`, or an optional capability (`Seeder`, `GatewaySyncer`, `ArgSetter`, …). Per-agent env/args: [docs/wt-agents/](docs/wt-agents/). New driver: the `wt-adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml` (`profiles.Resolve`), applied on both launch paths and by `wt smoke`; operator reference [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- Call the `config_content` cleanup explicitly after the run — `runAgentCmd`'s `os.Exit` skips defers.
- Apply order is env/args → `config_content` (with `config_defaults`) → wrapper; load/validate/apply errors degrade to an unprofiled launch with a warning.

**Slot profiles** (`~/.config/agent-wt/slot-profiles.toml`, hand-editable and written by wt only through `wt profile slots add|set|rm` (`internal/slotedit`: dated backup, validate under a `config.WithFileLock` flock, comment-preserving); [docs/wt-agents/slot-profiles.md](docs/wt-agents/slot-profiles.md)) map an agent's model slots to registry models and are pinned with `-M profile/<name>`; claude is the only slot agent. A rule that belongs to one agent goes on that interface and is asked through `profiles.SlotDrivers` (warn-only: `agents.SlotConfigWarner`), never into `internal/profiles` or `internal/catalog`, which do not import `internal/agents` (#418).

**No flag points a claude launch at another LiteLLM** (the LiteLLM url comes only from the proxy pair — the `~/.config/wt-model/` home first, the `[litellm]` table of `config.toml` its read-only fallback). To A/B a proxy setting, run a scratch `litellm --config <scratch>.yaml --port 4001` and start `claude` directly with the env `claudeDriver.BuildSlots` builds, `ANTHROPIC_BASE_URL` at the scratch (`internal/agents/claude_slots.go`: `ANTHROPIC_BASE_URL` / `ANTHROPIC_AUTH_TOKEN`, per-slot `ANTHROPIC_DEFAULT_*_MODEL` plus `_NAME` in the `--settings` env JSON, `--model`). The scratch config holds the omlx key, so delete it afterwards.

- **A slot profile is its own launch unit; the twins are `launchSlotProfileRemote` (`cmd/wt-session`) and `beginRemoteSlotLaunch` (`slot_flow.go`)**: one catalog read, register every `Distinct()` slot model, start the local one only when `catalog.NeedsStart`, build against the gateway; `TestSlotFlowParity` (both packages, `internal/profiles/testdata/slot_launch_remote_parity.txt`) holds them to the same server calls. `profile slots rm|usage` never open a client.
- **The file's problems are warnings, never a stopped wt** (`slotWarningsRemote`; #418, #419).
- **Fail closed, always the gateway, no file written**: `config_content` travels only in inline `--settings`, the gateway token only in the process env.
- **Automatic profiles are never applied to a slot-profile launch** (`applySlotProfile`, never `applyProfileForLaunch`).
- **A default may be an agent alias** (`SlotCapable.DefaultAlias`, #391): use `SlotCheck.RestSlot()`/`FilledOrder()`, never `Profile.Default`.
- **Read each input file once per command** (`wt profile slots usage|candidates`: `tiers.LoadWithFreshness`, `usage.AllActivity`): a second parse re-reads a file that may have changed between them, so a report can mix two generations — and the duplicated I/O buys nothing.

Read [docs/internals/launch-flow.md](docs/internals/launch-flow.md#profiles-internalprofiles) before changing profiles, or its [slot-profile section](docs/internals/launch-flow.md#slot-profile-launch) before changing a slot-profile launch step.

## Smoke test (`wt smoke`)

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model, built like a real launch (`agents.BuildLaunchCmdGateway`), and reports PASS/FAIL/SKIP ([docs/wt-smoke.md](docs/wt-smoke.md)). Each row runs in its own temporary git repo unless `--cwd` (standing in a repository root that warns and proceeds, terminal or not); permission checks are off (yolo forced on except codex) and the directory is not a sandbox (#193). A model fallback is FAIL; the default prompt never contains its sentinel verbatim. `--tools` is the tool-call check (a nonce planted in the row's directory, read with a tool) and `--hops N` chains N dependent reads; both are exclusive with `--prompt` and `--cwd`, and their combination is validated before the model is resolved.

**`runSmoke` always runs `runSmokeRemote`** (no model server configured: the refusal) (client session: `smoke.CandidatesWire`, one client session for the run, a start job only when `catalog.NeedsStart`, rows through `smoke.RunRowGateway`, no exit stop picker; a gateway 503 is rerun once, a gateway that stays down is SKIP, never FAIL; test seam: the `smoke.RowOptions` value `RunRowGateway` takes, zero in production); the shared row loop is `(*app).runSmokeRows`. [docs/wt-smoke.md](docs/wt-smoke.md) "On the model server".

Read [docs/internals/smoke.md](docs/internals/smoke.md) before changing row directories, yolo, process-group cleanup, env stripping, Ctrl+C, or timeouts (180s cloud, 900s local).

## Start/stop (`wt start`, `wt stop`)

`cmd/wt-session/model_cmds.go`; [docs/wt-start-stop.md](docs/wt-start-stop.md); code notes: [docs/internals/local-models.md](docs/internals/local-models.md#startstop-wt-start-wt-stop).

## Cloud sync (`wt cloud-sync`)

`internal/cloudsync` decides what a sync changes (no I/O, no clock); `cmd/wt-session/cloudsync*.go` owns fetches, the ollama CLI, the confirmation, the one registry write, the route sync and exit codes. Behavior: [docs/wt-cloud-sync.md](docs/wt-cloud-sync.md); operator procedure (parser repair after exit 3): the `wt-cloud-sync` skill. **Change the page and the skill with the behavior** — both quote its messages.

- **`ParsePricing` is the only code that knows the pricing page's HTML**; anything that stops looking like a price table is a `*ParseError`, never a short catalog (it would plan removing every ollama cloud entry).
- **The plan's text is the plan** (`CatalogPlan.Format`/`PricePlan.Format`): deterministic, no clock, compared with the re-plan under the registry lock; `Apply` methods are pure and write only the shown diff.
- **Exit codes 2 to 5 name a flow that changed nothing and win over 1** (2 and 3 are ollama's; 4 and 5 either flow's). **`source = "manual"` keeps a hand-added cloud model through a sync until its source lists it** (`wt model add` sets it, `wt model edit --source` marks older entries); a vanished OpenRouter model is removed only after approval. A flow whose provider the registry does not use is skipped (exit 0); cloud-sync never seeds a provider row.

Read [docs/internals/cloud-sync.md](docs/internals/cloud-sync.md) before changing a planner or parser, either `Format`, `cost.time_prices` rows, the removal digest, the registry write, an exit code, or how the ollama CLI is run.

## LiteLLM routes (`wt litellm`)

`cmd/wt-session/litellm.go` + `internal/litellm`, the sole implementation of `config.yaml` route management. Subcommands `sync|list|providers|status|set|restart` (`on|off` are retired hidden stubs, 2026-11-15); `set|status` manage the proxy pair in the `~/.config/wt-model/` home (setup is `wt-model litellm set --url URL --api-key KEY`, `wt-model litellm set|status` its twins), `sync` is the only CLI route write, `restart` is what `bin/update/litellm` runs. Operator doc: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned); JSON shapes: [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json).

- **wt owns only its own rows** (`model_info.wt_managed: true`); a hand-written row keeps everything except an empty ollama `api_base` (#202).
- **`sync` trusts only `ok` families** (an untrustworthy probe freezes the family with a warning) and **never takes "the first row with the id"** on a duplicated id.
- **An `os.environ/VAR` api_base stays as written**; "set for the proxy" comes from `litellm.ProxyEnv`, not `os.Getenv`. `status` shows the api key only as `api_key_set` / last 4 chars.
- Paths: `WT_LITELLM_CONFIG` (default `~/.config/litellm/config.yaml`), `WT_LITELLM_RESTART_CMD`; a redirected registry without `WT_LITELLM_CONFIG` is refused (`litellm.ErrRegistryRedirected`). **A restarting command can block about 3.5 minutes** (in-flight requests drain, #377); a restart waits up to 30 s for the proxy to answer (#382), but only when a `[litellm]` url is configured — with none it is made and not waited for (the state of a fresh install before `wt-model litellm set`). Manual forced restart: `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. Test seams `bounce` (`litellm.StubProxyReadinessForTest`) and `proxyProc` refuse inside a test binary.
- **`EnsureSettings` (`internal/litellm/configfile.go`) applies the launcher-required settings on every write:** `litellm_settings.drop_params` and `use_chat_completions_url_for_anthropic_messages` value-enforced to `true`, plus `general_settings.cancel_on_disconnect: true` only when absent (a hand-written `false` is the opt-out; merge keys are followed; an aliased or root-merged `general_settings` is skipped with a sync warning). Without it LiteLLM never cancels an abandoned non-streamed request upstream (#399); a cancelled one logs as a failed `499`. The first write to an older config adds the key and restarts the proxy once. **Adding a key here fails every "settled config needs no change" fixture** (`cmd/wt-session/litellm_test.go`, `internal/litellm/{discovered,service}_test.go`) until it carries the key; `go test ./...` lists them.
- The spend database (`litellm.DatabaseURL()`) is read-only, only for the model server's `GET spend` (which `wt stats` asks); errors never print a value.

Read [docs/internals/litellm-routes.md](docs/internals/litellm-routes.md) before changing sync's desired set, row ownership or adoption, the `api_base` repair, ollama-serve warnings, or `config.yaml` I/O.

## Guard, init seeding, sessions

- `internal/guard` manages the `block-main-commit` hook (`Install` is idempotent and appends; `git rev-parse --git-common-dir` covers all worktrees). A first-time install prints `wt: installed the block-main-commit hook (--no-guard to skip)` on stderr on every install path (launch-time and `wt --init`); a hook already present is silent.
- `wt --init` seeds `AGENTS.md` and a pointer file (`cmd/wt-session/main.go`, before any agent-binary requirement); existing files are never overwritten.
- wt launches a fresh agent every time and leaves session handling to the agent (#198, #204): continue with the agent's own flags after `--` (`claude-wt -- --continue`). Why the session lookup stays removed: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#sessions-go).
- Every launch claims the worktree the agent runs in — the root or a linked one — under `<git-common-dir>/wt-sessions/<pid>` (`internal/sessionclaim`), so other launches' warnings name the session (`held`) exactly while the agent runs; the warnings judge the resolved directory, and `-W <root's branch>` launches in the root with the root warning. A claims failure only prints a note. Stale claims (a dead or reused pid) are deleted only by the writers' locked prune and `sessionclaim.Prune` — the latter on the `wt worktrees` list's `F` refresh and a fetch-first `--json`, never on list-open and never with `--no-fetch`; reader scans delete nothing. The launch warnings (`internal/launchwarn`) run through `cmd/wt-session`'s `confirmLaunch` in `RunE` on the resolved directory before `runLaunchPath` — for `--root` (consent for the root warning alone), `-W` (a creation is announced before it exists) and `--cwd` — in `runSmoke` before the model is resolved for `wt smoke --cwd`, and in the TUI at the run commands' commit point (`internal/tui/launchgate.go`'s `launchGate`, which claims the launch's worktree there; a preset path RunE already confirmed is claimed but never asked again — a root launch is asked at most once); one default-yes question (`continue? [Y/n]`) covers every warning on a terminal, and with no terminal the warnings print and the launch proceeds. The root warning is silent when a parent wt session already holds the root *live* (a nested `shell-wt --cwd` under an agent at the root; a stale claim whose pid was recycled does not count — `docs/contracts/session-claims.md`). Where the warnings run and the claim is written and released: [docs/internals/launch-flow.md](docs/internals/launch-flow.md#session-claims-internalsessionclaim-internalrootclaim); the file format a reader needs (the retired repo-cleanup script read it too): [docs/contracts/session-claims.md](../docs/contracts/session-claims.md).

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`): worktree → agent+command → model picker → launch, plus catalog, starting, launching and replace-confirm screens (every model comes from the model server); a picker is skipped when its selection is resolved.

- **A view fits the terminal** (`internal/tuilayout`): every bubbles list screen renders through a `listFrame`, sized only by `fitTo` in `Update`; the worktree picker's row list is sized by `fitLists`, fits its row table (`rowtable.go` over `tuilayout.Columns`) to its width every frame and lays itself out with blank filler pinning the footer to the last terminal line (`TestEveryListPhaseFitsTheTerminal` covers both).
- **The update goroutine never waits**: every model-server call (catalog, resolve, start job, launch prep) runs in a command.
- **A start is a job on the model server** (`remote_start.go`): its screen shows the job's step; ctrl+c detaches, `c` cancels the job.
- **A slot-profile launch prepares behind the launching screen** (`beginRemoteSlotLaunch`); no route is written and no proxy restarted.
- **A model table is priced once, when built** (`modelrows.go`): read `tableRow.price()`/`timePriced()`, never `Model.Cost`'s flat fields or a clock.
- **TIER's score orders the picker's model rows** (descending, within the running-local and other bands) but is never routed on; a missing or unparseable `tiers.toml` is `-` and unscored, never an error.
- A test that sets state by hand goes through `Update` before reading `View()`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`) skip the TUI; `-W`/`--cwd` need a TTY when `-A` or `-M` is omitted. A launch warning's question is asked only on a terminal; with no TTY the warnings print and the launch proceeds (nothing is refused, and a new name is still created and announced).

Read [docs/internals/tui.md](docs/internals/tui.md) before changing layout, start-on-select, or the model table.

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate`) and creation (`Resolve` then `Plan.Ensure` for `-W`; the picker's new-worktree, bare-branch and remote-only rows bind a `Plan` at the pick and their launch gate ensures it at the commit point, `internal/tui/launchgate.go`'s `launchCreation`, so a declined gate creates nothing). Every function takes `dir` (repo root) first for testability.

> **Worktree paths derive from the full branch name** (`worktreeDirName`): path separators on any platform, colon, and ASCII control characters flatten to `-`, so `feature/x` → `.worktrees/feature-x` and `feature/x` + `bug/x` get distinct directories (once they raced for `.worktrees/x`). A name that flattens onto another's directory (`feature/x` vs `feature-x`) errors "already in use by branch …" on every creation path, `-W` and the picker alike (one `pathCollision` check); the check asks the filesystem (`os.SameFile`) so `Feature/x` vs `feature/x` collide on a case-insensitive volume (macOS default) and stay distinct on a case-sensitive one; a flattened name over 255 bytes is refused with a "shorten the branch name" error (git stores a long ref as nested directories, but the flattened name is one entry); a worktree from the old last-component layout (`.worktrees/x` holding `feature/x`) keeps resolving by branch name (the reuse loop matches branches, never paths). Derived `.`/`..` are rejected — `filepath.Join` would clean `..` to the repo root and `.` to the container.

> **Validate the derived path before running git** (`worktreePath`/`newWorktreePath`): `git worktree add -b` creates the branch first, so a path that then fails (too long, taken) leaves a stray branch. Every creation site goes through those two helpers; a new site must too.

> **A launch that has a directory needs no repository**: the repo root handed to `runLaunchPath` gates the guard install and nothing else (state is keyed by model, agent and pid). Only `-W` and the worktree picker refuse outside one; don't key new launch state on the repository. [Details](docs/internals/launch-flow.md#launch-directory-and-the-repository).

> **The picker fetches on the user's `F`, never when it opens** (`worktree.FetchRemotes`: `git fetch --all --prune`, 5s; the outcome is rendered in the header — `fetch: ok, …` / `fetch: FAILED (…)` — a failure is never silently ignored); `Enumerate` (only `--debug-worktrees` still uses it) does not, `-W`/`--cwd` skip the picker, and creation never fetches (`Resolve`, `Plan.Ensure` and `EnsureForName` read only the remote-tracking refs already present).

> **The picker is a view over `internal/cleanup`'s rows and its PR tags never hold it up** (`internal/tui/picker_rows.go`, #384, #390): it paints facts first (`cleanup.GatherFacts`: names, refs, base, claims and the git-free hard blockers — every detail cell `-`, every row `Pending`, nothing removable), then `loadRowFillCmd` runs `cleanup.FillDetails` off the update goroutine (`rowsFilledMsg` swaps through `rowsGen` and builds the `RemoveFuncs` from the FILLED snapshot only; an `F` refresh in flight spends the generation, a failure is one `fill failed:` status line), then `loadRowPRsCmd` runs `cleanup.FillPRs` (`?` in the `PR#` column until it lands), the cursor kept; `dropStalePRTags` clears a reused branch name's ended PR (#451). Seams `cleanup.StubPRListForTest`/`StubPRBaseListForTest` or `GatherInputs.PRList`. Its gather sets `OriginRows`, so the row keys `x d o r R` work in it (`enter` still launches). [Details](docs/internals/tui.md).

> **Creation routes create from one plan**: `-W` (`worktree.Resolve`, then `Plan.Ensure`) and every picker creation (`launchCreation.ensure`, the commit point for a typed name's, a bare-branch row's or a remote-only row's plan). The plan is one classification: the creation line the launch warnings print before anything exists (`launchwarn.Creation` → `creationLine`) names the branch, the worktree and the tracking ref — the base is deferred, the frozen `creationLine` does not carry it — and `Plan.Ensure` creates from the same object (the plan still carries the base; a moved base is refused, #525); `EnsureForName` remains as a one-shot wrapper. Nothing records what a route made — the exit prompt's creation records died with it (`internal/exitprompt` **deleted**, Task 304), and nothing is asked when an agent exits. A brand-new branch is made only for a name that matches nothing (`Plan.Creates`): a reused worktree, a new worktree on an existing branch or tracking a remote (the picker's bare-branch and remote-only rows, whose owed creation the launch gate makes — its `Plan.Creates` is false for both), `--cwd` and `--root` never make one.

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and directories inside `.git` have no worktree; don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **The default branch is an ordinary worktree name.** `EnsureForName`/`EnsureForBranch` create `.worktrees/<default>` like any branch (no `refuseDefaultBranch` guard remains; launch risk messaging is `internal/launchwarn`'s), and a second Ensure for the same name reuses the worktree at the derived path (`EnsureForBranch` reports what it made as `Created`). Git alone refuses a second worktree on a branch already checked out elsewhere, and its own error stays the user-visible message: surfaced verbatim from the captured stderr ("… is already used by worktree at …"), never wrapped away or replaced (`gitError`). `-W main` with the root on `main` still resolves to the root first (the reuse loop matches the root's entry) and launches there, with the root warning. `-W origin/feature` resolves like the picker (a local `feature` tracking it); `<remote>/HEAD` names no branch of its own and git refuses the `HEAD` name it would take.

## Cleanup classifier and removal (`wt worktrees` alias `wt cleanup`, `wt remove`, `wt rescue`, `wt restore-root`)

`internal/cleanup` classifies every worktree, local branch and `origin` branch into proven / less_sure / unique_work / in_use from one snapshot and is **read-only**; `cmd/wt-session/cleanup.go` prints it (`--json`, `--plain`) or shows the tick list (`internal/tui/cleanup_list.go`, with a row detail screen in `detail.go`). `wt remove` and the list go through one engine, `internal/removal`, **the only deletion code in wt**; `wt rescue` (`internal/rescue`) and `wt restore-root` (`internal/rootmove`) copy and verify, they never delete. Command reference: [docs/wt-cleanup.md](docs/wt-cleanup.md); design: [../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md](../docs/superpowers/specs/2026-11-04-wt-cleanup-design.md).

- **Fail closed.** A git error, an unresolved default branch, an unknown PR state or an unavailable gate is Unknown, never proven (`LookupUnknown` is the zero `Lookup`). The base is `origin/<default>`; a local default caps every verdict below proven.
- **Every git call the classifier runs has `GIT_OPTIONAL_LOCKS=0`** (`cleanup.Git`); the classifier writes no ref, index or working file. `Snapshot` is a plain value tests edit by hand: derive from its fields on read, never cache.
- **Deletion lives only in `internal/removal`**; source-scan tests (`TestDeletionStringsOnlyInRemovalPackage`, `TestCleanupCmdContainsNoDeletion`, `TestRescueContainsNoDeletion`, `TestRootmoveContainsNoDeletion`, `TestRootMutationFunctionsAreExactlyTheAllowList`) enforce it, so a new file with a deletion word fails them. Only `removal.ReleaseRoot` may reset or switch the root.
- **Weak signals are not proofs** (`git cherry`, patch-id: less_sure at most); `changes-in-base` is the content proof. Refs passed to `Prove`/`WeakSignals` are fully qualified or a 40-hex id. Min-age (`DefaultMinAge`, 10m) gates `same-commit` and `ancestor` evidence.
- **A remote ref folds into the local row that tracks it** (`Row.Upstream`): find a remote row through `Report.RemoteRows()`, list everything through `Report.AllRows()`.
- **Evidence strings, reason and blocker codes, outcomes and exit codes are frozen JSON contracts** (`../docs/contracts/cleanup-list.*`, `remove-result.*`, `rescue-result.*`, `restore-root-result.*`, each held by a `cmd/wt-session` contract test): change them only with the fixture. `--json` carries the header's caveats in `notes`; read it before acting on `rows`.
- **Rescan before acting; approvals are per item and sealed.** `removal` re-classifies each item right before touching it (changed state is `skipped_changed`, never an error). `Item.Approved` (`Needs{Confirm, Force, Delete, Remote}`) comes only from `ItemPreview.Confirmed`; no approval overrides a hard blocker, and a held item or the default branch is a floor (`--accept-loss`), not a blocker.
- **`Row.Losses` is raw, `EffectiveLosses()` is what removal reads**; every `.Losses` read carries a `// losses: raw, <why>` marker (`TestEveryLossesReadSiteIsLabelled`).
- **Origin removal is the only remote write in wt**: a proven `origin` row only, its own approval (`--remote`, typed `delete origin`; nothing else grants it), a `--force-with-lease=<ref>:<sha>` push, judged by a fresh `ls-remote`, never by the push's status.
- **Rescue is copy-only.** It writes the `refs/wt/rescued/<id>` record only after a verified copy; `internal/cleanup` reads that record (never `internal/rescue`, which it may not import) so a clean, or fully covered dirty, original reads proven or less_sure. Every refusal happens before anything exists; a failure after the destination exists keeps it.
- **`wt restore-root` moves the root's branch (not a copy) into a worktree** through rescue's engine in root mode, then `ReleaseRoot`; nothing is rolled back, and every `failed` reason carries the recovery text (`run.recovery`). A held root warns and asks, never refuses at `Preflight`.
- **TUI keys:** `space`/`a` tick, `enter` removes the ticked rows, `F` fetch + refresh, `q` quit; `i` opens the row's **detail screen** (`detail.go`) and `x d o r R` open it unarmed, the second press acting. `cleanup.ListOrder` orders the list (no headings); the verdict cell is one word (`safe`, `check`, `work`, `locked`). **Hard stops lock a row, floors never lock a worktree or local branch** (`cleanupLocked`). While the report is filling, `x d o r R` and manage-mode `enter` are refused (`m.filling()`). Typed `delete` confirms anything not proven, `delete origin` an origin branch.
- **The detail screen and a deferred fill** (`detail.go`, `cleanup_list.go`): a fill's removal-funcs build is held in `pendingRM` while a flow is open, and `esc` on the detail screen applies it (without that, `x d o r R` stay refused after a fill landed mid-screen). A report swap goes through `settleDetail`, which re-points the open screen at the same row of the new report or closes it; a new path that swaps `m.report` must call it. `cleanup.ReadRowDetail` reads a bare row or an unborn HEAD as empty with `RowDetail.Note`, never as an `Err`.
- No automatic cleanup anywhere: nothing runs at agent exit and the root is never switched for you.

Read [docs/internals/cleanup-and-removal.md](docs/internals/cleanup-and-removal.md) before changing the classifier, a proof, `internal/removal`, origin removal, `internal/rescue`, `internal/rootmove`, the list's keys or the detail screen: it holds the removal, rescue and restore-root invariants in full, the rulings, the declared losses and the still-to-do list.

## Import graph ratchet (`internal/depcheck`)

`go test ./internal/depcheck` holds the session library packages (`cleanup`, `agents`, `modelclient`, `smoke`, `profiles`, `catalog`, `survey`, `tui`) off the admin packages (`lifecycle`, `litellm`, `localmodels`, `cloudsync`, `spend`, `modeladmin`, `configeditor`, `candidates`, `hoststop`, and the model server's packages), `cmd/wt-model` off the session half, and `configeditor` off `tui` (share styling through `themes`/`tuilayout`). The test's allow-list of expected offenders (empty now) **only shrinks**: never add an entry to make a build pass (move the type or function down to a leaf instead), and delete an entry the moment its edge is gone (the test fails until you do). Rules: `docs/internals/packages.md` "Package graph rules".

## wt-model (`cmd/wt-model`)

`wt-model serve` is a second binary: one daemon on one port serving the model side of wt (a JSON control API under `/control/v1/` and a streaming gateway to LiteLLM under `/v1/`) so sessions on other machines need no registry. The same binary's `litellm set|status` manage the proxy pair the gateway forwards with (the `~/.config/wt-model/` home; setup is `wt-model litellm set --url URL --api-key KEY`). Reference: [docs/wt-model-server.md](docs/wt-model-server.md) (flags, exit codes, token file, the proxy pair, endpoints, error shapes, sessions, auto-stop); design: [../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md](../docs/superpowers/specs/2026-11-15-wt-model-session-split-design.md); wire types: `internal/modelapi`, fixture [../docs/contracts/model-api.sample.json](../docs/contracts/model-api.sample.json).

- **Change the wire types, the page and the fixture together** (#600): `internal/modelapi`, `docs/wt-model-server.md` and `../docs/contracts/model-api.sample.json` move in one change; so does any error message, code or the startup banner (`announceFormat`, which scripts parse).
- **Never open the server without a token** (#600): `--token-file` is required on every address, loopback included, and `NewServer` refuses an empty token. The gateway replaces the client's credentials with the LiteLLM key from the `~/.config/wt-model/` home's key file (the `[litellm]` table its read-only fallback); it never forwards them.
- **Auto-stop is the only stop nobody asked for** (#600): it considers only models a session registered. Every `wt` session is a client of the model server since Task 18 and registers its models, so auto-stop sees them.
- **`wt-model` is the server, `wt model` the registry command** (#600): write them that way in prose.
- `Deps.Config` reloads the registry per request, job and scan. `cmd/wt-model`'s tests inject a fake `Deps` through `depsFactory`; its `TestMain` carries the four isolations (config home, git, tiers, lifecycle processes).

## Verification commands

Run the Go test block above before any commit, then exercise the installed binary:

```bash
wt -W my-feature -A claude           # named worktree + launch (a new name is created and said so)
wt model list [--json]               # every registry model and local model found, with live status
wt cloud-sync --dry-run              # plan a price and ollama-catalog refresh; changes nothing
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix, gateway mode only (needs WT_MODEL_URL and WT_MODEL_TOKEN_FILE)
```

The full command list (`wt served`, `wt warm`, `wt cleanup`, `wt remove`, `wt model add|edit|rm|init`, `wt profile list`, `wt stats`, `wt profile list|show`, `wt start|stop`, `wt litellm`, …): [docs/internals/testing.md](docs/internals/testing.md#verification-commands).

> Verifying a branch's behavior requires building it first (`D=$(mktemp -d)`, then `go build -o "$D/wt" ./cmd/wt-session`) — `~/.local/bin/wt` is whatever was last `make install`ed and may predate the branch.

> **A built wt that must not touch this machine** runs with every path redirected. Any one left out reaches the real file:
>
> ```bash
> env -i PATH="$PATH" HOME="$D/home" WT_REGISTRY="$D/registry.toml" \
>   WT_LITELLM_CONFIG="$D/config.yaml" WT_LITELLM_RESTART_CMD=true \
>   WT_MTPLX_PIDFILE="$D/mtplx.pid" WT_REC_PIDFILE="$D/rec.pid" WT_SUSHI_PIDFILE="$D/sushi.pid" "$D/wt" model list
> ```
>
> This redirects files, not servers: a start, stop or probe still reaches whatever answers at the registry's `base_url`s, and `wt cloud-sync` still runs the real `ollama`. `cmd/wt-session`'s `TestMain` (through `lifecycle.IsolateProcessesForTest`) sets `WT_SUSHI_PIDFILE`, like every other process family's pidfile variable, to a path that does not exist.
