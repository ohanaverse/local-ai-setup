# CLAUDE.md

Guidance for Claude Code when working in this repo.

## What this repo is

The `wt` binary (`cmd/wt/`) launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. Three launch inputs:

1. **Where** — `--cwd` (current repo root), `-W <name>` (named worktree), or the worktree picker.
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
| `--cwd` | Launch in the current repo root; skip the worktree picker |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed AGENTS.md + pointer files, then exit |
| `--version` | Print version and exit |
| `--check-guard` / `--no-guard` | Check / remove the `block-main-commit` guard |
| `--debug-worktrees` | List worktrees and branches (test helper) |
| `--debug-session <agent>` | Print newest resumable session (test helper) |

## Passthrough args

Extra args forward to the launched agent. `--` is only required when the command starts with a flag-like token, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Post-exit flow

After the launched subprocess exits, the TUI and non-TUI paths run the same fixed sequence — order and user-facing behavior are in [docs/wt-agents/README.md#post-exit-order](docs/wt-agents/README.md#post-exit-order); don't reorder one path without the other. Code notes:

- **Summary line** (`wt: <agent> · <model-id> · <duration>`): formatter `internal/agents.Summary`, the single source for both paths. Printed on success and non-zero exit; never affects the exit code. Duration is measured when the agent exits.
- **Refcount release first.** `survey.ReleaseSession()` (`internal/survey/stop.go`, → `refcount` `Release(pid)`) drops wt's own entry before the stop picker — otherwise the model just used would always count as in use.
- **Stop picker** (`survey.Picker`): offers running local models with refcount zero, a trusted probe, and a stop backend (`lifecycle.CanStop`). Its loop (`stopEntries`, shared with `wt stop`) stops each model with `lifecycle.StopModelDeferred` (route removal written, no restart) and settles from a `defer`, so it reaches `lifecycle.SettleRoutes` on every exit path including Ctrl+C and failures — but only when some stop owed a restart, since a batch that removed nothing must not bounce the proxy. So N stops cost one LiteLLM restart, not N overlapping `kickstart -k`s (#142). Silent for non-TTY stdin, command agents, and native models. Ctrl+C/SIGTERM is caught on the picker's own context: at the prompt the read (on a goroutine raced against the ctx) is abandoned; during a stop the in-flight stop is cancelled and the rest skipped. Either way it prints `cancelled`, runs the TTY drain, and still reaches the summary.
- **Stale-pricing notice**: printed when modelman's top-level `price_refresh_last_run` in `modelman.toml` isn't today (absent or malformed → "never been refreshed" wording). wt only notifies; modelman owns the refresh. Parse errors stay silent. Skipped for command agents (`m.ID == ""`) and when the catalog has no OpenRouter-priced model (`agents.HasOpenRouterPricedModel`, #151). That function mirrors modelman's `_is_openrouter_priced` (an openrouter model, or one whose provider is a non-native cloud provider; ollama cloud models don't count), so change both together.

## Session survey (Go)

`internal/survey` records a post-session verdict (worked? speed? quality? task description) for every launch with a model — skipped for command agents (`m.ID == ""`) and native models (`m.Native`). `survey.PromptRun` is the single implementation for both paths (`cmd/wt/launch.go runAgentCmd`, and `internal/tui` via the same capture-then-emit pattern the summary uses). It no-ops when stdin is not a TTY and **returns** the after-survey stats block rather than printing it, so the caller can place it after the stop picker and summary.

- **Paste drain:** after the last question, `PromptRun` flushes the kernel TTY input queue (`drainTTYInput`: `TIOCFLUSH` darwin / `TCFLSH` linux / no-op elsewhere) — a multi-line paste at the free-text question delivers all its lines at once and anything unconsumed would otherwise execute as shell commands in the parent terminal after wt exits. Targeted at `os.Stdin`, not the injected reader, because the residue lives in the kernel queue for fd 0. Don't remove this to "simplify" the survey.
- **Store:** `~/.config/agent-wt/survey.jsonl`, wt-owned, 30-day retention, pruned on every write (mirrors `internal/usage`).
- **Worked%** = `worked / (worked + failed)` over *answered* surveys; skips are recorded but excluded from the denominator. The model picker's SURVEY segment (`✓<pct> q<quality> s<speed> n<answered>`) shows `⚠` instead of `✓` when `WorkedPct < 70%` and `Answered >= 3`.
- **Model ids:** surveys and usage key on a free-form id and never require a registry entry. Unregistered on-disk models use `config.DiscoveredModelID(provider, artifact)`; a registry match keeps its registry id.
- Reporting: `wt stats` — see [docs/wt-stats.md](docs/wt-stats.md).

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

**Test seams.** TTY, installed-check, guard, TUI behavior, usage store, local-inventory probing, model starting, profiles, and stop flows are stubbed via package-level var seams (`tuiRun`, `launchFiltered`, `stdinTTY`, `installed`, `maybeInstallGuard`, `newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`, `pickModelTUI`, `pickStartModelTUI`, `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`) — production code calls the var, tests swap it. New seams follow the same shape: a `var x = realX` plus a `realX` function.

Three `TestMain`s guarantee no Go test probes a real server, starts a real model, or drains the developer's terminal input queue:
- `internal/tui`: `runInventory` (stubs `localmodels.Inventory`) is a no-op; `startModel` (stubs `lifecycle.Start`) fails.
- `cmd/wt` (`testmain_test.go`): `probeInventory`, its own `startModel` (a *different* seam from the TUI's despite the shared name — wraps `startForLaunch`), and `lifecycleStart` are hard-failed, so an unstubbed test can neither start a model nor let the route hook rewrite the real `config.yaml`.
- `internal/survey`: `flushTTY` is a no-op.

`internal/smoke`'s `smokeProbe` is the same idea for `Eligibility`, with the exported `SetSmokeProbeForTest` for other packages. `internal/lifecycle` uses a different convention: every seam (HTTP clients, exec, inventory, timeouts, pidfile paths) lives in one `env` struct that `defaultEnv()` fills and tests rebuild with `testEnv()`; its `TestMain` doubles as a fake `mtplx` helper process when `LIFECYCLE_HELPER=mtplx`.

**Prefer asserting on unexported functions directly** — same-package tests can call them (e.g. `buildStatsRows`); parsing rendered lipgloss output couples tests to border glyphs/padding and flakes under forced-color ANSI.

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go test ./internal/agents -run TestOpenCodeOllamaPrefix -v   # one test
go vet ./...                         # static analysis
make check                           # shellcheck + shfmt check + go-format-check (gofmt -l gate wt-ci runs); `make format` writes both
```

From the monorepo root, `make test-all` runs the CI-equivalent sweep (root lint + modelman + wt build/vet/test).

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,smoke}`.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config and profiles.toml once) |
| `cmd/wt/commands.go` | `rotate` subcommand (debug helper) |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch from live `catalog` rows; a `-M` pin on a start row starts it |
| `cmd/wt/start.go` | `startForLaunch` — non-TUI start driver: stderr progress, Ctrl+C cancel, replace confirmation, `allowReplace` |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers; TTY seams and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `buildLaunch`, `launchFiltered`, `runAgentCmd`; profile apply (`applyProfileForLaunch`, `applyResolvedProfile`) |
| `cmd/wt/stats.go` | `wt stats` — read-only report over `survey.jsonl` |
| `cmd/wt/model_cmds.go` | `wt start` / `wt stop` |
| `cmd/wt/smoke.go` | `wt smoke` — one-shot model×agent smoke test |
| `cmd/wt/profile.go` | `wt profile list/show/status/on/off`; `setEnabledLine`'s surgical `enabled = ...` edit |
| `cmd/wt/litellm.go` | `wt litellm ...` |
| `internal/config/` | config load/validate/save (agents + joined registry catalog), route resolution (`ResolveRoute`), migrations |
| `internal/rotation/` | global rotation state (`rotation.state`) + next-model selection |
| `internal/usage/` | append-only JSONL launch history (1d/7d/30d); `RecordFor` tags the agent; `CountsForAgent` per agent×model, legacy agent-less lines count toward `Counts` only |
| `internal/refcount/` | live-session "in use" counts: JSONL keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point |
| `internal/survey/` | post-session survey + stats; stop picker and stop loop (`Picker`, `PickerWith`, `StopCandidates`, `StopEntries`) |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, capabilities), picker catalog, drivers, `RunAndCleanup` (shared apply-run-cleanup core for both launch paths), `BuildPassthroughCmd` |
| `internal/profiles/` | launch-profile overlays — see [Profiles](#profiles-internalprofiles) |
| `internal/smoke/` | `wt smoke`'s core: `Eligibility`/`Candidates` (rows per agent from one inventory snapshot — call `Eligibility` directly when you need both the model union and per-model agent lists, to avoid two probe rounds), `RunRow` (PASS/FAIL/SKIP via the `buildAndRun` seam) |
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build`, `Find` (includes discovered rows), `Row.Action` (launch/start/block), `Row.BlockReason`, presence status (`ok`/`absent`/`unknown`; `new` for discovered, including discovered models handed in via `Input.Models`). No rendering, counts or sorting |
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/lifecycle/` | local-model start/stop engine — see [Lifecycle](#lifecycle-internallifecycle) |
| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management (replaced modelman's Python writer): `configfile.go`, `entry.go`/`policy.go` (entries, provider mappings, ready gate), `service.go` (expose/unexpose/sync/list), `restart.go` (`RestartContext`, `Listening`, `WaitReady`) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection, enumeration, creation |
| `internal/initseed/` | `--init` seeding |
| `internal/session/` | resume detection (claude/opencode) |
| `internal/ollamacheck/` | pre-launch `ollama list` availability check |
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell, pickers, launch/resume, start-on-select flow (`start_flow.go`); `modelrows.go` (rows + sort), `modeltable.go` (`buildTable`, `renderTable`); `PickStartModel` — standalone picker without launch-route gating, used by `wt start` and `wt smoke` (`PickModel`, the route-gated variant, currently has no production caller) |

## Config (Go)

`~/.config/agent-wt/config.toml` (TOML) is wt-owned and holds **only Agents, DefaultTag, and the `[litellm]` routing table**. Providers/Models live in the registry (below); wt never writes them.

Key helpers: `Dir()` (config dir), `WriteFileAtomic`, `OllamaBaseURL` (`http://localhost:11434`), `FirstTag(s, fallback)`.

**Concurrent writers.** `config.toml` has three writers after startup: the `wt config` editor (Agents/DefaultTag), `wt litellm on|off|set` (`Config.UpdateLitellm`, `[litellm]`), and `Load`'s one-time migrations. None may blind-write a possibly-stale snapshot: the editor and `UpdateLitellm` go through `(*Config).PatchSave(apply func(fresh *Config))` — flock on `config.toml.lock` (`WithLock`), re-read fresh under the lock, `apply` mutates only its own fields. `Load`'s migrations use `lockedApply(apply func(fresh *Config) (bool, error))`, the same pattern plus a "did anything change" return so it only writes (and prints its notice) when a fixup fired — including re-checking that a concurrent `wt litellm set` hasn't already populated `[litellm]` before the legacy import. `Save(cfg)` (also locked) is only for a `Config` known to be fresh (tests, one-shot tools); anything holding a `Config` across an await/goroutine/process boundary must use `PatchSave` or `lockedApply`.

**Schema migrations.** `migrateConfigSchema` (`internal/config/migrate.go`) runs on every `Load` with self-extinguishing fixups: rename `google`→`agy` in agent refs; ensure an `agy` agent; rewire an existing `opencode` agent to ollama only; detect a legacy `[gateway]` block, print a one-time notice, and let the next save drop it (its url/api_key are not imported — use `wt litellm set`). Provider/model fixups are intentionally absent (the registry owns that data).

Full data model: [docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md](docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md).

> **Invalid-config repair.** `wt config` launches even when `config.toml` fails validation, so the editor can repair it. Other launch paths exit early on config errors.

`wt config theme [list|show|set|unset]` and `wt config path` — see [docs/wt-config.md](docs/wt-config.md).

## LiteLLM routing state (wt-owned)

Whether non-native models route through the LiteLLM proxy (`Config.IsLitellm()`) or dial providers directly (`Config.IsDirect()`) is the `[litellm]` table (`enabled`/`url`/`api_key`) in wt's `config.toml` (`Config.LitellmTable`). `modelman.toml`'s `[litellm]` is only a legacy read-only fallback (`finalizeCfg`/`loadModelmanState`): on the first `Load` where config.toml exists but lacks `[litellm]`, the legacy table is copied in once (never creates config.toml just to migrate); afterwards wt's copy wins. `UpdateLitellm` persists through `PatchSave` (file written 0600 when an api_key is stored); `LitellmConfigured()` = URL+key set. modelman's `litellm status|on|off|set` and TUI toggle are passthroughs to `wt litellm`, so modelman.toml's `[litellm]` is inert (round-tripped, never edited).

Toggling is routing policy only — it never touches the proxy. `LitellmBaseURL()` trims trailing slashes so drivers append `/v1` (or nothing for claude) cleanly. The proxy reads `config.yaml` only at startup; see [docs/wt-agents/README.md#litellm-proxy-lifecycle](docs/wt-agents/README.md#litellm-proxy-lifecycle).

## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models. Path precedence matches modelman's `_default_registry_path`: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` > `~/.config` — keep the two in sync. A `MODELMAN_REGISTRY` starting with `~/` (or exactly `~`) is expanded via `expandHome` to match Python's `Path.expanduser()`; Go never expands `~` itself, so this parity is deliberate.

wt loads it read-only via `config.Load` (fail-closed: missing/malformed registry is an error; seed with `modelman migrate`) and joins it in memory with `config.toml`. Decoded extra fields: `model_info` (`Model.ModelInfo`, merged into the LiteLLM rows wt writes — `internal/litellm/entry.go`), `cost` (`config.ModelCost`, rendered as the picker's price columns), `model_dir`, `auth.base_url`, `auth.secret_ref`. `fetch` is ignored.

**Missing registry → unconfigured-agent passthrough.** `config.Load` still returns `config.ErrRegistryMissing`, but `rootCmd().RunE` re-raises only other errors, and the returned `Config` keeps the parsed `Agents`/`DefaultTag` (empty catalog). An agent absent from `config.toml` (`agents.IsConfigured` false; commands are always configured) launches its installed binary with no model routing via `agents.BuildPassthroughCmd`, which reuses each driver's native branch through the sentinel `config.Model{Native: true, ModelName: "native"}` (same env-clearing as a real native launch; `opencode` gained a native guard for this). A *configured* agent with a missing registry fails on model resolution instead of silently launching native. `-M` on an unconfigured agent is an error; a real parse error still fails closed. The TUI marks an installed unconfigured agent `passthrough`; an uninstalled one stays blocked. Design: [docs/superpowers/specs/2026-09-22-wt-unconfigured-agent-passthrough-design.md](docs/superpowers/specs/2026-09-22-wt-unconfigured-agent-passthrough-design.md).

**Read-side schemas are pinned by contract fixtures** at the monorepo root: [../docs/contracts/registry.sample.toml](../docs/contracts/registry.sample.toml) and [../docs/contracts/modelman.sample.toml](../docs/contracts/modelman.sample.toml), loaded by `internal/config` contract tests and modelman's `tests/contracts/` — a schema change must update both sides or both CI jobs fail.

**Catalog membership.** `Config.InCatalog` (`internal/config/config.go`) decides tag/family/provider catalog membership (#179, "configured is exposed"): native models (`auth.type = "native"`), local models (location resolves to `"local"`), and cloud models are all in — modelman's `exposed`/`ready` flags no longer gate wt's lists. The only exclusion is a registry data gap: a `provider_id` naming no provider, or no location on model or provider, stays out even when the model names its own location (fail-closed on `ResolveLocation`).

The picker's EXPOSED column shows `Y` for native and otherwise the raw modelman flag (`Config.ExposedFlag`, no location special-casing). wt never writes modelman's `exposed` flag; for a local model the authoritative "is it routed" answer is `wt litellm list` (`config.yaml` membership). Design: [../docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md](../docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md), which supersedes the predicate design in [../docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md](../docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md).

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug** — e.g. models referencing `provider_id`s with `providers = []`. Fix with `modelman sync`/`modelman migrate` on that machine (`sync` recreates default provider entries), not a code change here.

**No subprocess discovery.** `newApp()` only loads config. `localmodels.Inventory` does HTTP + filesystem discovery only. The only execs are `internal/lifecycle` (provider binaries `omlx`, `mtplx`), the pre-launch `ollamacheck.Check` (see Agents), and resume detection's `opencode db` query (see Session resume) — the last one runs only when launching opencode, so it never costs anything on other paths.

> **Fixture gotcha.** `Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME` (and `RegistryPath()` also `MODELMAN_REGISTRY`) but use *different* subdirs: `config.toml` → `$XDG_CONFIG_HOME/agent-wt/`, `registry.toml` → `$XDG_CONFIG_HOME/local-ai/`. Test/smoke fixtures must populate both. Also, `migrateConfigSchema` unconditionally ensures an `agy` agent — any registry fixture with agents needs a matching `agy` provider or `Load`/`Validate` fails with `unknown provider "agy"`.

## Local-model resolution

Every model row wt shows or resolves — the TUI picker, `wt start`/`wt smoke`'s picker, and the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot. The TUI only decorates rows (counts, survey, sort, rendering).

- **launch** — a cloud row, or a local model the probe reports running.
- **start** — a non-running local row of ollama/omlx/omlx-6bit/mtplx whose status is not `absent` (a pulled ollama model is a start, not a launch).
- **block** — an `absent` row ("not on disk — pull or download it first") or a provider with no start engine such as `mlx_lm_server` (`modelman start <id>` hint). Separately, a row LiteLLM cannot serve — a discovered model, or an `Unmapped` cloud model (its provider has no `PolicyFor` mapping, so sync never routes it) — is refused ("not in LiteLLM") when its route goes through the proxy. The rule and its wording are `catalog.Row.RefusedByRoute`/`RouteRefusal`; `renderTable` (TUI), `pickerBlockedReason` (non-TUI) and `wt smoke` apply it. Direct (LiteLLM off) such rows launch normally.

**Inventory** (`localmodels.Inventory(cfg)`): probes ollama (`/api/tags` + `/api/ps`, cloud `remote_host` entries excluded), omlx/mtplx (model-dir scan + `/v1/models`) and mlx_lm_server (running only) concurrently. Per-family status `ok` / `partial` (the running-state probe failed, so `Running` is untrustworthy) / `unreachable` / `unsupported`. **Never reads modelman's `running` flag.** Probe origins resolve here (`FamilyOrigin`; `FamilyOriginPort` returns origin *and* port from one resolution, so a command-line port and dialed URL cannot disagree) and are shared with `internal/lifecycle`, so both describe the same server. `Entry.ArtifactKnown` distinguishes "confirmed missing" from "could not determine".

**A `-M` pin is looked up among ALL rows, discovered ones included** (`catalog.Find`). Its row decides: launch row → launch; start row → start flow; blocked → error with the row's reason; absent from the list → "not in the eligible list".

**Non-TUI (`cmd/wt/resolve.go`):** with no `-M`, only launch rows are eligible (`launchableModels`) — rotation never hands an agent a server that is not up, and a start row is never auto-selected; only a pin may start one. `startForLaunch`: stderr progress, Ctrl+C cancels (second Ctrl+C exits wt), an occupied provider needs TTY `y/N` or `--replace`. In the TUI, `--replace` covers only the pinned row. Both paths use `lifecycle.StartErrorMessage`. No launchable match → "no cloud or running local model for agent X — start one with `wt -M <id>`".

## Lifecycle (`internal/lifecycle`)

Go port of modelman's start/warmup: `Start(ctx, cfg, Target, Options{AllowReplace, Progress})`, `Stop`, `StopModelDeferred` + `SettleRoutes` (one proxy restart per batch, #142), `Occupant`. Backends: ollama (daemon must already answer — wt never kickstarts it; multi-tenant), omlx (`omlx start` if down; `omlx stop` to replace), mtplx (`mtplx serve` in its own session with modelman's pidfile/log paths; torn down on failure/cancel). Writes nothing to modelman state. Used by the TUI start flow, `startForLaunch`, `wt start`, and `wt smoke`.

- **Never replaces without `AllowReplace`** (`*OccupiedError`). If the probe cannot determine occupancy (a single-model server accepted then stalled, or answered unusably) it returns `*OccupancyUnknownError` rather than assuming empty; connection refused is an ordinary cold start. Failure wording: `message.go` (`StartErrorMessage`, `StageLabel`).
- **Route hook** (`routes.go`): after a successful start/stop, `config.yaml` routes are updated via `internal/litellm` — single-model providers (omlx, mtplx) replace sibling routes on start and drop all family routes on stop; ollama adds one on start; stop leaves an ollama route in place (a pulled model stays routed — #179). LiteLLM failures only warn; a missing `config.yaml` is silent. `Start` reports `StageRouting` before the hook.
- **`config.yaml` write is synchronous; restart + readiness wait are async** in a goroutine tracked by `WaitPendingRoutes()`. Every process exit must call it (`cmd/wt/main.go`, plus `runAgentCmd`'s exit-code propagation and `wt smoke`'s failure exit, which bypass main), **and every path about to hand the model to an agent must call it first** (`startForLaunch`, `start_flow.go` before `proceedToLaunch`, `smoke.go` before the first `RunRow`) — the agent talks through the proxy, so proceeding mid-restart gets a refused connection or the old route table.
- **That goroutine runs on `context.WithoutCancel(ctx)` on purpose.** Callers `defer cancel()` the instant the hook returns; inheriting cancellation means the restart never runs. Don't "restore" cancellation propagation — `TestRouteRestartSurvivesCallerCancelAfterReturn` and `TestStartWrapperRestartSurvivesCancelledCaller` pin it. Restart warnings are therefore always printed.
- **Readiness wait:** before writing, `litellm.Listening` probes `/health/liveliness` once (1.5s; false only for refused/unresolvable, so a slow or 503 proxy counts as present). `WaitReady` (up to 30s) runs only if the hook restarted the proxy **and** that probe found a listener — a stopped proxy costs nothing.
- **Replace:** the occupant's route is removed as soon as it's stopped (`env.onOccupantStopped`, wired only by `defaultEnv`) so a start that then fails can't strand it; that write uses `litellm.Options.NoRestart`, and `Start` owns one settling bounce (`routeAfterStart`, or `bounceRoutes` on failure) — one restart per replace, not two. `bounceRoutes` bounds its synchronous lock wait with `settleTimeout` (15s); the async restart re-detaches with its own `WithoutCancel`.

## Rotation (Go)

Global rotation: each successful launch records one model id in `~/.config/agent-wt/rotation.state` (atomic write); the picker cursor lands on the model *after* it (`FirstAfter`, shared by the picker and `wt rotate`). Launch paths call `rotation.RecordFor(agent, id)`, which also appends the agent-tagged usage event to `~/.config/agent-wt/usage.jsonl`. Rotation positions the cursor only when a last-launched registry model exists (else first launchable row) and never advances onto a discovered row.

- **The sort order is also the default selection.** `newPickModel` (the standalone `wt smoke`/`wt start` pickers) never calls `Select`, so its highlighted row is index 0; `enterModelPhase`'s no-rotation fallback picks the first actionable row. Native-first (#172) therefore makes a native model what a bare Enter launches there — deliberate, and pinned by `TestPickStartModelDefaultsToNativeRow` / `TestEnterModelPhaseNoRotationDefaultsToNativeRow`. Changing `sortRows` changes what Enter picks; don't add a display-only rule to it without deciding the default too.
- The last-launched row gets a `> ` prefix — **plain ASCII on purpose**: Unicode geometric shapes are East Asian Ambiguous width and misalign CJK terminals.
- The leftmost column is the live "in use" count from `refcount.Store.Counts` (2-rune prefix, clamped at 9), before the rotation marker.
- Columns: FAMILY, MODEL, LOC, STATUS, EXPOSED, RUNNING, COST, 1D, 7D, 30D, SURVEY. `wt smoke`'s picker has no agent context: SURVEY is empty and usage uses model-level `Counts`.
- Sort: native models first (#172); then cloud + running local by cost (output then input price; local/subscription-only = $0; no-data last), then 7-day usage; non-running local alphabetical. Native-first is a *partition*, not an exemption: native rows still fall through to the group rules among themselves.
- TUI callers fetch the agent's full catalog once (`cfg.ModelsForAgent`), narrow it with `cfg.EligibleModelsIn` (single-traversal filter), and pass it to `enterModelPhase`.

```bash
go run ./cmd/wt rotate code    # debug helper: print the model after the last-launched in the "code" tag group
```

## Agents (Go)

Each agent registers a `Driver` (`Build(m config.Model, yolo bool, r config.Route) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` is the shared constructor for both launch paths — it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build`; drivers must not bypass it.

`Route` carries `BaseOrigin` (scheme://host:port, no wire-path suffix), `APIKey`, `ModelRef`, `Display`, `ProviderID`, `Protocol`, `Litellm`, `Forced`. Direct routes dial the provider's own `auth.base_url` (key from `auth.secret_ref` via `ResolveSecret`); litellm/forced routes dial the `[litellm]` url/key.

**Agent protocols.** Agents declare wire protocols via `ProtocolDeclarer`; providers declare what they serve (`Provider.protocols`, default `openai-chat`). An empty intersection sets `Forced = true` — LiteLLM regardless of the toggle.

| Agent | Declared protocols |
|---|---|
| claude | `anthropic` |
| codex | `openai-responses` |
| copilot, opencode, pi | `openai-chat` |
| agy, shell | none — agy is a native passthrough, shell a command runner |

Consequence: **codex always routes through LiteLLM** (no local provider serves `openai-responses`), and `BuildLaunchCmd` prints the forced-LiteLLM notice on every direct-mode launch; claude × openrouter is forced the same way. litellm ≤ 1.98.0 cannot serve codex without a workaround — see [docs/wt-agents/codex-wt.md](docs/wt-agents/codex-wt.md).

**Model id contract** (resolved centrally in `ResolveRoute`): litellm/forced routes set `Route.ModelRef` to the **registry id** (`m.ID`, e.g. `ollama/qwen3.8:27b-mlx` — LiteLLM's `model_list` key); direct routes use the **provider-side name** (`m.ModelName`). `Route.Display` is always `m.ModelName`. Regression tests (`TestClaudeOllamaPrefix`, `TestOpenCodeOllamaPrefix`, …) use distinct `ID`/`ModelName` so a wrong id can't slip through.

**copilot must use the chat-completions wire (`WIRE_API=completions`)** — its `responses` wire drops leading characters through the OpenAI-compatible bridge (verified direct and via LiteLLM). This deliberately diverges from `ollama launch copilot`, which prescribes `responses`.

Per-agent env/args/config shapes (direct and LiteLLM): [docs/wt-agents/](docs/wt-agents/) `{claude,codex,copilot,opencode,pi,agy,shell}-wt.md`. Notable: **agy is not a command agent** (`IsCommand("agy")` is false), so `-A agy` without `-M` errors "multiple models match" when >1 agy model is eligible.

**Optional capabilities** (type assertions in `BuildLaunchCmd`):

| Capability | Purpose | Implemented by |
|---|---|---|
| `ProtocolDeclarer` | wire protocols → `ResolveRoute` | claude, codex, copilot, opencode, pi |
| `Seeder` | pre-launch AGENTS.md + pointer seeding | claude, copilot |
| `Syncer` | pre-launch sync (pi → `~/.pi/agent/models.json`) | pi |
| `ArgSetter` | passthrough args become argv | shell |
| `Resumer` | `ResumeFlag()` + `LatestSession(path)` | claude, opencode |
| `OneShotRunner` | `OneShotArgs(prompt)` for `wt smoke` | claude, codex, copilot, opencode, pi, agy |

**Pre-launch `ollamacheck.Check`** (both paths: `cmd/wt/launch.go`, `internal/tui/app.go`) runs only when the model's *resolved route* is direct and the model is ollama (`!route.Litellm && ollamacheck.IsOllamaModel(m)`) — not the raw toggle, so a protocol-forced route (codex+ollama) isn't spuriously blocked. It shells out to `ollama list`. The TUI start flow skips it for a model it just loaded.

New driver: see the `adding-a-wt-agent` skill.

## Profiles (`internal/profiles`)

Opt-in overlays from `~/.config/agent-wt/profiles.toml` (absent = enabled, zero profiles), resolved per agent × model (`profiles.Resolve`). Operator reference and per-agent mechanisms: [docs/wt-agents/profiles.md](docs/wt-agents/profiles.md).

- **Where applied:** both launch paths via `applyProfileForLaunch` (`cmd/wt/launch.go`; the TUI reaches it through the `profileApplier` seam set in `main.go`), and `wt smoke` via `applyResolvedProfile` directly. Skipped for command agents and native models.
- **Confirmation:** an interactive launch asks `apply? [Y/n]` on `/dev/tty` (`confirmProfile`); with no TTY it auto-applies (default yes). `wt smoke` never prompts.
- **Apply order** (`applyResolvedProfile`): env/args → `config_content` → wrapper. Smoke's one-shot args are inserted after profile flags but before the wrapper — codex silently drops a `-c model_provider=...` that trails `exec <prompt>`, and the wrapper splices the current argv.
- **`config_content`** backs up and rewrites the agent's config file (claude `<worktree>/.claude/settings.local.json`, codex `~/.codex/agent-wt-profile.config.toml`; opencode merges into `OPENCODE_CONFIG_CONTENT` instead). The cleanup must be called explicitly after the run (not deferred — `runAgentCmd`'s `os.Exit` would skip it). `SelfHeal` restores an orphaned backup from a killed session; it runs on *every* launch of that agent, before profiles.toml is even loaded.
- **Failure posture:** load/validate/apply errors degrade to an unprofiled launch with a stderr warning (a `config_content` failure also reverts env/args already applied). The one fatal case: a wrapper naming a missing binary.
- **CLI:** `wt profile list`, `wt profile show -A <agent> -M <id>` (dry run), `wt profile status`, `wt profile on|off` (refuses to write if profiles.toml failed to parse).

## Smoke test (`wt smoke`)

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model via `agents.BuildLaunchCmd` (the same construction a real launch uses), reporting PASS/FAIL/SKIP. It tests whatever routing mode is live — distinct from `make test-agents`' static agent×model matrix. It **applies matching profiles** (without prompting), may start an idle local pick first (shared `startModel` driver, honours `--replace`), and runs the exit-flow stop picker after a resolved run; it never flips LiteLLM routing. With no model-id on a TTY, the picker is `tui.PickStartModel` over `smoke.Eligibility`'s cross-agent union (route-skipping, because `smoke.Candidates` already applied each agent's route rules). Progress lines go to stderr (`logSmokeStart`/`logSmokeResult`, sharing `writeSmokeFailDetail` with the final report so they can't drift). Full behavior: [docs/wt-smoke.md](docs/wt-smoke.md).

- **Yolo is forced on for every agent except codex** (`internal/smoke/smoke.go`, pinned by `TestRealBuildAndRunPassesYolo`/`TestRealBuildAndRunSkipsYoloForCodex`) — no TTY can answer a permission prompt, and a model spiralling on denials burns the whole timeout. codex's `exec` never prompts, and its yolo flag would only strip its sandbox. **Cost:** other agents run in the cwd with permission checks off — inert with the sentinel prompt, genuinely unsupervised with `--prompt` or an exploring model.
- **Timeout is location-based** (`smokeTimeout`): unset `--timeout` (flag default `0`) → 180s cloud / 900s local (unresolvable = cloud); only an explicit value is validated. Local rows are slow because agents send 12–41k-token preambles that cold-prefill at 65–95 tok/s, so a timed-out local row usually isn't a broken agent.
- **Diagnosing a slow local row:** `/tmp/local-ai-setup-mtplx.log` has one `mtplx_openai_generation` line per *completed* request (`prompt_tokens`, `elapsed_s`) plus `memory guard`/`pressure_trim` lines; killed (timed-out) requests leave no line, and a repeat agent is fast only while its prefix is still in mtplx's session cache. Rows run sequentially.

## Start/stop (`wt start`, `wt stop`)

`cmd/wt/model_cmds.go`; user-facing behavior in [docs/wt-start-stop.md](docs/wt-start-stop.md). Code notes:
- `wt start <id>` resolves through `catalog.Find` over `localRows` (configured + detected locals, one snapshot); no-arg uses `tui.PickStartModel` via the `pickStartModelTUI` seam.
- `wt stop` uses `survey.StopCandidates`, `survey.StopEntries` (shared stop loop), and `survey.PickerWith(..., Options{IncludeInUse: true})`; the no-arg picker probes once and reports whether it had anything to offer (`stopPickerAll`). A bare provider is validated against `stoppableProviders` (what `lifecycle.CanStop` accepts). A target on omlx/mtplx widens to every running model of that provider (`withFamilyCollateral`). In-use targets go through `confirmStop` with `stopImpact` (sessions summed, a single-model provider counted once); `--yes` skips.

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`. Subcommands (`expose|unexpose|sync|list|providers|status|on|off|set`) and proxy lifecycle: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned). JSON shapes are pinned by [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json) (modelman parses them — `providers` replaced modelman's own provider table). Gotchas:

- **`sync` reconciles cloud and local rows (#179).** Desired = every `CloudModels` id plus the running registry local models; every row wt writes carries `model_info.wt_managed: true`. Sync removes a row only when it owns it (the marker, or an unmarked row named like a managed registry id — pre-marker rows, adopted on first sync); hand-written rows are never touched, and a marker-only removal spares an unmarked row of the same name. It never removes the row of a registry model with a data gap (dangling `provider_id`, no location), and a cloud row whose build fails (e.g. a `secret_ref` resolving empty) keeps its existing row.
- **`sync` trusts only `ok` families** (and desires an ollama model when pulled, not only when loaded). A family whose probe is `partial` (e.g. omlx/mtplx `/v1/models` failing) is left alone with a warning, and providers with no probe (retired llamacpp) silently; the exception is a server that refuses the connection (`Snapshot.Down`) — its local routes are stale and removed. That case warns only when a local route of the family is in `config.yaml` or the family serves registry cloud models.
- **Commands refuse only on a config *load* failure** (`app.loadErr`, so a default empty config is never acted on). Registry validation gaps (`cfgErr`) don't block: `litellm.Apply` validates each id and reports broken ones per id (exit 1) while applying the healthy ones.
- `expose` and `sync` take `--dry-run` (sync's plan: add, adopt, rewrite, remove, errors); `expose` alone takes `--skip-ready-gate` (used by modelman after it checked readiness). `list` prints a wt row as its bare id and a hand-written row as `id<TAB>(hand-written)`; `--json` adds `rows` with `managed`. `status` never prints the api key (JSON `api_key_set`, text last 4 chars).
- **`config.yaml` handling:** comment-preserving yaml.v3 edits, but sequence indentation is normalized and blank lines dropped on the first wt write. Permission bits preserved; atomic write guarded by flock on `<config>.lock`. Path `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`), default `~/.config/litellm/config.yaml`. Restart via `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_RESTART_CMD`), else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. The `wt litellm` commands restart and return without waiting; only the lifecycle route hook waits (see [Lifecycle](#lifecycle-internallifecycle)).

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Session resume (Go)

`internal/session` finds the newest resumable session (claude `*.jsonl` under `~/.claude/projects/<slug>`, opencode: the newest top-level, unarchived `session` row whose `directory` is exactly the worktree path — **not** opencode's project id, which every worktree of a repo shares). Others (incl. shell) return nil. On Enter in the TUI, a prior session offers Start fresh (default) / Cancel / Resume (`--resume <id>` claude, `--session <id>` opencode). Non-TUI does the same without prompting.

> **Both paths go through `agents.ResumeSession`.** opencode's lookup runs `opencode db <sql> --format json` through the `opencodeDBQuery` seam (production: `realOpenCodeDBQuery`) — opencode's own binary resolves the database location (`OPENCODE_DB`, the per-channel `opencode-<channel>.db` name) and reads it with bundled SQLite, so wt never hardcodes a path or needs a `sqlite3` CLI on PATH (#162). A **failed lookup warns and launches fresh** on both paths — deliberately not fatal. The TUI used to abort the launch on a lookup error the non-TUI path silently discarded: two behaviours for one failure, and the abort made the agent unlaunchable. Tests drive the seam, so they run without opencode or sqlite3 installed.

> **Native models never resume.** A native model (e.g. `claude/native`) launches with no model override; resuming would restore the session's stored model and silently override "native" (routing a proxy-routed model at the real Anthropic API). Both paths skip the session lookup for native models.

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → resume prompt → launch. Each picker is skipped when its selection is already resolved (`prePath` from `-W`/`--cwd`/outside-repo, `-A`, `-M`; a pinned non-running local starts immediately). Every picker uses `ThemedListDelegate` — production code never uses `list.NewDefaultDelegate`.

Start-on-select (`start_flow.go`): Enter on a start row → `phaseStarting` (engine stage + elapsed; esc/q/ctrl+c cancels and the engine tears down what it spawned). **Once cancellation is draining, esc and q are ignored and only ctrl+c quits** — a mashed key can't orphan a half-started server, while a hung teardown still has an escape hatch. Success goes straight to `proceedToLaunch`; a cancel or ordinary failure rebuilds the table from a fresh inventory with the cursor kept. `*OccupiedError`/`*OccupancyUnknownError` → `phaseReplaceConfirm` (Cancel is the default; "Replace and start" re-issues with `AllowReplace`). The single-row auto-launch shortcut applies only to launch rows.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`, `wt rotate`) skip the TUI. `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted (command agents like `shell` launch directly with no model layer).

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
wt --cwd -A codex                    # current repo root
claude-wt --cwd                      # shim forwards to wt
wt --init                            # seed agent instruction files
wt start [<id>] / wt stop [<id>|<provider>]   # local-model lifecycle (routes follow automatically)
wt litellm list / sync / status      # routed ids, reconcile cloud + running local routes, routing state
wt profile show -A <agent> -M <id>   # dry-run profile resolution
wt stats                             # survey report
wt smoke <model-id> [--only claude,codex] [--prompt P] [--timeout 5m] [--json]
make test-agents                     # live agent × model matrix in both routing modes
                                     # (flips routing via `wt litellm off|on`, snapshotting and restoring wt's config.toml; --modes current for one pass)
```

> Verifying a branch's behavior against live data requires building it first (`go build -o /tmp/wt-verify ./cmd/wt`) — `~/.local/bin/wt` is whatever was last `make install`ed and may predate the branch.
