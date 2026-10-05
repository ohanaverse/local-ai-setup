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
- **Model ids:** surveys and usage key on a free-form id and never require a registry entry. Unregistered on-disk models use `config.DiscoveredModelID(family, artifact)` (the probe family — `omlx` for an `omlx-6bit` row); a registry match keeps its registry id.
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

**Test seams.** TTY, installed-check, guard, TUI behavior, usage store, local-inventory probing, model starting, profiles, and stop flows are stubbed via package-level var seams (`tuiRun`, `launchFiltered`, `stdinTTY`, `installed`, `maybeInstallGuard`, `newUsageStore`, `flushTTY`, `stopSignalCtx`, `runInventory`, `startModel`, `probeInventory`, `smokeProbe`, `pickModelTUI`, `pickStartModelTUI`, `stopCandidates`, `stopEntries`, `stopPickerAll`, `confirmStop`, `ensureModelRoute`, `loadProfileStore`, `confirmProfile`, `openTTY`, `profileApplier`) — production code calls the var, tests swap it. New seams follow the same shape: a `var x = realX` plus a `realX` function.

Three `TestMain`s guarantee no Go test probes a real server, starts a real model, or drains the developer's terminal input queue:
- `internal/tui`: `runInventory` (stubs `localmodels.Inventory`) defaults to `localmodels.OnDiskSnapshotForTest` — every registry local model on disk, nothing running (local rows come only from the inventory, so an empty snapshot would hide them); `startModel` (stubs `lifecycle.Start`) fails; `ensureModelRoute` (stubs `lifecycle.EnsureModelRouteTo`) is a no-op and `tryEnsureModelRoute` (stubs `lifecycle.TryEnsureModelRouteTo`) reports "done, nothing changed" — both forms, since unstubbed either one reaches the real `config.yaml`.
- `cmd/wt` (`testmain_test.go`): `probeInventory` defaults to the same `localmodels.OnDiskSnapshotForTest`; its own `startModel` (a *different* seam from the TUI's despite the shared name — wraps `startForLaunch`) and `lifecycleStart` are hard-failed, so an unstubbed test can neither start a model nor let the route hook rewrite the real `config.yaml`; its own `ensureModelRoute` is a no-op for the same reason, and its `stubEnsureRoute(t)` also swaps `waitPendingRoutes` and captures `osStderr` — a check that reports "changed" goes on to announce a proxy restart, and that line would otherwise be sprayed over the test log by every launch test here (a test that wants to see it stubs `osStderr` itself, with `routeWaitInterval` shortened). Each package has a `stubEnsureRoute(t)` helper for tests that assert on the launch-time route check, and a new launch path must call the check through that seam.
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

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root. Packages are `cmd/wt` plus `internal/{config,rotation,usage,refcount,survey,agents,profiles,guard,worktree,initseed,themes,tui,configeditor,ollamacheck,catalog,localmodels,lifecycle,litellm,smoke}`.

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
| `internal/catalog/` | the shared row policy for every model list wt shows or resolves: `Build` (local rows only from the inventory), `Find` (includes discovered rows), `MissingReason` (why a pinned registry id has no row), `Row.Action` (launch/start/block), `Row.BlockReason`, presence status (`ok`/`unknown`; `new` for discovered, including discovered models handed in via `Input.Models`). No rendering, counts or sorting |
| `internal/localmodels/` | local inventory — see [Local-model resolution](#local-model-resolution) |
| `internal/lifecycle/` | local-model start/stop engine — see [Lifecycle](#lifecycle-internallifecycle) |
| `internal/litellm/` | the sole implementation of LiteLLM `config.yaml` route management (replaced modelman's Python writer): `configfile.go`, `entry.go`/`policy.go` (entries, provider mappings), `service.go` (`ApplyChange` — the targeted route writer the lifecycle hook uses, its unit `Change` with `RemoveFamilies`; `DiscoveredModel`, `RowFamily`; `Sync`/`PlanSync`), `restart.go` (`RestartContext`, `Listening`, `WaitReady`) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection, enumeration, creation |
| `internal/initseed/` | `--init` seeding |
| `internal/ollamacheck/` | pre-launch `ollama list` availability check |
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell, pickers, launch, start-on-select flow (`start_flow.go`); `modelrows.go` (rows + sort), `modeltable.go` (`buildTable`, `renderTable`); `PickStartModel` — standalone picker without launch-route gating, used by `wt start` and `wt smoke` (`PickModel`, the route-gated variant, currently has no production caller) |

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

**Catalog membership.** `Config.InCatalog` (`internal/config/config.go`) decides tag/family/provider catalog membership (#179, "configured is exposed"): native models (`auth.type = "native"`), local models (location resolves to `"local"`), and cloud models are all in — modelman's `exposed`/`ready` flags no longer gate wt's lists, and a local model's *row* additionally needs the live inventory to find it (see Local-model resolution). The only exclusion is a registry data gap: a `provider_id` naming no provider, no location on model or provider, or a location that is neither `local` nor `cloud` (#200) stays out (fail-closed on `ResolveLocation`).

**`ResolveLocation` is the one judge of a location.** It returns an error for a missing location and for one that is set but is not `local`/`cloud` (`Location.Valid`; a typo such as `"Local"`), on the model or inherited from its provider. Every consumer keys off that error and so agrees: out of the catalog, out of the inventory, a gap for `sync` (rows kept, warned), and a validation error — which makes `wt`, `wt start`, `wt stop` and `wt smoke` refuse to run until the registry is fixed, exactly as for a missing location. Validation also checks each provider entry directly, so one with no models (or whose models all override it) is still reported. Never compare a resolved location to decide validity, and never interpret an unknown value (modelman reads anything that is not exactly `local` as not local). The error matches `config.ErrLocation`; `configError` (`cmd/wt/helpers.go`) uses that to point the user at `registry.toml` instead of `wt config`, which cannot edit it.

wt does not read modelman's retired `exposed`/`litellm_exposed` keys, nor any other per-model `[model_state]` key (`ready`, legacy `downloaded`, `running`): what is on disk and what is running come from wt's live probes, and the picker has no EXPOSED column. Whether a model is routed is `wt litellm list` (`config.yaml` membership). Design: [../docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md](../docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md), which supersedes the predicate design in [../docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md](../docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md).

> **`unknown provider "X"` errors are usually a registry data gap, not a wt bug** — e.g. models referencing `provider_id`s with `providers = []`. Fix with `modelman sync`/`modelman migrate` on that machine (`sync` recreates default provider entries), not a code change here.

**No subprocess discovery.** `newApp()` only loads config. `localmodels.Inventory` does HTTP + filesystem discovery only. The only execs are `internal/lifecycle` (provider binaries `omlx`, `mtplx`) and the pre-launch `ollamacheck.Check` (see Agents).

> **Fixture gotcha.** `Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME` (and `RegistryPath()` also `MODELMAN_REGISTRY`) but use *different* subdirs: `config.toml` → `$XDG_CONFIG_HOME/agent-wt/`, `registry.toml` → `$XDG_CONFIG_HOME/local-ai/`. Test/smoke fixtures must populate both. Also, `migrateConfigSchema` unconditionally ensures an `agy` agent — any registry fixture with agents needs a matching `agy` provider or `Load`/`Validate` fails with `unknown provider "agy"`.

## Local-model resolution

Every model row wt shows or resolves — the TUI picker, `wt start`/`wt smoke`'s picker, and the non-TUI launch path — is built by `internal/catalog` from the agent's eligible list plus one `localmodels.Inventory` snapshot. The TUI only decorates rows (counts, survey, sort, rendering).

- **rows** — local rows come only from the inventory (#179 Phase B): a registry local model is listed when its entry is running, on disk, or of a family whose discovery failed (status `unknown`, so a flaky probe hides nothing); one confirmed missing from disk, a non-running model of a running-only family (`localmodels.RunningOnly`: an `mlx_lm_server` pairing), or one with no inventory entry has no row. A registry entry is an overlay matched by family + `model_name` (`source.matchArtifact`, `inventory.go`); unmatched artifacts are `new` rows under `config.DiscoveredModelID`, hidden whenever `-T`/`-F` is set. A hidden registry id still reserves its id, so an unregistered artifact spelling the same id is not listed under it.
- **launch** — a cloud row, or a local model the probe reports running.
- **start** — a non-running local row of ollama/omlx/omlx-6bit/mtplx (a pulled ollama model is a start, not a launch).
- **block** — a non-running row of a provider with no start engine (`modelman start <id>` hint; a stopped `mlx_lm_server` pairing has no row at all, so its pin gets the same hint from `MissingReason`). Separately, an `Unmapped` cloud model (its provider has no `PolicyFor` mapping, so sync never routes it) is refused ("not in LiteLLM") when its route goes through the proxy, and launches normally direct (LiteLLM off). A discovered model is never refused (#179 Phase B: wt routes it under its discovered id). The rule and its wording are `catalog.Row.RefusedByRoute`/`RouteRefusal`; `renderTable` (TUI), `pickerBlockedReason` (non-TUI) and `wt smoke` apply it.

**Inventory** (`localmodels.Inventory(cfg)`): probes ollama (`/api/tags` + `/api/ps`, cloud `remote_host` entries excluded), omlx/mtplx (model-dir scan + `/v1/models`) and mlx_lm_server (running only — `/v1/models`, no artifact discovery, so `ArtifactKnown` stays false; with 2+ registered pairings and none name-matching a served id it is `partial` + `Snapshot.Ambiguous`, since `/v1/models` lists HF repo ids, never a `+draft-` pairing name) concurrently. Per-family status `ok` / `partial` (the running-state probe failed, so `Running` is untrustworthy) / `unreachable` (discovery failed); a refused connection also sets `Snapshot.Down`. **Never reads modelman's `running` flag.** Probe origins resolve here (`FamilyOrigin`; `FamilyOriginPort` returns origin *and* port from one resolution, so a command-line port and dialed URL cannot disagree) and are shared with `internal/lifecycle`, so both describe the same server. `Entry.ArtifactKnown` distinguishes "confirmed missing" from "could not determine".

**A `-M` pin is looked up among ALL rows, discovered ones included** (`catalog.Find`). Its row decides: launch row → launch; start row → start flow; blocked → error with the row's reason; no row → `catalog.MissingReason` (`<id> is not on disk — pull or download it first`, or the `modelman start` hint for a running-only family), else "not in the eligible list". The TUI pin, `wt start` and `wt smoke` use the same fallback. A table with no rows at all — on entry or after a refresh — routes back to the agent picker with `catalog.NoRowsReason`.

**Non-TUI (`cmd/wt/resolve.go`):** with no `-M`, only launch rows are eligible (`launchableModels`) — rotation never hands an agent a server that is not up, a start row is never auto-selected (only a pin may start one), and rotation (`NextFromEligible`, which walks `cfg.Models`) never lands on a discovered row. `startForLaunch`: stderr progress, Ctrl+C cancels (second Ctrl+C exits wt), an occupied provider needs TTY `y/N` or `--replace`. In the TUI, `--replace` covers only the pinned row. Both paths use `lifecycle.StartErrorMessage`. No launchable match → "no cloud or running local model for agent X — start one with `wt -M <id>`".

## Lifecycle (`internal/lifecycle`)

Go port of modelman's start/warmup: `Start(ctx, cfg, Target, Options{AllowReplace, Progress})`, `Stop`, `StopModelDeferred` + `SettleRoutes` (one proxy restart per batch, #142), `Occupant`. Backends: ollama (daemon must already answer — wt never kickstarts it; multi-tenant), omlx (`omlx start` if down; `omlx stop` to replace), mtplx (`mtplx serve` in its own session with modelman's pidfile/log paths; torn down on failure/cancel). Writes nothing to modelman state. Used by the TUI start flow, `startForLaunch`, `wt start`, and `wt smoke`.

- **Never replaces without `AllowReplace`** (`*OccupiedError`). If the probe cannot determine occupancy (a single-model server accepted then stalled, or answered unusably) it returns `*OccupancyUnknownError` rather than assuming empty; connection refused is an ordinary cold start. Failure wording: `message.go` (`StartErrorMessage`, `StageLabel`).
- **Route hook** (`routes.go`): after a successful start/stop, `config.yaml` routes are updated via `internal/litellm` — a started model is routed under the catalog row's own id (`StartRouteChange` resolves `Target.ModelID`: the registry model with that exact id, else `litellm.DiscoveredModel`; `litellm.ModelFor`'s name match is the fallback only for a `Target` with an empty `ModelID`); single-model providers (omlx, mtplx) remove every marked route of the family (`Change.RemoveFamilies` — discovered siblings included, plus the family's registry ids; never a hand-written row) on start (minus the started id) and on stop; ollama adds one on start; stop leaves an ollama route in place (a pulled model stays routed — #179). The hook runs only when wt performs the start/stop. A launch row (already running, perhaps started outside wt) gets the launch-time route check instead, `lifecycle.EnsureModelRoute(cfg, model) bool` (#192): for a local model it writes the model's route if it is missing, prints `wt: LiteLLM route for <id> updated` on stderr when it wrote something, and restarts the proxy only if the file changed. It is Add-only — it never removes a route (no `RemoveFamilies`; stale routes are left to the next start, stop or `wt litellm sync`), never starts or stops a model, never replaces a hand-written row whose name is not a registry model id (one named like a registry id is adopted as wt's own row by `litellm.ApplyChange`, the rule `wt litellm sync` applies — guide 04, `docs/guides/04-litellm-config.md`, Gotchas) and never fails a launch. Callers: `launchFilteredImpl` (`cmd/wt/launch.go`: `-M` pin, single auto-resolved model, rotation) and the picker's `proceedToLaunch` (`internal/tui/app.go`), both only when the launch's route goes through LiteLLM; `wt start <running id>` (then `wt: <id> is already running`) and `wt smoke` on an already-running target, both unconditionally. LiteLLM failures only warn; a missing `config.yaml` is silent. It comes in two forms, differing only in how long they will wait for the `config.yaml` lock: `EnsureModelRoute` (bounded at `ensureRouteLockTimeout`, 10s; `EnsureModelRouteTo(out, …)` is the same check printing to the caller's writer) and `TryEnsureModelRouteTo`, which attempts the lock exactly once and reports `done == false` rather than waiting — for the TUI picker, whose update goroutine cannot block (see TUI). That form prints nothing on a contended lock; the retry, through the blocking form, reports. `Start` reports `StageRouting` before the hook.
- **`config.yaml` write is synchronous; restart + readiness wait are async** in a goroutine tracked by `WaitPendingRoutes()`. Every process exit must call it (`cmd/wt/main.go`, plus `runAgentCmd`'s exit-code propagation and `wt smoke`'s failure exit, which bypass main), **and every path about to hand the model to an agent must call it first** (`startForLaunch`, `start_flow.go` before `proceedToLaunch`, `smoke.go` before the first `RunRow`, and right after the launch-time route check: `ensureRouteBeforeLaunch` in `cmd/wt/start.go`, and the picker's `checkLaunchRoute` in `internal/tui/start_flow.go`, which waits in a command behind `phaseRouting` and only when the check changed `config.yaml`) — the agent talks through the proxy, so proceeding mid-restart gets a refused connection or the old route table. On the CLI paths that wait is the only stretch of a launch with nothing else on stderr to show for it, so `ensureRouteBeforeLaunch` announces it and repeats the line with elapsed time every `routeWaitInterval` (`waitForProxyRestart`) — the counterpart to the picker's `phaseRouting` screen. Which paths wait is the check's own return value: the write is the only thing that restarts the proxy (`restartIfChanged`), so an unchanged route — the common launch — prints nothing and its `waitPendingRoutes` returns at once.
- **Route output belongs to the operation:** everything `routes.go` prints (the `updated` line, every warning, the async restart's included) goes through one mutex-guarded helper, `routePrintf(ctx, …)`, to `routesWarn` (stderr) — unless the operation's context carries its own writer (`withRouteOutput`), which is how `EnsureModelRouteTo`/`TryEnsureModelRouteTo(out, …)` send one check's output to the caller. The writer rides on the context so that it follows the check into the restart goroutine (`context.WithoutCancel` keeps values) and nothing else: an unrelated operation's restart still in flight keeps printing to stderr instead of being collected as this check's (the process-wide `SetRouteOutput` redirect this replaced did exactly that — pinned by `TestRouteOutputIsNotSharedWithAnotherOperation`). `out` is safe to read only after `WaitPendingRoutes()`. The TUI picker is the one caller (below) — stderr is not reliably visible under the alt screen.
- **That goroutine runs on `context.WithoutCancel(ctx)` on purpose.** Callers `defer cancel()` the instant the hook returns; inheriting cancellation means the restart never runs. Don't "restore" cancellation propagation — `TestRouteRestartSurvivesCallerCancelAfterReturn` and `TestStartWrapperRestartSurvivesCancelledCaller` pin it. Restart warnings are therefore always printed.
- **Readiness wait:** before writing, `litellm.Listening` probes `/health/liveliness` once (1.5s; false only for refused/unresolvable, so a slow or 503 proxy counts as present). `WaitReady` (up to 30s) runs only if the hook restarted the proxy **and** that probe found a listener — a stopped proxy costs nothing.
- **Replace:** the occupant's route is removed as soon as it's stopped (`env.onOccupantStopped`, wired only by `defaultEnv`) so a start that then fails can't strand it; that write uses `litellm.Options.NoRestart`, and `Start` owns one settling bounce (`routeAfterStart`, or `bounceRoutes` on failure) — one restart per replace, not two. `bounceRoutes` bounds its synchronous lock wait with `settleTimeout` (15s); the async restart re-detaches with its own `WithoutCancel`.

## Rotation (Go)

Global rotation: each successful launch records one model id in `~/.config/agent-wt/rotation.state` (atomic write); the picker cursor lands on the model *after* it (`FirstAfter`, shared by the picker and `wt rotate`). Launch paths call `rotation.RecordFor(agent, id)`, which also appends the agent-tagged usage event to `~/.config/agent-wt/usage.jsonl`. Rotation positions the cursor only when a last-launched registry model exists (else first launchable row) and never advances onto a discovered row.

- **The sort order is also the default selection.** `newPickModel` (the standalone `wt smoke`/`wt start` pickers) never calls `Select`, so its highlighted row is index 0; `enterModelPhase`'s no-rotation fallback picks the first actionable row. Native-first (#172) therefore makes a native model what a bare Enter launches there — deliberate, and pinned by `TestPickStartModelDefaultsToNativeRow` / `TestEnterModelPhaseNoRotationDefaultsToNativeRow`. Changing `sortRows` changes what Enter picks; don't add a display-only rule to it without deciding the default too.
- The last-launched row gets a `> ` prefix — **plain ASCII on purpose**: Unicode geometric shapes are East Asian Ambiguous width and misalign CJK terminals.
- The leftmost column is the live "in use" count from `refcount.Store.Counts` (2-rune prefix, clamped at 9), before the rotation marker.
- Columns: FAMILY, MODEL, LOC, STATUS, RUNNING, COST, 1D, 7D, 30D, SURVEY. `wt smoke`'s picker has no agent context: SURVEY is empty and usage uses model-level `Counts`.
- Sort: native models first (#172); then cloud + running local by cost (output then input price; local/subscription-only = $0; no-data last), then 7-day usage; non-running local alphabetical. Native-first is a *partition*, not an exemption: native rows still fall through to the group rules among themselves.
- TUI callers fetch the agent's full catalog once (`cfg.ModelsForAgent`), narrow it with `cfg.EligibleModelsIn` (single-traversal filter), and pass it to `enterModelPhase`.

```bash
go run ./cmd/wt rotate code    # debug helper: print the model after the last-launched in the "code" tag group
```

## Agents (Go)

Each agent registers a `Driver` (`Build(m config.Model, yolo bool, r config.Route) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, cfg, extraArgs)` is the shared constructor for both launch paths — it resolves one `config.Route` via `cfg.ResolveRoute(m, agents.ProtocolsFor(agent))` and hands it to `Build`; drivers must not bypass it.

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
| `Syncer` | pre-launch sync, given the launch target and its resolved route (pi → `~/.pi/agent/models.json`: the registry models plus the launch target, so a discovered model gets its entry too, written where the route makes `Build` look — `litellm` for a protocol-forced route even with the toggle off) | pi |
| `ArgSetter` | passthrough args become argv | shell |
| `OneShotRunner` | `OneShotArgs(prompt)` for `wt smoke` | claude, codex, copilot, opencode, pi, agy |
| `StateDirer` | `StateDir(path)` — the agent's own per-working-directory state, which `wt smoke` removes for a temporary row directory | claude, pi |

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

`wt smoke [model-id]` runs a one-shot prompt through every agent eligible for one model via `agents.BuildLaunchCmdInfo` (the construction a real launch uses — `BuildLaunchCmd` wraps it — plus the `LaunchInfo` smoke needs), reporting PASS/FAIL/SKIP. It tests whatever routing mode is live — distinct from `make test-agents`' static agent×model matrix. It **applies matching profiles** (without prompting), may start an idle local pick first (shared `startModel` driver, honours `--replace`), and runs the exit-flow stop picker after a resolved run; it never flips LiteLLM routing. Each row runs in its own fresh temporary git repository (`smoke.NewRowDir`, made through a `smoke.RowDir` func only once the agent is about to start — a not-installed or model-fallback row makes none — removed after the row and swept again at the end of the run by `smoke.SweepRowDirs`, which also removes the agent's own state for that directory — `smoke.AgentStateDirs`, never for a `--cwd` run) unless the root `--cwd` flag is passed — agents run with permission checks off, so the default keeps them out of the caller's checkout (#193). The default prompt (`smoke.DefaultPrompt`) must never contain its sentinel verbatim: it gives the text in lower case and asks for upper case, because some agents (codex) print the prompt back and a sentinel the prompt contains would pass on that echo. With no model-id on a TTY, the picker is `tui.PickStartModel` over `smoke.Eligibility`'s cross-agent union (route-skipping, because `smoke.Candidates` already applied each agent's route rules). Progress lines go to stderr (`logSmokeStart`/`logSmokeResult`, sharing `writeSmokeFailDetail` with the final report so they can't drift). Full behavior: [docs/wt-smoke.md](docs/wt-smoke.md).

- **Yolo is forced on for every agent except codex** (`internal/smoke/smoke.go`, pinned by `TestRealBuildAndRunPassesYolo`/`TestRealBuildAndRunSkipsYoloForCodex`) — no TTY can answer a permission prompt, and a model spiralling on denials burns the whole timeout. codex's `exec` never prompts, and its yolo flag would only strip its sandbox. opencode's flag (`--auto`: approves what is "not explicitly denied", not an unconditional skip) is declared per command, so bare and in front of a subcommand it swallows it — the driver therefore emits it with its value attached (`--auto=true`), which is safe in any position: smoke's one-shot is `opencode --auto=true run <prompt>` (pinned by `TestRealBuildAndRunOpencodeYolo`). **Cost:** other agents run with permission checks off — in the row's temporary directory by default, in the caller's directory with `--cwd` — which is genuinely unsupervised with `--prompt` or an exploring model. The temporary directory only limits where a relative write lands; it is not a sandbox.
- **A model fallback is FAIL.** A driver that cannot select the model sets `LaunchCmd.ModelFallback` (pi's two "using default model" warnings); `agents.BuildLaunchCmdInfo` surfaces it, `realBuildAndRun` returns without running the agent and `RunRow` fails the row before looking at exit code or sentinel — the default model produces the sentinel just as well. A real launch (`BuildLaunchCmd`) only warns.
- **A row ends when the agent's process group is empty** (`settleGroup`): an exited agent's leftovers (claude's session-save hook recreated a removed row directory) get `groupGrace` (5s) and are then SIGKILLed, so the directory removal that follows is final. The end-of-run sweep is only the backstop for a process that left the group.
- **Git's repository variables are stripped** (`gitRepoEnv`: `GIT_DIR`, `GIT_WORK_TREE`, `GIT_INDEX_FILE`, …) from both `NewRowDir`'s `git init` and the agent's env, after the profile is applied: inherited from a git hook they beat `-C`/cwd and would point the row at the caller's repository.
- **`PWD` follows the working directory** (`agents.SetDir`, used by every launch and by smoke): the inherited `PWD` names wt's caller, os/exec only fills it in when the env has none, and opencode trusts it over getcwd — its smoke row ran in the caller's directory until this was set (pinned by `TestCommandSetsPWDToTheWorkdir`/`TestRealBuildAndRunSetsPWDToTheRowDir`). A shell script cannot observe a stale `PWD` (sh corrects it), so those tests read `cmd.Env`.
- **Ctrl+C is a cancellation, not a kill of wt.** Each agent runs in its own process group (`Setpgid`), so the terminal's SIGINT never reaches it. `runSmoke` runs the rows under `smokeSignalCtx` (a test seam over `signal.NotifyContext`, like `startSignalCtx`); a cancelled context makes `realBuildAndRun` SIGKILL the group and report `Interrupted`, the row FAILs as `interrupted`, its directory is removed, no further row runs, the report is printed, the stop picker is skipped and the command returns an `interrupted` error (pinned by `TestRealBuildAndRunCancelKillsTheAgentGroup`/`TestSmokeCmdInterruptEndsTheRun`).
- **Timeout is location-based** (`smokeTimeout`): unset `--timeout` (flag default `0`) → 180s cloud / 900s local (unresolvable = cloud); only an explicit value is validated. Local rows are slow because agents send 12–41k-token preambles that cold-prefill at 65–95 tok/s, so a timed-out local row usually isn't a broken agent.
- **Diagnosing a slow local row:** `/tmp/local-ai-setup-mtplx.log` has one `mtplx_openai_generation` line per *completed* request (`prompt_tokens`, `elapsed_s`) plus `memory guard`/`pressure_trim` lines; killed (timed-out) requests leave no line, and a repeat agent is fast only while its prefix is still in mtplx's session cache. Rows run sequentially.

## Start/stop (`wt start`, `wt stop`)

`cmd/wt/model_cmds.go`; user-facing behavior in [docs/wt-start-stop.md](docs/wt-start-stop.md). Code notes:
- `wt start <id>` resolves through `catalog.Find` over `localRows` (locals on disk or running, registered or detected, one snapshot), falling back to `catalog.MissingReason` for an id with no row; no-arg uses `tui.PickStartModel` via the `pickStartModelTUI` seam.
- `wt stop` uses `survey.StopCandidates`, `survey.StopEntries` (shared stop loop), and `survey.PickerWith(..., Options{IncludeInUse: true})`; the no-arg picker probes once and reports whether it had anything to offer (`stopPickerAll`). A bare provider is validated against `stoppableProviders` (what `lifecycle.CanStop` accepts). A target on omlx/mtplx widens to every running model of that provider (`withFamilyCollateral`). In-use targets go through `confirmStop` with `stopImpact` (sessions summed, a single-model provider counted once); `--yes` skips.

## LiteLLM routes (`wt litellm`)

`cmd/wt/litellm.go` + `internal/litellm`. Subcommands (`sync|list|providers|status|on|off|set`; `expose`/`unexpose` were removed in #179 — `sync` is the only CLI route write) and proxy lifecycle: [docs/wt-agents/README.md](docs/wt-agents/README.md#litellm-routes-are-wt-owned). JSON shapes are pinned by [../docs/contracts/litellm-cli.sample.json](../docs/contracts/litellm-cli.sample.json) (modelman parses them — `providers` replaced modelman's own provider table). Gotchas:

- **`sync` reconciles cloud and local rows (#179).** Desired = every `CloudModels` id plus the local models the probe found — running ones and pulled ollama models, registered or discovered (`desiredLocalModels`; #179 Phase B: a discovered route is written as `litellm.DiscoveredModel` under its discovered id, and a desired discovered id that already has an unmarked row is left to that row — no add, no adoption — on the start path too, `ApplyChange`); every row wt writes carries `model_info.wt_managed: true`. Sync removes a row only when it owns it (the marker, or an unmarked row named like a managed registry id — pre-marker rows, adopted on first sync); hand-written rows are never touched, and a marker-only removal spares an unmarked row of the same name. It never removes the row of a registry model with a data gap (`litellm.RegistryGap`: dangling `provider_id`, no location, or a location that is neither `local` nor `cloud`), and a cloud row whose build fails (e.g. a `secret_ref` resolving empty) keeps its existing row.
- **`sync` trusts only `ok` families** (and desires an ollama model when pulled, not only when loaded). A family whose probe ran and is neither `ok` nor refused (e.g. omlx/mtplx `/v1/models` failing) is frozen with a warning — its registry ids via `Options.Untouched`, and every row whose `litellm.RowFamily` is that family, discovered routes included, via `Options.UntouchedFamilies` (`untrustedFamilies`) — and providers with no probe (retired llamacpp) silently; the exception is a server that refuses the connection (`Snapshot.Down`) — its local routes are stale and removed. That case warns only when a local route of the family is in `config.yaml` or the family serves registry cloud models. A family that was never probed is frozen only when the registry references it unresolvably (`gapFamily`: a provider row with no location or one that is neither `local` nor `cloud`, or a `RegistryGap` model — warned as "could not be probed", with `gapReason` naming which); one the registry does not reference at all is not frozen, so its leftover marked rows are removed. A family frozen only for its discovered rows or its gap models' rows gets the warning when `config.yaml` holds a marked row of it. The removals are re-verified under the config.yaml lock (`Options.Recheck`) so a start landing between the probe and the write keeps its route.
- **Commands refuse only on a config *load* failure** (`app.loadErr`, so a default empty config is never acted on). Registry validation gaps (`cfgErr`) don't block: `sync` builds each desired row and reports broken ones per id (exit 1) while routing the healthy ones.
- **Every write fills an empty `api_base` on ollama rows (#202)** — `File.EnsureOllamaAPIBase`, called from `applyTo` (the in-memory apply step `applyPlanned` saves and `PlanSync` runs on a discarded `File`, so a dry run's `repair` is exactly what the real sync reports), with the registry ollama provider's `base_url` (`OllamaAPIBase`). LiteLLM runs `ollama serve` at proxy startup for any ollama row without one, which puts a second server on port 11434 and splits clients between the two; a model then stays loaded in the one wt cannot see. It is the one field wt sets on a hand-written row; a row that names an address — its own key, or one inherited through a YAML merge key (`mergedGet`) — is never changed, and a repaired row stays unmarked. Reported as outcome `litellm.ActionAPIBaseSet`, `api_base set` (dry run: `repair` / `would set api_base`); the lifecycle route writes print it too (`applyReported`: `wt: LiteLLM route for <id>: api_base set`). Test fixtures that assert a hand-written ollama row is byte-identical after a write must give it an `api_base`.
- `sync` takes `--dry-run` (its plan: add, adopt, rewrite, remove, repair, errors). `list` prints a wt row as its bare id and a hand-written row as `id<TAB>(hand-written)`; `--json` adds `rows` with `managed`. `status` never prints the api key (JSON `api_key_set`, text last 4 chars).
- **`config.yaml` handling:** comment-preserving yaml.v3 edits, but sequence indentation is normalized and blank lines dropped on the first wt write. Permission bits preserved; atomic write guarded by flock on `<config>.lock`. Path `WT_LITELLM_CONFIG` (legacy `MODELMAN_LITELLM_CONFIG`), default `~/.config/litellm/config.yaml`. Restart via `WT_LITELLM_RESTART_CMD` (legacy `MODELMAN_LITELLM_RESTART_CMD`), else `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`. `wt litellm sync` restarts and returns without waiting; only the lifecycle route hook and the launch-time route check wait (see [Lifecycle](#lifecycle-internallifecycle)).

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Sessions (Go)

wt does not manage agent sessions and never resumes one (#198, #204): no lookup, no prompt, no resume flag, on any launch path. `BuildLaunchCmd` appends the user's passthrough args unchanged, so continuing a conversation is the agent's own flags after `--` — in the picker too:

| | Continue the latest | A specific session | Fork it |
|---|---|---|---|
| claude | `claude-wt -- --continue` | `claude-wt -- --resume <id>` | `claude-wt -- --continue --fork-session` |
| opencode | `opencode-wt -- --continue` | `opencode-wt -- --session <id>` | `opencode-wt -- --continue --fork` |

claude's `--continue` means "the most recent conversation in the current directory", and wt starts the agent in the worktree (or, with `--cwd`, the directory the command was typed in), so it finds the sessions for that directory.

> **Don't bring the lookup back.** wt used to find the newest session recorded for the launch directory and add the agent's resume flag itself (the `Resumer` capability, `agents.ResumeSession`, the `internal/session` package, opencode's `opencode db` query, the picker's resume prompt, `--debug-session`). That appended a one-shot run (`-- -p "..."`) to whichever conversation was newest, including one another process was using (#204), failed an opencode launch whose resumed session had stored a different model (#198), and doubled the flag when the user passed their own. All of it is removed; claude's project-directory slug survives only as the unexported `claudeProjectSlug` in `internal/agents/claude.go`, for `StateDir`.

> **Native models are not special-cased.** Whether a resumed session's stored model overrides the chosen one is the agent's behaviour; wt does not guard against it.

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → launch (plus the starting, routing, replace-confirm and ollama-warning screens). Each picker is skipped when its selection is already resolved (`prePath` from `-W`/`--cwd`/outside-repo, `-A`, `-M`; a pinned non-running local starts immediately). Every picker uses `ThemedListDelegate` — production code never uses `list.NewDefaultDelegate`.

Start-on-select (`start_flow.go`): Enter on a start row → `phaseStarting` (engine stage + elapsed; esc/q/ctrl+c cancels and the engine tears down what it spawned). **Once cancellation is draining, esc and q are ignored and only ctrl+c quits** — a mashed key can't orphan a half-started server, while a hung teardown still has an escape hatch. Success goes straight to `proceedToLaunch`; a cancel or ordinary failure rebuilds the table from a fresh inventory with the cursor kept. `*OccupiedError`/`*OccupancyUnknownError` → `phaseReplaceConfirm` (Cancel is the default; "Replace and start" re-issues with `AllowReplace`). The single-row auto-launch shortcut applies only to launch rows.

Launch-time route check (`checkLaunchRoute`, `start_flow.go`, #192): `proceedToLaunch` is the funnel every model launch passes through; `launchSelected` is the build-the-command-and-launch half (it never looks up a session — see Sessions). For a launch row whose route goes through LiteLLM (a row this flow just started is skipped), `proceedToLaunch` calls `tryEnsureModelRoute` — the **non-blocking** form of the check — handing it a buffer of its own for everything the check prints (the retry and the restart's warnings go to the same buffer; nothing process-wide is redirected, so another operation's output cannot land in it). It runs on the update goroutine, so it may never wait: `lifecycle.EnsureModelRoute` bounds its config.yaml lock wait at 10s, and that wait on this goroutine freezes the screen with ctrl+c dead *before* its routing phase has been entered, which is the one screen built to cover a route wait. `TryEnsureModelRouteTo` instead attempts the flock once (an already-cancelled context is what a contended lock hits) and reports whether it got anywhere. **Free lock, unchanged** → record the notes, launch in the same `Update`: no screen, no wait. **Free lock, route written** → `phaseRouting`; **lock contended** → `phaseRouting` too, with the outcome not yet known. Either way the rest is in a command: `routingState.settle` re-runs the check through `ensureModelRoute` when the lock was contended (the one place the blocking form is reached from this flow), then calls `waitPendingRoutes()` and only then reads the buffer, all before its `routeDoneMsg` exists; the handler only sizes the model list, records the notes and calls `launchSelected`. The message is dropped when stale (by run id) and when the check is `quitting`. `phaseRouting` owns the keyboard: only ctrl+c does anything — it quits wt and sets `routingState.quitting`, because `tea.Quit`'s `QuitMsg` returns through the message channel and a `routeDoneMsg` landing first would otherwise launch an agent after the user quit. The write is done and the restart cannot be cancelled, so the view advertises no cancel. The captured text goes to `pendingRouteNotes` (`launch.go`) and nowhere else — not to the status line: the launch follows at once, so the only picker screen the user can land on next is the one a failed launch returns to, whose status is that failure (`proceedToLaunch` clears the status at the start of every attempt); `runAndWaitCmd` prints the notes to stderr the moment it releases the terminal, and `Run()` prints whatever is left after the program ends (`flushRouteNotesAfterRun`, which first settles a check the user quit out of with ctrl+c, printing `wt: waiting for the LiteLLM proxy restart…` when that wait is still ahead). `finishStart`'s own `waitPendingRoutes()` is still synchronous, behind the "Starting …" screen.

> **A view must never be taller or wider than the terminal** (`layout.go`). Bubble Tea drops a too-tall view's lines from the *top*, which is where every screen here puts its header and status line, and cuts a too-wide line at the right edge — so a mis-sized list does not clip the list, it silently removes the header, the status, or the last columns. Every list screen renders through a `listFrame` (`modelFrames`, `agentFrames`, `worktreeFrame`, `choiceFrame`); `fitTo` measures that same frame for the list's height (`fitList`) and side columns (`frameSides`), and measures the list itself (`listExtent`) for its least height and for the width at which bubbles really draws within the columns given — a bubbles list is not exactly as wide as it is told. That measuring happens only in `Update`: `View` picks its frame with `drawnFrame` — the fullest frame that leaves the list the height it already has, which is the one `fitTo` sized it for — and renders no probes. `Update` calls `fitLists` after every message, so a resize, a status appearing or clearing, or `?` expanding the help cannot leave a list stale; the standalone `pickModel` (`wt start`/`wt smoke` with no id) does the same through its own `frame()`/`fit()`, with its blocked-row notice as part of the frame. `fitTo` sizes through `sizeList`, which repeats `SetSize` until the page count, the rows per page and the page keys stop changing (at most three passes): bubbles computes rows-per-page from the pagination line and the help as they were *before* the call, and the expanded help is two rows taller once the page keys are enabled. Don't `SetSize` those lists anywhere else; add any new line to the frame, not to `View`; frames `clip` their own free text (status, path, hints) to the width. A test that sets state by hand must go through `Update` before measuring — or even reading — `View()`: a list still at its construction size is drawn in whichever frame leaves that much. A terminal too short for the model picker gives up its margin, then the agent/tag header, then the mode line and key hints — never the status or the table. `TestEveryListPhaseFitsTheTerminal` measures every screen at widths 40/80/120 and heights 12/24/50.

> **The model table drops whole columns on a narrow terminal** (`modeltable.go`, `tableColumns`). The header and every row share one `tableColumns`; `fitTableColumns` (called by `fitTo` and by the standalone `pickModel`) shows the columns that fit the list's width, giving up `colDropOrder` — SURVEY, 30D, 7D, 1D, COST, LOC, FAMILY — and never MODEL, STATUS or RUNNING. Nothing is rebuilt on a resize: rows redraw from the shared layout, so the inventory is not re-probed and the cursor, marker and filter survive. `modelItem.line` stays the *full* row and is the filter value, so a query still matches a hidden column. With everything shown the table is byte-for-byte the old one (`TestModelTableUnchangedWhenItFits`).

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
wt --cwd -A codex                    # current directory
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
