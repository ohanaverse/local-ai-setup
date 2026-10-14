# claude-wt

## Overview

`claude-wt` is the worktree launcher for [Claude Code](https://claude.ai/code), Anthropic's official CLI. Claude Code reads global identity, skills, and session state from `~/.claude/` and tracks OAuth state in `~/.claude.json`. See [README](README.md) for launcher flags and model rotation.

## Installation

```bash
curl -fsSL https://claude.ai/install.sh | bash
```

This installs the `claude` binary. The `claude-wt` file in this repo is a shim that forwards to `wt --agent claude`. Install `claude-wt` itself by copying `bin/claude-wt` (and the rest of `bin/`) into `~/.local/bin/`.

## Configuration files & locations

Claude Code reads from `~/.claude/`. Briefly:

| File / directory | Purpose |
|---|---|
| `~/.claude/CLAUDE.md` | Global persistent instructions for every session. |
| `~/.claude/settings.json` | Permissions, theme, default model. |
| `~/.claude/hooks/` | Pre/post-tool hooks for automation and safety. |
| `~/.claude/skills/` | Reusable workflows (each becomes a slash command). |
| `~/.claude/plugins/` | Installed Claude Code plugins. |
| `~/.claude/sessions/` | Session history. |
| `~/.claude.json` | OAuth sessions, MCP server config, internal caches. |

## Authentication & credentials

Claude Code uses OAuth, with state stored in `~/.claude.json`. The internals of that file are observable but should be treated as opaque — the supported way to (re-)authenticate is `claude /login`, not editing `~/.claude.json` by hand. There is no environment-variable credential pattern equivalent to pi's `$ANTHROPIC_API_KEY`-via-wrapper setup; `claude-wt` does not need a credential wrapper.

## Model selection

Claude Code picks a model from `--model <name>` or, absent that, from `~/.claude/settings.json`'s default. `claude-wt` handles two cases:

- **`claude/native`** — passes no `--model` flag and clears any inherited `ANTHROPIC_*` env vars, so Claude Code uses its native subscription (the model configured in `~/.claude/settings.json`).
- **`claude/<alias>`** (`claude/opus`, `claude/opusplan`, …) — the native subscription too, with `--model <alias>`. Add one with `wt model add claude <alias> --family claude`. `claude/opusplan` runs Opus in plan mode and Sonnet otherwise (checked on 2026-10-14, #391), so outside plan mode it works on Sonnet where a bare `claude/native` launch works on your account's default model.
- **Ollama-routed models** — passes `--model <name>` and points Claude Code at the local Ollama gateway via `ANTHROPIC_*` env vars (see [Cloud models via Ollama](#cloud-models-via-ollama)). It also maps all four of Claude Code's model slots to that model (see [Model slots on a single-model launch](#model-slots-on-a-single-model-launch)).

For Ollama-routed models, the launcher passes the **bare** provider-specific name (`config.Model.ModelName`), not the registry key (`config.Model.ID`, which is `provider/model` form). Using the registry key here would forward `ollama/<model>` to the Ollama gateway, which would not recognize the prefixed id.

### Cloud models via Ollama

When a non-native model (e.g., `minimax-m3:cloud`) is selected, `claude-wt` sets Anthropic-compatible environment variables pointing at the local Ollama gateway:

```bash
ANTHROPIC_AUTH_TOKEN=ollama
ANTHROPIC_API_KEY=""
ANTHROPIC_BASE_URL="http://localhost:11434"
```

The values shown are for the default ollama provider row. The base URL is the origin of the model's registry provider address (`auth.base_url` in `registry.toml` with a trailing `/v1` dropped; `http://localhost:11434` for the default ollama row), and `ANTHROPIC_AUTH_TOKEN` is that provider's resolved `secret_ref`, falling back to the placeholder `ollama` when the provider has no key. This allows Claude Code to use Ollama-hosted models that follow the `:cloud` naming convention.

### LiteLLM routing

When LiteLLM routing is enabled (`wt litellm status`), `claude-wt` routes non-native models through the LiteLLM proxy (URL from the `[litellm]` table in wt's config.toml, typically `http://localhost:4000`) instead of dialing the provider directly. The launcher sets:

```bash
ANTHROPIC_AUTH_TOKEN="<litellm.api_key>"
ANTHROPIC_API_KEY=""
ANTHROPIC_BASE_URL="http://localhost:4000"  # trailing slash is normalized
```

The `--model` value is the full registry model id (e.g. `ollama/qwen3.8:27b-mlx`), not the bare provider-specific name. The API key comes from `[litellm].api_key` in wt's `~/.config/agent-wt/config.toml` and is forwarded to the LiteLLM proxy as the provider's auth token. Native models (`claude/native`, `claude/opus`, etc.) continue to use the native subscription and ignore LiteLLM routing.

claude only speaks the `anthropic` wire protocol, so a provider that doesn't serve it (e.g. openrouter, `openai-chat` only) forces LiteLLM routing regardless of the on/off setting — wt prints a one-line stderr notice when that happens.

### Model slots on a single-model launch

Claude Code has four model slots (`fable`, `opus`, `sonnet`, `haiku`) and
uses them unasked: background requests go to the `haiku` slot, and a built-in
subagent may be defined with a slot alias. An unmapped slot resolves to an
Anthropic id such as `claude-haiku-5-5`, which another endpoint rejects with
`Invalid model name`; Claude Code then falls back to the session model.

So on every single-model launch where wt points Claude Code at another
endpoint (a direct provider or the LiteLLM proxy; never `claude/native` or
another native model), wt sets eight more variables in the process
environment, shown here for a launch of `ollama/qwen3.8:27b-mlx` through
LiteLLM (an example id):

```bash
ANTHROPIC_DEFAULT_FABLE_MODEL="ollama/qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_FABLE_MODEL_NAME="qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_OPUS_MODEL="ollama/qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_OPUS_MODEL_NAME="qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_SONNET_MODEL="ollama/qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_SONNET_MODEL_NAME="qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_HAIKU_MODEL="ollama/qwen3.8:27b-mlx"
ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME="qwen3.8:27b-mlx"
```

- Each `ANTHROPIC_DEFAULT_<SLOT>_MODEL` is exactly the value of `--model`:
  the registry id through LiteLLM, the bare provider-side name on a direct
  route.
- Each `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` is the model's provider-side
  name, the label Claude Code's `/model` picker shows for the slot.

Precedence, lowest first:

1. A value for one of the eight exported in your shell. wt removes it from
   the environment it hands to claude, so it never takes effect.
2. wt's value.
3. A value an applied [automatic profile](profiles.md) sets in its `env`.
   It wins for that variable, and the other seven keep wt's values. This
   holds for a launch from the command line and from the picker.

A profile that overrides one `ANTHROPIC_DEFAULT_<SLOT>_MODEL` should set the
matching `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` too. Otherwise wt's label for
that slot still names the session model, and Claude Code's `/model` list
shows the wrong name.

This precedence rule is for a profile's `env`. A profile that sets one of
these variables under `config_content.env` (written to
`.claude/settings.local.json`) is not decided by wt. Claude Code's own
settings-over-environment rule decides, and wt does not test it.

wt sets nothing else for the slots: no `modelOverrides`, no
`CLAUDE_CODE_SUBAGENT_MODEL`, and it leaves every other inherited variable
alone (`ANTHROPIC_SMALL_FAST_MODEL` included). The native launch gets none
of the eight and keeps whatever your shell exports.

### Slot profiles

A single-model launch sends all four slots to the one model it names. A slot
profile gives each slot (`fable`, `opus`, `sonnet`, `haiku`) its own
registry model in one launch, through the LiteLLM proxy:

```bash
claude-wt -M profile/<name>     # or pick the profile/<name> row in the model picker
```

wt then passes `--model <default slot>` and one inline `--settings` JSON
holding `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for every slot. It writes no
settings file. The file format, the picker row and every failure are in
[slot-profiles.md](slot-profiles.md).

## Sessions

`wt` leaves sessions to Claude Code: every launch starts fresh, and `wt` never looks one up, prompts about one, or adds a resume flag. To continue a conversation, pass claude's own flags after `--`; `wt` hands them over unchanged on every launch path, the picker included.

| Continue the latest | A specific session | Fork it |
|---|---|---|
| `claude-wt -- --continue` | `claude-wt -- --resume <id>` | `claude-wt -- --continue --fork-session` |

`--continue` means the most recent conversation in the current directory, and `wt` starts claude in the worktree (or, with `--cwd`, the directory the command was typed in), so it finds the sessions for that directory. Claude Code keeps them under `~/.claude/projects/<slug>/`, where `<slug>` is that directory's path with non-alphanumeric chars replaced by `-`.

`wt` used to resume the newest session by itself. That appended a one-shot run (`-- -p "..."`) to whichever conversation was newest, including one another process was using (#204), and — for opencode — failed a launch whose resumed session had stored a different model (#198). Whether a resumed session's stored model overrides the chosen one is claude's behaviour; `wt` does not guard against it.

## Agent init

`claude-wt --init` seeds project-level instruction files:

- `AGENTS.md` — shared instruction template (if missing)
- `CLAUDE.md` — pointer containing `@AGENTS.md` (if missing)

Claude natively supports `@path` imports in `CLAUDE.md`. When Claude reads `CLAUDE.md`, it automatically expands the `@AGENTS.md` import. Users can add Claude-specific instructions below the import.

## Verified on this machine

**Verified on this machine, 2026-06-01.**

- **Binary:** `~/.local/bin/claude`.
- **Home directory:** `~/.claude/` exists with `CLAUDE.md`, `settings.json`, `hooks/`, `skills/`, `plugins/`, `sessions/`, and additional subdirectories (`backups/`, `bin/`, `cache/`, `debug/`, `docs/`, `downloads/`, `file-history/`, `history.jsonl`, `ide/`, `memory/`, `plans/`, `projects/`, `security/`, `session-env/`).
- **OAuth state:** `~/.claude.json` exists. Internals are an OAuth-token cache; treat as opaque.
- **No credential wrapper.** `claude-wt`'s `exec claude …` works directly against the installed `claude` binary; no equivalent to pi's `activate-litellm.sh` is needed in this deployment.
