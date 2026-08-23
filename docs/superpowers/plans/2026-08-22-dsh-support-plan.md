# Implement DeepSeek Harness support (three agents)

Prereq: review `docs/superpowers/specs/2026-08-22-deepseek-harness-design.md`.

## Context

wt launches coding agents through `internal/agents`, and wt already has three
run modes for the same `dsh` binary — **web** (browser TUI, default),
**terminal**, and **headless** (non-interactive one-shot, task string as
positional arg, writes final answer to stdout, exits). `dsh` runs only through
the ollama provider via `ollama launch dsh --model <name>`. Per the design:

- Register three agents, one per mode, each in `internal/agents/deepseek/`,
  with a `dsh-` prefix: `dsh-webui` (browser, default), `dsh-tui` (terminal),
  `dsh-headless`.
- `dsh-headless` and `dsh-tui` are `IsCommand()` agents (their run command is
  fixed by the profile; no `--model` flag, no yolo flag).
- `dsh-webui` is model-driven: `ollama launch dsh --model <m.ModelName>` and it
  supports yolo plus `--web-search` (opt-in, off by default).
- Web search via the `--web-search <bool>` flag (ollama cloud access, optional
  `DEEPSEEK_SEARCH_BASE_URL` override). Off by default. Not yet wired.
- All three share the `dsh` binary path; yolo and `--web-search` are optional.

Per the design, config entries live in `~/.config/agent-wt/config.toml` under
`[agents]`. wt's config schema for agents already supports this with no schema
change; users configure the agents via `wt config` TUI (or edit `config.toml`).

Three agent registrations + tests.

## Plan

### 1. Three driver files in `internal/agents/deepseek/`

Create `internal/agents/deepseek/` with one file per mode. Keep the driver
types as simple marker structs with a `Run()` method that builds the command,
mirroring the shell/codex/opencode drivers.

- `internal/agents/deepseek/deepseek.go`
  - `const binary = "ollama"`
  - `func buildBase(extraArgs []string) *exec.Cmd` — returns
    `ollama launch dsh <profile> --model <m.ModelName> -- <extraArgs>`, where
    `<profile>` is `dsh-webui`/`dsh-tui`/`dsh-headless`; the model arg is
    passed only for `dsh-webui`.
  - `func (d *dshWebuiDriver) Run(cmd *exec.Cmd, extraArgs []string, yolo bool, webSearchEnabled bool) *exec.Cmd` — builds `ollama launch dsh --model <m.ModelName> <args> -- <extraArgs>`; appends `--profile dsh-webui`; prepends `--yolo` when `dshWebuiIsYoloEnabled`; prepends `--web-search true` when `webSearchEnabled`.
  - Similar `Run(...)` for the terminal and headless drivers.

Commit: **three driver files**.

- `internal/agents/deepseek/deepseek_test.go`
  - Per-mode args: assert the correct `dsh-` profile flag per driver.
  - Bare-model: assert `dsh-webui` uses the bare model name (never the full
    provider/model id).
  - Per-env flag: assert a per-env flag on `dsh-webui` (and maybe a comment)
    when web search is enabled.
  - Web search toggle: assert `--web-search <bool>` on the launch command when
    opted in (`dsh-webui-web-search`); no arg otherwise.
  - Passthrough: assert user args land after the model flag (dsh-webui).
  - Headless round-trip: headless writes the final answer to stdout and exits 0
    (success), exits 1/non-zero for other outcomes (failure).
  - No resume: assert no `--resume`/`--session` is present for any agent.

Commit: **per-drive file tests**.

### 2. Register the agents

In `internal/agents/agents.go` (or existing registration), register the three
drivers so wt discovers them via `agents.Names()` (wt's agent list is purely
`agents.Names()` + the config's `[agents]`, so no allowlist change needed).
Add the three agents to wt's "no model layer" handling (`resolveModel`
`agents.IsCommand(agent)` check + `internal/tui/app.go` IsCommand check) so wt
treats them as commands — no model picker, no rotation.

Commit: **package registration**.

### 3. Docs

Update `docs/wt-agents/`:
- List the three agents with a short description and link.
- Per-mode pages describe: how to install `dsh`, select browser/headless/etc.,
  model selection, web search (off by default), and resume.
- `dsh-headless` documents its non-interactive behavior (prints final answer to
  stdout, task string as positional arg); other pages describe browser web app
  / terminal TUI + `:3080` and where the model picker lives.

Commit: **docs**.

### 4. Testing & verification

- `go build ./...` then `go test ./...` (all green).
- `go vet ./...`.
- `make lint` (shellcheck on `bin/*-wt`).

> [!NOTE]
> Config entries and docs live out of tree (the `wt config` TUI or a manual
> `config.toml` edit). Nothing here changes wt's on-disk config schema.

---

### Verification steps

After the implementation, confirm:

- `go build ./...` and `go test ./...` are green.
- `go vet ./...` clean.
- `make lint` passes (shellcheck on `bin/*-wt`).
