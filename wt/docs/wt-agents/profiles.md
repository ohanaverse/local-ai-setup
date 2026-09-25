# wt profiles: local-model launch tweaks

`internal/profiles` overlays agent-specific tweaks onto a launch, resolved
from the same agent × model location/provider/id `wt` already computes
(`config.Route`/`config.ResolveLocation`). See
`docs/superpowers/specs/2026-09-24-wt-agent-profiles-design.md` (monorepo
root) for the full design; this page is the operator-facing reference.

**Nothing is enabled out of the box.** `~/.config/agent-wt/profiles.toml`
starts absent (equivalent to `enabled = true`, zero profiles) — the
example config below is a worked, verified local-model starting point,
drawn from
`docs/minimal-agent-harnesses/optimizing-coding-agents-for-local-models.md`.
It has been resolved end to end with `wt profile show` and cross-checked
against the installed agent binaries; it is meant to be copied into that
file by hand and adjusted, not shipped as a default.

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

## Working example: local models, all four agents

A verified starting point for a machine serving local models through
ollama/omlx/mtplx and driving claude, codex, opencode, and pi. Every
entry matches on `location = "local"`, so it applies to any local model
from any local provider and never to a native or cloud route. Copy it
into `~/.config/agent-wt/profiles.toml` and adjust; it is not a shipped
default.

```toml
enabled = true

# pi: run the launched pi under little-coder's scaffold. little-coder has no
# --pi-args flag; it forwards unrecognized args straight to pi, so it splices
# wt's own argv verbatim.
[[profiles]]
agent = "pi"
match = "location"
location = "local"
wrapper = { binary = "little-coder", args_template = ["{{args}}"] }

# claude: MAX_THINKING_TOKENS and CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC are
# plain process-env settings, so they use the env mechanism.
# CLAUDE_CODE_ATTRIBUTION_HEADER must NOT move into env: the optimization doc
# reports a shell env var does not stick for it, and a per-request attribution
# header invalidates the local KV cache (~90% slower). It goes through
# <worktree>/.claude/settings.local.json via config_content, which wt merges
# and restores.
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { MAX_THINKING_TOKENS = "4096", CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1" }
config_content = { env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0" } }

# codex: args are appended AFTER wt's driver args and any user passthrough, and
# codex's repeated -c form is last-wins — so these values also win over a user's
# own `-c model_reasoning_effort=...` on the command line.
[[profiles]]
agent = "codex"
match = "location"
location = "local"
args = ["-c", "model_reasoning_effort=\"low\"", "-c", "tool_output_token_limit=12000"]

# opencode: config_content is merged into the OPENCODE_CONFIG_CONTENT env
# payload wt already sets (no file). prune is spelled out explicitly.
[[profiles]]
agent = "opencode"
match = "location"
location = "local"
config_content = { compaction = { auto = true, prune = false, reserved = 10000 } }
```

### What each entry changes

| Agent | Mechanism | Effect on a local launch |
|---|---|---|
| pi | `wrapper` | runs `little-coder` instead of bare `pi`, splicing wt's own `--model …` argv into `{{args}}` |
| claude | `env` | caps thinking at 4096 tokens; suppresses nonessential background traffic |
| claude | `config_file` | merges `CLAUDE_CODE_ATTRIBUTION_HEADER=0` into `<worktree>/.claude/settings.local.json`, then restores it |
| codex | `args` | caps `model_reasoning_effort` at `low`; truncates tool output at 12000 tokens |
| opencode | `config_file` | merges `compaction { auto = true, prune = false, reserved = 10000 }` into the `OPENCODE_CONFIG_CONTENT` payload (no file written) |

### Why these values

- **pi — `wrapper`.** little-coder is pi plus a curated extension/skill
  set; wrapping the launch gets the scaffold without a new agent driver.
  wt still chooses the model: little-coder sees `--model` already present
  and does not inject its own default. No `--thinking` override is used;
  the effective level is pi's fallback default of `medium` in the
  installed pi 0.83.0 (a `defaultThinkingLevel` in
  `~/.pi/agent/settings.json` overrides it), and little-coder does not
  inject a level here — its thinking heuristic treats the `:` in a wt
  model id such as `litellm/ollama/qwen3.8:27b-mlx` as a `:level` suffix.
