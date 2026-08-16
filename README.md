# agent-worktree

Wrappers around AI coding agent CLIs that add git worktree management.

Each `*-wt` launcher presents an fzf picker of worktrees and branches, creates worktrees on demand, optionally rotates models, and launches the underlying agent.

> **Go rewrite:** A unified `wt` TUI tool in `cmd/wt/` is the primary implementation. The `bin/*-wt` shims forward to it; `shell-wt` still uses the bash engine. See `docs/go-course/` for the lesson plan and `docs/superpowers/specs/` for design docs.

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

`shell-wt` must remain co-located with `wt-core.sh` (it sources it via `SCRIPT_DIR`). The other `*-wt` shims just `exec wt --agent <name>` and only need `wt` on `$PATH`.

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
- The hidden `wt rotate <tag>` subcommand prints the next model in a tag group
  and exits — a test helper until the TUI arrives (lesson 12)

```bash
go run ./cmd/wt rotate code    # prints next code model, advances state
go run ./cmd/wt rotate design  # independent rotation for design
```

#### Agents (Go)

The `internal/agents` package abstracts how each coding agent is launched —
the Go equivalent of the per-launcher `wt_exec` logic in the bash wrappers.
Each agent registers a `Driver` that builds a `LaunchCmd` (binary, args, and
extra env vars) for a given model.

- `Driver` interface: `Build(m config.Model, yolo bool) LaunchCmd` and `YoloFlag() string`
- `LaunchCmd` — plain struct: `Bin`, `Args`, `Env`
- Registry keyed by agent name; `ByName(name)` returns a driver or nil
- `Command(d, m, yolo, workdir)` resolves the binary via `LookPath` and returns a ready `exec.Cmd`
- `Installed(bin)` reports whether a binary is on PATH

Per-agent behavior:
- **claude** — cloud/local models set `ANTHROPIC_*` env vars + `--model`; native uses no args
- **codex** — `--model <id>`; native uses no args
- **copilot** — sets `COPILOT_PROVIDER_*` env vars; never passes `--model`
- **opencode** — sets `OPENCODE_CONFIG_CONTENT` (inline JSON)
- **pi** — `--model <id>`; no yolo flag
- **agy** — no model passthrough (model chosen inside its TUI)

#### Worktree (Go)

The `internal/worktree` package enumerates worktrees, local branches, and
remote-tracking branches by shelling out to `git` and parsing porcelain
output, and creates worktrees on demand. This is the data source for the TUI
picker (lessons 12–13) and the engine behind the `-w` flag.

- `worktree.Enumerate(dir, cwdRoot)` returns `[]Entry` with `Type` (`current`, `worktree`, `branch`)
- `worktree.EnsureForName(dir, name)` — idempotent worktree for the `-w` flag
  (reuses an existing worktree path, else creates via `git worktree add`)
- `worktree.EnsureForBranch(dir, branch)` — the picker path, handling local,
  remote-tracking, and brand-new branches
- Parses `git worktree list --porcelain` (block-oriented state machine)
- Uses `git for-each-ref` for stable branch listing
- Deduplicates: remote branches shadowed by locals are excluded; default branch is hidden as a bare branch

```bash
go run ./cmd/wt -w my-feature   # create/reuse worktree, print its path
```

#### Testing (Go)

The Go packages have unit and integration tests. Every `Test*` function has a
top-level `//` block explaining **what** it tests and **why** that matters (the
user-facing consequence of a regression).

```bash
go test ./...        # all packages
go test ./internal/worktree -v  # verbose, one package
go vet ./...         # static analysis
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

- `bin/wt-core.sh` — shared bash engine, sourced only by `shell-wt`
- `bin/*-wt` — thin shims that `exec wt --agent <name>` (except `shell-wt`, which sources `wt-core.sh`)
- `cmd/wt/` — unified Go tool: cobra CLI, Bubble Tea TUI, model registry, rotation, guard, init seeding, session resume

See `CLAUDE.md` for the full architecture documentation.
