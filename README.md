# agent-worktree

Launch AI coding agent CLIs in git worktrees with model rotation and session resume.

The `wt` binary is a Go TUI tool that picks a worktree or branch, rotates models by tag, and launches the selected agent. The `bin/*-wt` shims are thin wrappers (`claude-wt` → `wt --agent claude`, `shell-wt` → `wt --agent shell`). See `docs/go-course/` for the lesson plan and `docs/superpowers/specs/` for design docs.

## Supported agents

| Launcher | Agent | Model rotation | Session resume |
|---|---|---|---|
| `claude-wt` | Claude Code | Yes | Yes |
| `codex-wt` | OpenAI Codex CLI | Yes | No |
| `copilot-wt` | GitHub Copilot CLI | Yes | No |
| `opencode-wt` | OpenCode | Yes | Yes |
| `pi-wt` | pi-coding-agent | Yes | No |
| `agy-wt` | Antigravity CLI | No | No |
| `shell-wt` | Shell command | No | No |

## Installation

### Go binary (primary)

```bash
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
```

Requires Go 1.26.3 (see `go.mod`).

### Legacy shims

Copy the bash wrappers to a directory on `$PATH`:

```bash
make install      # Install bin/* to ~/.local/bin/
make uninstall    # Remove from ~/.local/bin/
```

The `*-wt` shims just `exec wt --agent <name>`, so `wt` must be on `$PATH`.

## Quick start

```bash
# Launch in any git repo — TUI picker for worktrees and branches
claude-wt

# Work on a specific branch directly (skip TUI)
claude-wt -w my-feature

# Run in the current directory (no picker)
claude-wt --cwd

# Seed agent instruction files in a new repo
claude-wt --init

# Skip permission prompts
claude-wt --yolo
```

### In the TUI

| Key | Action |
|-----|--------|
| `enter` | Select worktree → agent+model screen → launch |
| `r` | Rotate to next model in the active tag group |
| `d` | Toggle between code and design tag groups |
| `m` | Open model browser (filter by tag or source) |
| `q` / `esc` | Quit / go back |

## Flags

All launchers support:

| Flag | Description |
|---|---|
| `-w <name>`, `--worktree <name>` | Use or create a worktree for the given branch; skip TUI |
| `--cwd` | Launch in the current repo root; skip TUI and session resume |
| `--agent <name>` | Pin the agent (claude, codex, copilot, pi, agy, opencode, shell) |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer) and exit |
| `--version` | Print version and exit |
| `--check-guard` | Report whether the main guard is installed |
| `--no-guard` | Remove the main-branch commit guard and exit |

Model rotation happens inside the TUI — there are no `--code`/`--design`/`--native` flags. Use `r` to rotate, `d` to switch tag groups, and `m` to browse all models.

## Configuration

The Go tool uses `~/.config/agent-wt/config.toml` (TOML) with three entity types:

- **Provider** — model source with auth config (ollama, openrouter, claude, copilot)
- **Model** — specific variant from a provider, grouped by family and tagged (e.g. `code`, `design`)
- **Agent** — AI coding tool with supported providers and optional default

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

### Migration from legacy `models.conf`

On first run, `wt` migrates the legacy bash `~/.config/agent-wt/models.conf` into `config.toml` automatically. The migration:

- Parses `CODE_MODELS`/`DESIGN_MODELS` bash arrays
- Creates `Provider`/`Agent` entries for each `native:X` model
- Merges models in both code and design rotations (union of tags)
- Runs only once — skipped if `config.toml` already exists

## Model rotation

Works for `claude-wt`, `codex-wt`, `copilot-wt`, `opencode-wt`, and `pi-wt`.

The `internal/rotation` package implements tag-based model rotation. Any tag can be a rotation group, not just `code` and `design`.

- `r` in the TUI rotates to the next model in the active tag group
- `d` toggles between code and design tag groups
- Cross-tag skip: if the other group just used a model, it's skipped to avoid duplication
- State is persisted to `~/.config/agent-wt/rotation-<tag>.state` (two lines: next index, last selected model)
- The hidden `wt rotate <tag>` subcommand prints the next model and advances state (test helper)

```bash
wt rotate code    # prints next code model, advances state
wt rotate design  # independent rotation for design
```

### Ollama availability check

Before launching with an Ollama model, `wt` verifies the model is locally available via `ollama list`. In the TUI, if the model is missing, you can proceed anyway, rotate to the next model, or cancel. In non-TUI mode (`-w` or `--cwd`), a missing model causes an error with a pull suggestion.

## Copilot-specific: Ollama passthrough

`copilot-wt` does not pass `--model` for Ollama models. Instead it sets environment variables for the OpenAI-compatible API:

```bash
COPILOT_PROVIDER_BASE_URL=http://localhost:11434/v1
COPILOT_PROVIDER_API_KEY=
COPILOT_PROVIDER_WIRE_API=responses
COPILOT_MODEL=<model-name>
```

The base URL comes from the ollama provider config in `config.toml`, defaulting to `http://localhost:11434`.

## Main guard

Launchers auto-install a `block-main-commit` pre-commit hook on every invocation when inside a git repo. It blocks commits to `main`/`master` unless bypassed:

- `git commit --no-verify` (emergency)
- `WT_SKIP_MAIN_BLOCK=1` env var (CI/automation)
- `<launcher> --no-guard` (removes the hook)

## Session resume (`claude-wt`, `opencode-wt`)

When a prior Claude Code or OpenCode session exists for the worktree path, the launcher prompts whether to resume it. Skipped when `--cwd` is used.

## Development

### Go module (`wt`)

```bash
go build ./...     # Build everything
go test ./...      # Run all tests
go run ./cmd/wt    # Run the wt CLI
go fmt ./...       # Format
go vet ./...       # Vet
```

#### Structure

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
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent/model screen + model browser + launch/resume prompt |
| `internal/ollamacheck/` | Ollama model availability check before launch |
| `testdata/` | Sample configs for manual testing |
| `docs/go-course/` | 20-lesson course building the Go rewrite |
| `docs/superpowers/specs/` | Design specs |
| `docs/superpowers/plans/` | Implementation plans |

#### Testing

The Go packages have unit and integration tests. Every `Test*` function has a top-level `//` block explaining **what** it tests and **why** that matters (the user-facing consequence of a regression).

```bash
go test ./...        # all packages
go test ./internal/worktree -v  # verbose, one package
go vet ./...         # static analysis
```

### Legacy bash scripts

The `*-wt` shims are thin bash wrappers that `exec wt --agent <name>`. They have no automated unit test suite. Quality gates:

```bash
make lint         # shellcheck
make format       # shfmt -w -i 2 -ci
make check        # lint + format-check
make test         # smoke test invocations (requires make install first)
make clean        # Remove build artifacts
```

## Architecture

- `bin/*-wt` — thin shims that `exec wt --agent <name>`
- `cmd/wt/` — unified Go tool: cobra CLI, Bubble Tea TUI, model registry, rotation, guard, init seeding, session resume

See `CLAUDE.md` for the full architecture documentation.