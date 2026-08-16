# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this repo is

Shell scripts that wrap AI coding agent CLIs (claude, codex, copilot, pi, agy, opencode) with git worktree management. Each `*-wt` launcher presents an fzf picker of worktrees and branches, creates worktrees on demand, optionally selects a model via rotation, and then `exec`s the underlying agent.

A Go rewrite is in progress — the `wt` binary (`cmd/wt/`) will eventually replace the bash wrappers with a unified TUI tool. See `docs/go-course/` for the lesson plan.

## Installation

Copy everything in `bin/` to a single directory on `$PATH` (e.g., `~/.local/bin/`). All scripts must remain co-located — wrappers find `wt-core.sh` and `wt-install-guard` via `SCRIPT_DIR`. If `wt-install-guard` is missing, launchers print a warning and skip auto-installing the main-branch commit guard.

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

### Plugin pattern

`bin/wt-core.sh` is the shared engine. It is never executed directly — wrappers `source` it and implement a contract before calling `wt_main`. The contract is:

| Global / Function | Required | Purpose |
|---|---|---|
| `WT_DEFAULT_CODE` | Yes (model rotators) | Fallback model for `--code` mode |
| `WT_DEFAULT_DESIGN` | Yes (model rotators) | Fallback model for `--design` mode |
| `WT_AGENT_NAME` | Yes (model rotators) | Agent identifier for model-usability checks (e.g. `claude`) |
| `wt_check_deps()` | Yes | Verify agent binary exists; call `die` with install hint if not |
| `wt_yolo_flag()` | Yes | Echo the tool's skip-permissions flag (or empty string) |
| `wt_exec "$@"` | Yes | Construct and `exec` the final agent launch command |
| `wt_pre_exec()` | No | Hook called after `cd` into worktree, before `wt_exec` (`claude-wt` and `opencode-wt` define this) |

### Core flow (`wt_main`)

1. Parse flags (`--code`, `--design`, `--native`, `-w`, `--yolo`, `--cwd`, `--no-guard`, `--check-guard`, `--init`) — all flag parsing is shared in `wt-core.sh`
2. If `--init` was given: call `seed_agent_instructions`, auto-install guard, and exit — no agent binary required
3. Call `wt_check_deps`
4. Auto-install `block-main-commit` pre-commit hook via `wt-install-guard`
5. If `-w <name>` given: `ensure_worktree_for_name` then launch — skip fzf
6. If `--cwd` given: launch in current repo root — skip fzf
7. If outside a git repo: pure passthrough to agent — skip fzf
8. Otherwise: `gather_entries` → fzf → `handle_worktree_selection` or `handle_branch_selection`

Branch names with slashes (e.g. `feature/my-branch`, `origin/feature`) are supported: the last path component is used for the worktree directory name (`.worktrees/my-branch`, `.worktrees/feature`) while the full branch name is passed to `git worktree add`. Remote tracking branches are checked out as new local branches.

### Model rotation

