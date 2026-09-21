# CLAUDE.md

Guidance for Claude Code when working in this repo.

## What this repo is

The `wt` binary (`cmd/wt/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--cwd` (current repo root), `-W <name>` (named worktree), or the worktree picker.
2. **What** — `-A <name>`: an LLM agent, or `shell` for a plain command. **Never defaulted**: omitting `-A` always shows the agent/command picker.
3. **Which model** — `-M <provider>/<name>`, filtered by `-T <tags>` / `-F <family>`, or picked (eligible models rotate on successive launches).

`bin/*-wt` are shims that forward to `wt` (`claude-wt` → `wt --agent claude`). All logic lives in Go.

## Installation

```bash
make install      # builds bin/wt and copies it + the shims to ~/.local/bin/
```

Requires Go 1.26.7 (see `go.mod`).

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`). A drifted linker signature is rejected by AMFI with "Taskgated Invalid Signature" and SIGKILLs the binary (exit 137).

Run `make help` for the full target list. `make test` requires `make install` first — it exercises the installed binary, not just the build.

The `bin/*-wt` shims forward to `wt`, so `wt` must be on `$PATH`.

## Key flags

Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; missing flags come from pickers or defaults. The agent is never defaulted.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use/create the worktree for `<name>`; skip the worktree picker |
| `-A <name>`, `--agent <name>` | Pin the agent or `shell`; never defaulted |
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`; cloud or running → launch; a non-running local starts (with a progress display; `--replace` skips the replace confirmation); a blocked model errors with that row's reason. Without `-A`, prompts for the agent first, then validates the pin |
| `--replace` | With `-M`, start the model even if it means stopping a running one |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR) |
| `-F <family>`, `--family <family>` | Filter models by family (comma-delimited, OR) |
| `--cwd` | Launch in the current repo root; skip the worktree picker |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed AGENTS.md + pointer files, then exit |
| `--version` | Print version and exit |
| `--check-guard` / `--no-guard` | Check / remove the `block-main-commit` guard |
| `--debug-worktrees` | List worktrees and branches (test helper) |
| `--debug-session <agent>` | Print newest resumable session (test helper) |

Legacy bash `--code`/`--design`/`--native` are unsupported; rotation is global, not slot-based (see [Rotation (Go)](#rotation-go)), the main guard is `internal/guard`.

## Passthrough args

Extra args forward to the launched agent. `--` is only required when the command starts with a flag-like token, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Post-run summary line

After the launched subprocess exits, both the TUI and non-TUI paths print a single `wt: <agent> · <model-id> · <duration>` line to stdout (model segment omitted for command agents like `shell`). Emitted on success and non-zero exit; never affects the exit code. The formatter lives in `internal/agents.Summary` and is the single source of truth for both paths. See `docs/wt-agents/README.md#post-run-summary-line`.

**Post-exit order (issues #115/#116), identical in the TUI and non-TUI paths:** release this session's refcount entry → survey (skipped for native models) → stop picker (below) → summary line → after-survey stats → stale-pricing notice. The interactive steps come first and the informational output last. The duration is measured when the agent exits.

The stop picker (`survey.Picker`, `internal/survey/stop.go`) offers running local models that no live wt session uses (refcount zero, probe trusted, stop backend exists — `lifecycle.CanStop`/`lifecycle.StopModel`). It is line-typed (number toggles, `all`/`none`, Enter confirms, `q`/`esc` skips) and nothing starts selected. It is silent when nothing qualifies, when stdin is not a TTY, and for command agents and native models (neither runs a local server of its own). Ctrl+C or SIGTERM is caught for the whole picker on a context of its own: at the menu prompt it abandons the prompt (the read runs on a goroutine raced against the context), and during a stop it cancels the stop in flight and skips the remaining models — either way it prints `cancelled`, runs the TTY drain, and still reaches the summary line. wt's own refcount entry is released first (`refcount.Release`) — otherwise the model just used would always count as in use.

After the summary, a stale-pricing notice may print (one line, issue #69): when modelman's `price_refresh_last_run` (top-level key in `~/.config/local-ai/modelman.toml`) isn't today's date — or is absent — wt prints `wt: token pricing last refreshed <date> — run 'modelman refresh-prices'` (or the "never been refreshed" variant; a malformed (non-`YYYY-MM-DD`) value also uses the "never been refreshed" wording). wt only notifies; modelman owns the refresh. Parse errors on modelman.toml stay silent. Skipped for command agents like `shell` (`m.ID == ""` — same convention the session survey below uses), since they never touch a priced model.

Before the summary (see the post-exit order above), a post-session survey prompts up to four questions (did it work? speed? quality? — and on non-skip answers, what task were you doing) on the parent terminal — see [Session survey](#session-survey-go) below.

## Session survey (Go)

`internal/survey` records a post-session verdict for every agent launch with
a model (skipped for command agents like `shell`, whose `m.ID == ""`, and for
native models, `m.Native` — issue #116).
`survey.PromptRun` is the single implementation wired into both the non-TUI
path (`cmd/wt/launch.go runAgentCmd`, before the exit-code propagation) and
the TUI path (`internal/tui`, via the same capture-then-emit pattern the
summary line uses). It silently no-ops when stdin is not a TTY. It
**returns** the after-survey stats block instead of printing it, so the
caller can place it after the stop picker and summary.

Order on every launch: **survey prompts → stop picker → summary → after-survey stats → pricing notice**.

- **Paste drain:** after the last question, `PromptRun` flushes the kernel
  TTY input queue (`drainTTYInput`: `TIOCFLUSH` darwin / `TCFLSH` linux /
  no-op elsewhere) — a multi-line paste at the free-text question delivers
  all its lines at once and anything unconsumed would otherwise execute as
  shell commands in the parent terminal after wt exits. Targeted at
  `os.Stdin`, not the injected reader, because the residue lives in the
  kernel queue for fd 0. Don't remove this to "simplify" the survey.

- **Store:** `~/.config/agent-wt/survey.jsonl`, wt-owned, 30-day retention,
  pruned on every write (mirrors `internal/usage`).
- **Worked% semantics:** `worked / (worked + failed)` over *answered*
  surveys only; skips are recorded but excluded from the denominator.
- **Model picker (SURVEY column):** each row gets a trailing `✓<pct> q<quality> s<speed>
  n<answered>` segment sourced from the agent-scoped 30-day stats (omitted
  when nothing has been answered yet); `⚠` replaces `✓` when
  `WorkedPct < 70%` and `Answered >= 3`.
- **`wt stats`** — see `docs/wt-stats.md`.
- **Model id scheme:** Surveys and usage key on a free-form model id and never require a registry entry. Unregistered on-disk models use `config.DiscoveredModelID(provider, artifact)`; a registry match keeps its registry id.

## Docs

- `docs/configuration.md` — Claude Code / Codex CLI config
- `docs/wt-config.md` — `wt config` subcommands
- `docs/wt-agents/` — per-agent reference (one file per launcher)
- `docs/superpowers/specs/` — design specs (input to implementation)
- `docs/superpowers/plans/` — implementation plans (output of planning, input to execution)
- `../CLAUDE.md` — monorepo-wide commands, benchmark isolation helpers, and shared config ownership (modelman owns `registry.toml`/`modelman.toml`; wt owns `~/.config/agent-wt/config.toml`).

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

**Test seams.** TTY, installed-check, guard, TUI behavior, the model
picker's usage store, local-inventory probing, and model starting are stubbed via package-level var seams (`tuiRun`,
`launchFiltered`, `stdinTTY`, `installed`, `maybeInstallGuard`,
`newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`, `pickModelTUI`, `pickStartModelTUI`, `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`) — production code calls the var, tests swap it. `runInventory` (in `internal/tui`) stubs `localmodels.Inventory`; the package's `TestMain` sets it to no-op so no test probes live servers. `startModel` (in `internal/tui/start_flow.go`) stubs `lifecycle.Start`; the same `TestMain` stubs it to fail so no test can start a real model process. `cmd/wt` carries its own seams, stubbed by `testmain_test.go`: `probeInventory` (the non-TUI inventory probe), a second `startModel` — a different package and signature (it wraps `startForLaunch`), a separate seam from the TUI's despite the shared name — and `lifecycleStart` (the engine behind that driver), all hard-failed by default so an unstubbed test can neither start a real model nor let the engine's route hook rewrite the real `config.yaml`. `internal/survey`'s `TestMain` stubs `flushTTY` the same way, so it is a no-op package-wide. Together the three `TestMain`s mean no Go test probes a real server, starts a real model, or drains the developer's terminal input queue. `internal/smoke`'s `smokeProbe` is the same idea for `Eligibility`, with the exported `SetSmokeProbeForTest` hook for other packages' tests. When adding
a new seam, follow the same shape: a `var x = realX` plus a `realX` function.
`internal/lifecycle` is the other convention: every seam (HTTP clients, exec, inventory, timeouts, pidfile paths) lives in one `env` struct that `defaultEnv()` fills and tests rebuild with `testEnv()`; its package `TestMain` doubles as a fake `mtplx` helper process when `LIFECYCLE_HELPER=mtplx`.

**Prefer asserting on unexported functions directly** — same-package tests
can call them (e.g. `buildStatsRows`); parsing rendered lipgloss output
couples tests to border glyphs/padding and flakes under forced-color ANSI.

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go test ./internal/agents -run TestOpenCodeOllamaPrefix -v   # one test
go vet ./...                         # static analysis
make help                            # list Makefile targets
```

Key `make` targets: `build` (compile), `install` (compile + re-seal codesign + place on `$PATH`), `test` (requires `install` — exercises the installed binary), `check` (shellcheck lint + shfmt format-check + `go-format-check`, a `gofmt -l` gate that wt-ci also runs; `make format` writes both shell and Go).

Package list: `internal/{config,rotation,usage,refcount,survey,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,smoke}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts — each test's focus is documented in its own `//` comment (see above).

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config once) |
| `cmd/wt/commands.go` | hidden `rotate` subcommand |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch, resolved from live `catalog` rows; a `-M` pin on a start row starts it through `startModel` |
| `cmd/wt/start.go` | `startForLaunch` — the non-TUI start driver: progress on stderr, Ctrl+C cancel, the replace confirmation, and the package-level `allowReplace` the flag sets |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers (`maybeInstallGuard`, `checkGuardStatus`, `removeGuard`); TTY seams (`isStdinTTY`/`stdinTTY`) and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `buildLaunch`, `launchFiltered` (all take `extraArgs`) |
| `cmd/wt/stats.go` | `wt stats` command — read-only report over `survey.jsonl` |
| `cmd/wt/model_cmds.go` | `wt start` / `wt stop` — `runStart` (screen-1 picker over all local rows via `localRows`, then the shared `startModel` driver) and `runStop` (argument forms, in-use confirmation); owns the `stopCandidates`/`stopEntries`/`stopPickerAll`/`confirmStop` seams |
| `cmd/wt/smoke.go` | `wt smoke` command — one-shot model×agent smoke test, human/JSON output |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
| `internal/rotation/` | global rotation state + `Next` for the picker (replaces the per-slot model) |
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model picker's per-row usage columns, and the rotation module; events carry an optional `agent` (`RecordFor`); `CountsForAgent` gives per-agent-model-pair counts, legacy agent-less lines count toward `Counts` only |
| `internal/refcount/` | live-session "in use" model counts: JSONL state file keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point, consumed by the model picker's ref column |
| `internal/survey/` | post-session survey: append-only JSONL verdicts (worked/speed/quality + task description), 1d/7d/30d stats per model and per agent×model combo — used by the post-run prompt, the model picker's survey segment, and `wt stats` |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/smoke/` | `wt smoke`'s testable core: `Eligibility` (builds `catalog` rows per agent from one `localmodels.Inventory` snapshot — via the `smokeProbe` seam, with `SetSmokeProbeForTest` for other packages' tests — and keeps the launch rows (cloud, or a local model the probe reports running); it never starts or stops a model itself; `Candidates` additionally returns startable idle local rows (Action start) alongside launch rows, each with its eligible agents, for `wt smoke`'s start-first flow; returns both the eligible-model union and each model's eligible-agent list, so `EligibleAgents`/`AllEligibleModels` are thin wrappers over it and a caller needing both answers in one invocation should call `Eligibility` directly to avoid paying two probe rounds), `RunRow` (PASS/FAIL/SKIP classification via the `buildAndRun` seam) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection (`IsRepo`, `RepoRootAt`, `RepoRoot`), enumeration (`Enumerate`), creation (`EnsureForName`/`EnsureForBranch`) |
| `internal/initseed/` | `--init` seeding |
| `internal/session/` | resume detection (claude/opencode) |
| `internal/ollamacheck/` | availability check before launch |
| `internal/catalog/` | The shared row policy for every model list wt shows or resolves: `Build` (rows from an agent's eligible list plus one `localmodels.Inventory` snapshot), `Find` (row lookup by id, discovered rows included — a `-M` pin names a model, not necessarily a registry entry), `Row.Action` (launch / start / block), `Row.BlockReason`, and the presence status (`ok`/`absent`/`unknown` for registered locals, `new` for discovered rows). Knows nothing about rendering, counts or sorting — `internal/tui` wraps a `Row` in `tableRow` with those; `cmd/wt`'s `resolveModel` and `smoke.Eligibility` consume it directly. `Build` also marks a discovered model handed in via `Input.Models` (not just those it discovers itself) as `new`, so `wt start`/`smoke` rows built from a caller-supplied list keep discovered-row semantics. |
| `internal/litellm/` | LiteLLM `config.yaml` route management (Go port of modelman's expose logic): `configfile.go` (read/write `model_list`), `entry.go`/`policy.go` (route entries, provider mappings, ready gate), `service.go` (expose/unexpose/sync/list), `restart.go` (proxy restart: `WT_LITELLM_RESTART_CMD`, else `launchctl kickstart -k` fallback; `RestartContext` runs it on the caller's ctx, `Alive` is the one-shot `/health/liveliness` probe and `WaitReady` the poll). Backs `wt litellm ...` and `internal/lifecycle`'s route hook. |
| `internal/localmodels/` | Local model inventory: `Inventory(cfg)` probes ollama (`/api/tags` + `/api/ps`, cloud `remote_host` entries excluded), omlx/mtplx (model-dir scan + `/v1/models`) and mlx_lm_server (running only) concurrently and returns registered + discovered entries with live `Running`; registry match keeps the registry id, else `config.DiscoveredModelID`. Status per family is `ok`/`partial` (the live running-state probe failed — ollama `/api/ps`, or omlx/mtplx `/v1/models` — so Running is untrustworthy)/`unreachable`/`unsupported`. Never reads modelman's `running` flag. The registry `auth.base_url` origins behind every probe resolve here (`FamilyOrigin`; `FamilyOriginPort(cfg, family)` returns the family origin **and** its numeric port from one resolution, with the family default applied to the origin when the registry value omits a port, so a command-line port and a dialed URL cannot disagree) — the inventory's own probes and `internal/lifecycle`'s start/stop backends share them, so the two always describe the same server. `Entry.ArtifactKnown` distinguishes an artifact the probe confirmed missing from one it could not determine (an unreachable family; `mlx_lm_server`, which has no artifact discovery). Each entry also carries `ModelName` (the provider-side name: the registry `model_name` when registered, else the artifact), and `Family(providerID)` is exported for consumers like `lifecycle`. |
| `internal/lifecycle/` | Local model start engine (Go port of modelman's start/warmup): `Start(ctx, cfg, Target{ProviderID, ModelName}, Options{AllowReplace, Progress})`, `Occupant(target, snapshot)`, `Stop`. Backends: ollama (daemon must answer; 1-token chat loads the model; multi-tenant), omlx (`omlx start` when the daemon is down, warm by model basename, `omlx stop` to replace), mtplx (`mtplx serve …` spawned in its own session with modelman's pidfile/log paths; `mtplx stop --port N` to replace; torn down on failure/cancel). Live Inventory decides already-running and occupant; never replaces without `AllowReplace` (returns `*OccupiedError`); typed errors `DaemonDownError`/`BinaryMissingError`/`PortBusyError`/`UnsupportedError`/`OccupancyUnknownError`. Where the live probe cannot determine the running state — a single-model provider's server accepted the connection and then stalled, or answered unusably — `Start` returns `*OccupancyUnknownError` instead of assuming no occupant, and only `AllowReplace` proceeds. A provider that is definitively down (connection refused) is not indeterminate: that is an ordinary cold start and needs no confirmation. Writes nothing to modelman state; does not kickstart the ollama daemon. Consumed by the TUI's start-on-select flow (`internal/tui/start_flow.go`) and by `cmd/wt`'s `startForLaunch` (`wt -A <agent> -M <id>` on a non-running local model); `wt start` and `wt smoke` (for an idle pick) also start through the same driver; `wt stop` only stops. After a successful start/stop the public wrappers (`Start`, `Stop`, `StopModel`; `routes.go`) update LiteLLM `config.yaml` routes via `internal/litellm` and restart the proxy: single-model providers (omlx, mtplx) replace sibling routes on start and drop all family routes on stop, ollama adds/removes just the one model; LiteLLM failures only warn on stderr and never fail the start/stop, and a missing `config.yaml` is silent. `Start` reports `StageRouting` through `Options.Progress` before the hook runs, so callers stop rendering the engine's last stage during the route window. The caller's ctx reaches both the restart command (`litellm.RestartContext`) and the readiness wait, so Ctrl+C abandons them at once. Before writing, the hook probes `<url>/health/liveliness` once (`litellm.Alive`, 1.5s); it waits up to 30s for the proxy only when routes changed **and** that probe said the proxy was already up — a configured-but-stopped proxy costs nothing instead of stalling every start/stop for the full timeout. A replace is a separate case: the occupant's route is removed the moment it is stopped (the `env.onOccupantStopped` hook, wired only by `defaultEnv`), because a start that then fails returns without any route hook and would strand the dead occupant's row. Failure wording lives in `internal/lifecycle/message.go` (`StartErrorMessage`, `StageLabel`), shared by both paths. |
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor (agent add/edit/delete) |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell + pickers + launch/resume; also exports `PickModel`, a standalone single-purpose picker (not part of the app.go state machine) reusing `buildTable`, consumed by `wt smoke`; `PickStartModel` is the same picker without launch-route gating (starting is not launching; `wt smoke`'s candidates are already route-checked per agent), used by `wt start` and `wt smoke`; rows that cannot start (blocked) are unselectable and show a notice with the block reason instead of returning. Selector table: `modelrows.go` (`buildRows`, `sortRows`; row action/block reason come from the embedded `catalog.Row`) delegates row inclusion and action to `internal/catalog`, and adds counts, survey stats and sort (cost ascending by output then input price, local and subscription-only = $0, no-data last; then 7d usage ascending; non-running local alphabetical); `modeltable.go` (`buildTable`, `renderTable`, header as list title via `styleTableTitle`) does the header and aligned rendering. `start_flow.go` is the start-on-select flow (`startModel` seam, `beginStart`, `runStart`, the `phaseStarting` progress + cancel keys, and the `phaseReplaceConfirm` dialog). `runInventory` is a test seam stubbing `localmodels.Inventory` for probing local models live. |
| `docs/superpowers/` | specs + plans |

## Config (Go)

`~/.config/agent-wt/config.toml` (TOML) is wt-owned and contains **only agents and preferences** — Providers/Models live in the registry (below), and LiteLLM routing state lives in modelman's `modelman.toml` (below). wt never writes providers/models to this file.

Fields:

- **Agent** — tool with ≥1 supported provider and optional default.
- **DefaultTag** — the default rotation tag group.

> **Legacy `[gateway]` blocks.** LiteLLM routing used to be configured here (`GatewayConfig`); the type and the `[gateway]` table were removed — routing is now modelman-owned (below). `migrateConfigSchema` detects a config.toml still carrying `[gateway]` and prints a one-time stderr notice (`wt: found a legacy [gateway] block in config.toml — LiteLLM routing is now controlled by modelman (see 'modelman litellm status'); this block will be dropped on next save`); since `Config` no longer decodes the field, the block is dropped on wt's next save. **Ordering matters**: `modelman migrate` is the only thing that reads `[gateway]` back out (importing it into modelman.toml's `[litellm]` table), and it's read-only/best-effort — it never re-adds the block. Any other `wt` command run first (a launch, `wt config`, …) triggers the drop via its own config save, permanently losing the auto-import opportunity; the user then has to re-enter `--url`/`--api-key` by hand via `modelman litellm set`. There's no code-level guard against this — it's a documented upgrade-order caveat (see `docs/guides/00-config-map.md`), not a bug to "fix" by having wt write modelman.toml (that would violate the ownership split above).

Key helpers: `Dir()` (config dir), `WriteFileAtomic` (atomic save), `OllamaBaseURL` (`http://localhost:11434`), `FirstTag(s, fallback)`.

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

## LiteLLM routing state (modelman-owned)

Whether non-native models route through the LiteLLM proxy — `Config.IsLitellm()`, read-only from `~/.config/local-ai/modelman.toml`'s `[litellm]` table (`enabled`/`url`/`api_key`, loaded via `finalizeCfg`/`loadModelmanState`) — or dial providers directly (`Config.IsDirect()`) is decided by modelman (`modelman litellm status|on|off|set`). Toggling is routing policy only: wt never flips the switch, but it does manage `config.yaml` routes and restarts the proxy after a change through `wt litellm ...` (`lifecycle.Start`/`Stop`/`StopModel` call it after a successful start/stop; routing on/off/url/key is still modelman-owned for now). `LitellmBaseURL()` trims trailing slashes so drivers append `/v1` (or nothing for claude) cleanly; the proxy loads `~/.config/litellm/config.yaml` only at startup and modelman restarts it after expose changes via `MODELMAN_LITELLM_RESTART_CMD` (see `docs/wt-agents/README.md#litellm-proxy-lifecycle`).

## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models.
The location is resolved with the same precedence as modelman's
`_default_registry_path`: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` >
`~/.config` — keep the two in sync when changing either side. A
`MODELMAN_REGISTRY` value starting with `~/` (or exactly `~`) is expanded
via `expandHome`, matching Python's `Path.expanduser()` used by modelman's
resolver — Go/the OS never expand `~` on their own, so this parity is
deliberate, not incidental.
wt loads it read-only via `config.Load` (fail-closed: missing/malformed
registry is an error; seed with `modelman migrate`) and joins it in memory
with its own `config.toml`, which now holds only Agents + DefaultTag. `Save`
persists wt-owned fields only — wt never writes providers/models. Extra
registry fields `model_info` and `fetch` are ignored by wt's parser; `cost`,
`model_dir` (`Provider.ModelDir`), `auth.base_url` (`Auth.BaseURL`), and
`auth.secret_ref` (`Auth.SecretRef`) are
decoded (`config.ModelCost` in `internal/config/config.go`) and the cost is
rendered as per-token + subscription pricing columns in the model picker
(`internal/tui/model_list.go`).

**Read-side schemas are pinned by contract fixtures.** `docs/contracts/registry.sample.toml`
and `docs/contracts/modelman.sample.toml` are loaded by `internal/config`
contract tests and modelman's `tests/contracts/` — a schema change must
update both sides or both CI jobs fail.

**Exposure predicate (2026-09-15 local-model visibility design):** wt's `IsExposed` decides Stage-1 (tag/family/provider) catalog membership:
- Native models (provider `auth.type = "native"`): always exposed.
- Local models (location resolves to `"local"`): always exposed here too — the picker lists every configured and discovered local model with live STATUS/RUNNING, and the non-TUI path consults the same rows; what a row can do (launch, start, or block) is decided per row from the live probe, not from any flag (see the "Local-model resolution" section below). A model whose location can't be resolved (registry data gap) falls back to the cloud/native check below, fail-closed.
- Cloud (and any model whose location doesn't resolve to local): `exposed` true (legacy `litellm_exposed` still read, ORed) AND (`ready = true` OR `location = "cloud"`), unchanged from before.

The model picker's EXPOSED column shows `Y` for native models (as modelman's own column does, unconditionally) and otherwise reads the raw modelman flag via `Config.ExposedFlag` — unlike `IsExposed`, it does no location-based special-casing, so cloud-location models show their true exposure state while local models show modelman's via-flag visibility (which says nothing about whether the model is running — see the "Local-model resolution" section below).

This means wt's picker can now show a local model modelman's own TUI still renders `–` for in its EXPOSED column — that divergence is intentional for local models; LiteLLM-forced routes (see the Agents table below) get their `config.yaml` route from wt itself: `lifecycle.Start`/`Stop`/`StopModel` add or remove routes through the route hook in `internal/lifecycle/routes.go` (see the `wt litellm` section below). wt still never writes modelman's `exposed` flag. See `docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md`.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt
> bug** — e.g. a `registry.toml` with models referencing `provider_id`s but
> `providers = []`. Fix is `modelman sync`/`modelman migrate` on that machine,
> not a code change here. (`modelman sync` now repairs a `providers = []`
> registry by creating default entries for reconcilable providers.)

**Lazy:** `newApp()` only loads config. wt never shells out for discovery;
`-W`/`--cwd` runs one `ollama list` via `ollamacheck.Available()`. `localmodels.Inventory` performs HTTP and filesystem discovery only (still no subprocess) and is called by both `enterModelPhase` (TUI picker, pinned and non-pinned models) and `PickModel` (standalone picker for `wt smoke`) to show live local-model status. Discovery stays subprocess-free; `internal/lifecycle` is where wt execs provider binaries (`omlx`, `mtplx`) to start or stop models.

> **Fixture gotcha.** `Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME`
> (and `RegistryPath()` also honors `MODELMAN_REGISTRY`) but write to
> *different* subdirs: `config.toml` → `$XDG_CONFIG_HOME/agent-wt/`,
> `registry.toml` → `$XDG_CONFIG_HOME/local-ai/`. Test/smoke fixtures must
> populate both. Also: `migrateConfigSchema` (runs on every `Load`)
> unconditionally ensures an `agy` agent — any registry fixture with agents
> needs a matching `agy` provider or `Load`/`Validate` fails with
> `unknown provider "agy"`.

## Local-model resolution (live rows, 2026-09-20)

Every model row wt shows or resolves — the TUI picker, `wt smoke`'s picker, and the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot; the TUI only decorates the rows (counts, survey stats, sort, rendering). The rules live once in `catalog` (`Row.Action`/`Row.BlockReason`):

- **launch** — a cloud row, or a local model the probe reports running.
- **start** — a non-running local row of ollama/omlx/omlx-6bit/mtplx whose
  status is not `absent` (a pulled ollama model is a start, not a launch
  exception).
- **block** — an `absent` row ("not on disk — pull or download it first") or
  a provider with no start engine such as `mlx_lm_server` (the
  `modelman start <id>` hint). Separately from these, a discovered row whose
  route goes through LiteLLM is refused by the picker and the CLI ("not in
  LiteLLM", unselectable — `renderTable` in the TUI, `pickerBlockedReason`
  on the non-TUI path, not `catalog`).

**TUI model picker:** Lists all configured and discovered local models —
running and non-running alike — with live STATUS/RUNNING columns from the
inventory (never the modelman flag). Enter on a start row runs
`lifecycle.Start` with live progress, a cancel key, and a replace-confirm
dialog (see TUI below); the single-row auto-launch shortcut applies only to
launch rows.

**A `-M` pin is looked up among ALL rows, discovered ones included**
(`catalog.Find`) — the launch path accepts a model the registry does not
name. The pin's own row decides the outcome wherever it appears: a launch
row (cloud or running local) launches; a start row selects its row and
enters the start flow; a blocked row routes back with that row's reason;
a pin absent from the list keeps the "not in the eligible list" message.

**Non-TUI launches (`cmd/wt/resolve.go`):** `resolveModel` builds the same
`catalog` rows from one inventory snapshot. With no `-M`, only the launch
rows are eligible for resolution (`launchableModels`) — rotation sees cloud
plus running-local rows only, so it can never hand an agent a server that
is not up, and a start row is never auto-selected: only a pin may start
one. A `-M` pin on a start row is started by `startForLaunch`
(`cmd/wt/start.go`): timestamped progress on stderr, Ctrl+C cancels (a
second Ctrl+C exits wt), and an occupied provider needs a TTY `y/N` or
`--replace`. The TUI starts a non-running pin at once too (with or without
`-W`), and `--replace` there covers only the pinned row. Start failures on
both paths use `lifecycle.StartErrorMessage`. When models matched but none is cloud or running, the error
names the fix: "no cloud or running local model for agent X — start one with
`wt -M <id>`".

Start/stop local models with modelman: `modelman start <provider>/<name>`
/ `modelman stop <provider>/<name>` / `modelman stop --all`, or the TUI's
`s` keybinding (see `modelman/CLAUDE.md`).

## Config (themes)

`wt config` is the user-preference surface (separate from `config.toml`):

```bash
wt config                    # interactive viewer/editor (needs TTY)
wt config theme              # active theme + available names
wt config theme list         # list built-in themes
wt config theme show <name>  # tokens with dark/light hex previews
wt config theme set <name>   # activate (effective next launch)
wt config theme unset        # revert to default
wt config path               # print the config directory
```

> **Invalid-config repair.** `wt config` launches even when `config.toml` fails validation, so the editor can repair it. Other launch paths exit early on config errors.

## Rotation (Go)

Global rotation — the Go equivalent of bash `--code`/`--design`. Each successful launch records a single model id; the next picker entry lands on the model *after* it. Per-slot rotation (`Slot{Agent,Tag,Family}`) was retired; only the global state file remains.

- Public API (package `rotation`):
  - `Rotation` (struct) — `New()` / `NewAt(dir)` constructors; `Last() (string, bool)`, `Record(modelID string) error`, `RecordFor(agent, modelID string) error` (rotation state plus an agent-attributed usage event; `Record` is `RecordFor("", id)`), `Next(cfg, agent, tags, family) (config.Model, bool)`, `StateDir() string`.
  - Package-level `FirstAfter(models []config.Model, target config.Model) (config.Model, bool)` — shared by the picker and `wt rotate`.
- State file: `~/.config/agent-wt/rotation.state` (atomic write, owns one model-id-per-line).
- The model picker marks the last-launched row with a `> ` prefix
  (`renderTable` sets `marked` in `internal/tui/modeltable.go`, value
  from `rotation.Last()`; `Title()` composes the prefix — plain ASCII
  because Unicode geometric shapes are East Asian Ambiguous width and
  misalign CJK terminals); the cursor still lands on the rotation's
  next-to-use model. Rotation positions the cursor only when a last-launched
  registry model exists; otherwise (fresh install, stale id, or a
  last-launched discovered model) it lands on the first launchable table
  row. Rotation never advances onto a discovered row.
- The model picker's leftmost column shows a live "in use" session count
  (issue #73): `buildTable` queries `refcount.Store.Counts` over the
  table's row IDs and sets
  `modelItem.ref`; `Title()` renders it as a 2-rune prefix ("`3 `" or two
  blank spaces, clamped at 9) *before* the rotation marker. See
  `internal/refcount`.
- **Picker table columns:** FAMILY, MODEL, LOC, STATUS, EXPOSED, RUNNING, COST, 1D, 7D, 30D, SURVEY (the SURVEY column is present but always empty for `wt smoke`'s picker, which has no agent context).
- Usage history (1d/7d/30d per-model counts) lives at `~/.config/agent-wt/usage.jsonl` (JSONL, appended by `usage.Store.Record`; launch paths from `cmd/wt/launch.go` and `internal/tui` call `rotation.RecordFor(agent, id)`, which also records the agent-tagged usage event; consumed by the model picker — see `internal/usage`). Usage tracking uses `usage.Store.CountsForAgent(agent, modelIDs)` for per-agent-model-pair counts; `wt smoke`'s picker uses model-level `Counts` since there's no agent context yet. The picker's TUI callers fetch the agent's **full** catalog **once** via `cfg.ModelsForAgent`, narrow it in place with `cfg.EligibleModelsIn` (the shared single-traversal filter; `EligibleModels` is a thin wrapper that passes a nil catalog), and pass the eligible slice to `enterModelPhase`. `buildTable` (`internal/tui/modeltable.go`) then builds and sorts the rows (cloud plus running local by cost then 7-day usage, non-running local alphabetically) and renders them as an aligned table with per-model 1D/7D/30D usage cells; the header is the list title.

```bash
go run ./cmd/wt rotate code    # debug helper: print the model after the last-launched in the "code" tag group
```

> **Migration from per-slot rotation.** On first load after upgrading, if `rotation.state` is absent and any legacy `rotation-<agent>-<tag>-<family>.state` or `rotation-<tag>.state` files exist, `Rotation.migrate` imports only the **single newest-mtime** entry and then deletes every legacy file. Distinct per-slot histories (e.g. `claude-code` vs. `claude-design`) are reduced to one global value — there is no in-repo back-compat layer for multi-slot users. Back up `~/.config/agent-wt/rotation-*.state` before upgrading if that matters.

## Agents (Go)

Each agent registers a `Driver` (`Build(m config.Model, yolo bool, r config.Route) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` is the shared constructor used by both TUI and non-TUI launch paths — it resolves one `config.Route` per launch via `(*config.Config).ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build`; drivers should not bypass it.

`Route` (`internal/config`) carries everything a launch needs: `BaseOrigin` (scheme://host:port, no wire-path suffix), `APIKey`, `ModelRef`, `Display`, `ProviderID`, `Protocol`, `Litellm`, `Forced`. Direct routes dial the model's own provider (`auth.base_url`, normalized via `BaseOrigin`, key from `auth.secret_ref` via `ResolveSecret`) with the provider-side model name; litellm/forced routes dial modelman's `[litellm]` URL/key with the registry id.

**Agent protocols.** Agents declare the wire protocols they speak via the `ProtocolDeclarer` capability (`Protocols() []Protocol`); registry providers declare what they serve (`Provider.protocols` in registry.toml, defaulting to `openai-chat`). `ResolveRoute` intersects the two — an empty intersection sets `Forced = true`, routing through LiteLLM regardless of the on/off toggle.

| Agent | Declared protocols |
|---|---|
| claude | `anthropic` |
| codex | `openai-responses` |
| copilot | `openai-chat` |
| opencode | `openai-chat` |
| pi | `openai-chat` |
| agy, shell | none — agy is a native passthrough and shell is a command runner; neither resolves a route |

Consequence: codex has no direct path for any local provider (none serves `openai-responses`) — codex always routes through LiteLLM, and `BuildLaunchCmd` prints the forced-LiteLLM stderr notice on every direct-mode launch. claude × openrouter (`openai-chat` only) forces LiteLLM the same way.

**Optional capabilities** (interface checks via type assertion in `BuildLaunchCmd`):

| Capability | Consumer | Drivers that implement it |
|---|---|---|
| `ProtocolDeclarer` | `Protocols() []Protocol` — the wire protocols the agent speaks; consumed by `ResolveRoute` (direct vs forced-LiteLLM) and the model picker | claude, codex, copilot, opencode, pi |
| `Seeder` | Pre-launch file seeding (AGENTS.md + pointer files) | claude, copilot (only drivers implementing `InstructionPointers()`; others skip seeding entirely) |
| `Syncer` | Pre-launch sync step (e.g. `pi` syncs models to `~/.pi/agent/models.json`) | pi |
| `ArgSetter` | Consumes passthrough args as argv instead of appending | shell |
| `Resumer` | `ResumeFlag()` + `LatestSession(path)` for resume support | claude, opencode |
| `OneShotRunner` | `OneShotArgs(prompt)` — non-interactive single-prompt invocation, consumed by `wt smoke` | claude, codex, copilot, opencode, pi, agy |

Drivers without `Resumer` (codex, copilot, pi, agy, shell) never resume — the session lookup in `internal/session` returns nil for them.

**Model id contract.** The `m.ID`-vs-`m.ModelName` split is now a property of `Route.Litellm`, resolved centrally in `ResolveRoute` rather than per-driver: litellm/forced routes set `Route.ModelRef` to the **registry id** (`m.ID`, e.g. `ollama/qwen3.8:27b-mlx` — LiteLLM's `model_list` is keyed on it); direct routes set it to the **provider-side name** (`m.ModelName`, dialed at the provider's own `auth.base_url` via `Route.BaseOrigin`). `Route.Display` always carries `m.ModelName` for catalog "name" fields. Regression tests (`TestClaudeOllamaPrefix`, `TestOpenCodeOllamaPrefix`, …) use a model with distinct `ID`/`ModelName` so a wrong id can't slip through silently.

| Agent | Launch behavior |
|---|---|
| claude | `ANTHROPIC_*` env (`ANTHROPIC_BASE_URL=r.BaseOrigin`; token `ollama` direct, `r.APIKey` litellm) + `--model <r.ModelRef>`; native: no args |
| codex | `--model <r.ModelRef>` plus inline `-c` overrides declaring the `agent-wt` provider at `<r.BaseOrigin>/v1/` (`wire_api="responses"`); **non-native codex models always route through LiteLLM** (no local provider serves `openai-responses`), key exported as `AGENT_WT_GATEWAY_API_KEY`; native: no args (see `docs/wt-agents/codex-wt.md`) |
| copilot | `COPILOT_PROVIDER_BASE_URL=<r.BaseOrigin>/v1`/`API_KEY`/`WIRE_API`/`COPILOT_MODEL=<r.ModelRef>` env; never `--model`. **Must use the chat-completions wire (`WIRE_API=completions`)** — copilot's `responses` wire drops leading characters through the OpenAI-compatible bridge (observed via LiteLLM; verified in both direct and litellm modes 2026-09-01, `glm-5.3-flash:cloud`). This deliberately diverges from `ollama launch copilot`, which still prescribes `responses`. Native models clear all `COPILOT_*` provider env vars. |
| opencode | `OPENCODE_CONFIG_CONTENT` inline JSON with the wt-declared custom provider `agent-wt` at `<r.BaseOrigin>/v1` in both direct and litellm routes; model `agent-wt/<r.ModelRef>`; never `--model` |
| pi | syncs models to `~/.pi/agent/models.json` (`_launch: true`); direct: `--model <r.ProviderID>/<m.ModelName>` from the pi provider named after the registry provider; litellm: `--model litellm/<r.ModelRef>`; no yolo |
| agy | no model passthrough (chosen in its TUI); **not** a command agent — `IsCommand("agy")` is false, so the launch path still resolves a model and `-A agy` without `-M` errors "multiple models match" (→ TTY error on non-TTY stdin) when >1 agy model is eligible |
| shell | execs passthrough args as argv, or interactive `bash`; no model/yolo/resume; `ArgSetter` |

With LiteLLM routing on (`modelman litellm on`) — or forced by a protocol mismatch — non-native models route through the proxy in modelman.toml's `[litellm]` table (`r.BaseOrigin` = its URL with trailing slashes trimmed, `r.APIKey` = its key, `r.ModelRef` = `m.ID`):

| Agent | litellm routing |
|---|---|
| claude | `ANTHROPIC_BASE_URL`=`r.BaseOrigin` (no suffix) + `ANTHROPIC_AUTH_TOKEN`=`r.APIKey`, `--model <m.ID>` |
| codex | same `-c` overrides with base_url `<r.BaseOrigin>/v1/` (trailing slash), key exported as `AGENT_WT_GATEWAY_API_KEY` from `r.APIKey`. **Always litellm, even in direct mode** (forced — no local provider serves `openai-responses`). **litellm ≤ 1.98.0 cannot serve codex** (responses→`ollama_chat` bridge crashes on codex's structured `reasoning`); see `docs/wt-agents/litellm-troubleshooting.md` |
| copilot | `COPILOT_PROVIDER_BASE_URL=<r.BaseOrigin>/v1` + `COPILOT_PROVIDER_API_KEY=<r.APIKey>`, `COPILOT_MODEL=<m.ID>` |
| opencode | `OPENCODE_CONFIG_CONTENT` with wt-declared custom provider `agent-wt` at `<r.BaseOrigin>/v1`, models map keyed by `m.ID`, model `agent-wt/<m.ID>`, `small_model` pinned the same — the builtin `openai` provider can't serve registry ids (catalog validation + responses-API stream mismatch) |
| pi | syncModels creates the dedicated `litellm` provider (`<r.BaseOrigin>/v1` + `r.APIKey`) keyed by registry id and launches `--model litellm/<m.ID>`; pi splits `--model` on the first slash, so registry ids under a direct provider block are unreachable — empty `apiKey` invalidates pi's whole models.json, reverts write placeholder `"ollama"` |

The pre-launch `ollamacheck.Check` is **skipped when LiteLLM routing is on** (`cfg.IsLitellm()`): a model absent from local `ollama list` is not an error when LiteLLM serves it from a non-local upstream.

### Adding a new agent driver

See the `adding-a-wt-agent` skill.

## Smoke test (`wt smoke`)

`wt smoke <model-id>` finds every agent currently eligible for one model and
runs a one-shot prompt through each via `agents.BuildLaunchCmd` (the same
in-process launch construction a real launch uses), reporting PASS/FAIL/SKIP.
Distinct from `make test-agents`/agents-smoke.sh's hand-curated regression
matrix (static agent×model list, both routing modes) — `wt smoke` tests
whatever routing mode is live right now, against whichever model you point it
at. Read-only against modelman-owned state; never flips LiteLLM routing. It
does start an idle local pick first (via the shared `startModel` driver,
honouring root `--replace`) and, once a model is resolved, runs the
exit-flow stop picker on pass or fail (registered only after the start step
succeeds, so a failed start returns its error without the picker; skipped for `--json`
or a non-TTY stdin; a FAIL still exits 1). It records no refcount entry, so
there is none to release first. With no model-id on a TTY, the interactive picker
is `internal/tui.PickStartModel` (route-skipping: `smoke.Candidates` already applied each agent's own route rules, and the agent-less check in `PickModel` could block a row an agent's forced-LiteLLM route accepts) — a standalone Bubble Tea program (not the main
app's worktree→agent→model state machine) that reuses `buildTable` for
the same table rows the agent flow's model picker renders, over
`smoke.Eligibility`'s unfiltered cross-agent union (never narrowed to one
agent's supported providers, since here the eligible agents are derived
*from* the chosen model rather than the reverse). The picker performs a live
`localmodels.Inventory` probe so STATUS/RUNNING columns show current local-model state. Always-on, timestamped
progress lines go to stderr — a start line before each agent's row and a
result line after, with FAIL rows carrying the same command/exit-code/output
detail the final report shows (`logSmokeStart`/`logSmokeResult` in
`cmd/wt/smoke.go`, sharing `writeSmokeFailDetail` with the final report so
the two can't drift apart) — leaving stdout (the human table and `--json`
report) unaffected. See `docs/wt-smoke.md`.

- **Timeout is location-based** (`smokeTimeout`, `cmd/wt/smoke.go`): unset `--timeout` → 180s cloud / 900s local (unresolvable location counts as cloud). The flag default is `0` = unset; only an explicit value is validated. Local rows are slow because agents send 12–41k-token preambles that cold-prefill at 65–95 tok/s, so a timed-out local row usually isn't a broken agent.
- **Diagnosing a slow local row:** `/tmp/local-ai-setup-mtplx.log` has one `mtplx_openai_generation` line per *completed* request (`prompt_tokens`, `elapsed_s`) plus `memory guard`/`pressure_trim` lines; killed (timed-out) requests leave no line, and a repeat agent is fast only while its prefix is still in mtplx's session cache. Rows run sequentially.

## Start/stop (`wt start`, `wt stop`)

`wt start [model]` starts a local model without launching an agent (`cmd/wt/model_cmds.go`). With an id it resolves through `catalog.Find` over `localRows` (every configured local model plus detected ones, one inventory snapshot): an idle row starts via `startModel` (`--replace` skips the replace question), a running row is a no-op ("already running"), a blocked row errors with its `BlockReason`, and a cloud or unknown id errors. With no argument it needs a TTY and shows the screen-1 picker (`tui.PickStartModel`, via the `pickStartModelTUI` seam — `PickModel` minus launch-route gating, since starting is not launching) over all local rows; blocked rows are unselectable.

`wt stop` with no argument probes the inventory once: `survey.PickerWith` takes the snapshot itself and returns whether it had anything to offer (the `stopPickerAll` seam), which decides the "no running local models" note. A bare provider is validated against the configured providers `lifecycle.CanStop` accepts (`stoppableProviders`), the same list its error message prints.

`wt stop [model|provider]` uses `survey.StopCandidates` (running models with live-session counts), `survey.StopEntries` (the shared stop loop) and `survey.PickerWith(..., Options{IncludeInUse: true})` for the no-arg picker (needs a TTY; in-use rows are listed with their session count, unlike the exit-flow pickers, which hide them). `<provider>/<name>` stops one model (not running: error; unknown: error); a bare provider (`ollama|omlx|omlx-6bit|mtplx`) stops all its running models (nothing running: exit 0 with a note). A target on a single-model provider (omlx, mtplx) widens to every running model of that provider (`withFamilyCollateral`; the collateral models are named on stdout) since it stops as a whole. If a live wt session uses the targets, `confirmStop` asks y/N on `/dev/tty` (default No) with the total session count (`stopImpact`: summed across models, a single-model provider counted once) and the in-use models named; `--yes` skips it. See `docs/wt-start-stop.md`.

## LiteLLM routes (`wt litellm`)

`wt litellm expose|unexpose <id>...` add or remove `config.yaml` routes for registry models (the proxy is restarted after a change; an unchanged config writes nothing). `wt litellm sync` makes local-model routes match the running models (it leaves alone any provider family whose probe status is not `ok` — a stopped omlx/mtplx reports `partial` — so it never removes routes it cannot verify), `wt litellm list` prints routed ids from `config.yaml`, and `wt litellm providers` lists providers with a LiteLLM mapping. All take `--json`; `expose` alone also takes `--dry-run` (validate only) and `--skip-ready-gate` (caller already verified readiness; used by modelman). Code: `cmd/wt/litellm.go`, `internal/litellm`. The same route updates run automatically from `wt start`/`wt stop` (the hook is described in the `internal/lifecycle/` package-table row). wt never writes modelman's `exposed` flag.

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Session resume (Go)

`internal/session` finds the newest resumable session (claude `*.jsonl` under `~/.claude/projects/<slug>`, opencode `*.json` under `~/.local/share/opencode/storage/session/<project-id>`). Others (incl. shell) return nil.

On Enter in the TUI, a prior session offers Start fresh (default) / Cancel / Resume (`--resume <id>` claude, `--session <id>` opencode). Non-TUI does the same without prompting.

> **Native models never resume.** A native model (e.g. `claude/native`) launches with no model override; resuming would restore the session's stored model and silently override "native" (routing a proxy-routed model at the real Anthropic API). Both paths skip the session lookup for native models.

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → resume prompt → launch, plus the start-on-select screens (`internal/tui/start_flow.go`): `phaseStarting` — Enter on a non-running local row of ollama/omlx/mtplx starts it through `internal/lifecycle`, showing the current engine stage and elapsed seconds; esc/q/ctrl+c cancels the run (the engine tears down anything it spawned); once cancellation is draining esc and q are ignored and only ctrl+c quits wt, so a mashed key cannot orphan a half-started server while a hung teardown still has an escape hatch; success launches immediately through `proceedToLaunch`, skipping the ollama availability check since the model just loaded; a cancel or any failure other than the two confirm cases rebuilds the table from a fresh inventory with the cursor kept — and `phaseReplaceConfirm` — when Start reports the provider's single slot occupied (`*OccupiedError`, naming the running model) or its state unknowable (`*OccupancyUnknownError`, naming provider and origin), a dialog offers Cancel (the default cursor position) and "Replace and start"; confirming re-issues Start with `AllowReplace: true`. Each picker is skipped only when its selection is already resolved: `prePath` (from `-W`/`--cwd`/outside-repo) skips the worktree picker, `-A` skips the agent+command picker, and `-M` skips the model picker (a pinned non-running local model starts immediately; a pinned model is validated against the agent's eligible list once the agent is resolved). Every picker uses `ThemedListDelegate` (active color theme) — production code never uses `list.NewDefaultDelegate`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`, `wt rotate`) skip the TUI. `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted (command agents like `shell` launch directly with no model layer).

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate` → worktrees / local branches / remote-only branches) and creation (`EnsureForName` for `-W`, `EnsureForBranch` for the picker). Every function takes `dir` (repo root) first for testability.

> **`Enumerate` fetches before listing.** It runs `git fetch --all --prune` (5s timeout) before building the picker's groups, so branches teammates pushed since your last manual fetch/pull/push show up, and remote-tracking refs for branches deleted upstream are pruned. Fetches every configured remote, not just `origin` (fork workflows: `upstream`, etc.). Failure — offline, no remotes, timeout — is silently ignored; the picker falls back to whatever local refs already exist. `-W`/`--cwd` skip `Enumerate` entirely (no picker, no fetch) and `EnsureForName`'s `-W` path never consults remotes in the first place.

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and
> directories inside `.git` have no worktree, so `--show-toplevel` fails there;
> `--git-dir` succeeds. Don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it, and the picker skips bare default-branch rows.

## Migration (Go)

On first run, `wt` migrates legacy `~/.config/agent-wt/models.conf` → `config.toml` (parses `CODE_MODELS`/`DESIGN_MODELS`, skips comments, seeds native providers, merges tag unions). Runs once, and writes the full legacy shape — including Providers/Models — via `saveFull` so `modelman migrate` can later import them into `registry.toml`; `Load`'s normal path still overwrites in-memory Providers/Models with the joined registry. `migrateConfigSchema` runs on every `Load()`, but only its **agent** fixups still matter (renames `google`→`agy`, ensures agy entries, removes `opencode` native provider) — its provider/model fixups are dead code now that `Load` overwrites those fields from the registry.

## Verification commands

```bash
go test ./...        # before any commit
go vet ./...         # static analysis
go build ./...       # verify compilation
wt                   # interactive TUI (needs TTY)
wt -W my-feature -A claude   # named worktree + launch
wt --cwd -A codex    # current repo root
claude-wt --cwd      # shim forwards to wt
wt --init            # seed agent instruction files
wt smoke <model-id>     # one-shot smoke test: every agent currently eligible for one model
                       # (live routing only, read-only against modelman.toml; see docs/wt-smoke.md)
make test-agents        # live one-shot smoke: every agent × configured models, both routing modes
                       # (flips [litellm].enabled off/on in modelman.toml — resolved at ${XDG_CONFIG_HOME:-$HOME/.config}/local-ai/modelman.toml, the path wt reads — and restores it afterwards; --modes current for one pass)
```

> Verifying a branch's behavior against live data requires building it first
> (`go build -o /tmp/wt-verify ./cmd/wt`) — `~/.local/bin/wt` is whatever was
> last `make install`ed and may predate the branch.
