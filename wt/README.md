# wt

Launch AI coding agent CLIs in git worktrees with model rotation.

The `wt` binary is a Go TUI tool that picks a worktree or branch, rotates models by tag, and launches the selected agent. The `bin/*-wt` shims are thin wrappers (`claude-wt` → `wt --agent claude`, `shell-wt` → `wt --agent shell`). See `docs/superpowers/specs/` for design docs.

## Supported agents

| Launcher | Agent | Model rotation |
|---|---|---|
| `claude-wt` | Claude Code | Yes |
| `codex-wt` | OpenAI Codex CLI | Yes |
| `copilot-wt` | GitHub Copilot CLI | Yes |
| `opencode-wt` | OpenCode | Yes |
| `pi-wt` | pi-coding-agent | Yes |
| `agy-wt` | Antigravity CLI | No |
| `shell-wt` | Shell command | No |

`wt` does not resume sessions for any agent — see [Sessions are the agent's own](#sessions-are-the-agents-own).

## Installation

### Go binary + shims

```bash
make install      # Build bin/wt, copy it + the shims to ~/.local/bin/ (re-signs on macOS)
make uninstall    # Remove from ~/.local/bin/
```

Requires Go 1.26.7 (see `go.mod`). `make install` builds the binary itself
(`make build`) — on macOS it also re-seals the ad-hoc code signature, which a
manual `go build` into `$GOPATH/bin` skips (AMFI then SIGKILLs the binary on
launch, exit 137).

The `*-wt` shims just `exec wt --agent <name>`, so the installed `wt` must be on `$PATH`.

## Quick start

```bash
# Launch in any git repo — TUI picker for worktrees and branches
claude-wt

# Work on a specific branch directly (skip TUI); a new name is created and said so
claude-wt -W my-feature

# --new is accepted and ignored (kept for old scripts)
claude-wt -W my-feature --new

# Run in the current directory (no worktree picker; works outside a git repo too)
claude-wt --cwd

# Seed agent instruction files in a new repo
claude-wt --init

# Skip permission prompts
claude-wt --yolo
```

### In the TUI

| Key | Action |
|-----|--------|
| `enter` | Select worktree → agent+model screen → launch (and advance rotation) |
| `up` / `down` (or `j` / `k`) | Move the model cursor |
| `n` | New worktree (in the worktree picker) |
| `q` / `esc` | Quit / go back |

Tag groups are chosen on the command line with `-T <tags>`; no key switches them.

## Flags

All launchers support:

| Flag | Description |
|---|---|
| `-W <name>`, `--worktree <name>` | Use the worktree for the given branch; skip the worktree picker. An existing worktree is reused with no question of its own (one `launching in …` line when nothing warns). A name that creates anything is announced first by its creation line, one shape per case: `creating branch <b> and worktree <p>` (a new branch), `creating worktree <p> for branch <b>` (a worktree for an existing branch), `creating worktree <p> for branch <b> tracking <r>` (a local branch tracking a remote-tracking name). The one default-yes question is asked before it only when a launch warning also applies, on a terminal. The root's own branch names the root and launches there with the root warning |
| `--new` | Accepted and ignored: a name that does not exist yet is created and said so either way (kept for old scripts). A no-op for an existing name; a usage error without `-W` or with `--cwd`/`--root` |
| `--root` | Launch in the repository's primary checkout, from anywhere (also from a linked worktree or subdirectory); skips the picker and accepts the root warning, though any other warning still asks. Exclusive with `--cwd` and `-W`. |
| `--cwd` | Launch in the current directory, inside a git repository or not; skips the worktree picker. Standing in the repository root it warns and proceeds (a terminal with any warning is asked the one question first). The agent starts there, so its own `--continue` (after `--`) finds that directory's sessions. |
| `--agent <name>` | Pin the agent (claude, codex, copilot, pi, agy, opencode, shell) |
| `-M <provider>/<name>`, `--model <provider>/<name>` | Pin the model (or `profile/<name>` for a slot profile); skips the model picker |
| `-T <tags>`, `--tags <tags>` | Filter the model list by tag (comma-delimited, OR within the flag) |
| `-F <families>`, `--family <families>` | Filter the model list by family (comma-delimited, OR within the flag) |
| `--replace` | With `-M`, start the model even if it means stopping a running one |
| `--others keep\|stop` | When a local start loads a model beside other providers' models: keep them or stop the ones wt can stop (default: ask; with no terminal keep and warn) |
| `--yolo` | Prepend the agent's skip-permissions flag |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer) and exit |
| `--version` | Print version and exit |
| `--check-guard` | Report whether the main guard is installed |
| `--no-guard` | Remove the main-branch commit guard and exit |

Model rotation happens inside the TUI — there are no `--code`/`--design`/`--native` flags. Use `-T <tag>` to filter to a tag group, then `up`/`down` to pick a model — launching it advances the rotation automatically for the next entry.

## Commands

Beyond launching, `wt` has subcommands; `wt --help` lists them. Each has a reference page:

| Command | What it does | Reference |
|---|---|---|
| `wt cleanup` | Lists worktrees, local branches and `origin` branches that can go, in four groups with the proof for each; on a terminal removes the ones you tick | [docs/wt-cleanup.md](docs/wt-cleanup.md), [guide 08 §9](../docs/guides/08-maintenance-and-troubleshooting.md) |
| `wt remove <worktree-path-or-branch>...` | Removes named worktrees and branches through the same engine, re-checking each; `--dry-run` first | [docs/wt-cleanup.md](docs/wt-cleanup.md#wt-remove-arguments-output-and-exit-codes) |
| `wt rescue <worktree-path-or-branch>` | Copies a worktree's local-only work into a new branch and worktree before you remove it | [docs/wt-cleanup.md](docs/wt-cleanup.md#wt-rescue-keep-work-before-it-is-lost) |
| `wt restore-root` | Moves the root's branch and uncommitted work into a worktree and puts the root back on the default branch | [docs/wt-cleanup.md](docs/wt-cleanup.md#wt-restore-root-move-the-roots-branch-into-a-worktree) |
| `wt start`, `wt stop`, `wt served`, `wt warm` | Start and stop local models | [docs/wt-start-stop.md](docs/wt-start-stop.md) |
| `wt smoke` | One-shot prompt through every agent eligible for a model | [docs/wt-smoke.md](docs/wt-smoke.md) |
| `wt model`, `wt config` | Registry and preferences | [docs/wt-model.md](docs/wt-model.md), [docs/wt-config.md](docs/wt-config.md) |
| `wt stats` | Launches and LiteLLM spend per model | [docs/wt-stats.md](docs/wt-stats.md) |
| `wt cloud-sync` | Refresh cloud prices and the ollama catalog | [docs/wt-cloud-sync.md](docs/wt-cloud-sync.md) |

## Configuration

The Go tool uses `~/.config/agent-wt/config.toml` (TOML) for wt-owned state — Agents (AI coding tool with supported providers and optional default) and the default rotation tag, plus the legacy `[launch]` table (its `exit_prompt` key is tolerated and ignored; see [Exit prompt](#exit-prompt-for-an-untouched-worktree)).

Providers (model source with auth config: ollama, openrouter, claude, copilot) and Models (a variant from a provider, grouped by family and tagged, e.g. `code`, `design`) are no longer stored in `config.toml`. They live in `~/.config/local-ai/registry.toml`. `wt model init` creates that file and adds the default provider rows; models are listed, added, edited and removed with `wt model list|add|edit|rm`, or on the Models tab of `wt config` (`wt model`) — see `docs/wt-model.md`. `wt` joins the registry in memory with `config.toml`; nothing writes providers or models into `config.toml`.

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

### Migration from legacy `models.conf`

On first run, `wt` migrates the legacy bash `~/.config/agent-wt/models.conf` into `config.toml` automatically (parsing `CODE_MODELS`/`DESIGN_MODELS` bash arrays, creating `Provider`/`Agent` entries for each `native:X` model, merging models in both code and design rotations). It runs only once — skipped if `config.toml` already exists — and writes the full legacy shape, including Providers/Models, which nothing reads back (the sections are inert); `wt` itself reads Providers/Models back out of the registry, and writes that file through `wt model init`, `wt model add|edit|rm`, the Models tab of `wt config` and `wt cloud-sync`.

## User preferences (`wt config`)

Bare `wt config` opens an interactive editor with two tabs: Agents (the agents in `config.toml`) and Models (the models in `registry.toml`; `wt model` opens the editor on this tab). Its subcommands are `wt config path`, which prints the config directory, and `wt config theme` — four built-in color palettes with dark/light variants, stored in `~/.config/agent-wt/themes.toml`.

```bash
wt config theme              # show the active theme + available names
wt config theme list         # list all built-in themes
wt config theme show         # show the active theme's tokens
wt config theme show solarized
wt config theme set tokyo-night    # effective on next wt launch
wt config theme unset              # revert to default
wt config path               # print the config directory
```

Themes apply to the TUI picker, CLI tables, and `wt config theme list` output. See `docs/wt-config.md` for the full reference, including the `themes.toml` file format and the full list of built-in palettes.

## Model rotation

Works for `claude-wt`, `codex-wt`, `copilot-wt`, `opencode-wt`, and `pi-wt`.

The `internal/rotation` package implements tag-based model rotation. Any tag can be a rotation group, not just `code` and `design`. Rotation is implicit: every picker entry lands the cursor on the model after the one most recently launched, and pressing Enter records the launch. There is no `r` key — the user navigates with up/down and the rotation advances as a side effect of launching.

- State is persisted to `~/.config/agent-wt/rotation.state` (single global slot: the model ID of the last single-model launch)
- `~/.config/agent-wt/last-picked.state` holds the row the picker's `> ` marker sits on. Every launch writes it, a slot-profile launch included, which leaves `rotation.state` alone
- Legacy per-tag `rotation-<tag>.state` files are one-shot migration inputs; they are merged into the global slot on first run and then deleted
- The `wt rotate <tag>` subcommand prints the model after the last-launched one (read-only debug helper)

```bash
wt rotate code    # print the model after the last launch within the "code" tag group
```

### Ollama availability check

Before launching with an Ollama model, `wt` verifies the model is locally available via `ollama list`, asked of the ollama address in `registry.toml` (not the shell's `OLLAMA_HOST`). In the TUI, if the model is missing, you can proceed anyway or cancel; use `up`/`down` to pick a different model. When the launch runs without the TUI (`-W`/`--worktree` or `--cwd`, with `-A` and a model that resolves without the picker), a missing model causes an error with a pull suggestion.

## Copilot-specific: Ollama passthrough

`copilot-wt` does not pass `--model` for Ollama models. Instead it sets environment variables for the OpenAI-compatible API:

```bash
COPILOT_PROVIDER_BASE_URL=http://localhost:11434/v1
COPILOT_PROVIDER_API_KEY=
COPILOT_PROVIDER_WIRE_API=completions
COPILOT_MODEL=<model-name>
```

The base URL is the resolved route's origin plus `/v1`. On a direct route that origin is the ollama provider row's `auth.base_url` in `registry.toml` (a row with no `base_url` is an error, not a default); when the launch goes through LiteLLM it is the `[litellm]` url, and `COPILOT_MODEL` is the registry id. `WIRE_API=completions` (chat-completions) is deliberate — copilot's `responses` wire drops leading characters through the OpenAI-compatible bridge; see [docs/wt-agents/copilot-wt.md](docs/wt-agents/copilot-wt.md).

## Two sessions in one checkout

wt notes each launch in a repository's root checkout (not a linked worktree). If another wt session is already running in that root, the launch warnings name it (agent, pid, how long, branch) and, on a terminal, ask one default-yes question before proceeding — the picker's launches ask the same one question at the commit point, just before the agent runs. Launching in a linked worktree (`-W <name>` or a worktree row) gets no root or held warning (other warnings can still ask once); `-W` naming the root's branch launches in the root with the root warning. The picker's root row, `--root` and `--cwd` name the root directly. A launch in a subdirectory of the root is judged as the root checkout for claims, so it carries that checkout's hold warning and the warnings its own directory earns (a drifted root, a default branch) — never the root warning, since only the root's own top level is the root — and writes no claim of its own. A linked worktree is the only launch judged strictly on its own directory: no root and no held warning. With no terminal the warnings print and the launch proceeds; a no at the question ends the launch quietly. Claims are small files under `<git-common-dir>/wt-sessions/`, removed when the session ends; if they cannot be read or written, wt prints a note and launches anyway.

## Main guard

Launchers auto-install a `block-main-commit` pre-commit hook on every invocation when inside a git repo, and `wt --init` installs it too. A first-time install prints one line on stderr — `wt: installed the block-main-commit hook (--no-guard to skip)` — on every install path; a hook already present is left alone and prints nothing. It blocks commits to `main`/`master` unless bypassed:

- `git commit --no-verify` (emergency)
- `WT_SKIP_MAIN_BLOCK=1` env var (CI/automation)
- `<launcher> --no-guard` (removes the hook)

## Sessions are the agent's own

`wt` never resumes a session: every launch starts the agent fresh, with no lookup, no prompt and no resume flag. To continue a conversation, pass the agent's own flags after `--`; `wt` hands them over unchanged on every launch path, the picker included.

| | Continue the latest | A specific session | Fork it |
|---|---|---|---|
| claude | `claude-wt -- --continue` | `claude-wt -- --resume <id>` | `claude-wt -- --continue --fork-session` |
| opencode | `opencode-wt -- --continue` | `opencode-wt -- --session <id>` | `opencode-wt -- --continue --fork` |

claude's `--continue` means the most recent conversation in the current directory, and `wt` starts the agent in the worktree (or, with `--cwd`, the directory the command was typed in), so it finds the sessions for that directory.

`wt` used to resume the newest session by itself. That appended a one-shot run (`-- -p "..."`) to whichever conversation was newest, including one another process was using (#204), and made an opencode launch fail when the resumed session had stored a different model (#198). Whether a resumed session's stored model overrides the chosen one is the agent's behaviour; `wt` does not guard against it, and native models are not special-cased.

## Exit prompt for an untouched worktree

There is no exit prompt: when the agent exits, `wt` asks nothing about the worktree, and cleanup is user-driven — `wt cleanup` / `wt worktrees` ([docs/wt-cleanup.md](docs/wt-cleanup.md)) lists what can go and `wt remove` removes named targets. The `[launch] exit_prompt` key in `~/.config/agent-wt/config.toml` (the old prompt's off-switch) is tolerated and ignored: any value parses and survives wt's saves, nothing validates or consults it, and a non-empty value prints one note per process — `wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored`. The tail never changes the agent's exit status. See [docs/wt-agents/README.md](docs/wt-agents/README.md#post-exit-order).

## Post-run summary line

After every agent or command subprocess exits, `wt` prints a single line to stdout summarizing the run:

```
wt: <agent> · <model-id> · <duration>
```

The line is emitted on both success and non-zero exit and never affects the exit code. Model agents show the full `<provider>/<name>` model ID; command agents (`shell`) omit the model segment. Durations round to seconds when ≥1s, otherwise they are shown in milliseconds (e.g. `850ms`). See [docs/wt-agents/README.md](docs/wt-agents/README.md#post-run-summary-line) for details and the rationale.

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
| `cmd/wt/app.go` | Shared dependency struct: loads and validates config, the active theme and `profiles.toml` once (no model discovery) |
| `cmd/wt/commands.go` | Subcommand constructors: `rotate` (debug helper) |
| `cmd/wt/helpers.go` | Centralized helpers: `mustGetString`, `yolo`, `borderStyle`, `renderTable`, guard helpers (`checkGuardStatus`, `removeGuard`), `isStdinTTY`, `configError`, `pickerNeedsTTYError` |
| `cmd/wt/launch.go` | Non-TUI launch helpers: `buildFilteredCmd`, `launchFiltered`, `launchPassthroughImpl`, `runAgentCmd` |
| `internal/config/` | Config loading, model registry types (joined from `registry.toml`), validation, secrets, legacy migration |
| `internal/rotation/` | Tag-based model rotation with snapshot-based model set and persistent state |
| `internal/agents/` | Agent driver abstraction — builds per-agent launch commands |
| `internal/guard/` | Main guard — installs/removes `block-main-commit` pre-commit hook |
| `internal/worktree/` | Git worktree and branch enumeration (picker data source) and creation (EnsureForName/EnsureForBranch) |
| `internal/initseed/` | `--init` seeding: AGENTS.md + agent pointer files, skip-if-exists |
| `internal/tui/` | Bubble Tea app shell + worktree picker + agent/model screen + model browser + launch |
| `internal/ollamacheck/` | Ollama model availability check before launch |
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
- `cmd/wt/` — unified Go tool: cobra CLI, Bubble Tea TUI, model registry, rotation, guard, init seeding

See `CLAUDE.md` for the full architecture documentation.