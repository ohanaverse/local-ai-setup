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

- **Native mode** (`mode = "native"` under `[model-server]`, or `WT_MODEL_MODE=native`) — a plain `wt -A claude` (no `-M`, `-T` or `-F`) launches Claude Code natively: no model prompt, no `--model` flag and no `ANTHROPIC_*` gateway variables, so its own configuration decides the model, with one `wt:` line saying so ([wt-session.md](../wt-session.md), "Native launch").
- **`claude/native`** — passes no `--model` flag and clears any inherited `ANTHROPIC_*` env vars, so Claude Code uses its native subscription (the model configured in `~/.claude/settings.json`).
- **`claude/<alias>`** (`claude/opus`, `claude/opusplan`, …) — the native subscription too, with `--model <alias>`. Add one with `wt-model model add claude <alias> --family claude` (on the machine that runs wt-model). `claude/opusplan` runs Opus in plan mode and Sonnet otherwise (checked on 2026-10-14, #391), so outside plan mode it works on Sonnet where a bare `claude/native` launch works on your account's default model.
- **Every other model** — passes `--model <gateway id>` and points Claude Code at the model server's gateway via `ANTHROPIC_*` env vars (see [Through the model server's gateway](#through-the-model-servers-gateway)). It also maps all four of Claude Code's model slots to that model (see [Model slots on a single-model launch](#model-slots-on-a-single-model-launch)).

### Through the model server's gateway

Every non-native model goes through the model server's gateway (`wt-model serve`; see [wt-session.md](../wt-session.md)); there are no direct routes, and wt never points Claude Code at a provider or at LiteLLM itself. The launcher sets:

```bash
ANTHROPIC_AUTH_TOKEN="<gateway token>"
ANTHROPIC_API_KEY=""
ANTHROPIC_BASE_URL="<gateway origin>"  # no /v1; a trailing slash is normalized
```

The `--model` value is the model's gateway id — its full registry id (e.g. `ollama/qwen3.8:27b-mlx`) unless the server names another — never the bare provider-specific name. The token is the model server's client token, which the gateway swaps for the LiteLLM key; no provider key and no `[litellm]` key reaches Claude Code. Native models (`claude/native`, `claude/opus`, etc.) continue to use the native subscription and never see the gateway or its token.

### Model slots on a single-model launch

Claude Code has four model slots (`fable`, `opus`, `sonnet`, `haiku`) and
uses them unasked: background requests go to the `haiku` slot, and a built-in
subagent may be defined with a slot alias. An unmapped slot resolves to an
Anthropic id such as `claude-haiku-5-5`, which another endpoint rejects with
`Invalid model name`; Claude Code then falls back to the session model.

So on every single-model launch where wt points Claude Code at another
endpoint (the model server's gateway; never `claude/native` or another
native model), wt sets eight more variables in the process environment,
shown here for a launch of `ollama/qwen3.8:27b-mlx` (an example id):

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
  the gateway id.
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

### Context window of a gateway model

Claude Code knows the context window of Anthropic's own models only. For any
other id it assumes 200K tokens and prints `Model "…" isn't recognized ·
context assumed to be 200k tokens` at startup. That notice is Claude Code's
own message, not a wt fault.

When the model's entry in the host's `tiers.toml` (modeltiers' output, read
by the model server, which reports it as the row's `context`) has a `context`
**taken from the model's own host row** (`context_basis = "host"`), wt also
sets `CLAUDE_CODE_MAX_CONTEXT_TOKENS` to it, so Claude Code compacts against the real window and the notice goes away.
`tiers.toml` records a window for **cloud** models from models.dev, and none
for a local build, whose served window modeltiers cannot know. When the
model's host has no row of its own, modeltiers still writes the largest
window among the model's rows (the tier gate wants that optimistic read), but
marks it `context_basis = "largest_row"`, and wt does not declare it: no host
serves that window, and Claude Code would compact too late against one the
provider rejects. A `tiers.toml` written before the key existed has no basis,
so it declares nothing until the next `modeltiers run`. A model with no
declarable window gets nothing from wt: set `CLAUDE_CODE_MAX_CONTEXT_TOKENS`
yourself, in your shell or in a profile's `env`, to the window you serve.

The variable holds one value per session. A value already exported in your
shell is left alone (unlike the slot variables above, wt does not remove it),
and an applied profile's `env` value wins over wt's. For a
[slot profile](slot-profiles.md#what-claude-code-is-given) the value is the
smallest window among the slots that have one, provided the default slot has
one (`/model` can move the session onto any slot). A slot with no window of
its own, a local build for instance, is ignored and can still run past a
smaller declared value: set the variable in the profile's `env` to override.

wt sets nothing else for the slots: no `modelOverrides` (a slot profile
may set its own under `config_content`, a table checked as in
[slot-profiles.md](slot-profiles.md#a-modeloverrides-target-wt-does-not-route)), no
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

wt then passes `--model <default slot>` (`--model opusplan` for an [opusplan](slot-profiles.md#opusplan) profile) and one inline `--settings` JSON
holding `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for every slot. It writes no
settings file. The file format, the picker row and every failure are in
[slot-profiles.md](slot-profiles.md).

A dated observation (2026-10-14, #393) concerns a launch that gives
Claude Code a local mtplx model through the LiteLLM proxy, slot profile or
not (a direct launch to mtplx was not tested): on a 64 GB machine
mtplx refused some of Claude Code's 30–41k-token requests with HTTP 507
`insufficient memory`, and the retries made turns slow. See
[A local slot model and memory](slot-profiles.md#a-local-slot-model-and-memory).

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
