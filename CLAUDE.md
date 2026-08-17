# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

The `wt` binary (`cmd/wt/`) is a Go tool that launches AI coding agent CLIs (claude, codex, copilot, pi, agy, opencode, shell) in a chosen worktree or branch. It presents a Bubble Tea TUI picker of worktrees and branches, creates worktrees on demand, rotates models by tag, and launches the selected agent. Non-interactive flags (`-w`, `--cwd`, `--agent`) skip the TUI and launch directly.

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

`bin/*-wt` are one-line shims that `exec wt --agent <name> "$@"`. They exist for ergonomic convenience (`claude-wt` is shorter than `wt --agent claude`). All logic — worktree selection, model rotation, guard, init seeding, session resume, and shell command execution — is in Go.

## Key flags (`wt`)

| Flag | Effect |
|---|---|
| `-w <name>`, `--worktree <name>` | Use/create worktree for branch `<name>`; skip the TUI and launch |
| `--cwd` | Launch in the current repo root; skip the TUI |
| `--agent <name>` | Pin the agent to launch (claude, codex, copilot, pi, agy, opencode, shell); defaults to the first configured agent |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--version` | Print version and exit |
| `--check-guard` | Check if the `block-main-commit` guard is installed and exit |
| `--no-guard` | Remove the `block-main-commit` guard and exit |
| `--debug-worktrees` | List worktrees and branches (test helper) |
| `--debug-session <agent>` | Print the newest resumable session for an agent (test helper) |

The legacy bash flags `--code`, `--design`, and `--native` are not supported by `wt`. Model rotation is now tag-based (see [Rotation (Go)](#rotation-go)); the main guard is managed by `internal/guard`.

### Passthrough args

Extra args are forwarded to the launched agent. This restores the bash
engine's `WT_PASSTHROUGH_ARGS` behavior and enables `shell-wt <command>`.
`--` is only required when the command starts with a flag-like token (e.g.
`shell-wt -- rm --init`), since the root `wt` command is registered with
`cobra.ArbitraryArgs` so bare positional args reach the launcher instead of
being rejected as an unknown subcommand — but pflag still consumes anything
starting with `-` before it sees `--`.

```bash
claude-wt -- --verbose     # → claude --model X --verbose
shell-wt -w test -- npm test  # → exec npm test in .worktrees/test
wt --agent codex -- --full-auto  # → codex --model X --full-auto
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
| `internal/config` | 38 | Load, Validate, ValidateAll, Save, HasTag, ResolveLocation, migration, secrets |
| `internal/registry` | 15 | Merge (curated wins), parseOllamaList, OpenRouter JSON, FilterByTag/FilterBySource |
| `internal/rotation` | 7 | Next advances, cross-skip, fallback, state persistence |
| `internal/agents` | 30 | Per-agent Build output, Installed, Command, BuildLaunchCmd, ArgSetter, shell driver, shell quoting |
| `internal/guard` | 11 | Status check, install idempotency, foreign-hook preservation, uninstall restore |
| `internal/worktree` | 23 | Worktree parsing, branch dedup, default-branch skip, remote shadowing, worktree creation (EnsureForName/EnsureForBranch) |
| `internal/initseed` | 7 | `--init` seeding: AGENTS.md + pointer files, idempotency, Root() in/outside repo |
| `internal/session` | 7 | Slug, relative time, newest-session-by-mtime, missing-dir handling, project-id, HOME-override integration |
| `internal/tui` | 88 | List: WindowSizeMsg, quit keys, unknown keys, loading/not-ready/ready View, list build; Agent+model screen: selection → model phase, rotate (`r`) with temp state, rotate ignored in list phase, tag toggle (`d`), model-phase View; Model browser (lesson 15): `modelItem` filter/description, `refreshBrowser` cache + filter + deferred build, browser open/close, esc phase-gating, Enter-picks (no rotation advance), tag-filter toggle (`f`), source-filter cycle (`c`), WindowSizeMsg rebuild, empty-list state; Launch (lesson 16): `launchAgent`, resume flag injection, `runAndWaitCmd` stdio wiring, `phaseResume` prompt, resume/start-fresh/cancel choices, launch returns ONLY `runAndWaitCmd` (NOT batched with `tea.Quit`, which would kill the agent via process exit); Ollama warn: unavailable model warning, cancel/proceed; Helpers: `DefaultAgent`/`firstModel` defaults & placeholders, state persistence, cross-tag skip, single-model group, placeholder View |
| `cmd/wt` | 20 | Non-TUI launch (lesson 17): `DefaultAgent`/`defaultModel` resolution, `buildLaunch` resume-flag injection with extraArgs, `inGitRepoAt`, pi sync, ollama unavailable |

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
| `cmd/wt/launch.go` | Non-TUI launch helpers: `launch`, `buildLaunch`, `launchDirect` — all accept `extraArgs` for passthrough |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration; shared helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `DefaultAgent`) |
| `internal/registry/` | Live model discovery (Ollama CLI, OpenRouter API) and registry merge |
| `internal/rotation/` | Tag-based model rotation with cross-tag skip and persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands; `BuildLaunchCmd` shared constructor; `ArgSetter` interface for shell; drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook, reports status (`Check`/`Install`/`Uninstall`) |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
| `internal/ollamacheck/` | Ollama model availability check before launch (`IsOllamaModel`, `Available` — reuses `registry.Ollama{}.Discover()`) |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent/model screen + model browser + launch/resume prompt + ollama warning + guard warning (lessons 12–17): Model/Update/View, alternate-screen runner, `bubbles/list` picker, model rotation, model browser, agent launch, session resume prompt, shell agent skip-model-screen |
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
`registry.Discover` (including the OpenRouter HTTP call) runs on demand — in
the `wt models` subcommand and the TUI model browser. Flag-only paths never
hit the OpenRouter API. `--version` and `--init` don't shell out to ollama at
all; `-w` and `--cwd` run a single `ollama list` via `ollamacheck.Available()`
for the pre-launch availability check, but skip the full registry discovery.

