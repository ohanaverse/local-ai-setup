# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

The `wt` binary (`cmd/wt/`) is a Go tool that launches AI coding agent CLIs (claude, codex, copilot, pi, agy, opencode) in a chosen worktree or branch. It presents a Bubble Tea TUI picker of worktrees and branches, creates worktrees on demand, rotates models by tag, and launches the selected agent. Non-interactive flags (`-w`, `--cwd`, `--agent`) skip the TUI and launch directly.

The `bin/*-wt` files are thin legacy shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`). The original bash engine (`wt-core.sh`, `wt-install-guard`) is retained only for `shell-wt`, which launches a shell command rather than an agent and has no Go equivalent yet. See `docs/go-course/` for the lesson plan.

## Installation

Build the `wt` binary and put it on `$PATH`:

```bash
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
```

Requires Go 1.26.3 (see `go.mod`).

> **Makefile scope.** The Makefile handles bash script tasks (install, lint, format, smoke tests) and a `clean` target that removes Go build artifacts (`bin/wt`, `*.test`). It does **not** build the Go binary — use `go build` above for that.

The `bin/*-wt` shims forward to `wt`, so `wt` must be on `$PATH` for them to work. `shell-wt` is the only remaining bash launcher and still needs `wt-core.sh` co-located.

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

The Go tool is the primary implementation; its packages are documented in [Go module](#go-module) below. The bash engine described here is **legacy** — retained only for `shell-wt`, which has no Go equivalent yet.

### Legacy bash engine

`bin/wt-core.sh` is the shared bash engine that `shell-wt` still sources. It implements the plugin contract (`wt_check_deps`, `wt_yolo_flag`, `wt_exec`, optional `wt_pre_exec`), flag parsing, worktree/branch selection via fzf, model rotation, the main-branch commit guard, and `--init` seeding. `bin/wt-install-guard` installs the `block-main-commit` pre-commit hook. All of this functionality is now implemented in Go (`internal/agents`, `internal/worktree`, `internal/rotation`, `internal/guard`, `internal/initseed`, `internal/session`), so the bash engine is only exercised by `shell-wt`.

## Key flags (`wt`)

| Flag | Effect |
|---|---|
| `-w <name>`, `--worktree <name>` | Use/create worktree for branch `<name>`; skip the TUI and launch |
| `--cwd` | Launch in the current repo root; skip the TUI |
| `--agent <name>` | Pin the agent to launch (claude, codex, copilot, pi, agy, opencode); defaults to the first configured agent |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--version` | Print version and exit |
| `--debug-worktrees` | List worktrees and branches (test helper) |
| `--debug-session <agent>` | Print the newest resumable session for an agent (test helper) |

The legacy bash flags `--code`, `--design`, `--native`, `--no-guard`, and `--check-guard` are not supported by `wt`. Model rotation is now tag-based (see [Rotation (Go)](#rotation-go)); the main guard is managed by `internal/guard`.

## Adding a new agent

1. Add a driver in `internal/agents/` (e.g. `internal/agents/<name>.go`) that registers a `Driver` via `register("<name>", ...)` and implements `Build`/`YoloFlag`.
2. Add a legacy shim in `bin/<name>-wt`:

   ```sh
   #!/usr/bin/env bash
   exec wt --agent <name> "$@"
   ```

3. Add a doc file to `docs/wt-agents/`.

## Docs

- `docs/configuration.md` — Claude Code and Codex CLI configuration (hooks, settings.json, environment filtering)
- `docs/wt-agents/` — per-agent reference docs (one file per launcher)

## Bash quality gates

The remaining bash scripts (`wt-core.sh`, `wt-install-guard`, `shell-wt`, and the `*-wt` shims) have no automated unit test suite. Quality gates:

```bash
make lint         # shellcheck (ignores expected warnings for dynamic sourcing)
                  # Suppressed: SC1090,SC1091 (dynamic source), SC2034 (unused vars), SC2155 (declare+assign)
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
| `internal/agents` | 10 | Per-agent Build output, Installed, Command |
| `internal/guard` | 11 | Status check, install idempotency, foreign-hook preservation, uninstall restore |
| `internal/worktree` | 23 | Worktree parsing, branch dedup, default-branch skip, remote shadowing, worktree creation (EnsureForName/EnsureForBranch) |
| `internal/initseed` | 7 | `--init` seeding: AGENTS.md + pointer files, idempotency, Root() in/outside repo |
| `internal/session` | 7 | Slug, relative time, newest-session-by-mtime, missing-dir handling, project-id, HOME-override integration |
| `internal/tui` | 78 | List: WindowSizeMsg, quit keys, unknown keys, loading/not-ready/ready View, list build; Agent+model screen: selection → model phase, rotate (`r`) with temp state, rotate ignored in list phase, tag toggle (`d`), model-phase View; Model browser (lesson 15): `modelItem` filter/description, `refreshBrowser` cache + filter + deferred build, browser open/close, esc phase-gating, Enter-picks (no rotation advance), tag-filter toggle (`f`), source-filter cycle (`c`), WindowSizeMsg rebuild, empty-list state; Launch (lesson 16): `launchAgent`, resume flag injection, `runAndWaitCmd` stdio wiring, `phaseResume` prompt, resume/start-fresh/cancel choices, launch batch returns `tea.Quit`; Helpers: `firstAgent`/`firstModel` defaults & placeholders, state persistence, cross-tag skip, single-model group, placeholder View |
| `cmd/wt` | 10 | Non-TUI launch (lesson 17): `defaultAgent`/`defaultModel` resolution, `buildLaunch` resume-flag injection, `inGitRepoAt` |

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
| `cmd/wt/app.go` | Shared dependency struct: loads and validates config once, discovers live models |
| `cmd/wt/commands.go` | Subcommand constructors: `models`, `agents`, `rotate` (hidden) |
| `cmd/wt/helpers.go` | Centralized helpers: `mustGetString`, `yolo`, `defaultAgent`, `defaultModel`, `renderTable` |
| `cmd/wt/launch.go` | Non-TUI launch helpers: `launch`, `buildLaunch`, `launchDirect` |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration |
| `internal/registry/` | Live model discovery (Ollama CLI, OpenRouter API) and registry merge |
| `internal/rotation/` | Tag-based model rotation with cross-tag skip and persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook, reports status (`Check`/`Install`/`Uninstall`) |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent/model screen + model browser + launch/resume prompt (lessons 12–16): Model/Update/View, alternate-screen runner, `bubbles/list` picker, model rotation, model browser, agent launch, session resume prompt |
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

### Live discovery (Go)

The `internal/registry` package queries connected providers at runtime and merges the results with the curated config:

- **Ollama** — runs `ollama list`, parses both local models (with a size) and cloud models (size `-`).
- **OpenRouter** — fetches `https://openrouter.ai/api/v1/models` via HTTP.

Curated entries win on ID collisions; discovered entries fill gaps. The `wt models` subcommand prints the merged registry.

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

The `internal/agents` package abstracts how each coding agent is launched —
the Go equivalent of the per-launcher `wt_exec` logic in the bash wrappers.
Each agent registers a `Driver` that builds a `LaunchCmd` (binary, args, and
extra env vars) for a given model, so the TUI/CLI can say "launch agent X
with model Y" without knowing the agent's quirks.

- `Driver` interface: `Build(m config.Model, yolo bool) LaunchCmd` and `YoloFlag() string`
- `LaunchCmd` — plain struct: `Bin`, `Args`, `Env` (extra env merged over `os.Environ()`)
- Registry keyed by agent name; `ByName(name)` returns a driver or nil
- `Command(d, m, yolo, workdir)` resolves the binary via `LookPath` and returns a ready `exec.Cmd`
- `Installed(bin)` reports whether a binary is on PATH
- Native models (e.g. `claude/native`) are detected via `Model.IsNative()` and launch with no model args/env

Per-agent behavior:

- **claude** — cloud/local models set `ANTHROPIC_*` env vars pointing at the ollama gateway plus `--model`; native uses no args
- **codex** — `--model <id>`; native uses no args
- **copilot** — sets `COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_MODEL` env vars; never passes `--model`
- **opencode** — sets `OPENCODE_CONFIG_CONTENT` (inline JSON) for the ollama provider
- **pi** — `--model <id>` if model is present in `models.json` with `._launch: true`; requires `jq`; no yolo flag
- **agy** — no model passthrough (model chosen inside its TUI)

The `wt agents` subcommand lists registered drivers, whether each binary is
installed, and its yolo flag.

### Guard (Go)

The `internal/guard` package ports the bash `wt-install-guard` script into Go.
It installs a `block-main-commit v1` pre-commit hook that blocks commits to
`main`/`master`. The hook is embedded into the binary with `//go:embed`.

- `Check()` — reports `Installed`, `NotInstalled`, or `Err` by reading the
  common git dir's `hooks/pre-commit` and looking for the marker string.
- `Install()` — idempotent; appends the guard to an existing hook rather
  than overwriting it. Returns `changed` so callers can skip noisy output.
- `Uninstall()` — restores any preserved original hook; removes the file only
  when we created it and there was nothing before. Leaves foreign hooks alone.

The common git dir (via `git rev-parse --git-common-dir`) is used so the
hook applies to all worktrees of the repo.

### Init seeding (Go)

The `internal/initseed` package ports `seed_agent_instructions` from
`wt-core.sh`. It is called by `wt --init` and, in later lessons, by the
TUI when launching a specific agent.

- `Root()` — resolves the current repo root via `git rev-parse --show-toplevel`.
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

The `internal/session` package ports `find_latest_session`,
`compute_project_slug`, and `relative_time` from `wt-core.sh`, plus the
opencode-specific `_find_opencode_sessions` from `opencode-wt`. It detects the
newest resumable session for an agent in a worktree.

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
(lesson 17) performs the same resume check in `buildLaunch`, appending
`--resume`/`--session` without prompting.

```bash
# Test helper: print the newest resumable session for an agent
wt --debug-session claude
wt --debug-session opencode
```

### TUI shell (Go)

The `internal/tui` package holds the Bubble Tea app shell. `Run(yolo bool)`
starts `tea.NewProgram` with `tea.WithAltScreen()` and returns when the program
exits. The `model` type implements `tea.Model` (`Init` / `Update` / `View`);
lessons 12+ layer on the worktree list, agent+model screen, model browser,
resume prompt, and launch command.

- `Run(yolo bool)` — entry point; reached from `rootCmd.RunE` as the fallback
  after every flag handler has had a chance to early-return. The `yolo`
  argument is stored in the model and passed through to the selected agent's
  driver at launch time.
- `tea.WithAltScreen()` — Bubble Tea manages the alternate screen buffer
  for full-screen TUI rendering.
- `currentProgram` — a package-level pointer to the running `tea.Program`,
  set by `Run()` so `runAndWaitCmd` can call `ReleaseTerminal()` before the
  agent takes over and `RestoreTerminal()` when it exits.
- `launchAgent()` — builds the `exec.Cmd` via `agents.Command` and appends
  `--resume` / `--session` flags when resuming a claude/opencode session.
- `runAndWaitCmd()` — a `tea.Cmd` that releases the terminal, wires stdio,
  runs the agent, restores the terminal, and returns a `launchDoneMsg`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`. Running from a pipe,
> CI runner, or editor output panel fails with
> `could not open a new TTY: open /dev/tty: device not configured`. Always
> run `wt` from a real terminal session. The non-TUI flag paths (`--version`,
> `-w`, `--cwd`, `wt rotate`, etc.) skip `tui.Run()` and don't need a TTY
> for `wt` itself (though the launched agent is interactive).

### Worktree picker screen (Lesson 13)

The worktree list screen replaces the bash `fzf` picker with a richer
`bubbles/list` widget:

- `internal/tui/worktree_list.go` — adapts `worktree.Entry` to `list.Item`
  via `entryItem` (`Title()`, `Description()`, `FilterValue()`)
- `buildList(entries, width, height)` — builds a `list.Model` with title
  `"Pick a worktree or branch"`
- `loadEntriesCmd()` — async `tea.Cmd` that calls `worktree.RepoRoot()` +
  `worktree.Enumerate(root, root)` in a goroutine
- `entriesLoadedMsg` — carries the enumerated entries (or error) back into
  `Update`
- `selectedEntryMsg` — emitted on Enter carrying the chosen `worktree.Entry`

The list supports arrow navigation and built-in fuzzy filtering (type to
narrow branches). Picking an entry emits `selectedEntryMsg`, which lesson 14
wires to the agent+model screen; worktree creation and agent launch are wired
in later lessons.

```bash
go run ./cmd/wt           # interactive TUI (needs a TTY)
go run ./cmd/wt --version # non-interactive, no TTY needed
```

### Agent+model screen (Lesson 14)

After the user picks a worktree, the TUI moves to an **agent+model screen**
that shows the selected agent and the currently shown model, with explicit
one-keystroke actions replacing the bash tool's silent auto-rotation. A
`phase` value (`phaseList` / `phaseModel` / `phaseBrowser` / `phaseResume`)
tracks which screen is active.

- `selectedEntryMsg` now transitions to `phaseModel`, resolving the initial
  agent (first in `cfg.Agents`) and model (first in the default tag group)
  via `firstAgent` and `firstModel`. Worktree selection is stored for lesson
  16's launch.
- **`r`** — rotate to the next model in the active tag group via
  `rotation.ForTag(cfg, tag).Next(otherTag)`, advancing the on-disk
  `rotation-<tag>.state` file.
- **`m`** — open the model browser (lesson 15).
- **`d`** — toggle the active tag group between `code` and `design`,
  re-resolving the shown model; `otherTag` drives the cross-tag skip so
  rotation avoids the other group's last-used model.
- **`enter`** — launch the agent (lesson 16). If `session.LatestForAgent`
  finds a claude/opencode session, switch to `phaseResume` and show a
  resume prompt instead of launching immediately.
- `View` renders the model phase distinctly (agent / model / tag + keybind
  hints); `Run(yolo bool)` loads the config up front and passes it into the
  model.

```bash
go run ./cmd/wt           # interactive TUI (needs a TTY)
```

Pick a worktree, then on the agent+model screen press `r` to cycle the
model (and watch `~/.config/agent-wt/rotation-code.state` advance), `d` to
toggle the tag group, `m` to open the model browser, `enter` to launch or
show the resume prompt, `q` to quit.

The full TUI flow (worktree list → agent/model screen → model browser →
resume prompt → launch) is built across lessons 13–16 and wired in lesson 17.

### Model browser screen (Lesson 15)

The model browser is the "choose the backend LLM from a list" feature: a
`bubbles/list`-backed picker over the curated + discovered model registry
(opened with `m` from the agent+model screen). The browser is a *view*
into the registry, not rotation: picking a model here sets the current
model directly (no state-file advance), distinguishing deliberate
selection from `r`'s quick rotation.

- `internal/tui/model_browser.go` — `modelItem` adapter (lists
  `config.Model` with provider/location/source/tags columns), `buildModelItems`,
  `refreshBrowser`, and `browserView`.
- `phaseBrowser` constant extends the existing `phase` enum; the prior
  lesson's `browserOpen bool` was rejected in favor of an enum constant so
  every key gate is uniform (`m.phase == phaseBrowser`).
- `m` key opens the browser; `enter` picks the selection back into
  `m.current` and returns to `phaseModel`. Picking never writes the
  rotation state file (asserted in `TestBrowserEnterPicksModel`).
- `esc` is phase-aware: pops back from the browser, quits from
  `phaseModel`.
- `f` toggles the tag filter between `""` and `m.tag`; `c` cycles a
  `sourceCycle` int 0→1→2 (all → curated → discovered), wired through
  `registry.FilterByTag` and `registry.FilterBySource`.
- **Discovery cache** — `m.browserCache` snapshots `registry.Discover(cfg)`
  once per browser-open; subsequent filter toggles reuse the cache rather
  than re-shell to `ollama list` and re-HTTP OpenRouter. The cache is
  cleared on every `m` press so each browser session re-discovers.
- **Deferred build** — `refreshBrowser` skips the list build when
  `width`/`height` are zero (no `WindowSizeMsg` yet); the next
  `WindowSizeMsg` triggers a rebuild while the browser is open.
- **Test seam** — `registry.Discover` now has a `DiscoverWith(cfg,
  []Discoverer)` variant for test injection; production callers still use
  `Discover(cfg)` with the default Ollama + OpenRouter discoverers.

```bash
go run ./cmd/wt           # interactive TUI (needs a TTY)
```

Pick a worktree, press `m` to open the browser. You'll see curated +
discovered models with provider/location/source/tags columns. Filter by
typing; press `f` to toggle tag filtering, `c` to cycle source filter,
`enter` to select a model, `esc` to go back.

### Worktree (Go)

The `internal/worktree` package handles both enumeration (lesson 7) and
creation (lesson 8) — the Go equivalent of `gather_entries` and
`ensure_worktree_for_name`/`handle_branch_selection` in `wt-core.sh`. Every
function takes `dir` (the repo root) as its first parameter so tests can run
git inside a temp repo.

- `RepoRoot()` — returns the current repo root via `git rev-parse --show-toplevel`;
  used by the TUI to discover which repo to enumerate without an explicit path
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
