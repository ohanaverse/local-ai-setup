# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

The `wt` binary (`cmd/wt/`) is a Go tool that launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. To launch, `wt` needs three pieces of information:

1. **Where** — the directory: the current repo root (`--cwd`), a named worktree (`-W <name>` / `--worktree <name>`), or a picker screen when running from a git repo with no flags.
2. **What** — the agent or command: an agent that drives an LLM (`-A <name>` / `--agent <name>`: claude, codex, copilot, pi, agy, opencode), or a command that does not (`shell`, also via `-A`). The agent is never defaulted: when `-A` is omitted, the agent+command picker is always shown (agents alphabetically, then commands alphabetically).
3. **Which model** — for agents only: pin with `-M <provider>/<name>` / `--model <provider>/<name>`, narrow the eligible list with `-T <tags>` / `--tags <tags>` and `-F <family>` / `--family <family>`, or pick from the picker. Multiple eligible models rotate on successive launches.

The `bin/*-wt` files are thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`, `shell-wt` → `wt --agent shell`). All functionality — including shell command execution — is implemented in Go. See `docs/go-course/` for the lesson plan.

## Installation

Build the `wt` binary and put it on `$PATH`:

```bash
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
```

Requires Go 1.26.3 (see `go.mod`).

> **Makefile scope.** The Makefile handles bash script tasks (install, lint, format, smoke tests), a `build` target that compiles the Go binary to `bin/wt`, and a `clean` target that removes Go build artifacts (`bin/wt`, `*.test`). On macOS, `build`/`install` re-seal the ad-hoc code signature (`codesign --force --sign -`) — a stale/drifted linker signature is rejected by AMFI with "Taskgated Invalid Signature" and SIGKILLs the binary on launch (exit 137).

The `bin/*-wt` shims forward to `wt`, so `wt` must be on `$PATH` for them to work.

### Using the Makefile

```bash
make build        # Build the Go binary to bin/wt (re-seals macOS signature)
make install      # Install to ~/.local/bin/
make uninstall    # Remove from ~/.local/bin/
make help         # List all available targets
make lint         # shellcheck on bin/*-wt
make format       # shfmt -w on bin/*-wt
make format-check # shfmt -d (CI check)
make check        # Run lint + format-check
make test         # Run smoke tests (requires make install first)
make clean        # Remove build artifacts
```

## Architecture

The Go tool is the primary implementation; its packages are documented in [Go module](#go-module) below.

### Bash shims

`bin/*-wt` are one-line shims that `exec wt --agent <name> "$@"`. They exist for ergonomic convenience (`claude-wt` is shorter than `wt --agent claude`). All logic — directory selection, agent+command picker, model picker with `-T`/`-F` filters, slot rotation, guard, init seeding, session resume, and shell command execution — is in Go.

## Key flags (`wt`)

`wt` exposes short flags for the three launch inputs plus the supporting flags. Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; flags not supplied are gathered from picker screens or sensible defaults. The agent is the one input never defaulted: it always comes from `-A` or the agent/command picker.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use or create the worktree for branch `<name>`; skip the worktree picker. The agent/command picker still appears when `-A` is omitted |
| `-A <name>`, `--agent <name>` | Pin the agent (`claude`, `codex`, `copilot`, `pi`, `agy`, `opencode`) or command (`shell`) to launch. The agent is never defaulted: when `-A` is omitted, the agent/command picker is always shown |
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>`; errors if it isn't in the eligible list for the chosen agent |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR within flag) |
| `-F <family>`, `--family <family>` | Filter models by model family (comma-delimited, OR within flag) |
| `--cwd` | Launch in the current repo root; skip the worktree picker. The agent/command picker still appears when `-A` is omitted |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--version` | Print version and exit |
| `--check-guard` | Check if the `block-main-commit` guard is installed and exit |
| `--no-guard` | Remove the `block-main-commit` guard and exit |
| `--debug-worktrees` | List worktrees and branches (test helper) |
| `--debug-session <agent>` | Print the newest resumable session for an agent (test helper) |

The legacy short flag `-w` for `--worktree` has been removed — use `-W` or `--worktree`. The legacy bash flags `--code`, `--design`, and `--native` are not supported by `wt`; model rotation is now slot-based (see [Rotation (Go)](#rotation-go)), and the main guard is managed by `internal/guard`.

### Passthrough args

Extra args are forwarded to the launched agent. This restores the bash
engine's `WT_PASSTHROUGH_ARGS` behavior and enables `shell-wt <command>`.
`--` is only required when the command starts with a flag-like token (e.g.
`shell-wt -- rm --init`), since the root `wt` command is registered with
`cobra.ArbitraryArgs` so bare positional args reach the launcher instead of
being rejected as an unknown subcommand — but pflag still consumes anything
starting with `-` before it sees `--`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose (worktree picker skipped via -W)
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
wt -W feat -A codex -- --full-auto   # → codex --model X --full-auto
```

For regular agents, extra args are appended to the agent's command. For the
shell agent (which implements `ArgSetter`), the args become argv directly
(no shell involved) — `d.args[0]` is the binary, `d.args[1:]` its args.

## Adding a new agent

1. Add a driver in `internal/agents/` (e.g. `internal/agents/<name>.go`) that registers a `Driver` via `register("<name>", ...)` and implements `Build`/`YoloFlag`.
2. Add a shim in `bin/<name>-wt`:

   ```sh
   #!/usr/bin/env bash
   exec wt --agent <name> "$@"
   ```

3. Add a doc file to `docs/wt-agents/`.

## Docs

- `docs/configuration.md` — Claude Code and Codex CLI configuration (hooks, settings.json, environment filtering)
- `docs/wt-config.md` — `wt config` subcommands: themes, ollama model sync, and the interactive config viewer/editor
- `docs/wt-agents/` — per-agent reference docs (one file per launcher)
- `docs/go-course/` — 20-lesson course documenting the Go rewrite

## Shim quality gates

The `*-wt` shims are one-line bash wrappers with no automated unit test suite. Quality gates:

```bash
make lint         # shellcheck
make format       # shfmt -w -i 2 -ci
make format-check # shfmt -d (CI check)
make check        # lint + format-check
make test         # smoke test invocations (requires make install first)
```

## Go tests

The Go packages have unit and integration tests with a **what/why comment convention**: every `Test*` function has a top-level `//` block explaining what it tests and why that matters (the user-facing consequence of a regression). This convention is formalized in [Lesson 18](docs/go-course/lesson-18-testing.md); it makes the test suite self-documenting and helps reviewers understand the stakes of each assertion.

The Go test gate is separate from the Makefile's bash smoke tests:

```bash
go test ./...        # run all Go tests
go test ./internal/worktree -v  # verbose, one package
go vet ./...         # static analysis
```

Test coverage by package:

| Package | Tests | Focus |
|---|---|---|
| `internal/config` | 47 | Load, Validate, ValidateAll, Save, HasTag, ResolveLocation, migration (legacy + schema), secrets, EligibleModels, ParseFilterList |
| `internal/registry` | 15 | Merge (curated wins), parseOllamaList, OpenRouter JSON, FilterByTag/FilterBySource |
| `internal/rotation` | 18 | Slot{Agent,Tag,Family}, SlotFromFlags, LastLaunched/RecordLaunch/FirstAfter, snapshot-based model set, per-slot state file with backward-compat read of legacy single-tag state files |
| `internal/agents` | 41 | Per-agent Build output, Installed, Command, BuildLaunchCmd, ArgSetter, IsCommand (Commanded interface), shell driver, shell quoting, pi model sync |
| `internal/guard` | 11 | Status check, install idempotency, foreign-hook preservation, uninstall restore |
| `internal/worktree` | 34 | Worktree parsing, three-group enumeration (worktrees / locals / remotes), branch dedup (skip branches already checked out in worktrees), remote shadowing, worktree creation (EnsureForName/EnsureForBranch), default-branch refusal across all remotes (never a linked worktree) |
| `internal/initseed` | 7 | `--init` seeding: AGENTS.md + pointer files, idempotency, Root() in/outside repo |
| `internal/session` | 7 | Slug, relative time, newest-session-by-mtime, missing-dir handling, project-id, HOME-override integration |
| `internal/themes` | 24 | Registry: Builtins/Get/Token/AvailableList/AllTokens with fallback to Default; load/save/unset of `themes.toml` (missing file, empty file, valid theme, unknown theme, empty value, duplicate keys, malformed TOML, unknown keys ignored, case-insensitive, permission denied, atomic write, unknown name doesn't write, unset removes file, unset missing file no-op) |
| `internal/tui` | 144 | Worktree list: sentinel + locals + remotes ordering, separator, footer `n` shortcut, default-branch bare-row skip; phaseAgent: agent+command picker (agents-then-commands ordering), per-row issue indication (not configured / not installed) with block-on-select, `Enter` advances to phaseModel for launchable agents, launches immediately for commands; phaseModel: filter-aware via `-T`/`-F` + EligibleModels, picker key forwarding (up/down/j/k), picker cursor positioned on last-launched + 1, Enter records launch, `r` and `d` removed, model-status rendering; prePath worktree-picker skip; Launch (lesson 16): `launchAgent`, resume flag injection, `runAndWaitCmd` stdio wiring, `phaseResume` prompt, resume/start-fresh/cancel choices, launch returns ONLY `runAndWaitCmd` (NOT batched with `tea.Quit`, which would kill the agent via process exit); Ollama warn: unavailable model warning, cancel/proceed; phaseNewWorktree: prompts for a name; **theming**: every picker list (worktree, agent+command, model, resume, ollama) is built with `ThemedListDelegate` from `delegate.go` so the active color theme applies to every screen — production code never uses `list.NewDefaultDelegate` (tests do, since they assert model state not colors); Helpers: `DefaultAgent` (config helper only, not launch defaulting), agent-list ordering, model-status rendering, prePath skip, state persistence, placeholder View |
| `internal/ollamaconfig` | 33 | Union computation (synced/missing/untracked, sorting by family+name, non-ollama exclusion, empty edge cases); tags parsing/location toggle; save existing/new model, delete by ID; TUI phase transitions (Enter on synced→edit, missing→resolve, untracked→edit; Esc from edit/resolve→list; `r` refresh; quit keys) |
| `internal/configeditor` | 30 | Config viewer-and-editor TUI: tab navigation, sort orders, form validation, FK-blocked deletes, atomic save with validation gating, dirty tracking, quit-with-unsaved prompt |
| `internal/ollamacheck` | 3 | Ollama model availability check before launch (`Check`, `IsOllamaModel`, `Available`) |
| `cmd/wt` | 40 | Non-TUI launch: `-W`/`-A`/`-M`/`-T`/`-F` flag wiring, `resolveModel`, `launchFiltered` (rotation-by-launch + `pinnedSupplied` warn for command agents), `buildLaunch` resume-flag injection with extraArgs, `inGitRepoAt`, pi sync, ollama unavailable, prePath picker-skip for `-W`/`--cwd`/non-git-repo (agent picker still shown without `-A`), legacy `-w` short flag; `wt config` cobra subcommand: `config` (no args) launches viewer TUI, `config path`, `config theme {list,show,set,unset}` with case-insensitive theme lookup, accent-color list rendering, dark/light preview output, atomic theme writes, unset-no-op semantics; `config ollama` launches the sync TUI |

## Go module

The Go `wt` tool is the primary implementation; the `bin/*-wt` files are shims that forward to it. Module root is the repo root.

```bash
go build ./...       # build everything
go test ./...        # run all tests
go run ./cmd/wt      # run the wt CLI
go fmt ./...         # format
go vet ./...         # vet
```

### Structure

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra): thin wiring, exit-code handling, subcommand registration |
| `cmd/wt/app.go` | Shared dependency struct: loads and validates config once |
| `cmd/wt/commands.go` | Subcommand constructors: `rotate` (hidden), `models` (merged model list), `agents` (configured+registered agent list) |
| `cmd/wt/commands_config.go` | `wt config` subcommand family: `config path`, `config theme {list,show,set,unset}`, `config ollama` (launches the sync TUI) |
| `cmd/wt/resolve.go` | `resolveModel` — computes the single model for non-TUI launch (command-agent sentinel, pinned/eligible resolution, "multiple models match" error) |
| `cmd/wt/helpers.go` | Centralized helpers: `mustGetString`, `yolo`, `renderTable` |
| `cmd/wt/launch.go` | Non-TUI launch helpers: `buildFilteredCmd` (central dispatcher over `-T`/`-F`/`-M`), `buildLaunch`, `buildCommandForModel`, `buildCommandForCommand`, `launchFiltered`, `runAgentCmd`, `ollamaUnavailableError` — all accept `extraArgs` for passthrough |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration; shared helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `DefaultAgent`, `FirstTag`) |
| `internal/registry/` | Live model discovery (Ollama CLI, OpenRouter API) and registry merge |
| `internal/rotation/` | Slot-based model rotation (`Slot{Agent, Tag, Family}`) with snapshot-based model set, `NewForSlot`/`LastLaunched`/`RecordLaunch`/`FirstAfter` API, per-slot persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands; `BuildLaunchCmd` shared constructor; `ArgSetter` interface for shell; drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook, reports status (`Check`/`Install`/`Uninstall`) |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
| `internal/ollamaconfig/` | `wt config ollama` TUI: syncs config.toml ollama models with `ollama list` — union list (synced/missing/untracked), edit screen (family/tags/location), resolve prompt (pull/delete) |
| `internal/ollamacheck/` | Ollama model availability check before launch (`Check`, `IsOllamaModel`, `Available` — reuses `registry.Ollama{}.Discover()`) |
| `internal/themes/` | Active color theme: `Theme` struct + 4 built-in palettes (`default`/`solarized`/`mono`/`tokyo-night`), `themes.toml` load/save/unset, `lipgloss.AdaptiveColor` per token so the same theme works in light and dark terminals |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent+command picker (phaseAgent, ordered agents-then-commands; per-row issue indication for not-configured/not-installed agents with block-on-select; always shown when `-A` is omitted, `prePath` skips the worktree picker for `-W`/`--cwd`/outside-repo) + agent/model picker screen (phaseModel, list-based) + new-worktree prompt + launch/resume prompt + ollama warning: Model/Update/View, alternate-screen runner, `bubbles/list` picker, rotation by launch (no `r` key, no separate browser), agent launch, session resume prompt (Start fresh is the cursor default), shell agent skip-model-screen. Picker styling comes from `delegate.go`, which threads the active theme through every `list.Model` (worktree, agent, model, resume/ollama confirmations) via the exported `ThemedListDelegate` function (also used by `internal/ollamaconfig`). |
| `testdata/` | Sample configs for manual testing |
| `docs/go-course/` | 20-lesson course building the Go rewrite |
| `docs/superpowers/specs/` | Design specs |
| `docs/superpowers/plans/` | Implementation plans |

### Config (Go)

The Go tool uses `~/.config/agent-wt/config.toml` (TOML) with three entity types:

- **Provider** — model source with auth config (ollama, agy, claude, codex, copilot). Native providers match their agent name (e.g. `claude` provider for the `claude` agent). `opencode` and `pi` have no native provider — they are ollama-only.
- **Model** — specific variant from a provider, grouped by family (e.g. gemma4). Models carry a `Source` field (`curated` | `discovered`) to distinguish config-file entries from live discovery results.
- **Agent** — AI coding tool with supported providers (at least one required) and optional default

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

Shared helpers in `internal/config`:

- `Dir()` — base config directory (`~/.config/agent-wt`, or `$XDG_CONFIG_HOME/agent-wt`)
- `Path()` — `Dir()/config.toml`
- `WriteFileAtomic(path, data, perm)` — atomic temp-file + rename write (used by `Save`, rotation state, and pi model sync)
- `OllamaBaseURL` — the `http://localhost:11434` gateway constant (used by the claude/copilot/opencode drivers and migration)
- `(*Config).DefaultAgent()` — first configured agent, else `"claude"`. Retained as a config helper; the launcher no longer calls it to auto-select an agent (the agent/command picker is always shown when `-A` is omitted)
- `FirstTag(s, fallback)` — first comma-delimited tag from `s`, else `fallback`; the shared form of the rotation slot's tag component (used by both the non-TUI launch path and the TUI picker)

### Live discovery (Go)

The `internal/registry` package queries connected providers at runtime and merges the results with the curated config:

- **Ollama** — runs `ollama list`, parses both local models (with a size) and cloud models (size `-`).
- **OpenRouter** — fetches `https://openrouter.ai/api/v1/models` via HTTP.

Curated entries win on ID collisions; discovered entries fill gaps.

Discovery is lazy: `newApp()` loads and validates config only. The full
`registry.Discover` (including the OpenRouter HTTP call) runs on demand.
The TUI picker sources its model list exclusively from `config.toml`
(no live discovery) so flag-only and TUI paths never hit the OpenRouter
API. `--version` and `--init` don't shell out to ollama at all; `-W`
and `--cwd` run a single `ollama list` via `ollamacheck.Available()` for
the pre-launch availability check, but skip the full registry discovery.

### Config (themes)

`wt config` is the user-preference surface (separate from `config.toml`,
which holds the agent/model registry). Subcommands include `wt config theme`
(four built-in palettes with dark/light variants, stored in
`~/.config/agent-wt/themes.toml`), `wt config ollama` (interactive TUI to
sync config.toml ollama models with `ollama list`), and `wt config` with no
arguments (interactive viewer/editor for agents, providers, and models in
`config.toml`). Themes style the TUI picker, CLI tables, and
`wt config theme list` output. See `docs/wt-config.md`.

```bash
wt config                    # interactive viewer/editor (requires a TTY)
wt config theme              # show the active theme + available names
wt config theme list         # list all built-in themes
wt config theme show <name>  # show a theme's tokens with dark/light hex previews
wt config theme set <name>   # activate a theme (effective on next wt launch)
wt config theme unset        # revert to the default theme
wt config path               # print the config directory
wt config ollama             # interactive TUI to sync ollama models with config.toml
```

> **Invalid-config repair.** `wt config` launches even when `config.toml` fails
> validation, so the interactive editor can repair a broken config. Other
> launch paths still exit early on config errors.

### Rotation (Go)

The `internal/rotation` package implements slot-based model rotation — the Go
equivalent of the bash `--code`/`--design` rotation, generalized so any
`(agent, tag, family)` triple can be a rotation group. Rotation is driven by
launches: each successful launch records the model id to the per-slot state
file, and the next picker entry lands the cursor on the model *after* it.

- `rotation.Slot{Agent, Tag, Family}` is the rotation key. Construct with
  `rotation.SlotFromFlags(agent, tag, family)` (the tag component is the
  first tag from `-T`, falling back to `cfg.DefaultTag`; `family` from `-F`).
- `rotation.NewForSlot(slot, models, stateDir)` builds a `Rotation` from a
  snapshot of the *eligible* models (the agent-filtered, tag-filtered,
  family-filtered list — not the full `cfg.ModelsWithTag`)
- `LastLaunched()` reads the last-launched model from the state file, or
  returns `(zero, false)` if the file is missing or references a model no
  longer in the snapshot
- `RecordLaunch(m)` writes the model ID to the state file (best-effort;
  launch proceeds even if the write fails)
- `FirstAfter(models, target)` returns the model after `target` in the
  snapshot, wrapping to the first item — the "what to show on next picker
  entry" calculation
- State file: `~/.config/agent-wt/rotation-<agent>-<tag>-<family>.state` —
  one line: `<model_id>\n`, written atomically (temp + rename). Legacy
  single-tag files (`rotation-<tag>.state`) are still read for backward
  compatibility via a placeholder agent/family
- The hidden `wt rotate <tag>` debug subcommand prints the model after the
  last-launched one and exits

```bash
go run ./cmd/wt rotate code    # prints the model after the last launch in the code slot (any agent)
go run ./cmd/wt rotate design  # independent rotation for design
```

### Agents (Go)

The `internal/agents` package abstracts how each coding agent is launched.
Each agent registers a `Driver` that builds a `LaunchCmd` (binary, args, and
extra env vars) for a given model, so the TUI/CLI can say "launch agent X
with model Y" without knowing the agent's quirks.

- `Driver` interface: `Build(m config.Model, yolo bool) LaunchCmd` and `YoloFlag() string`
- `LaunchCmd` — plain struct: `Bin`, `Args`, `Env` (extra env merged over `os.Environ()`)
- Registry keyed by agent name; `ByName(name)` returns a driver or nil
- `Command(d, m, yolo, workdir)` resolves the binary via `LookPath` and returns a ready `exec.Cmd`
- `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` — shared launch constructor used by both TUI and non-TUI paths; calls Syncer/ArgSetter, builds the command, appends passthrough args and resume flags
- `Syncer` interface — optional pre-launch step (pi syncing its model catalog)
- `ArgSetter` interface — optional; drivers that need passthrough args to construct their command (shell)
- `Installed(bin)` reports whether a binary is on PATH
- Native models (e.g. `claude/native`) are detected via `Model.IsNative()` and launch with no model args/env

**Model id contract.** Drivers hand the **bare provider-specific name** (`m.ModelName`) to the agent CLI on the model-id slot — not the registry key (`m.ID`, which carries the `provider/` prefix and is for registry lookups only). Most agent CLIs (claude/codex/copilot/pi) accept the bare name as-is. OpenCode is the exception: its CLI uniquely requires `provider/model`, so `opencodeDriver.Build` constructs `"ollama/" + m.ModelName` deliberately. Adding the prefix from `m.ID` would produce a double prefix (`ollama/ollama/<model>`); forgetting to add it (or passing `m.ID` directly) would forward `ollama/<model>` to the Ollama gateway, which would not recognize the prefixed id. Each driver has a regression test in `internal/agents/agents_test.go` (`TestClaudeOllamaPrefix`, `TestCodexOllamaPrefix`, `TestCopilotOllamaPrefix`, `TestOpenCodeOllamaPrefix`) using `ollamaPrefixedModel()` — a model with distinct `ID`/`ModelName` so the wrong-field bug cannot reappear silently.

Per-agent behavior:

- **claude** — cloud/local models set `ANTHROPIC_*` env vars pointing at the ollama gateway plus `--model <m.ModelName>` (bare provider name); native uses no args
- **codex** — `--model <m.ModelName>` (bare provider name); native uses no args
- **copilot** — sets `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL=<m.ModelName>` env vars; never passes `--model`
- **opencode** — ollama-only (no native provider); sets `OPENCODE_CONFIG_CONTENT` (inline JSON) with `model: "ollama/<m.ModelName>"` (constructs the `provider/` prefix deliberately); never passes `--model`
- **pi** — syncs non-native models into `~/.pi/agent/models.json` (idempotent, `_launch: true`) and passes `--model <ModelName>` only when the model is present and marked `_launch: true`; falls back to pi's default model with a warning otherwise; no `jq` dependency (native Go JSON); no yolo flag
- **agy** — no model passthrough (model chosen inside its TUI)
- **shell** — execs the user's passthrough args directly as argv (no shell involved), or interactive `bash` when no command is given; no model, no yolo, no session resume; implements `ArgSetter`

The `wt config` subcommand (no args) launches an interactive TUI for
viewing and editing agents, providers, and models in `config.toml`.

### Guard (Go)

The `internal/guard` package manages the `block-main-commit` pre-commit hook
that blocks commits to `main`/`master`. The hook is embedded into the binary
with `//go:embed`.

- `Check()` — reports `Installed`, `NotInstalled`, or `Err` by reading the
  common git dir's `hooks/pre-commit` and looking for the marker string.
- `Install()` — idempotent; appends the guard to an existing hook rather
  than overwriting it. Returns `changed` so callers can skip noisy output.
- `Uninstall()` — restores any preserved original hook; removes the file only
  when we created it and there was nothing before. Leaves foreign hooks alone.

The common git dir (via `git rev-parse --git-common-dir`) is used so the
hook applies to all worktrees of the repo.

### Init seeding (Go)

The `internal/initseed` package handles `--init` seeding.
It is called by `wt --init` and, in later lessons, by the
TUI when launching a specific agent.

- `Root()` — resolves the current repo root via `worktree.RepoRoot()`.
- `Seed(agent, repoRoot)` — writes `AGENTS.md` from a template if missing,
  then writes an agent-specific pointer file if the agent supports one:
  - **claude** → `CLAUDE.md` with `@AGENTS.md`
  - **copilot** → `.github/copilot-instructions.md` with a pointer sentence
  - other agents → no pointer file
- Skip-if-exists: existing files are never overwritten; they are reported in
  `Result.Skipped` instead of `Result.Created`.

The `--init` flag is handled in `cmd/wt/main.go` *before* any agent-binary
requirement, so it works even when no agent is installed. After seeding, the
main guard is auto-installed, just like a normal launcher invocation.

```bash
# Seed AGENTS.md in the current repo (no agent binary required)
wt --init
```

### Session resume (Go)

The `internal/session` package detects the newest resumable session for an
agent in a worktree. It supports claude and opencode; all other agents
(including shell) return nil.

- `Slug(path)` — replaces every char outside `[a-zA-Z0-9-]` with `-`. The
  leading `/` becomes a leading `-`, matching the real `~/.claude/projects/-Users-...` dirs.
- `LatestClaude(path)` — newest `*.jsonl` under `~/.claude/projects/<slug>`.
- `OpenCodeProjectID(path)` — runs `git -C <path> rev-list --max-parents=0 HEAD`
  to get the repo's root commit hash (opencode's project id).
- `LatestOpenCode(path)` — newest `*.json` (not `.jsonl`) under
  `~/.local/share/opencode/storage/session/<project-id>`.
- `LatestForAgent(agent, path)` — dispatches to claude/opencode, else nil.
- `RelativeTime(t)` — "just now", "5m ago", "3h ago", "2d ago", "1w ago".

Sessions are ranked by mtime (newest wins). A missing session dir yields `nil`,
not an error.

In the TUI (lesson 16), pressing Enter on the agent+model screen checks
`session.LatestForAgent` for claude/opencode. If a session exists, a
`phaseResume` prompt offers three choices:
- **Start fresh** — launch without resume args. **Default cursor position;
  Enter launches a fresh session unless Resume is highlighted.**
- **Cancel** — return to the agent+model screen.
- **Resume** — append `--resume <id>` (claude) or `--session <id>` (opencode)
  and launch.

If no session exists, the agent launches immediately. The non-TUI launch path
(lesson 17) performs the same resume check in `BuildLaunchCmd`, appending
`--resume`/`--session` without prompting.

**Native models never resume.** A native model (e.g. `claude/native`) launches
with no model override, so resuming a session would restore the session's
stored model and silently override the user's "native" choice (for claude,
routing a gateway model at the real Anthropic API). Both the TUI
(`proceedToLaunch`) and non-TUI (`buildCommandForModel`) paths skip the
session lookup for native models, and `BuildLaunchCmd` guards the resume flag
with `!m.IsNative()` as defense-in-depth.

```bash
# Test helper: print the newest resumable session for an agent
wt --debug-session claude
wt --debug-session opencode
```

### TUI shell (Go)

The `internal/tui` package holds the Bubble Tea app shell. `Run(yolo, agent, extraArgs)`
starts `tea.NewProgram` with `tea.WithAltScreen()` and returns when the program
exits. The `model` type implements `tea.Model` (`Init` / `Update` / `View`);
lessons 12+ layer on the worktree list, agent+model picker screen, resume prompt,
and launch command.

- `Run(yolo bool, agent, tags, family string, extraArgs []string, theme themes.Theme, prePath string)` — entry point; reached from `rootCmd.RunE` as the fallback
  after every flag handler has had a chance to early-return. `agent` is the `--agent` flag value
  ("" = no agent pinned; the agent/command picker is shown). `extraArgs` are the user's
  passthrough args after `--`. `prePath`, when non-empty (`-W`/`--cwd`/outside-repo), skips
  the worktree picker and starts at the agent/command picker (or model phase when pinned).
  Both are stored in the model and passed through to `agents.BuildLaunchCmd` at launch time.
- `tea.WithAltScreen()` — Bubble Tea manages the alternate screen buffer
  for full-screen TUI rendering.
- `currentProgram` — a package-level pointer to the running `tea.Program`,
  set by `Run()` so `runAndWaitCmd` can call `ReleaseTerminal()` before the
  agent takes over and `RestoreTerminal()` when it exits.
- `launchAgent()` — thin wrapper around `agents.BuildLaunchCmd`; builds the `exec.Cmd`
  with passthrough args and resume flags.
- `proceedToLaunch()` — shared session-check → launch-or-resume-prompt flow used by
  both the `phaseModel` enter handler and the ollama warning proceed choice.
- `runAndWaitCmd()` — a `tea.Cmd` that releases the terminal, wires stdio,
  runs the agent, restores the terminal, and returns a `launchDoneMsg`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`. Running from a pipe,
> CI runner, or editor output panel fails with
> `could not open a new TTY: open /dev/tty: device not configured`. Always
> run `wt` from a real terminal session. The non-TUI flag paths (`--version`,
> `wt rotate`, etc.) skip `tui.Run()` and don't need a TTY for `wt` itself
> (though the launched agent is interactive). `-W`/`--cwd` need a TTY only
> when `-A` is omitted (they route through the agent/command picker); with
> `-A` they launch directly and skip the TUI.

### Worktree picker screen

`internal/tui/worktree_list.go` adapts `worktree.Entry` to `list.Item` via
`entryItem` (`Title()`/`Description()`/`FilterValue()`). `buildList` builds
the `list.Model`; `loadEntriesCmd` async-loads via `worktree.RepoRoot()` +
`Enumerate(root, root)` (which returns three `EntryGroup` slices:
worktrees / local branches / remote-only branches); `entriesLoadedMsg`
carries the groups plus the default branch and repo root into `Update`,
and `selectedEntryMsg` carries the Enter-picked entry. The list is
prepended with a "+ New worktree…" sentinel row (`kindNewWorktree`) that
opens the new-worktree prompt on Enter, alongside the `n` keybinding.
`buildList` also advertises `n` in the footer help line (and full help
view) via the list's `AdditionalShortHelpKeys`/`AdditionalFullHelpKeys`,
so the shortcut is discoverable without scrolling to the sentinel row.
The picker is skipped entirely when `-W`, `--cwd`, or a non-git-repo
condition holds; the picker also always shows the default branch plus
local worktrees and remotes, even when launched from inside a worktree.
On entry, the cursor lands on the worktree for the repo default branch
(so Enter launches on `main` without an extra keystroke); bare default
branches are filtered out of the picker entirely. Enter on a default-branch
row launches straight through — there is no confirmation prompt before
launching on the protected branch.
Full walkthrough:
[docs/go-course/lesson-13-worktree-screen.md](docs/go-course/lesson-13-worktree-screen.md).

### Agent+model screen

`selectedEntryMsg` transitions to `phaseAgent`, which lists configured
agents plus the registered command agents (currently `shell`), ordered
deterministically: agents alphabetically, then commands alphabetically.
`Enter` on an agent advances to `phaseModel` (with the agent
pre-selected); `Enter` on a command agent launches immediately (no model
screen, no rotation, no session resume).

Each agent row carries a launchability issue when it cannot launch:
"not configured — add it to config.toml" (registered but missing from
`cfg.Agents`) or "not installed — install the binary" (configured but no
binary on PATH). `Enter` on such a row is blocked with a clear
`cannot launch <name>: <issue>` status instead of advancing to a model
screen that can never succeed. Commands (e.g. `shell`) are always
launchable. The same check guards the pinned `-A` path
(`proceedFromSelectedPath`).

There is **no default agent**: the agent/command picker is always shown
when `-A` is omitted, across every launch path. When `-A` is supplied,
the picker is skipped and `phaseModel` opens directly (or launches
immediately for a command agent). Launch paths that already know the
worktree (`-W`, `--cwd`, outside a repo) skip the worktree picker by
seeding `model.prePath`; `Init()` emits a `selectedEntryMsg` for it, so
the agent/command picker (unpinned) or model phase (pinned) opens
straight away. A `phase` enum
(`phaseList` / `phaseAgent` / `phaseModel` / `phaseResume` /
`phaseOllamaWarn` / `phaseNewWorktree`) tracks the active screen.

The phaseModel View is itself a `bubble/list` picker over the
agent+tag+family filtered models (sourced from `config.toml` only — no
live discovery). `Update()` forwards `tea.KeyMsg` to the list
(mirroring the worktree-list forwarding) so up/down/enter work natively.
**`n`** opens the new-worktree prompt (also reachable via the
"+ New worktree…" sentinel row in the worktree picker); **`enter`**
launches the highlighted model, recording it as the new "last-launched"
so the next picker entry advances automatically. If
`session.LatestForAgent` finds a prior claude/opencode session, Enter
switches to `phaseResume` instead. The `r` and `d` keys are gone
(rotation-by-launch replaces `r`; `-T`/`--tags` replaces the in-picker
tag toggle). Full walkthrough:
[docs/go-course/lesson-14-agent-model-screen.md](docs/go-course/lesson-14-agent-model-screen.md).

### Model list helpers

`internal/tui/model_list.go` (`modelItem`, `buildModelList`, `indexOfModel`,
`FindAfter`, `phaseModelView`) builds the picker list. `modelItem` adapts
`config.Model` to `list.Item`; `buildModelList` constructs the `bubble/list`
from a snapshot of eligible models (agent + `-T` tags + `-F` family
filtered via `cfg.EligibleModels`); `indexOfModel` and `FindAfter` (a thin
wrapper over `rotation.FirstAfter`) compute cursor positions;
`phaseModelView` renders the picker screen with the agent/tag header and
keybind footer, and prepends any `m.status` error (launch/config/session/
ollama failures are shown on the model screen rather than silently
swallowed). Full walkthrough:
[docs/go-course/lesson-15-model-browser.md](docs/go-course/lesson-15-model-browser.md)
(lesson 15's separate browser screen has been folded into phaseModel; the
walkthrough is kept as historical record per the project convention).

### Worktree (Go)

The `internal/worktree` package handles both enumeration (lesson 7) and
creation (lesson 8). Every
function takes `dir` (the repo root) as its first parameter so tests can run
git inside a temp repo.

- `RepoRoot()` — returns the current repo root via `git rev-parse --show-toplevel`;
  used by the TUI, `initseed.Root()`, and the `cmd/wt` debug handlers to
  discover the repo root without an explicit path
- `Enumerate(dir, cwdRoot)` — returns three `EntryGroup` slices in order:
  worktrees (including the current worktree), local branches that aren't
  checked out anywhere, and remote-only branches. The TUI picker consumes
  the groups directly and inserts a separator between locals and remotes
- `EnsureForName(dir, name)` — idempotent worktree for the `-W`/`--worktree`
  flag: reuses an already-checked-out worktree path, reuses an existing
  `.worktrees/<name>` path, otherwise creates via `git worktree add` (or
  `-b` for a new branch). Refuses to create a linked worktree for the default
  branch (it must only ever be the primary checkout)
- `EnsureForBranch(dir, branch)` — the picker path, handling local,
  remote-tracking, and brand-new branches. Remote branches create a local
  branch tracking them; errors if the short name collides with a local branch.
  Refuses the default branch and any `<remote>/<default>` form (origin,
  upstream, … — matched by short name across all remotes, gated on the ref
  really existing under `refs/remotes/` so a local `feature/main` is not
  falsely refused); see `IsDefaultBranchForm` / `isDefaultBranchSelection`
- Helpers: `refExists`, `branchAtPath`, `isWorktreePath`, `IsDefaultBranchForm`

The default branch (main/master) must never be checked out in a linked
worktree (`.worktrees/*`); it may only ever be the primary checkout. Both
creation functions refuse it, and the TUI picker skips bare default-branch
rows so it is never offered as a create-target.

Branch names with slashes use the last worktree path component
(`.worktrees/my-branch`). The `-W`/`--worktree` flag is wired in
`cmd/wt/main.go`: it creates/reuses the worktree and then, with `-A`,
launches the agent there directly (no TUI); without `-A` it passes the
resolved worktree to the TUI via `prePath` so the agent/command picker
(and model picker) still appear.

```bash
go run ./cmd/wt -W my-feature -A claude   # create/reuse worktree, launch claude there
```

### Migration (Go)

On first run, `wt` migrates the legacy bash `~/.config/agent-wt/models.conf`
into `config.toml` automatically. The migration:

- Parses multi-line `CODE_MODELS`/`DESIGN_MODELS` bash arrays
- Strips `#`-commented-out models
- Creates `Provider`/`Agent` entries for each `native:X` model (skipping `noNativeAgents`: `pi`, `opencode` — ollama-only agents with no native provider)
- Seeds the `agy` provider/model/agent (provider name matches agent name, not `google`)
- Merges models that appear in both code and design rotations (union of tags)
- Runs only once — skipped if `config.toml` already exists

**Schema migration** (`migrateConfigSchema`): On every `Load()`, idempotent
fixups rewrite existing configs to the current schema:

- Renames legacy `google` provider/model/agent references to `agy`
- Ensures `agy` provider, `agy/native` model, and `agy` agent exist
- Removes `opencode` native provider/model; rewires `opencode` agent to ollama-only
- Saves and logs to stderr if any fixup fired; no-op on already-current configs

Validate changes by running tests and the launcher manually. Representative invocations:

```bash
# Go tests — run before any commit
go test ./...          # all packages
go vet ./...           # static analysis
go build ./...         # verify compilation

# CLI smoke tests
# Interactive TUI (needs a TTY)
wt

# Skip picker, use/create a named worktree and launch
wt -W my-feature -A claude

# Launch in current repo root (no worktree switch)
wt --cwd -A codex

# Legacy shim forwards to wt
claude-wt --cwd   # → wt --agent claude --cwd

# Seed agent instruction files in a new repo (no agent binary required)
wt --init

# Live registry discovery
wt models    # merged list of curated + discovered models
wt agents    # merged list of configured + registered agents
```
