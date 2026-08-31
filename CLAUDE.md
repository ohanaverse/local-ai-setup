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

Requires Go 1.26.7 (see `go.mod`).

> **macOS codesign.** `make build`/`install` re-seal the ad-hoc signature (`codesign --force --sign -`). A drifted linker signature is rejected by AMFI with "Taskgated Invalid Signature" and SIGKILLs the binary (exit 137).

Run `make help` for the full target list. `make test` requires `make install` first — it exercises the installed binary, not just the build.

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

Legacy bash `--code`/`--design`/`--native` are unsupported; rotation is global, not slot-based (see [Rotation (Go)](#rotation-go)), the main guard is `internal/guard`.

## Passthrough args

Extra args forward to the launched agent. `--` is only required when the command starts with a flag-like token, since the root `wt` uses `cobra.ArbitraryArgs`.

```bash
claude-wt -W feat -- --verbose  # → claude --model X --verbose
shell-wt -W test -- npm test    # → exec npm test in .worktrees/test
```

For agents, args append to the command; for `shell` (implements `ArgSetter`), they become argv directly (`d.args[0]` = binary).

## Post-run summary line

After the launched subprocess exits, both the TUI and non-TUI paths print a single `wt: <agent> · <model-id> · <duration>` line to stdout (model segment omitted for command agents like `shell`). Emitted on success and non-zero exit; never affects the exit code. The formatter lives in `internal/agents.Summary` and is the single source of truth for both paths. See `docs/wt-agents/README.md#post-run-summary-line`.

## Docs

- `docs/configuration.md` — Claude Code / Codex CLI config
- `docs/wt-config.md` — `wt config` subcommands
- `docs/wt-agents/` — per-agent reference (one file per launcher)
- `docs/superpowers/specs/` — design specs (input to implementation)
- `docs/superpowers/plans/` — implementation plans (output of planning, input to execution)

## Go tests

Every `Test*` has a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression).

**Test seams.** TTY, installed-check, guard, and TUI behavior are stubbed via
package-level var seams (`tuiRun`, `launchFiltered`, `stdinTTY`, `installed`,
`maybeInstallGuard`) — production code calls the var, tests swap it. When adding
a new seam, follow the same shape: a `var x = realX` plus a `realX` function.

```bash
go test ./...                        # all Go tests
go test ./internal/worktree -v       # verbose, one package
go test ./internal/agents -run TestOpenCodeOllamaPrefix -v   # one test
go vet ./...                         # static analysis
make help                            # list Makefile targets
```

Key `make` targets: `build` (compile), `install` (compile + re-seal codesign + place on `$PATH`), `test` (requires `install` — exercises the installed binary), `dev` (dev deps).

Package list: `internal/{config,rotation,usage,agents,guard,worktree,initseed,session,themes,tui,configeditor,ollamacheck}`, `cmd/wt`. Run `grep -c '^func Test' <pkg>/*_test.go` for current counts — each test's focus is documented in its own `//` comment (see above).

## Go module

Module root is the repo root.

| Path | Purpose |
|---|---|
| `cmd/wt/main.go` | CLI entry point (cobra), exit-code handling |
| `cmd/wt/app.go` | shared dependency struct (loads/validates config once) |
| `cmd/wt/commands.go` | hidden `rotate` subcommand |
| `cmd/wt/commands_config.go` | `wt config` subcommand family |
| `cmd/wt/resolve.go` | `resolveModel` — single model for non-TUI launch |
| `cmd/wt/helpers.go` | `mustGetString`, `yolo`, `renderTable`; guard helpers (`maybeInstallGuard`, `checkGuardStatus`, `removeGuard`); TTY seams (`isStdinTTY`/`stdinTTY`) and picker-TTY errors |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `buildLaunch`, `launchFiltered` (all take `extraArgs`) |
| `internal/config/` | config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`) |
| `internal/rotation/` | global rotation state + `Next` for the picker (replaces the per-slot model) |
| `internal/usage/` | append-only JSONL launch history with 1d/7d/30d counts, shared by `wt rotate`, the model-picker badges, and the rotation module |
| `internal/agents/` | driver abstraction (`BuildLaunchCmd`, `ArgSetter`); picker catalog (`ListEntries`, `IssueFor`, `IsCommand`, `ByName`, `Names`, `Installed`); drivers: claude, codex, copilot, opencode, pi, agy, shell |
| `internal/guard/` | `block-main-commit` pre-commit hook |
| `internal/worktree/` | repo detection (`IsRepo`, `RepoRootAt`, `RepoRoot`), enumeration (`Enumerate`), creation (`EnsureForName`/`EnsureForBranch`) |
| `internal/initseed/` | `--init` seeding |
| `internal/session/` | resume detection (claude/opencode) |
| `internal/ollamacheck/` | availability check before launch |
| `internal/themes/` | color themes (4 palettes, `themes.toml`) |
| `internal/tui/` | Bubble Tea shell + pickers + launch/resume |
| `testdata/` | sample configs |
| `docs/superpowers/` | specs + plans |

## Config (Go)

`~/.config/agent-wt/config.toml` (TOML) is wt-owned, but contains **only agents, preferences, and the optional `[gateway]` section** — Providers/Models live in the registry (below). wt never writes providers/models to this file.

Fields:

- **Agent** — tool with ≥1 supported provider and optional default.
- **DefaultTag** — the default rotation tag group.
- **`[gateway]`** — optional routing section. `mode` is `"direct"` (default) or `"litellm"`; `url` + `api_key` are required when `mode = "litellm"` (fail-fast validation). In litellm mode non-native models route through the LiteLLM proxy's OpenAI-compatible endpoint instead of local ollama. `GatewayConfig.BaseURL()` trims trailing slashes so drivers append `/v1` (or none for claude) cleanly. The proxy loads `~/.config/litellm/config.yaml` only at startup — modelman (the writer of that file) restarts it after expose changes via `MODELMAN_LITELLM_RESTART_CMD`; wt never restarts the proxy (see `docs/wt-agents/README.md#litellm-proxy-lifecycle`).

Key helpers: `Dir()` (config dir), `WriteFileAtomic` (atomic save), `OllamaBaseURL` (`http://localhost:11434`), `FirstTag(s, fallback)`.

See `docs/superpowers/specs/2026-08-14-model-registry-data-model-design.md` for the full data model.

## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models.
The location is resolved with the same precedence as modelman's
`_default_registry_path`: `MODELMAN_REGISTRY` > `XDG_CONFIG_HOME` >
`~/.config` — keep the two in sync when changing either side. A
`MODELMAN_REGISTRY` value starting with `~/` (or exactly `~`) is expanded
via `expandHome`, matching Python's `Path.expanduser()` used by modelman's
resolver — Go/the OS never expand `~` on their own, so this parity is
deliberate, not incidental.
wt loads it read-only via `config.Load` (fail-closed: missing/malformed
registry is an error; seed with `modelman migrate`) and joins it in memory
with its own `config.toml`, which now holds only Agents + DefaultTag + gateway. `Save`
persists wt-owned fields only — wt never writes providers/models. Extra
registry fields (cost, model_info, fetch, model_dir, auth secret_ref/base_url)
are ignored by wt's parser.

> **`unknown provider "X"` errors are usually a registry data gap, not a wt
> bug** — e.g. a `registry.toml` with models referencing `provider_id`s but
> `providers = []`. Fix is `modelman sync`/`modelman migrate` on that machine,
> not a code change here. (`modelman sync` now repairs a `providers = []`
> registry by creating default entries for reconcilable providers.)

**Lazy:** `newApp()` only loads config. wt never shells out for discovery;
`-W`/`--cwd` runs one `ollama list` via `ollamacheck.Available()`.

> **Fixture gotcha.** `Dir()` and `RegistryPath()` both honor `XDG_CONFIG_HOME`
> (and `RegistryPath()` also honors `MODELMAN_REGISTRY`) but write to
> *different* subdirs: `config.toml` → `$XDG_CONFIG_HOME/agent-wt/`,
> `registry.toml` → `$XDG_CONFIG_HOME/local-ai/`. Test/smoke fixtures must
> populate both. Also: `migrateConfigSchema` (runs on every `Load`)
> unconditionally ensures an `agy` agent — any registry fixture with agents
> needs a matching `agy` provider or `Load`/`Validate` fails with
> `unknown provider "agy"`.

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

Each agent registers a `Driver` (`Build(m config.Model, yolo bool) LaunchCmd`, `YoloFlag() string`). `BuildLaunchCmd(agent, m, worktreePath, yolo, sess, cfg, extraArgs)` is the shared constructor used by both TUI and non-TUI launch paths — drivers should not bypass it.

**Optional capabilities** (interface checks via type assertion in `BuildLaunchCmd`):

| Capability | Consumer | Drivers that implement it |
|---|---|---|
| `Seeder` | Pre-launch file seeding (AGENTS.md + pointer files) | claude, copilot (only drivers implementing `InstructionPointers()`; others skip seeding entirely) |
| `OllamaURLer` | Per-driver ollama gateway URL (overrides `config.OllamaBaseURL`) | claude, codex, copilot, opencode |
| `Syncer` | Pre-launch sync step (e.g. `pi` syncs models to `~/.pi/agent/models.json`) | pi |
| `ArgSetter` | Consumes passthrough args as argv instead of appending | shell |
| `Resumer` | `ResumeFlag()` + `LatestSession(path)` for resume support | claude, opencode |

Drivers without `Resumer` (codex, copilot, pi, agy, shell) never resume — the session lookup in `internal/session` returns nil for them.

**Model id contract.** In **direct mode** drivers hand the **bare provider name** (`m.ModelName`) to the agent CLI — never the registry key (`m.ID`, which carries the `provider/` prefix). In **litellm mode** the rule inverts: claude, codex, copilot, and opencode pass the **registry id** (`m.ID`, e.g. `ollama/qwen3.8:27b-mlx`), because LiteLLM's `model_list` is keyed on it. OpenCode is also mode-dependent: `ollama/<m.ModelName>` in direct mode, `openai/<m.ID>` in litellm mode. Regression tests (`TestClaudeOllamaPrefix`, `TestOpenCodeOllamaPrefix`, …) use a model with distinct `ID`/`ModelName` so a wrong id can't slip through silently.

| Agent | Launch behavior |
|---|---|
| claude | `ANTHROPIC_*` env → ollama gateway + `--model <m.ModelName>`; native: no args |
| codex | `--model <m.ModelName>` plus four inline `-c` overrides (`model_provider=agent-wt`, `model_providers.agent-wt.{name,base_url,wire_api}`) for ollama-routed models; native: no args (see `docs/wt-agents/codex-wt.md`) |
| copilot | `COPILOT_PROVIDER_BASE_URL`/`API_KEY`/`WIRE_API`/`COPILOT_MODEL` env; never `--model`. **Ollama-routed models must use the OpenAI-compatible endpoint `http://localhost:11434/v1` with `WIRE_API=responses`, matching `ollama launch copilot`**. Native models clear all `COPILOT_*` provider env vars. |
| opencode | ollama-only; `OPENCODE_CONFIG_CONTENT` inline JSON with `ollama/<m.ModelName>`; never `--model` |
| pi | syncs models to `~/.pi/agent/models.json` (`_launch: true`); passes `--model` only when present; no yolo |
| agy | no model passthrough (chosen in its TUI) |
| shell | execs passthrough args as argv, or interactive `bash`; no model/yolo/resume; `ArgSetter` |

In **gateway (litellm) mode** (`[gateway].mode = "litellm"`), non-native models switch from the local ollama gateway to LiteLLM's `/v1`:

| Agent | litellm routing |
|---|---|
| claude | `ANTHROPIC_BASE_URL`=`[gateway].url` (no suffix) + `ANTHROPIC_AUTH_TOKEN`=`[gateway].api_key`, `--model <m.ID>` |
| codex | `--model <m.ID>` plus four `-c` overrides plus `-c model_providers.agent-wt.env_key="AGENT_WT_GATEWAY_API_KEY"` pointing `agent-wt` at `<url>/v1/` (trailing slash), key exported as `AGENT_WT_GATEWAY_API_KEY`. **litellm ≤ 1.98.0 cannot serve codex** (responses→`ollama_chat` bridge crashes on codex's structured `reasoning`); see `docs/wt-agents/litellm-troubleshooting.md` |
| copilot | `COPILOT_PROVIDER_BASE_URL=<url>/v1` + `COPILOT_PROVIDER_API_KEY=<api_key>`, `COPILOT_MODEL=<m.ID>` |
| opencode | `OPENCODE_CONFIG_CONTENT` with wt-declared custom provider `agent-wt` (`@ai-sdk/openai-compatible`) at `<url>/v1`, models map keyed by `m.ID`, model `agent-wt/<m.ID>`, `small_model` pinned the same — the builtin `openai` provider can't serve registry ids (catalog validation + responses-API stream mismatch) |
| pi | syncModels creates a dedicated `litellm` provider (`<url>/v1` + `api_key`) keyed by registry id and launches `--model litellm/<m.ID>`; pi splits `--model` on the first slash, so gateway ids under the `ollama` provider are unreachable — the `ollama` provider stays local (reverted + pruned of wt-generated prefixed entries; empty `apiKey` invalidates pi's whole models.json, reverts write placeholder `"ollama"`) |

The pre-launch `ollamacheck.Check` is **skipped in litellm mode**: a model absent from local `ollama list` is not an error when LiteLLM serves it from a non-local upstream.

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

> **`IsRepo` uses `rev-parse --git-dir`, not `--show-toplevel`.** Bare repos and
> directories inside `.git` have no worktree, so `--show-toplevel` fails there;
> `--git-dir` succeeds. Don't "simplify" `IsRepo` to delegate to `RepoRootAt`.

> **Default branch is never a linked worktree.** main/master may only ever be the primary checkout — both creation functions refuse it, and the picker skips bare default-branch rows.

## Migration (Go)

On first run, `wt` migrates legacy `~/.config/agent-wt/models.conf` → `config.toml` (parses `CODE_MODELS`/`DESIGN_MODELS`, skips comments, seeds native providers, merges tag unions). Runs once, and writes the full legacy shape — including Providers/Models — via `saveFull` so `modelman migrate` can later import them into `registry.toml`; `Load`'s normal path still overwrites in-memory Providers/Models with the joined registry. `migrateConfigSchema` runs on every `Load()`, but only its **agent** fixups still matter (renames `google`→`agy`, ensures agy entries, removes `opencode` native provider) — its provider/model fixups are dead code now that `Load` overwrites those fields from the registry.

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
