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

A profile using a mechanism its agent doesn't declare fails at startup with
a clear error (`wt -A <agent> ...` still launches, unprofiled — see
"Malformed or invalid profiles.toml" below) — `copilot`/`shell` accept
none. **Note:** `wt profile status` does NOT run this check — it only
reports the `enabled` flag, so a profiles.toml with a mechanism/agent
mismatch still reports on/off normally; the mismatch surfaces on the next
launch attempt instead (and on `wt -A <agent> ...` at startup, as a
stderr warning). `wt profile on`/`off`/`list`/`show` still work on such a
file too — see below.

## Malformed or invalid profiles.toml

Two different problems can affect profiles.toml, and they're handled
differently:

- **A load error** — the file doesn't parse (a TOML syntax error) or can't
  be read. `wt profile on`/`off`/`list`/`show`/`status` all refuse and
  report the error, rather than silently treating the broken file as
  empty: `on`/`off` in particular NEVER writes over a file it couldn't
  parse, since doing so would silently replace every hand-authored
  `[[profiles]]` entry and comment with just `enabled = ...`. Fix the file
  by hand, then retry. A launch (`wt -A <agent> ...`) still degrades
  gracefully — it prints a stderr warning and launches unprofiled, it just
  never blocks the agent.
- **A validation error** — the file parsed fine, but some profile names a
  mechanism its agent doesn't declare support for. The data is intact, so
  `wt profile on`/`off`/`list`/`show` all still work normally (nothing was
  lost). The practical consequence is at launch time: `applyProfileForLaunch`
  also runs this same validation and, on failure, degrades the WHOLE
  profile layer for that launch (not just the offending entry) to an
  unprofiled launch with a stderr warning — so a single bad profile entry
  effectively disables every profile until it's fixed.

## File safety

Any real file a profile's `config_content` writes
(`.claude/settings.local.json`, `agent-wt-profile.config.toml`) is
snapshotted before the write. What happens at write time and at restore
differs by agent:

- **claude's `settings.local.json` is MERGED, not replaced.** It isn't a
  wt-exclusive file — Claude Code itself writes to it during the session
  (e.g. persisting a user-approved "always allow" permission grant) — so
  the write merges `config_content` into whatever is already there, and
  cleanup restores only the specific top-level keys `config_content`
  itself touched (or removes them, if the file didn't exist before the
  write), leaving anything the agent wrote to OTHER keys during the
  session untouched. Hand-editing a key the profile does NOT touch is
  therefore safe even mid-session; hand-editing a key it DOES touch will
  be reverted to its pre-launch value (or removed, if it didn't exist
  before) when the session ends.
- **codex's `agent-wt-profile.config.toml` is a dedicated, wt-owned global
  file** nothing else writes to, so it keeps a simple whole-file
  snapshot/replace/restore — don't hand-edit it while a profiled codex
  session might be running.

Every other file a profile touches (env vars, CLI args) leaves nothing
behind.

Restore runs right after the launched agent's `cmd.Run()` call returns —
on a normal exit, success or a non-zero exit code. **It is NOT restored on
Ctrl+C or SIGHUP**: Go processes exit on those signals by default without
running wt's explicit (non-deferred) cleanup call, so interrupting a
profiled session, or closing the terminal mid-session, can leave the
profile-rewritten file in place. Nothing is lost forever, though — every
subsequent launch of that same agent (claude or codex, INCLUDING a
native-model launch — self-heal runs before profile resolution and does
not depend on this launch matching a profile of its own; only a
command-agent launch skips it, since command agents have no
config_content target at all) checks that agent's config_content target
for a leftover file from a past session, whether or not a profile happens
to match THIS launch, and restores it automatically before doing anything
else, printing a one-line notice
(`wt: restored a leftover profile-managed file from a previous session: <path>`)
when it does. So a Ctrl+C mid-session just delays the restore until the
*next* launch of that agent, rather than losing the original content.

**A second launch touching the same target while the first is still
running is refused, not raced.** Each backup records the pid of the wt
process that owns it; a launch whose config_content target is still owned
by a DIFFERENT, still-live wt process (most relevant to codex, whose
target is one fixed global path shared across every worktree) fails with
"already in use by another live wt session" instead of self-healing over
it or writing on top of it — that launch degrades to unprofiled, same as
any other config_content failure. A process is always free to restore its
own backup at its own normal exit.

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

An interactive launch (`wt -A <agent> -M <model>`, or the worktree/agent/
model picker) that resolves a non-empty profile asks before applying it —
default **yes** on a bare Enter. A non-interactive launch (no controlling
terminal: scripts, CI) applies automatically with no prompt.

**`wt smoke` does NOT apply profiles today.** It builds its launch
commands directly via `agents.BuildLaunchCmd` and never goes through
`runAgentCmd` — the only choke point profile resolution is wired into — so
a smoke-tested agent always launches unprofiled, regardless of
profiles.toml. This is a known gap, not a feature; wiring `wt smoke`
through the same profile resolution is a natural follow-up but isn't done
yet.
