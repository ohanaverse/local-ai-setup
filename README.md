# agent-worktree

Wrappers around AI coding agent CLIs that add git worktree management.

Each `*-wt` launcher presents an fzf picker of worktrees and branches, creates worktrees on demand, optionally rotates models, and launches the underlying agent.

> **Go rewrite in progress:** A unified `wt` TUI tool is being built in `cmd/wt/`. See `docs/go-course/` for the lesson plan and `docs/superpowers/specs/` for design docs.

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

Copy everything in `bin/` to a single directory on `$PATH` (e.g., `~/.local/bin/`):

```bash
cp -r bin/* ~/.local/bin/
chmod +x ~/.local/bin/*-wt
```

Or use the Makefile:

```bash
make install      # Install to ~/.local/bin/
make uninstall    # Remove from ~/.local/bin/
```

All scripts must remain co-located — wrappers find `wt-core.sh` via `SCRIPT_DIR`.

## Quick start

```bash
# Launch in any git repo — pick a worktree or branch via fzf
claude-wt

# Work on a specific branch directly
claude-wt -w my-feature

# Run in the current directory (no worktree picker)
claude-wt --cwd

# Seed agent instruction files in a new repo
claude-wt --init

# Skip permission prompts
claude-wt --yolo

# Rotate through code models from models.conf
claude-wt --code     # default
claude-wt --design
```

## Flags

All launchers support:

| Flag | Description |
|---|---|
| `-w <name>`, `--worktree <name>` | Use or create a worktree for the given branch; skip fzf |
| `--cwd` | Launch in the current repo root; skip fzf and session resume |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--no-guard` | Remove the main-branch commit guard and exit |
| `--check-guard` | Report whether the guard is installed |

Model-rotating launchers (`claude-wt`, `codex-wt`, `copilot-wt`, `opencode-wt`, `pi-wt`) also support:

| Flag | Description |
|---|---|
| `--code` | Use code model rotation (default) |
| `--design` | Use design model rotation |
| `--native` | Use `NATIVE_<AGENT>` from `models.conf`; error if not configured |

## Model rotation

Works for `claude-wt`, `codex-wt`, `copilot-wt`, `opencode-wt`, and `pi-wt`.

### Configuration

`~/.config/agent-wt/models.conf`:

```bash
CODE_MODELS=(llama3.3:70b qwen2.5-coder:32b)
DESIGN_MODELS=(llama3.3:70b-vision qwen2.5:72b)

NATIVE_CLAUDE=claude-sonnet-4-5
NATIVE_CODEX=gpt-4o
NATIVE_COPILOT=claude-sonnet-4-5
NATIVE_PI=claude-sonnet-4-6

PROVIDER_OLLAMA_BASE_URL=http://localhost:11434
```

State is tracked in `~/.config/agent-wt/rotation-{code,design}.state` (two lines: next index, last selected model).

### How it works

- `--code` rotates through `CODE_MODELS`; `--design` rotates through `DESIGN_MODELS`
- Cross-rotation coordination: if the other mode just used a model, skip it to avoid duplication
- Cloud models (anything not `native:X`) must be available in ollama — the launcher verifies this before use
- `native:<agent>` falls through to the agent's own default; other `native:X` values are skipped
- Missing config → use the agent's built-in default

### `--native` flag

Bypasses rotation entirely and uses the agent's dedicated native model from `models.conf`:

```bash
claude-wt --native     # uses NATIVE_CLAUDE
opencode-wt --native    # uses NATIVE_OPENCODE
pi-wt --native         # uses NATIVE_PI
codex-wt --native      # uses NATIVE_CODEX
copilot-wt --native    # uses NATIVE_COPILOT
```

## Copilot-specific: Ollama passthrough

`copilot-wt` does not pass `--model` for Ollama models. Instead it sets environment variables for the OpenAI-compatible API:

```bash
COPILOT_PROVIDER_BASE_URL=http://localhost:11434/v1
COPILOT_PROVIDER_API_KEY=
COPILOT_PROVIDER_WIRE_API=responses
COPILOT_MODEL=<model-name>
```

The base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `models.conf`, defaulting to `http://localhost:11434`.

## Main guard

Launchers auto-install a `block-main-commit v1` pre-commit hook on every invocation when inside a git repo. It blocks commits to `main`/`master` unless bypassed:

- `git commit --no-verify` (emergency)
- `WT_SKIP_MAIN_BLOCK=1` env var (CI/automation)
- `<launcher> --no-guard` (removes the hook)

## Session resume (`claude-wt`, `opencode-wt`)

When a prior Claude Code or OpenCode session exists for the worktree path, the launcher prompts whether to resume it. Skipped when `--cwd` is used.

## Development

### Bash wrappers

```bash
make help         # Show available targets
make install      # Install to ~/.local/bin/
make lint         # Run shellcheck
make format       # Format with shfmt
make check        # Run lint + format-check
make test         # Run smoke tests
make clean        # Remove build artifacts
```

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
| `cmd/wt/main.go` | CLI entry point (cobra) |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration |
| `internal/rotation/` | Tag-based model rotation with cross-tag skip and persistent state |
| `testdata/` | Sample configs for manual testing |
| `docs/go-course/` | 20-lesson course building the Go rewrite |
| `docs/superpowers/specs/` | Design specs |
| `docs/superpowers/plans/` | Implementation plans |

#### Config (Go)

The Go tool uses `~/.config/agent-wt/config.toml` (TOML) with three entity types:

- **Provider** — model source with auth config (ollama, openrouter, claude, copilot)
- **Model** — specific variant from a provider, grouped by family (e.g. gemma4)
- **Agent** — AI coding tool with supported providers and optional default

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

#### Rotation (Go)

The `internal/rotation` package implements tag-based model rotation — the Go
equivalent of the bash `--code`/`--design` rotation. Any tag can be a rotation
group, not just `code` and `design`.

- `rotation.ForTag(cfg, tag)` builds a `Rotation` from all models tagged with `tag`
- `Next(otherTag)` advances to the next model in the group, persists state to
  `rotation-<tag>.state`, and optionally cross-skips the model that `otherTag`
  most recently used (avoids both groups landing on the same model)
- State is a two-line file: `<next_index>\n<last_selected_model>`, written
  atomically via temp file + rename
- The hidden `--rotate-tag <tag>` flag prints the next model in a tag group
  and exits — a test helper until the TUI arrives (lesson 12)

```bash
go run ./cmd/wt --rotate-tag code    # prints next code model, advances state
go run ./cmd/wt --rotate-tag design  # independent rotation for design
```

#### Migration (Go)

On first run, `wt` migrates the legacy bash `~/.config/agent-wt/models.conf`
into `config.toml` automatically. The migration:

- Parses multi-line `CODE_MODELS`/`DESIGN_MODELS` bash arrays
- Strips `#`-commented-out models
- Creates `Provider`/`Agent` entries for each `native:X` model
- Merges models that appear in both code and design rotations (union of tags)
- Runs only once — skipped if `config.toml` already exists

## Architecture

- `bin/wt-core.sh` — shared engine, sourced by all wrappers
- `bin/*-wt` — per-agent wrappers; each sets globals and implements `wt_check_deps`, `wt_yolo_flag`, `wt_exec`, and optionally `wt_pre_exec`

### Wrapper globals (for model-rotating launchers)

Before sourcing `wt-core.sh`:

```bash
WT_DEFAULT_CODE="native:claude"    # fallback for --code mode
WT_DEFAULT_DESIGN="native:claude"  # fallback for --design mode
WT_AGENT_NAME="claude"             # used to filter native:X models
```

See `CLAUDE.md` for the full architecture documentation.