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
| `-M <id>`, `--model <id>` | Pin model as `<provider>/<name>`; errors if not eligible. Without `-A`, prompts for the agent first, then validates the pin |
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

Immediately after the summary, a stale-pricing notice may print (one line, issue #69): when modelman's `price_refresh_last_run` (top-level key in `~/.config/local-ai/modelman.toml`) isn't today's date — or is absent — wt prints `wt: token pricing last refreshed <date> — run 'modelman refresh-prices'` (or the "never been refreshed" variant; a malformed (non-`YYYY-MM-DD`) value also uses the "never been refreshed" wording). wt only notifies; modelman owns the refresh. Parse errors on modelman.toml stay silent. Skipped for command agents like `shell` (`m.ID == ""` — same convention the session survey below uses), since they never touch a priced model.

Immediately after the summary, a post-session survey prompts up to four questions (did it work? speed? quality? — and on non-skip answers, what task were you doing) on the parent terminal — see [Session survey](#session-survey-go) below.

## Session survey (Go)

`internal/survey` records a post-session verdict for every agent launch with
a model (skipped for command agents like `shell`, whose `m.ID == ""`).
`survey.PromptRun` is the single implementation wired into both the non-TUI
path (`cmd/wt/launch.go runAgentCmd`, after the summary line, before the
exit-code propagation) and the TUI path (`internal/tui`, via the same
capture-then-emit pattern the summary line uses). It silently no-ops when
stdin is not a TTY.

Order on every launch: **summary → survey prompts → after-survey stats**.

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
- **Model picker:** each row gets a trailing `✓<pct> q<quality> s<speed>
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

**Test seams.** TTY, installed-check, guard, TUI behavior, and the model
picker's usage store are stubbed via package-level var seams (`tuiRun`,
`launchFiltered`, `stdinTTY`, `installed`, `maybeInstallGuard`,
`newUsageStore`, `flushTTY`) — production code calls the var, tests swap it. When adding
a new seam, follow the same shape: a `var x = realX` plus a `realX` function.

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

Package list: `internal/{config,rotation,usage,refcount,survey,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck,localgate,localmodels,smoke}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts — each test's focus is documented in its own `//` comment (see above).

## Go module

Module root is `wt/` (`go.mod` declares `github.com/ohanaverse/local-ai-setup/wt`); run `go build ./...` / `go test ./...` from there, not from the monorepo root.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config once) |
| `cmd/wt/commands.go` | hidden `rotate` subcommand |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers (`maybeInstallGuard`, `checkGuardStatus`, `removeGuard`); TTY seams (`isStdinTTY`/`stdinTTY`) and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `buildLaunch`, `launchFiltered` (all take `extraArgs`) |
| `cmd/wt/stats.go` | `wt stats` command — read-only report over `survey.jsonl` |
| `cmd/wt/smoke.go` | `wt smoke` command — one-shot model×agent smoke test, human/JSON output |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
| `internal/rotation/` | global rotation state + `Next` for the picker (replaces the per-slot model) |
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model picker's per-row usage columns, and the rotation module; events carry an optional `agent` (`RecordFor`); `CountsForAgent` gives per-agent-model-pair counts, legacy agent-less lines count toward `Counts` only |
| `internal/refcount/` | live-session "in use" model counts: JSONL state file keyed by pid, swept for dead pids on every launch, recorded at each launch path's commit point, consumed by the model picker's ref column |
| `internal/survey/` | post-session survey: append-only JSONL verdicts (worked/speed/quality + task description), 1d/7d/30d stats per model and per agent×model combo — used by the post-run prompt, the model picker's survey segment, and `wt stats` |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/smoke/` | `wt smoke`'s testable core: `Eligibility` (one localgate probe round → both the eligible-model union and each model's eligible-agent list; `EligibleAgents`/`AllEligibleModels` are thin wrappers over it — a caller needing both answers in one invocation should call `Eligibility` directly to avoid paying two probe rounds), `RunRow` (PASS/FAIL/SKIP classification via the `buildAndRun` seam) |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection (`IsRepo`, `RepoRootAt`, `RepoRoot`), enumeration (`Enumerate`), creation (`EnsureForName`/`EnsureForBranch`) |
| `internal/initseed/` | `--init` seeding |
| `internal/session/` | resume detection (claude/opencode) |
| `internal/ollamacheck/` | availability check before launch |
| `internal/localgate/` | multi-model local-running gate (2026-09-14 design): probes every flagged model (except ollama, which trusts the flag) + shared Apply policy |
| `internal/localmodels/` | Local model inventory: `Inventory(cfg)` probes ollama (`/api/tags` + `/api/ps`, cloud `remote_host` entries excluded), omlx/mtplx (model-dir scan + `/v1/models`) and mlx_lm_server (running only) concurrently and returns registered + discovered entries with live `Running`; registry match keeps the registry id, else `config.DiscoveredModelID`. Never reads modelman's `running` flag. `localgate` delegates its `nameMatches`/`fetchModelIDs` here. |
| `internal/configeditor/` | Bubble Tea forms behind `wt config`'s interactive editor (agent add/edit/delete) |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell + pickers + launch/resume; also exports `PickModel`, a standalone single-purpose picker (not part of the app.go state machine) reusing `buildModelItems`, consumed by `wt smoke` |
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

Whether non-native models route through the LiteLLM proxy — `Config.IsLitellm()`, read-only from `~/.config/local-ai/modelman.toml`'s `[litellm]` table (`enabled`/`url`/`api_key`, loaded via `finalizeCfg`/`loadModelmanState`) — or dial providers directly (`Config.IsDirect()`) is decided by modelman (`modelman litellm status|on|off|set`). Toggling is routing policy only: wt never flips the switch and never starts, stops, or restarts the proxy. `LitellmBaseURL()` trims trailing slashes so drivers append `/v1` (or nothing for claude) cleanly; the proxy loads `~/.config/litellm/config.yaml` only at startup and modelman restarts it after expose changes via `MODELMAN_LITELLM_RESTART_CMD` (see `docs/wt-agents/README.md#litellm-proxy-lifecycle`).

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
`model_dir` (`Provider.ModelDir`), and `auth.base_url` (`Auth.BaseURL`) are
decoded (`config.ModelCost` in `internal/config/config.go`) and the cost is
rendered as per-token + subscription pricing columns in the model picker
(`internal/tui/model_list.go`).

**Read-side schemas are pinned by contract fixtures.** `docs/contracts/registry.sample.toml`
and `docs/contracts/modelman.sample.toml` are loaded by `internal/config`
contract tests and modelman's `tests/contracts/` — a schema change must
update both sides or both CI jobs fail.

**Exposure predicate (2026-09-15 local-model visibility design):** wt's `IsExposed` decides Stage-1 (tag/family/provider) catalog membership:
- Native models (provider `auth.type = "native"`): always exposed.
- Local models (location resolves to `"local"`): always exposed here too — catalog membership for local models is governed entirely by the live-verified running gate below (`internal/localgate.Apply`/`FilterToRunningLocal`), not by `exposed`/`ready`. A model whose location can't be resolved (registry data gap) falls back to the cloud/native check below, fail-closed.
- Cloud (and any model whose location doesn't resolve to local): `exposed` true (legacy `litellm_exposed` still read, ORed) AND (`ready = true` OR `location = "cloud"`), unchanged from before.

This means wt's picker can now show a local model modelman's own TUI still renders `–` for in its EXPOSED column — that divergence is intentional for local models; `modelman start` keeps `exposed` in sync automatically so LiteLLM-forced routes (see the Agents table below) keep working without a separate manual expose step. See `docs/superpowers/specs/2026-09-15-wt-local-model-visibility-design.md`.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt
> bug** — e.g. a `registry.toml` with models referencing `provider_id`s but
> `providers = []`. Fix is `modelman sync`/`modelman migrate` on that machine,
> not a code change here. (`modelman sync` now repairs a `providers = []`
> registry by creating default entries for reconcilable providers.)

**Lazy:** `newApp()` only loads config. wt never shells out for discovery;
`-W`/`--cwd` runs one `ollama list` via `ollamacheck.Available()`. `localmodels.Inventory` performs HTTP and filesystem discovery only (still no subprocess) and is not yet wired into a launch path (sub-project 3 consumes it).

> **Fixture gotcha.** `Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME`
> (and `RegistryPath()` also honors `MODELMAN_REGISTRY`) but write to
> *different* subdirs: `config.toml` → `$XDG_CONFIG_HOME/agent-wt/`,
> `registry.toml` → `$XDG_CONFIG_HOME/local-ai/`. Test/smoke fixtures must
> populate both. Also: `migrateConfigSchema` (runs on every `Load`)
> unconditionally ensures an `agy` agent — any registry fixture with agents
> needs a matching `agy` provider or `Load`/`Validate` fails with
> `unknown provider "agy"`.

## Local-model gate (multi-model design, 2026-09-14)

wt offers cloud models plus every LOCAL model that is both flagged
`running` in modelman-owned `modelman.toml` and confirmed by a live
probe right now — replacing issue #65's single-marker,
one-model-at-a-time gate. The gate policy lives in ONE place —
`internal/localgate.Apply` — shared by `cmd/wt/resolve.go`'s
`resolveModel` (non-TUI) and `internal/tui`'s `enterModelPhase` (TUI), so
the two launch paths cannot diverge. Apply reads modelman-owned
`modelman.toml`'s per-model `running` flags (`internal/config`'s
`Config.RunningLocalModelIDs()`/`LocalGateActive()`), verifies each one
with `internal/localgate.ResolveAll`'s probes — ollama is exempt from live
verification (the flag is trusted unconditionally since `modelman start` for
ollama is flag-only with no warmup, and an `ollama ps` check would read the
model as not-loaded on the very first probe after start, self-clearing the
flag); omlx/omlx-6bit via a name-checked `/v1/models` — 4-bit and 6-bit
variants share port 8000 and differ exactly in the variant tail; mlx_lm_server
via a non-empty `/v1/models`, exact names unreconstructable since one process
serves one target+draft pairing; mtplx via a name-checked `/v1/models` on port
8003 — then rejects a
`-M` pin naming a local model that isn't among the verified set, and
narrows the list with `Config.FilterToRunningLocal`, which fails closed
on unresolvable locations (a registry data gap drops the model rather
than keeping a possibly-local one). Callers map the outcome to their own
UX:

- No flags set → cloud models only.
- One or more verified flags → cloud models plus every verified-running
  local model.
- A flagged-but-unverified model (crashed, stopped outside modelman, a
  benchmark run tore it down) → silently excluded from the eligible
  list — never fatal, since one drifted model must not block a launch
  that doesn't need it.
- Pinned local model that isn't among the verified set → `*NotRunningError`
  is fatal for THAT launch (non-TUI fatal; TUI routes back to the agent
  picker with the message as status, clearing the bad pin) — the one
  surviving fatal case, since a pin is an explicit request that can't be
  silently substituted.
- Gate empties a non-empty eligible list (every eligible model was local,
  none verified running) → a gate-specific error ("all of agent X's
  eligible models are local and no local model is running — start one
  with `modelman start <id>`"), not the generic "no models match"
  wording; the TUI likewise routes back to the agent picker instead of
  showing a silent empty model list.

`LocalGateActive()` is true only for a `Config` built by `Load()`
(production); a hand-built `Config{}` literal — the shape nearly every
pre-issue-#65 test uses — defaults to false, making the gate a no-op
there unless a test opts in via `SetLocalRunningForTest`.

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
  (`buildModelItems` sets `marked` in `internal/tui/model_list.go`, value
  from `rotation.Last()`; `Title()` composes the prefix — plain ASCII
  because Unicode geometric shapes are East Asian Ambiguous width and
  misalign CJK terminals); the cursor still lands on the rotation's
  next-to-use model.
- The model picker's leftmost column shows a live "in use" session count
  (issue #73): `buildModelItems` queries `refcount.Store.Counts` over the
  same full-catalog IDs used for usage, in the same pass, and sets
  `modelItem.ref`; `Title()` renders it as a 2-rune prefix ("`3 `" or two
  blank spaces, clamped at 9) *before* the rotation marker. See
  `internal/refcount`.
- Usage history (1d/7d/30d per-model counts) lives at `~/.config/agent-wt/usage.jsonl` (JSONL, appended by `usage.Store.Record`; launch paths from `cmd/wt/launch.go` and `internal/tui` call `rotation.RecordFor(agent, id)`, which also records the agent-tagged usage event; consumed by the model picker — see `internal/usage`). The picker's TUI callers fetch the agent's **full** catalog **once** via `cfg.ModelsForAgent`, narrow it in place with `cfg.EligibleModelsIn` (the shared single-traversal filter; `EligibleModels` is a thin wrapper that passes a nil catalog), and hand both the eligible slice and the full catalog to `enterModelPhase`. It builds the `familyOf` map from that full catalog, then `buildModelItems` (`internal/tui/model_list.go`) runs ONE `Store.Counts` pass over those full-catalog IDs and aggregates per-family totals in memory via `usage.AggregateByFamily`. That keeps family 30-day counts accurate even when `-T`/`-F` filters narrow the eligible slice, and avoids a second full-catalog walk per picker entry. It then sorts eligible models descending by family-then-model `CompositeScore` (a recency-weighted integer key) and renders each model as one compact line — the family name and its 30-day count lead every row; no divider/header rows, family context inline. The empty (unnamed "other") family renders `-` in the family column but still shows its true 30-day aggregate, matching the sort key.

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
at. Read-only against modelman-owned state; never starts/stops local models
or flips LiteLLM routing. With no model-id on a TTY, the interactive picker
is `internal/tui.PickModel` — a standalone Bubble Tea program (not the main
app's worktree→agent→model state machine) that reuses `buildModelItems` for
the same decorated/sorted rows the agent flow's model picker renders, over
`smoke.Eligibility`'s unfiltered cross-agent union (never narrowed to one
agent's supported providers, since here the eligible agents are derived
*from* the chosen model rather than the reverse). Always-on, timestamped
progress lines go to stderr — a start line before each agent's row and a
result line after, with FAIL rows carrying the same command/exit-code/output
detail the final report shows (`logSmokeStart`/`logSmokeResult` in
`cmd/wt/smoke.go`, sharing `writeSmokeFailDetail` with the final report so
the two can't drift apart) — leaving stdout (the human table and `--json`
report) unaffected. See `docs/wt-smoke.md`.

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Session resume (Go)

`internal/session` finds the newest resumable session (claude `*.jsonl` under `~/.claude/projects/<slug>`, opencode `*.json` under `~/.local/share/opencode/storage/session/<project-id>`). Others (incl. shell) return nil.

On Enter in the TUI, a prior session offers Start fresh (default) / Cancel / Resume (`--resume <id>` claude, `--session <id>` opencode). Non-TUI does the same without prompting.

> **Native models never resume.** A native model (e.g. `claude/native`) launches with no model override; resuming would restore the session's stored model and silently override "native" (routing a proxy-routed model at the real Anthropic API). Both paths skip the session lookup for native models.

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → resume prompt → launch. Each picker is skipped only when its selection is already resolved: `prePath` (from `-W`/`--cwd`/outside-repo) skips the worktree picker, `-A` skips the agent+command picker, and `-M` skips the model picker (a pinned model is validated against the agent's eligible list once the agent is resolved). Every picker uses `ThemedListDelegate` (active color theme) — production code never uses `list.NewDefaultDelegate`.

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