### Rotation (Go)

The `internal/rotation` package implements tag-based model rotation — the Go
equivalent of the bash `--code`/`--design` rotation, generalized so any tag
can be a rotation group.

- `rotation.ForTag(cfg, tag)` builds a `Rotation` from all models tagged with `tag`
- `Next(otherTag)` advances to the next model, persists state to
  `rotation-<tag>.state`, and optionally cross-skips the model that `otherTag`
  most recently used (avoids both groups landing on the same model)
- State file: `~/.config/agent-wt/rotation-<tag>.state` — two lines:
  `<next_index>\n<last_selected_model>`, written atomically (temp + rename)
- The hidden `wt rotate <tag>` subcommand prints the next model in a tag
  group and exits — a test helper until the TUI (lesson 12)

```bash
go run ./cmd/wt rotate code    # prints next code model, advances state
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
lessons 12+ layer on the worktree list, agent+model screen, model browser,
resume prompt, and launch command.

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
> `-w`, `--cwd`, `wt rotate`, etc.) skip `tui.Run()` and don't need a TTY
> for `wt` itself (though the launched agent is interactive).

### Worktree picker screen

`internal/tui/worktree_list.go` adapts `worktree.Entry` to `list.Item` via
`entryItem` (`Title()`/`Description()`/`FilterValue()`). `buildList` builds
the `list.Model`; `loadEntriesCmd` async-loads via `worktree.RepoRoot()` +
`Enumerate(root, root)`; `entriesLoadedMsg` and `selectedEntryMsg` carry the
results and the Enter-picked entry into `Update`. The list is prepended with a
"+ New worktree…" sentinel row (`kindNewWorktree`) that opens the
new-worktree prompt on Enter, alongside the `n` keybinding. `buildList` also
advertises `n` in the footer help line (and full help view) via the list's
`AdditionalShortHelpKeys`/`AdditionalFullHelpKeys`, so the shortcut is
discoverable without scrolling to the sentinel row. Full walkthrough:
[docs/go-course/lesson-13-worktree-screen.md](docs/go-course/lesson-13-worktree-screen.md).

### Agent+model screen

`selectedEntryMsg` transitions to `phaseModel`, resolving the initial agent
via `cfg.DefaultAgent()` and model via `firstModel`. A `phase` enum
(`phaseList` / `phaseModel` / `phaseBrowser` / `phaseResume` /
`phaseGuardWarn` / `phaseOllamaWarn` / `phaseNewWorktree`) tracks the active
screen. When `--agent shell`, this screen is skipped entirely — shell has no
model, rotation, or session resume.

Keybindings: **`r`** rotates via `rotation.ForTag(cfg, tag).Next(otherTag)`
(advances `rotation-<tag>.state`); **`m`** opens the model browser; **`d`**
toggles the `code`/`design` tag group, driving the cross-tag skip; **`n`**
opens the new-worktree prompt (also reachable via the "+ New worktree…"
sentinel row in the picker); **`enter`** launches, or switches to
`phaseResume` if `session.LatestForAgent` finds a prior claude/opencode
session. Full walkthrough:
[docs/go-course/lesson-14-agent-model-screen.md](docs/go-course/lesson-14-agent-model-screen.md).

### Model browser screen

`internal/tui/model_browser.go` (`modelItem`, `buildModelItems`,
`refreshBrowser`, `browserView`) implements a `bubbles/list` picker over the
curated + discovered model registry, opened with `m` from the agent+model
screen. It's a *view*, not rotation: picking a model sets `m.current`
directly with no state-file write. `phaseBrowser` extends the phase enum;
`f` toggles the tag filter, `c` cycles the source filter (all → curated →
discovered) via `registry.FilterByTag`/`FilterBySource`, `esc` pops back,
`enter` picks.

`m.browserCache` snapshots `registry.Discover(cfg)` once per browser-open
(cleared on every `m` press) so filter toggles don't re-shell to `ollama
list` or re-HTTP OpenRouter; `refreshBrowser` defers the list build until a
`WindowSizeMsg` supplies real dimensions. `registry.DiscoverWith(cfg,
[]Discoverer)` is the test-injection seam. Full walkthrough:
[docs/go-course/lesson-15-model-browser.md](docs/go-course/lesson-15-model-browser.md).

### Worktree (Go)

The `internal/worktree` package handles both enumeration (lesson 7) and
creation (lesson 8). Every
function takes `dir` (the repo root) as its first parameter so tests can run
git inside a temp repo.

- `RepoRoot()` — returns the current repo root via `git rev-parse --show-toplevel`;
  used by the TUI, `initseed.Root()`, and the `cmd/wt` debug handlers to
  discover the repo root without an explicit path
- `Enumerate(dir, cwdRoot)` — lists pickable targets (current, worktrees, bare branches)
- `EnsureForName(dir, name)` — idempotent worktree for the `-w` flag: reuses an
  already-checked-out worktree path, reuses an existing `.worktrees/<name>`
  path, otherwise creates via `git worktree add` (or `-b` for a new branch)
- `EnsureForBranch(dir, branch)` — the picker path, handling local,
  remote-tracking, and brand-new branches. Remote branches create a local
  branch tracking them; errors if the short name collides with a local branch
- Helpers: `branchExists`, `remoteExists`, `isWorktreePath`

Branch names with slashes use the last path component as the worktree
directory (`.worktrees/my-branch`). The `-w` flag is wired in `cmd/wt/main.go`:
it creates/reuses the worktree and then launches the agent there (no TUI).

```bash
go run ./cmd/wt -w my-feature --agent claude   # create/reuse worktree, launch claude there
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
wt -w my-feature --agent claude

# Launch in current repo root (no worktree switch)
wt --cwd --agent codex

# Legacy shim forwards to wt
claude-wt --cwd   # → wt --agent claude --cwd

# Seed agent instruction files in a new repo (no agent binary required)
wt --init
```