Applies to `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, and `opencode-wt` (not `agy-wt`, which has no CLI `--model` flag).

- `get_model_from_rotation()` is in `wt-core.sh` — shared across all model-rotating launchers. It reads `WT_DEFAULT_CODE`, `WT_DEFAULT_DESIGN`, `WT_AGENT_NAME`, and `WT_MODEL_MODE` directly.
- Config: `~/.config/agent-wt/models.conf` — defines `CODE_MODELS`, `DESIGN_MODELS` arrays, `NATIVE_<AGENT>` vars, and `PROVIDER_OLLAMA_BASE_URL` (used by copilot-wt). Respects `XDG_CONFIG_HOME` override.
- State: `~/.config/agent-wt/rotation-{code,design}.state` — two-line file: `<next_index>\n<last_selected>`
- Cross-rotation coordination: each mode checks the other's last-used model and skips it to avoid duplication
- Model values: `native:<agent>` or bare `native` (use agent's own default) or a model name string (cloud/ollama)
- `--code` (default) and `--design` select which rotation list to use
- `--native` bypasses rotation entirely, reads `NATIVE_<AGENT>` from `models.conf`; errors if not configured

### Main guard

`wt-install-guard` writes a `block-main-commit v1` pre-commit hook that blocks commits to `main`/`master`. Launchers auto-install it on every invocation when inside a git repo. The hook can be bypassed by:
- `git commit --no-verify` (emergency)
- `WT_SKIP_MAIN_BLOCK=1` env var (CI/automation)
- `<launcher> --no-guard` (removes the hook via `wt-install-guard --uninstall`)

### Agent init

`seed_agent_instructions` in `wt-core.sh` seeds project-level instruction files when `--init` is passed. It creates `AGENTS.md` with a seed template if missing, plus an agent-specific pointer file if the agent supports one (`CLAUDE.md` for Claude, `.github/copilot-instructions.md` for Copilot). Files that already exist are never overwritten. Requires a git working tree (bare repos are rejected); exits with an error if not in one. After seeding, the main guard is auto-installed just as a normal launch would. If `-w` is also given, it is ignored with a warning — seeding always targets the current working tree root.

### Session resume (claude-wt, opencode-wt)

`wt_pre_exec` checks for prior sessions and, if found, offers "Resume" or "Start fresh" via fzf. Skipped when `--cwd` is used.

- `claude-wt`: checks `~/.claude/projects/<slug>/*.jsonl`, where `<slug>` is the worktree path with non-alphanumeric chars replaced by `-`.
- `opencode-wt`: checks `~/.local/share/opencode/storage/session/<project-id>/`, where `<project-id>` is the git commit hash of the repo's root commit (matches OpenCode's own project-id algorithm).

## Key flags (all launchers)

| Flag | Effect |
|---|---|
| `-w <name>`, `--worktree <name>` | Use/create worktree for branch `<name>`; skip fzf |
| `--cwd` | Launch in current repo root; skip fzf and session resume |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--code` | Use code model rotation (default) — rotation-supporting launchers only |
| `--design` | Use design model rotation — rotation-supporting launchers only |
| `--native` | Use `NATIVE_<AGENT>` from `models.conf`; error if not configured — rotation-supporting launchers only |
| `--no-guard` | Remove the main-branch commit guard and exit |
| `--check-guard` | Report guard status and exit |

## Adding a new launcher

1. Copy an existing wrapper (e.g., `agy-wt` for no model rotation, or `claude-wt` for model rotation)
2. Set `SCRIPT_DIR` at the top (via `"$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"`)
3. If the agent supports model rotation, set `WT_DEFAULT_CODE`, `WT_DEFAULT_DESIGN`, and `WT_AGENT_NAME` **before** `source "$SCRIPT_DIR/wt-core.sh"` — these must exist before the source so `parse_wt_args` can detect rotation support
4. `source "$SCRIPT_DIR/wt-core.sh"`, then set `WT_NAME="$(basename "$0")"`
5. Implement `wt_check_deps()`, `wt_yolo_flag()`, `wt_exec`
6. Implement `wt_pre_exec()` if the agent has a session concept (see `claude-wt` and `opencode-wt` for two different session-detection approaches)
7. Call `wt_main "$@"`
8. Add a doc file to `docs/wt-agents/`

## Copilot-specific: Ollama model passthrough

`copilot-wt` does not pass `--model` for Ollama models. Instead it sets four environment variables (`COPILOT_PROVIDER_BASE_URL`, `COPILOT_PROVIDER_API_KEY`, `COPILOT_PROVIDER_WIRE_API`, `COPILOT_MODEL`) before `exec copilot`. The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `models.conf`, defaulting to `http://localhost:11434`.

## OpenCode-specific: Ollama model passthrough

`opencode-wt` does not pass `--model` for Ollama models. Instead it sets `OPENCODE_CONFIG_CONTENT` (inline JSON) before `exec opencode`, e.g. `{"model":"ollama/<model>","provider":{"ollama":{"options":{"baseURL":"<url>/v1","apiKey":""}}}}`. This is OpenCode's highest-precedence config layer and overrides `~/.config/opencode/opencode.json`. The Ollama base URL is read from `PROVIDER_OLLAMA_BASE_URL` in `models.conf`, same as `copilot-wt`.

## Pi-specific: model sync

`pi-wt` launches pi with `--model <id>` only when the model is present in `~/.pi/agent/models.json` with `._launch: true` (orphaned entries are skipped) — this check requires `jq`. `pi` reads its model catalog from `models.json`, so `pi-wt` auto-syncs **non-native** models from `~/.config/agent-wt/models.conf` into it on every launch — both cloud models (`:cloud` suffix) and local models (MLX, etc.). `native:` entries are skipped. The sync is idempotent (only adds, never removes) and requires `jq`.

## Docs

- `docs/configuration.md` — Claude Code and Codex CLI configuration (hooks, settings.json, environment filtering)
- `docs/wt-agents/` — per-agent reference docs (one file per launcher)

## No test suite (bash)

The bash scripts have no automated unit test suite. Quality gates:

```bash
make lint         # shellcheck (ignores expected warnings for dynamic sourcing)
                  # Suppressed: SC1090,SC1091 (dynamic source), SC2034 (unused vars), SC2155 (declare+assign)
make format       # shfmt -w -i 2 -ci
make format-check # shfmt -d (CI check)
make check        # lint + format-check
make test         # regression tests + smoke test invocations
```

## Go tests

The Go packages have unit and integration tests with a **what/why comment convention**: every `Test*` function has a top-level `//` block explaining what it tests and why that matters (the user-facing consequence of a regression). This makes the test suite self-documenting and helps reviewers understand the stakes of each assertion.

```bash
go test ./...        # run all Go tests
go test ./internal/worktree -v  # verbose, one package
go vet ./...         # static analysis
```

Test coverage by package:

| Package | Tests | Focus |
|---|---|---|
| `internal/config` | 30+ | Load, Validate, Save, HasTag, ResolveLocation, migration, secrets |
| `internal/registry` | 9 | Merge (curated wins), parseOllamaList, OpenRouter JSON |
| `internal/rotation` | 7 | Next advances, cross-skip, fallback, state persistence |
| `internal/agents` | 10 | Per-agent Build output, Installed, Command |
| `internal/guard` | 11 | Status check, install idempotency, foreign-hook preservation, uninstall restore |
| `internal/worktree` | 21 | Worktree parsing, branch dedup, default-branch skip, remote shadowing, worktree creation (EnsureForName/EnsureForBranch) |
| `internal/initseed` | 7 | `--init` seeding: AGENTS.md + pointer files, idempotency, Root() in/outside repo |
| `internal/session` | 7 | Slug, relative time, newest-session-by-mtime, missing-dir handling, project-id, HOME-override integration |

## Go module

The Go `wt` tool lives alongside the bash wrappers. Module root is the repo root.

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
| `cmd/wt/main.go` | CLI entry point (cobra) |
| `internal/config/` | Config loading, model registry types, validation, secrets, legacy migration |
| `internal/registry/` | Live model discovery (Ollama CLI, OpenRouter API) and registry merge |
| `internal/rotation/` | Tag-based model rotation with cross-tag skip and persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook, reports status (`Check`/`Install`/`Uninstall`) |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/session/` | Session resume detection: claude slug dirs, opencode project-id, mtime ranking |
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
- The hidden `--rotate-tag <tag>` flag on `wt` prints the next model in a tag
  group and exits — a test helper until the TUI (lesson 12)

```bash
go run ./cmd/wt --rotate-tag code    # prints next code model, advances state
go run ./cmd/wt --rotate-tag design  # independent rotation for design
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
not an error. The bash wrappers resume with a per-agent flag — claude uses
`--resume <id>`, opencode uses `--session <id>` — and skip the prompt on
`--cwd` or an explicit model mode; that launch wiring lands in the TUI
(lesson 16).

```bash
# Test helper: print the newest resumable session for an agent
wt --debug-session claude
wt --debug-session opencode
```

### Worktree (Go)

The `internal/worktree` package handles both enumeration (lesson 7) and
creation (lesson 8) — the Go equivalent of `gather_entries` and
`ensure_worktree_for_name`/`handle_branch_selection` in `wt-core.sh`. Every
function takes `dir` (the repo root) as its first parameter so tests can run
git inside a temp repo.

- `Enumerate(dir, cwdRoot)` — lists pickable targets (current, worktrees, bare branches)
- `EnsureForName(dir, name)` — idempotent worktree for the `-w` flag: reuses an
  already-checked-out worktree path, reuses an existing `.worktrees/<name>`
  path, otherwise creates via `git worktree add` (or `-b` for a new branch)
- `EnsureForBranch(dir, branch)` — the picker path, handling local,
  remote-tracking, and brand-new branches. Remote branches create a local
  branch tracking them; errors if the short name collides with a local branch
- Helpers: `branchExists`, `remoteExists`, `isWorktreePath`

Branch names with slashes use the last path component as the worktree
directory (`.worktrees/my-branch`). The `-w` flag is wired in `cmd/wt/main.go`
and prints the resulting worktree path.

```bash
go run ./cmd/wt -w my-feature   # create/reuse worktree, print its path
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
# Basic flow — fzf picker inside a git repo (TUI not yet implemented)
claude-wt

# Skip picker, use/create a named worktree
claude-wt -w my-feature

# Launch in current directory (no worktree switch)
claude-wt --cwd

# Model rotation flags
claude-wt --code
claude-wt --design
claude-wt --native          # requires NATIVE_CLAUDE in models.conf

# Seed agent instruction files in a new repo (no agent binary required)
wt --init

# Guard management
claude-wt --check-guard
claude-wt --no-guard

# Non-rotation wrapper (agy-wt passes unknown flags through to agy)
agy-wt -w my-feature
```
