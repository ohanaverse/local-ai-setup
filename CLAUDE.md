# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

The `wt` binary (`cmd/wt/`) is a Go tool that launches an AI coding agent CLI (claude, codex, copilot, pi, agy, opencode) or a shell command in a chosen worktree, branch, and model. To launch, `wt` needs three pieces of information:

1. **Where** — the directory: the current repo root (`--cwd`), a named worktree (`-W <name>` / `--worktree <name>`), or a picker screen when running from a git repo with no flags.
2. **What** — the agent or command: an agent that drives an LLM (`-A <name>` / `--agent <name>`: claude, codex, copilot, pi, agy, opencode), or a command that does not (`shell`, also via `-A`). When `-A` is omitted, an agent+command picker is shown.
3. **Which model** — for agents only: pin with `-M <provider>/<name>` / `--model <provider>/<name>`, narrow the eligible list with `-T <tags>` / `--tags <tags>` and `-F <family>` / `--family <family>`, or pick from the picker. Multiple eligible models rotate on successive launches.

The `bin/*-wt` files are thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`, `shell-wt` → `wt --agent shell`). All functionality — including shell command execution — is implemented in Go. See `docs/go-course/` for the lesson plan.

## Installation

Build the `wt` binary and put it on `$PATH`:

```bash
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
```

Requires Go 1.26.3 (see `go.mod`).

> **Makefile scope.** The Makefile handles bash script tasks (install, lint, format, smoke tests) and a `clean` target that removes Go build artifacts (`bin/wt`, `*.test`). It does **not** build the Go binary — use `go build` above for that.

The `bin/*-wt` shims forward to `wt`, so `wt` must be on `$PATH` for them to work.

### Using the Makefile

```bash
make install      # Install to ~/.local/bin/
make uninstall    # Remove from ~/.local/bin/
make help         # List all available targets
make check        # Run lint + format-check
make test         # Run smoke tests (requires make install first)
make clean        # Remove build artifacts
```

## Architecture

The Go tool is the primary implementation; its packages are documented in [Go module](#go-module) below.

### Bash shims

`bin/*-wt` are one-line shims that `exec wt --agent <name> "$@"`. They exist for ergonomic convenience (`claude-wt` is shorter than `wt --agent claude`). All logic — directory selection, agent+command picker, model picker with `-T`/`-F` filters, slot rotation, guard, init seeding, session resume, and shell command execution — is in Go.

## Key flags (`wt`)

`wt` exposes short flags for the three launch inputs plus the supporting flags. Any combination of `-W`, `-A`, `-M`, `-T`, `-F` is valid; flags not supplied are gathered from picker screens or sensible defaults.

| Flag | Effect |
|---|---|
| `-W <name>`, `--worktree <name>` | Use or create the worktree for branch `<name>`; skip the worktree picker |
| `-A <name>`, `--agent <name>` | Pin the agent (`claude`, `codex`, `copilot`, `pi`, `agy`, `opencode`) or command (`shell`) to launch; defaults to the first configured agent |
| `-M <id>`, `--model <id>` | Pin the model as `<provider>/<name>`; errors if it isn't in the eligible list for the chosen agent |
| `-T <tags>`, `--tags <tags>` | Filter models by tag (comma-delimited, OR within flag) |
| `-F <family>`, `--family <family>` | Filter models by model family (comma-delimited, OR within flag) |
| `--cwd` | Launch in the current repo root; skip the worktree picker |
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
- `docs/wt-agents/` — per-agent reference docs (one file per launcher)

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
| `internal/config` | 44 | Load, Validate, ValidateAll, Save, HasTag, ResolveLocation, migration, secrets, EligibleModels, ParseFilterList |
| `internal/registry` | 15 | Merge (curated wins), parseOllamaList, OpenRouter JSON, FilterByTag/FilterBySource |
| `internal/rotation` | 18 | Slot{Agent,Tag,Family}, SlotFromFlags, LastLaunched/RecordLaunch/FirstAfter, snapshot-based model set, per-slot state file with backward-compat read of legacy single-tag state files |
| `internal/agents` | 32 | Per-agent Build output, Installed, Command, BuildLaunchCmd, ArgSetter, IsCommand (Commanded interface), shell driver, shell quoting, pi model sync |
| `internal/guard` | 11 | Status check, install idempotency, foreign-hook preservation, uninstall restore |
| `internal/worktree` | 29 | Worktree parsing, three-group enumeration (worktrees / locals / remotes), branch dedup, default-branch skip, remote shadowing, worktree creation (EnsureForName/EnsureForBranch) |
| `internal/initseed` | 7 | `--init` seeding: AGENTS.md + pointer files, idempotency, Root() in/outside repo |
| `internal/session` | 7 | Slug, relative time, newest-session-by-mtime, missing-dir handling, project-id, HOME-override integration |
| `internal/tui` | 124 | Worktree list: sentinel + locals + remotes ordering, separator, footer `n` shortcut; phaseAgent: agent+command picker, `Enter` advances to phaseModel for agents, launches immediately for commands; phaseModel: filter-aware via `-T`/`-F` + EligibleModels, picker key forwarding (up/down/j/k), picker cursor positioned on last-launched + 1, Enter records launch, `r` and `d` removed; Launch (lesson 16): `launchAgent`, resume flag injection, `runAndWaitCmd` stdio wiring, `phaseResume` prompt, resume/start-fresh/cancel choices, launch returns ONLY `runAndWaitCmd` (NOT batched with `tea.Quit`, which would kill the agent via process exit); Ollama warn: unavailable model warning, cancel/proceed; phaseNewWorktree: prompts for a name; Helpers: `DefaultAgent` defaults, state persistence, placeholder View |
| `cmd/wt` | 29 | Non-TUI launch: `-W`/`-A`/`-M`/`-T`/`-F` flag wiring, `resolveModel`, `launchFiltered` (rotation-by-launch + `pinnedSupplied` warn for command agents), `buildLaunch` resume-flag injection with extraArgs, `inGitRepoAt`, pi sync, ollama unavailable, picker-skip conditions for `-W`/`--cwd`/non-git-repo, legacy `-w` short flag |

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
| `cmd/wt/app.go` | Shared dependency struct: loads and validates config once (live model discovery is deferred to the `models` subcommand) |
| `cmd/wt/commands.go` | Subcommand constructors: `models`, `agents`, `rotate` (hidden) |
| `cmd/wt/helpers.go` | Centralized helpers: `mustGetString`, `yolo`, `defaultModel`, `renderTable` |
| `cmd/wt/launch.go` | Non-TUI launch helpers: `buildFilteredCmd` (central dispatcher over `-T`/`-F`/`-M`), `buildLaunch`, `buildCommandForModel`, `buildCommandForCommand`, `launchFiltered`, `runAgentCmd`, `ollamaUnavailableError`, `firstOrDefault` — all accept `extraArgs` for passthrough |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration; shared helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `DefaultAgent`) |
| `internal/registry/` | Live model discovery (Ollama CLI, OpenRouter API) and registry merge |
| `internal/rotation/` | Slot-based model rotation (`Slot{Agent, Tag, Family}`) with snapshot-based model set, `NewForSlot`/`LastLaunched`/`RecordLaunch`/`FirstAfter` API, per-slot persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands; `BuildLaunchCmd` shared constructor; `ArgSetter` interface for shell; drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook, reports status (`Check`/`Install`/`Uninstall`) |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
| `internal/ollamacheck/` | Ollama model availability check before launch (`IsOllamaModel`, `Available` — reuses `registry.Ollama{}.Discover()`) |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent+command picker (phaseAgent) + agent/model picker screen (phaseModel, list-based) + new-worktree prompt + launch/resume prompt + ollama warning + guard warning: Model/Update/View, alternate-screen runner, `bubbles/list` picker, rotation by launch (no `r` key, no separate browser), agent launch, session resume prompt, shell agent skip-model-screen |
| `testdata/` | Sample configs for manual testing |
| `docs/go-course/` | 20-lesson course building the Go rewrite |
| `docs/superpowers/specs/` | Design specs |
| `docs/superpowers/plans/` | Implementation plans |

### Config (Go)

The Go tool uses `~/.config/agent-wt/config.toml` (TOML) with three entity types:

- **Provider** — model source with auth config (ollama, openrouter, claude, copilot)
- **Model** — specific variant from a provider, grouped by family (e.g. gemma4). Models carry a `Source` field (`curated` | `discovered`) to distinguish config-file entries from live discovery results.
- **Agent** — AI coding tool with supported providers and optional default

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

Shared helpers in `internal/config`:

- `Dir()` — base config directory (`~/.config/agent-wt`, or `$XDG_CONFIG_HOME/agent-wt`)
- `Path()` — `Dir()/config.toml`
- `WriteFileAtomic(path, data, perm)` — atomic temp-file + rename write (used by `Save`, rotation state, and pi model sync)
- `OllamaBaseURL` — the `http://localhost:11434` gateway constant (used by the claude/copilot/opencode drivers and migration)
- `(*Config).DefaultAgent()` — first configured agent, else `"claude"`

### Live discovery (Go)

The `internal/registry` package queries connected providers at runtime and merges the results with the curated config:

- **Ollama** — runs `ollama list`, parses both local models (with a size) and cloud models (size `-`).
- **OpenRouter** — fetches `https://openrouter.ai/api/v1/models` via HTTP.

Curated entries win on ID collisions; discovered entries fill gaps. The `wt models` subcommand prints the merged registry.

Discovery is lazy: `newApp()` loads and validates config only. The full
`registry.Discover` (including the OpenRouter HTTP call) runs on demand —
in the `wt models` subcommand. The TUI picker sources its model list
exclusively from `config.toml` (no live discovery) so flag-only and TUI
paths never hit the OpenRouter API. `--version` and `--init` don't shell
out to ollama at all; `-W` and `--cwd` run a single `ollama list` via
`ollamacheck.Available()` for the pre-launch availability check, but skip
the full registry discovery.

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

Per-agent behavior:

- **claude** — cloud/local models set `ANTHROPIC_*` env vars pointing at the ollama gateway plus `--model`; native uses no args
- **codex** — `--model <id>`; native uses no args
- **copilot** — sets `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL` env vars; never passes `--model`
- **opencode** — sets `OPENCODE_CONFIG_CONTENT` (inline JSON) for the ollama provider
- **pi** — syncs non-native models into `~/.pi/agent/models.json` (idempotent, `_launch: true`) and passes `--model <ModelName>` only when the model is present and marked `_launch: true`; falls back to pi's default model with a warning otherwise; no `jq` dependency (native Go JSON); no yolo flag
- **agy** — no model passthrough (model chosen inside its TUI)
- **shell** — execs the user's passthrough args directly as argv (no shell involved), or interactive `bash` when no command is given; no model, no yolo, no session resume; implements `ArgSetter`

The `wt agents` subcommand lists registered drivers, whether each binary is
installed, and its yolo flag.

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
- **Resume** — append `--resume <id>` (claude) or `--session <id>` (opencode)
  and launch.
- **Start fresh** — launch without resume args.
- **Cancel** — return to the agent+model screen.

If no session exists, the agent launches immediately. The non-TUI launch path
(lesson 17) performs the same resume check in `BuildLaunchCmd`, appending
`--resume`/`--session` without prompting.

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

- `Run(yolo bool, agent string, extraArgs []string)` — entry point; reached from `rootCmd.RunE` as the fallback
  after every flag handler has had a chance to early-return. `agent` is the `--agent` flag value
  ("" = use config default). `extraArgs` are the user's passthrough args after `--`.
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
> `-W`, `--cwd`, `wt rotate`, etc.) skip `tui.Run()` and don't need a TTY
> for `wt` itself (though the launched agent is interactive).

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
Full walkthrough:
[docs/go-course/lesson-13-worktree-screen.md](docs/go-course/lesson-13-worktree-screen.md).

### Agent+model screen

`selectedEntryMsg` transitions to `phaseAgent`, which lists configured
agents plus the registered command agents (currently `shell`). `Enter` on
an agent advances to `phaseModel` (with the agent pre-selected); `Enter`
on a command agent launches immediately (no model screen, no rotation,
no session resume). When `-A` is supplied, the agent+command picker is
skipped and `phaseModel` opens directly (or launches immediately for a
command agent). A `phase` enum
(`phaseList` / `phaseAgent` / `phaseModel` / `phaseResume` /
`phaseGuardWarn` / `phaseOllamaWarn` / `phaseNewWorktree`) tracks the
active screen.

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
keybind footer. Full walkthrough:
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
  `-b` for a new branch)
- `EnsureForBranch(dir, branch)` — the picker path, handling local,
  remote-tracking, and brand-new branches. Remote branches create a local
  branch tracking them; errors if the short name collides with a local branch
- Helpers: `branchExists`, `remoteExists`, `isWorktreePath`

Branch names with slashes use the last path component as the worktree
directory (`.worktrees/my-branch`). The `-W`/`--worktree` flag is wired in
`cmd/wt/main.go`: it creates/reuses the worktree and then launches the
agent there (no TUI).

```bash
go run ./cmd/wt -W my-feature -A claude   # create/reuse worktree, launch claude there
```

### Migration (Go)

On first run, `wt` migrates the legacy bash `~/.config/agent-wt/models.conf`
into `config.toml` automatically. The migration:

- Parses multi-line `CODE_MODELS`/`DESIGN_MODELS` bash arrays
- Strips `#`-commented-out models
- Creates `Provider`/`Agent` entries for each `native:X` model
- Merges models that appear in both code and design rotations (union of tags)
- Runs only once — skipped if `config.toml` already exists

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
```
