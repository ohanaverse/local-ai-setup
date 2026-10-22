# opencode-wt

## Overview

`opencode-wt` is the worktree launcher for [OpenCode](https://opencode.ai) ([anomalyco/opencode](https://github.com/anomalyco/opencode)), an open-source, provider-agnostic AI coding agent. OpenCode supports 75+ LLM providers and runs as a terminal app, desktop app, or IDE extension. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://opencode.ai/install | bash
```

This installs the `opencode` binary. The `opencode-wt` file in this repo is a shim that forwards to `wt --agent opencode`. Install `opencode-wt` itself by copying `bin/opencode-wt` (and the rest of `bin/`) into `~/.local/bin/`.

Alternative install methods: `npm install -g opencode-ai@latest`, `brew install anomalyco/tap/opencode`, etc.

## Configuration files & locations

OpenCode reads from `~/.config/opencode/`. Key files:

| File / directory | Purpose |
|---|---|
| `~/.config/opencode/opencode.json` | Global config: providers, models, permissions, plugins, agents, tools. |
| `opencode.json` (project root) | Per-project overrides. |
| `~/.local/share/opencode/auth.json` | API keys configured via `/connect` command. |
| `~/.local/share/opencode/storage/` | Sessions, messages, parts, diffs. |

Config format is JSON/JSONC. OpenCode deep-merges config sources: remote → global → custom → project → inline.

## Authentication & credentials

OpenCode stores API keys in `~/.local/share/opencode/auth.json`, configured interactively via the `/connect` command in the TUI. Alternatively, environment variables can be referenced in config:

```json
{
  "provider": {
    "anthropic": {
      "options": {
        "apiKey": "{env:ANTHROPIC_API_KEY}"
      }
    }
  }
}
```

The `opencode-wt` launcher does not manage credentials — it relies on the user having configured providers via `/connect` or environment variables.

## Model selection

OpenCode requires the `provider/model` form in its config (e.g., `anthropic/claude-sonnet-4-5`). `opencode-wt` selects non-native models by generating inline JSON via the `OPENCODE_CONFIG_CONTENT` environment variable. The JSON declares a **custom provider** (`agent-wt`, `npm: "@ai-sdk/openai-compatible"` — chat-completions wire) and points `model` and `small_model` at it. On a direct route (shown for the default ollama provider row):
```json
{"model":"agent-wt/<model>","small_model":"agent-wt/<model>","provider":{"agent-wt":{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":"http://localhost:11434/v1","apiKey":""},"models":{"<model>":{"name":"<model>"}}}}}
```

OpenCode is the one agent whose CLI uniquely requires the `provider/model` form, so the launcher prefixes its own provider id, `agent-wt/`, to the model name. On a direct route that name is the **bare** provider-specific name (`config.Model.ModelName`), not `config.Model.ID`: the registry id already carries the registry provider id (`ollama/<model>`), which the provider's endpoint would not recognize. OpenCode splits a model ref on the first slash, so `agent-wt/<model>` selects the wt provider and the rest reaches the endpoint verbatim.

The base URL is the origin of the model's registry provider address (`auth.base_url` in `registry.toml` with a trailing `/v1` dropped; `http://localhost:11434` for the default ollama row) with a `/v1` suffix, and `apiKey` is that provider's resolved `secret_ref` (empty when it has none). `OPENCODE_CONFIG_CONTENT` is OpenCode's highest-precedence layer and overrides any conflicting key in `~/.config/opencode/opencode.json` (e.g. `model`, `provider.agent-wt.options.baseURL`).

OpenCode's builtin providers resolve model ids against its own catalog (models.dev), so a registry model absent from that catalog — every local/cloud model wt launches — is rejected with `ProviderModelNotFoundError`. A custom provider with an explicit `models` map registers the bare name so OpenCode accepts it. The LiteLLM route (below) uses the same `agent-wt` provider; only the base URL, key and model id differ.

### LiteLLM routing

When LiteLLM routing is enabled (`wt litellm status`), the same `agent-wt` provider is pointed at the proxy's `/v1`, with the full registry id declared in the provider's `models` map and `small_model` pinned to the same proxy model:

```json
{"model":"agent-wt/ollama/<model-id>","small_model":"agent-wt/ollama/<model-id>","provider":{"agent-wt":{"npm":"@ai-sdk/openai-compatible","name":"Agent WT Gateway","options":{"baseURL":"http://localhost:4000/v1","apiKey":"<litellm.api_key>"},"models":{"ollama/<model-id>":{"name":"<bare-name>"}}}}}
```

The builtin `openai` provider is not usable for LiteLLM-routed models: opencode validates model ids against its own catalog ("Model not found"), and the models.dev openai path speaks the responses API, whose bridged stream opencode cannot map ("text part … not found"). opencode splits a model ref on the first slash, so `agent-wt/<id>` selects the wt provider while the registry id stays verbatim inside it. Explicit `models` + `small_model` are required: catalog-unknown ids are rejected, and opencode's default background model (`gpt-5-nano`) otherwise hits the proxy with a name it does not expose. Full rationale: [litellm-troubleshooting.md](litellm-troubleshooting.md).

## Permissions (`--yolo`)

`wt --yolo -A opencode` launches `opencode --auto=true`. opencode describes `--auto` as "auto-approve permissions that are not explicitly denied": it is **not** an unconditional skip — a permission your opencode config sets to `deny` stays denied, and only what would otherwise prompt is approved. (opencode 1.18 has no `--dangerously-skip-permissions`; passing it prints the usage text and exits 1.)

wt attaches the value (`=true`) because `--auto` is declared per opencode command: bare and directly in front of a subcommand, it swallows it — `opencode --auto run …` never reaches `run` and instead starts the TUI with `run` as the project directory. `--auto=true` cannot take the next argument, so the flag leads every form: the interactive launch (`opencode --auto=true [<passthrough args>]`), a passthrough subcommand (`wt --yolo -A opencode -- run "<prompt>"` → `opencode --auto=true run "<prompt>"`), and `wt smoke`'s one-shot (`opencode --auto=true run <prompt>`).

## Sessions

`wt` leaves sessions to OpenCode: every launch starts fresh, and `wt` never looks one up, prompts about one, or adds a resume flag. To continue a conversation, pass opencode's own flags after `--`; `wt` hands them over unchanged on every launch path, the picker included.

| Continue the latest | A specific session | Fork it |
|---|---|---|
| `opencode-wt -- --continue` | `opencode-wt -- --session <id>` | `opencode-wt -- --continue --fork` |

`wt` used to resume the newest session by itself. That made a launch fail when the resumed session had stored a different model (#198), and appended a one-shot run to whichever conversation was newest, including one another process was using (#204). Whether a resumed session's stored model overrides the chosen one is opencode's behaviour; `wt` does not guard against it.

## Agent init

`opencode-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

OpenCode reads `AGENTS.md` natively and also has its own `/init` command for project-specific setup. No pointer file is needed.

## Verified on this machine

Verified on this machine, 2026-09-02 — opencode v1.17.7 at `~/.opencode/bin/opencode`. Statements above are sourced from the [OpenCode docs](https://opencode.ai/docs) and the [Ollama integration guide](https://docs.ollama.com/integrations/opencode). The direct-mode `models` map (catalog bypass) was verified end-to-end with `scripts/agents-smoke.sh --only opencode` in both direct and litellm modes.
