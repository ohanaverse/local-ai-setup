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

OpenCode selects models via `--model provider/model` (e.g., `--model anthropic/claude-sonnet-4-5`). `opencode-wt` handles two cases:

- **`opencode/native`** — no `--model` flag and no `OPENCODE_CONFIG_CONTENT`; OpenCode uses its configured default.
- **Ollama models** — generates inline JSON via the `OPENCODE_CONFIG_CONTENT` environment variable:

```json
{"model":"ollama/<model>","provider":{"ollama":{"options":{"baseURL":"http://localhost:11434/v1","apiKey":""}}}}
```

OpenCode is the one agent whose CLI uniquely requires the `provider/model` form, so the launcher constructs the literal `ollama/` prefix from the **bare** provider-specific name (`config.Model.ModelName`), not from `config.Model.ID`. Using `m.ID` here would produce `ollama/ollama/<model>` (a double prefix) because the registry already prefixes IDs with the provider id. This is the symmetric trap to the one in `claude-wt`/`codex-wt`/`copilot-wt`, where the launcher must NOT add a prefix.

The base URL is the `config.OllamaBaseURL` constant (`http://localhost:11434`) with a `/v1` suffix. `OPENCODE_CONFIG_CONTENT` is OpenCode's highest-precedence layer and overrides any conflicting key in `~/.config/opencode/opencode.json` (e.g. `model`, `provider.ollama.options.baseURL`).

## Session resume

`wt` detects a previous OpenCode session (via `internal/session`) and, in the TUI, prompts to **Resume** or **Start fresh**; **Start fresh** is the cursor default so Enter launches a new session unless Resume is highlighted. The non-TUI launch path appends `--session <id>` automatically. Sessions are detected by git commit hash (OpenCode's project ID), not path-based slug.

## Agent init

`opencode-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)

OpenCode reads `AGENTS.md` natively and also has its own `/init` command for project-specific setup. No pointer file is needed.

## Verified on this machine

Verified on this machine, 2026-06-11 — opencode v1.17.3 at `~/.opencode/bin/opencode`. Statements above are sourced from the [OpenCode docs](https://opencode.ai/docs) and the [Ollama integration guide](https://docs.ollama.com/integrations/opencode); session-resume and project-id behavior were verified against the binary's `--help` output and `git rev-list` output on this repo.
