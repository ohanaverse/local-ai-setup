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
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
```

Requires Go 1.26.3 (see `go.mod`).

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`). A drifted linker signature is rejected by AMFI with "Taskgated Invalid Signature" and SIGKILLs the binary (exit 137).

```bash
make build        # build to bin/wt (re-seals macOS signature)
make install      # install to ~/.local/bin/
make uninstall    # remove installed scripts from ~/.local/bin/
make lint         # shellcheck on bin/*-wt
make format       # shfmt -w on bin/*-wt
make format-check # shfmt -d (CI check)
make check        # lint + format-check
make test         # smoke tests (requires make install first)
make clean        # remove build artifacts
```

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

Legacy `-w` is removed — use `-W`. Legacy bash `--code`/`--design`/`--native` are unsupported; rotation is slot-based (see [Rotation (Go)](#rotation-go)), the main guard is `internal/guard`.

## Passthrough args

Extra args forward to the launched agent. `--` is only required when the command starts with a flag-like token, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Adding a new agent

1. Add a driver in `internal/agents/<name>.go` implementing `Build`/`YoloFlag`, registered via `register("<name>", ...)`.
2. Add a shim `bin/<name>-wt`: `exec wt --agent <name> "$@"`.
3. Add a doc to `docs/wt-agents/`.

## Docs

- `docs/configuration.md` — Claude Code / Codex CLI config
- `docs/wt-config.md` — `wt config` subcommands
- `docs/wt-agents/` — per-agent reference (one file per launcher)

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go vet ./...                         # static analysis
```

Coverage by package:

| Package | Tests | Focus |
|---|---|---|
| `internal/config` | 55 | load/validate/save, migration, secrets, EligibleModels |
| `internal/registry` | 15 | merge, ollama/OpenRouter parsing, filter |
| `internal/rotation` | 17 | global last-launched model + picker `Next`/`FirstAfter` |
| `internal/usage` | 9 | JSONL launch history with 1d/7d/30d counts |
| `internal/agents` | 41 | per-agent Build, BuildLaunchCmd, shell quoting |
| `internal/guard` | 11 | install/uninstall idempotency, foreign-hook preservation |
| `internal/worktree` | 34 | enumeration, creation, default-branch refusal |
| `internal/initseed` | 7 | AGENTS.md seeding, idempotency |
| `internal/session` | 7 | claude/opencode session detection |
| `internal/themes` | 24 | builtins, themes.toml load/save/unset |
| `internal/tui` | 142 | picker screens, theming, launch/resume |
| `internal/ollamaconfig` | 33 | union sync, edit/resolve screens |
| `internal/configeditor` | 48 | viewer/editor TUI, validation, FK-blocked deletes |
| `internal/ollamacheck` | 3 | availability before launch |
| `cmd/wt` | 43 | flag wiring, resolveModel, launchFiltered, `wt config` |

## Go module

Module root is the repo root.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config once) |
| `cmd/wt/commands.go` | hidden `rotate` subcommand |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable` |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `buildLaunch`, `launchFiltered` (all take `extraArgs`) |
| `internal/config/` | config load/validate/save, migration; helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
| `internal/registry/` | live discovery (Ollama CLI, OpenRouter API) + merge |
| `internal/rotation/` | global rotation state + `Next` for the picker (replaces the per-slot model) |
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model-picker badges, and the rotation module |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | enumeration + creation (`EnsureForName`/`EnsureForBranch`) |
| `internal/initseed/` | `--init` seeding |
| `internal/session/` | resume detection (claude/opencode) |
| `internal/ollamaconfig/` | `wt config ollama` TUI |
| `internal/ollamacheck/` | availability check before launch |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell + pickers + launch/resume |
| `testdata/` | sample configs |
| `docs/superpowers/` | specs + plans |

## Config (Go)

`~/.config/agent-wt/config.toml` (TOML) with three entity types:

- **Provider** — model source (ollama, agy, claude, codex, copilot). Native providers match their agent name; `opencode` and `pi` are ollama-only (no native provider).
- **Model** — a variant from a provider, grouped by family. `Source` is `curated` (config file) or `discovered` (live).
- **Agent** — tool with ≥1 supported provider and optional default.

Key helpers: `Dir()` (config dir), `WriteFileAtomic` (atomic save), `OllamaBaseURL` (`http://localhost:11434`), `FirstTag(s, fallback)`.

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

## Live discovery

`internal/registry` queries connected providers at runtime and merges with curated config (curated wins on ID collisions):

- **Ollama** — `ollama list` (local + cloud models).
- **OpenRouter** — `https://openrouter.ai/api/v1/models` (HTTP).

**Lazy:** `newApp()` only loads config. Full `registry.Discover` (incl. the OpenRouter HTTP call) runs on demand. The TUI picker sources models only from `config.toml` (no live discovery), so flag-only and TUI paths never hit OpenRouter. `--version`/`--init` never shell out to ollama; `-W`/`--cwd` run one `ollama list` via `ollamacheck.Available()`.

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
wt config ollama             # sync config.toml ollama models with `ollama list`
```

> **Invalid-config repair.** `wt config` launches even when `config.toml` fails validation, so the editor can repair it. Other launch paths exit early on config errors.

## Rotation (Go)

Global rotation — the Go equivalent of bash `--code`/`--design`. Each successful launch records a single model id; the next picker entry lands on the model *after* it. Per-slot rotation (`Slot{Agent,Tag,Family}`) was retired; only the global state file remains.

- Public API (package `rotation`):
  - `Rotation` (struct) — `New()` / `NewAt(dir)` constructors; `Last() (string, bool)`, `Record(modelID string) error`, `Next(cfg, agent, tags, family) (config.Model, bool)`, `StateDir() string`.
  - Package-level `FirstAfter(models []config.Model, target config.Model) (config.Model, bool)` — shared by the picker and `wt rotate`.
- State file: `~/.config/agent-wt/rotation.state` (atomic write, owns one model-id-per-line).
- Usage history (1d/7d/30d per-model counts) lives at `~/.config/agent-wt/usage.jsonl` (JSONL, appended by `usage.Store.Record`, consumed by the model's picker footer — see `internal/usage`).

```bash
go run ./cmd/wt rotate code    # debug helper: print the model after the last-launched in the "code" tag group
```

> **Migration from per-slot rotation.** On first load after upgrading, if `rotation.state` is absent and any legacy `rotation-<agent>-<tag>-<family>.state` or `rotation-<tag>.state` files exist, `Rotation.migrate` imports only the **single newest-mtime** entry and then deletes every legacy file. Distinct per-slot histories (e.g. `claude-code` vs. `claude-design`) are reduced to one global value — there is no in-repo back-compat layer for multi-slot users. Back up `~/.config/agent-wt/rotation-*.state` before upgrading if that matters.

## Agents (Go)

Each agent registers a `Driver` (`Build(m config.Model, yolo bool) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` is the shared constructor (both TUI and non-TUI). `Syncer` (pi) is an optional pre-launch step; `ArgSetter` (shell) consumes passthrough args.

**Model id contract.** Drivers hand the **bare provider name** (`m.ModelName`) to the agent CLI — never the registry key (`m.ID`, which carries the `provider/` prefix). OpenCode is the exception: its CLI needs `provider/model`, so it constructs `"ollama/" + m.ModelName` deliberately. Passing `m.ID` would double-prefix (`ollama/ollama/<model>`) or forward `ollama/<model>` to the gateway. Regression tests (`TestClaudeOllamaPrefix`, `TestOpenCodeOllamaPrefix`, …) use a model with distinct `ID`/`ModelName` so this bug can't reappear silently.

| Agent | Launch behavior |
|---|---|
| claude | `ANTHROPIC_*` env → ollama gateway + `--model <m.ModelName>`; native: no args |
| codex | `--model <m.ModelName>` plus four inline `-c` overrides (`model_provider=agent-wt`, `model_providers.agent-wt.{name,base_url,wire_api}`) for ollama-routed models; native: no args (see `docs/wt-agents/codex-wt.md`) |
| copilot | `COPILOT_PROVIDER_BASE_URL`/`API_KEY`/`WIRE_API`/`COPILOT_MODEL` env; never `--model`. **Ollama-routed models must use the OpenAI-compatible endpoint `http://localhost:11434/v1` with `WIRE_API=responses`, matching `ollama launch copilot`**. Native models clear all `COPILOT_*` provider env vars. |
| opencode | ollama-only; `OPENCODE_CONFIG_CONTENT` inline JSON with `ollama/<m.ModelName>`; never `--model` |
| pi | syncs models to `~/.pi/agent/models.json` (`_launch: true`); passes `--model` only when present; no yolo |
| agy | no model passthrough (chosen in its TUI) |
| shell | execs passthrough args as argv, or interactive `bash`; no model/yolo/resume; `ArgSetter` |

## Guard (Go)

`internal/guard` manages the `block-main-commit` pre-commit hook (embedded via `//go:embed`). `Check`/`Install`/`Uninstall`; `Install` is idempotent and appends rather than overwrites. Uses `git rev-parse --git-common-dir` so the hook applies to all worktrees.

## Init seeding (Go)

`wt --init` seeds `AGENTS.md` (and a pointer file: claude → `CLAUDE.md` `@AGENTS.md`, copilot → `.github/copilot-instructions.md`). Existing files are never overwritten (`Result.Skipped`). Handled in `cmd/wt/main.go` before any agent-binary requirement, so it works with no agent installed.

## Session resume (Go)

`internal/session` finds the newest resumable session (claude `*.jsonl` under `~/.claude/projects/<slug>`, opencode `*.json` under `~/.local/share/opencode/storage/session/<project-id>`). Others (incl. shell) return nil.

On Enter in the TUI, a prior session offers Start fresh (default) / Cancel / Resume (`--resume <id>` claude, `--session <id>` opencode). Non-TUI does the same without prompting.

> **Native models never resume.** A native model (e.g. `claude/native`) launches with no model override; resuming would restore the session's stored model and silently override "native" (routing a gateway model at the real Anthropic API). Both paths skip the session lookup for native models.

## TUI (Go)

`internal/tui` is the Bubble Tea shell (`tea.WithAltScreen()`). Phases: worktree picker → agent+command picker → model picker → resume prompt → launch. Each picker is skipped only when its selection is already resolved: `prePath` (from `-W`/`--cwd`/outside-repo) skips the worktree picker, `-A` skips the agent+command picker, and `-M` skips the model picker (a pinned model is validated against the agent's eligible list once the agent is resolved). Every picker uses `ThemedListDelegate` (active color theme) — production code never uses `list.NewDefaultDelegate`.

> **TTY required.** `WithAltScreen` opens `/dev/tty`; from a pipe/CI it fails with `could not open a new TTY`. Flag paths (`--version`, `wt rotate`) skip the TUI. `-W`/`--cwd` need a TTY only when `-A` or `-M` is omitted (command agents like `shell` launch directly with no model layer).

## Worktree (Go)

`internal/worktree` handles enumeration (`Enumerate` → worktrees / local branches / remote-only branches) and creation (`EnsureForName` for `-W`, `EnsureForBranch` for the picker). Every function takes `dir` (repo root) first for testability.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it, and the picker skips bare default-branch rows.

## Migration (Go)

On first run, `wt` migrates legacy `~/.config/agent-wt/models.conf` → `config.toml` (parses `CODE_MODELS`/`DESIGN_MODELS`, skips comments, seeds native providers, merges tag unions). Runs once. `migrateConfigSchema` runs on every `Load()`: renames `google`→`agy`, ensures agy entries, removes `opencode` native provider, no-op if current.

## Smoke test

```bash
go test ./...        # before any commit
go vet ./...         # static analysis
go build ./...       # verify compilation
wt                   # interactive TUI (needs TTY)
wt -W my-feature -A claude   # named worktree + launch
wt --cwd -A codex    # current repo root
claude-wt --cwd      # shim forwards to wt
wt --init            # seed agent instruction files
```