- **claude — thinking + attribution.** `MAX_THINKING_TOKENS=4096` stops a
  small model from over-deliberating instead of acting;
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` keeps background requests
  off an endpoint that cannot use them. The attribution header is the one
  setting the optimization doc says must live in Claude Code's settings
  rather than the process environment, hence the `config_file` split.
- **codex — reasoning effort + output cap.** `model_reasoning_effort="low"`
  is codex's thinking-budget knob; `tool_output_token_limit=12000` is its
  built-in read-guard, capping runaway file reads before they flood a
  small context window. `model_context_window` and
  `model_auto_compact_token_limit` are deliberately left out — the
  optimization doc reports them as unreliable in current builds.
- **opencode — compaction.** Compacts earlier and reserves less headroom
  (10000 tokens) so the working set stays small; `prune` is spelled out
  as `false` to match the source doc rather than tracking an upstream
  default. `small_model` is already pinned to the same provider by the
  opencode driver, so it is not repeated here.

### Verifying a profile

`wt profile show` resolves without launching. Substitute a model id from
your own `registry.toml`:

```bash
wt profile show -A claude -M ollama/qwen3.8:27b-mlx   # -> env/config_content lines
wt profile show -A claude -M claude/native            # -> (no matching profile)
```

Every agent should resolve against any local model, and resolve nothing
against a native or cloud model. A `location` profile is provider-agnostic:
the same entry matches an `ollama`, `omlx`, or `mtplx` model.

### Notes and caveats

- **`--pi-args` is not a little-coder flag.** little-coder's launcher
  strips only its own documented flags and forwards everything else to pi,
  so `args_template = ["--pi-args", "{{args}}"]` would hand pi a flag it
  rejects. Use `["{{args}}"]`. A missing wrapper binary is the one fatal
  profile error — a requested wrapper must actually run — so `little-coder`
  must be on `PATH`.
- **The claude `ANTHROPIC_DEFAULT_*_MODEL` mapping cannot use
  `{{model_name}}`.** With LiteLLM on, a launch routes through the proxy
  using the full registry id (`ollama/qwen3.8:27b-mlx`); that is what
  `wt/internal/litellm` writes as the proxy's `model_name`. The only value
  placeholder `internal/profiles` substitutes is `{{model_name}}` =
  `m.ModelName` (`qwen3.8:27b-mlx`), which the proxy does not know. The
  mapping is therefore omitted above. To route claude's tier defaults to
  a local model, add a `match = "model"` profile per model with the
  literal registry id, or turn LiteLLM off (`wt litellm off`) so the
  provider-side `{{model_name}}` is what's dialled — that only yields
  direct mode if the provider speaks claude's protocol; a provider with
  no Anthropic support still force-routes through LiteLLM. There is no
  `{{model_ref}}`/`{{model_id}}` placeholder yet.
- **The claude attribution header belongs in `config_content`, not
  `env`.** A shell environment variable does not stick for
  `CLAUDE_CODE_ATTRIBUTION_HEADER`; it has to be set in Claude Code's
  settings, so it goes through the `config_file` mechanism
  (`.claude/settings.local.json`). Moving it into the `env` map silently
  reintroduces the KV-cache invalidation this profile exists to avoid.
- **codex's `-c` values are last-wins.** Because profile `args` are
  appended after wt's driver args and after any user passthrough, a
  user's own `codex-wt -M <local> -- -c model_reasoning_effort="high"`
  is overridden back to `low`. The profile winning is wt's own arg
  ordering (profile args are appended last; the design spec records this
  as a deferred "ExtraArgs ordering tradeoff"), not a codex behavior —
  codex's repeated `-c` last-wins format is upstream, but the outcome
  here is wt's. It is not something this config file can fix. To force a different value for
  one model, add a `match = "model"` profile for it whose own `args` list
  replaces this one's — a later tier's non-empty `args` fully replaces an
  earlier tier's, so it must repeat any codex args you still want;
  `wt profile off` disables the whole layer.

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
