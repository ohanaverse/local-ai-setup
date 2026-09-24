# wt profiles: local-model launch tweaks

`internal/profiles` overlays agent-specific tweaks onto a launch, resolved
from the same agent × model location/provider/id `wt` already computes
(`config.Route`/`config.ResolveLocation`). See
`docs/superpowers/specs/2026-09-24-wt-agent-profiles-design.md` (monorepo
root) for the full design; this page is the operator-facing reference.

**Nothing is enabled out of the box.** `~/.config/agent-wt/profiles.toml`
starts absent (equivalent to `enabled = true`, zero profiles) — the four
examples below are Phase-1 recipes, drawn from
`docs/minimal-agent-harnesses/optimizing-coding-agents-for-local-models.md`,
meant to be copied into that file by hand and adjusted, not shipped
defaults.

## Commands

```bash
wt profile list                       # every defined profile
wt profile show -A claude -M <id>     # dry-run resolution, no launch
wt profile status                     # on/off
wt profile on / wt profile off        # global kill switch
```

## Mechanisms by agent

| Agent | Mechanisms | Notes |
|---|---|---|
| claude | env, config_file | config_file → `<worktree>/.claude/settings.local.json`, never your global `~/.claude/settings.json` |
| pi | wrapper | swaps the launched binary (e.g. `little-coder`) around pi's own argv |
| codex | env, args, config_file | config_file → `~/.codex/agent-wt-profile.config.toml` (`--profile agent-wt-profile`); codex has no project-scoped profile mechanism, so this one file is global and wt-owned |
| opencode | env, config_file | config_file merges into the `OPENCODE_CONFIG_CONTENT` env payload wt already sets — no file at all |

A profile using a mechanism its agent doesn't declare fails
`wt profile status` (and every launch) with a clear error at startup —
`copilot`/`shell` accept none.

## File safety

Any real file a profile's `config_content` writes
(`.claude/settings.local.json`, `agent-wt-profile.config.toml`) is
snapshotted before the write and restored the moment the launched agent
exits — success, failure, or Ctrl+C. If `wt` itself is killed
(`kill -9`) before it can restore, the *next* launch that would write the
same file restores the orphaned original first. Don't hand-edit these two
specific files while a profiled session might be running; every other
file a profile touches (env vars, CLI args) leaves nothing behind.

## Phase-1 example profiles

```toml
enabled = true

[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["--pi-args", "{{args}}"] }

[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0", MAX_THINKING_TOKENS = "4096" }
config_content = { env = { ANTHROPIC_DEFAULT_SONNET_MODEL = "{{model_name}}", ANTHROPIC_DEFAULT_HAIKU_MODEL = "{{model_name}}" } }

[[profiles]]
agent = "codex"
match = "provider"
provider = "ollama"
args = ["-c", "model_reasoning_effort=\"low\"", "-c", "tool_output_token_limit=12000"]

[[profiles]]
agent = "opencode"
match = "location"
location = "local"
config_content = { compaction = { auto = true, reserved = 10000 } }
```

## Confirm prompt

An interactive launch (`wt -A <agent> -M <model>`, or the picker once the
TUI follow-up lands) that resolves a non-empty profile asks before
applying it — default **yes** on a bare Enter. A non-interactive launch
(no controlling terminal: scripts, `wt smoke`, CI) applies automatically
with no prompt.
