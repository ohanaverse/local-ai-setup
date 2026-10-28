# wt profiles: local-model launch tweaks

`internal/profiles` overlays agent-specific tweaks onto a launch, resolved
from the same agent × model location/provider/id `wt` already computes
(`config.Route`/`config.ResolveLocation`). See
`docs/superpowers/specs/2026-09-24-wt-agent-profiles-design.md` (monorepo
root) for the full design; this page is the operator-facing reference.

This page covers **automatic profiles**: unnamed entries wt applies to a
single-model launch that matches them. A **slot profile** is a different
kind: a named entry you pick in the model picker, which assigns a registry
model to each of Claude Code's model slots. Slot profiles have their own
file and page: [slot-profiles.md](slot-profiles.md). `wt profile list` and
`wt profile show` report both kinds; `wt profile on|off|status` and
everything else on this page are about automatic profiles only.

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
| claude | env, config_file, config_defaults | config_file and config_defaults → `<worktree>/.claude/settings.local.json`, never your global `~/.claude/settings.json` |
| pi | wrapper, env | swaps the launched binary (e.g. `little-coder`) around pi's own argv; env lands on the launched wrapper process, which reads `LITTLE_CODER_*` |
| codex | env, args, config_file | config_file → `~/.codex/agent-wt-profile.config.toml` (`--profile agent-wt-profile`); codex has no project-scoped profile mechanism, so this one file is global and wt-owned |
| opencode | env, config_file | config_file merges into the `OPENCODE_CONFIG_CONTENT` env payload wt already sets — no file at all |

**`config_defaults`** (claude only) has the same shape as `config_content`
but writes a value only where `<worktree>/.claude/settings.local.json`
does not already have one: every leaf the file already holds, whatever its
value, is left alone, and a leaf whose path crosses a value that is not a
table (`permissions = "x"`) counts as present. It is applied after
`config_content`, so a leaf both set is `config_content`'s. "Absent" means
absent from that one file: `.claude/settings.json` and
`~/.claude/settings.json` are never read, so a value set there is still
overridden for the session. A leaf it writes is restored at exit like a
`config_content` leaf (see "File safety"). A later tier's value wins per
leaf, as for `config_content`. Slot profiles do not take it: in
`slot-profiles.toml` it is reported as an unknown key.

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
  cleanup works per leaf: each value the profile wrote (anything that is
  not a non-empty table, found by its path, such as
  `permissions.defaultMode`) is put back to its pre-launch value, or
  removed if it didn't exist before, and a table the merge created for it
  is removed once it is empty. Everything else in the file stays as the
  session left it, a sibling under the same table included — a
  `permissions.allow` grant Claude Code saves beside a pinned
  `permissions.defaultMode` survives the session (#401). A leaf the
  session changed or deleted after the merge also keeps the session's
  state, so a hand edit mid-session is kept whether or not the profile
  pins that value; wt keeps a hash of each value it wrote to tell, never
  the value itself. A file that didn't exist before is deleted at exit
  when nothing but the profile's own values was in it — or when what the
  session left no longer parses as a JSON object, which the per-value
  restore cannot act on.
  `config_defaults` leaves are written and restored the same way, and a
  value the file already had is never written, so it is never restored
  either.
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
# env lands on the wrapper process — little-coder itself — so this is its permission-mode lever (accept-all skips the shell whitelist).
env = { LITTLE_CODER_PERMISSION_MODE = "accept-all" }

# claude: MAX_THINKING_TOKENS and CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC are
# plain process-env settings, so they use the env mechanism.
# CLAUDE_CODE_ATTRIBUTION_HEADER must NOT move into env: the optimization doc
# reports a shell env var does not stick for it, and a per-request attribution
# header invalidates the local KV cache (~90% slower). It goes through
# <worktree>/.claude/settings.local.json via config_content, which wt merges
# and restores value by value.
[[profiles]]
agent = "claude"
match = "location"
location = "local"
env = { MAX_THINKING_TOKENS = "4096", CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = "1" }
config_content = { env = { CLAUDE_CODE_ATTRIBUTION_HEADER = "0" } }
# CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 was seen starting Claude Code in
# auto mode on 2.1.300 (#393); on 2.1.312 `claude -p` starts in default mode
# with it (checked 2026-10-28), and reaching auto took --permission-mode auto.
# Auto mode's classifier sends the local model a ~31k-token request on every
# tool turn, so defaultMode = "default" keeps local sessions out of it if a
# version or setting starts one there.
# A default, not an override: a defaultMode already in settings.local.json wins.
config_defaults = { permissions = { defaultMode = "default" } }

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
| pi | `env` | sets `LITTLE_CODER_PERMISSION_MODE=accept-all` on the wrapper process (little-coder), skipping its shell whitelist |
| claude | `env` | caps thinking at 4096 tokens; suppresses nonessential background traffic |
| claude | `config_file` + `config_defaults` | `config_content` merges `CLAUDE_CODE_ATTRIBUTION_HEADER=0` into `<worktree>/.claude/settings.local.json`; `config_defaults` adds `permissions.defaultMode = "default"` only when that file has no `defaultMode`. At exit the header is put back and a `defaultMode` wt added is removed; one the file already had is never touched. What the session saved beside them stays (see the `permissions` cost under "Why these values") |
| codex | `args` | caps `model_reasoning_effort` at `low`; truncates tool output at 12000 tokens |
| opencode | `config_file` | merges `compaction { auto = true, prune = false, reserved = 10000 }` into the `OPENCODE_CONFIG_CONTENT` payload (no file written) |

### Why these values

- **pi — `wrapper` + `env`.** little-coder is pi plus a curated
  extension/skill set; wrapping the launch gets the scaffold without a new
  agent driver. wt still chooses the model: little-coder sees `--model`
  already present and does not inject its own default. No `--thinking`
  override is used; the effective level is pi's fallback default of
  `medium` in the installed pi 0.83.0 (a `defaultThinkingLevel` in
  `~/.pi/agent/settings.json` overrides it), and little-coder does not
  inject a level here — its thinking heuristic treats the `:` in a wt
  model id such as `litellm/ollama/qwen3.8:27b-mlx` as a `:level` suffix.
  The `env` entry is safe despite pi having no env lever of its own: wt
  applies profile env to the launched process before the wrapper runs, and
  with little-coder that process is little-coder itself — so
  `LITTLE_CODER_PERMISSION_MODE` is that binary's own knob for its shell
  whitelist (`accept-all` skips the gate entirely; a deliberate,
  spec-approved trade-off —
  `docs/superpowers/specs/2026-09-25-pi-local-accept-all-design.md`,
  monorepo root).
- **claude — thinking, attribution + permission mode.** `MAX_THINKING_TOKENS=4096` stops a
  small model from over-deliberating instead of acting;
  `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1` keeps background requests
  off an endpoint that cannot use them. The attribution header is the one
  setting the optimization doc says must live in Claude Code's settings
  rather than the process environment, hence the `config_file` split.
  `permissions.defaultMode = "default"` goes to the same file, as a
  `config_defaults` entry, as a guard against auto mode: its classifier sends
  the local model a non-streamed request of about 31k tokens on every turn
  that uses a tool, which in #393 made turns 2–5x slower and added prefill
  memory pressure. Measured on 2026-10-28 against a local 27B model, one
  trivial Bash command took about 3–5 minutes in auto mode; Claude Code gave
  up on a classifier request after about a minute ("Classifier unavailable …
  timed out") and retried it. `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`
  was seen starting auto mode on 2.1.300 (#393). On 2.1.312, `claude -p`
  starts in default mode with it set (checked 2026-10-28; non-interactive
  runs only, interactive sessions not tested), and reaching auto took
  `--permission-mode auto`, so there the pin is a guard rather than a fix.
  The request Claude Code abandons is cancelled in omlx by LiteLLM's
  `general_settings.cancel_on_disconnect`, which wt writes (#399).
  Because it is a default, a `permissions.defaultMode` in
  `.claude/settings.local.json` wins; one set in `.claude/settings.json` or
  `~/.claude/settings.json` is still overridden for the session, and an
  existing `auto` brings back the classifier slowdown (#393). An "always
  allow" grant Claude Code saves under `permissions.allow` during the
  session is kept (see "File safety" above).
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
- **wt maps claude's model slots itself; a profile's `env` can still
  override one.** On every single-model launch that is not native, wt sets
  `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for all four slots (`fable`, `opus`,
  `sonnet`, `haiku`) to the reference it passes as `--model`, and
  `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` to the model's provider-side name
  (see [claude-wt.md](claude-wt.md#model-slots-on-a-single-model-launch)).
  A profile does not need to set them. One that does keeps working: a value
  an applied profile sets in `env` for one of these eight variables wins
  over wt's, and the others keep wt's values. A value exported in your shell
  never reaches claude; wt replaces it. A value has to be what the endpoint
  routes: the registry id through LiteLLM, the provider-side name on a
  direct route. The only placeholder here, `{{model_name}}`, is the
  provider-side name, so through LiteLLM write the registry id out. To give
  each slot its own model, use a slot profile, which names a registry model
  per slot: see [slot-profiles.md](slot-profiles.md).
  A profile that overrides one `ANTHROPIC_DEFAULT_<SLOT>_MODEL` should set
  the matching `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` too. Otherwise wt's
  label for that slot still names the session model, and Claude Code's
  `/model` list shows the wrong name. This rule is for a profile's `env`. A
  profile that sets one of these variables under `config_content.env`
  (written to `.claude/settings.local.json`) is not decided by wt. Claude
  Code's own settings-over-environment rule decides, and wt does not test it.
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

**`wt smoke` applies profiles too.** For each agent × model row it
resolves the profile from the same already-loaded profiles.toml state and
applies it via `applyResolvedProfile` — the same function a real launch
uses (`cmd/wt/smoke.go`) — so smoke and a real launch can't drift on
apply/rollback semantics. It never prompts. If profiles.toml fails to load
or validate, smoke warns on stderr and runs unprofiled; `wt profile off`
disables it for smoke as well. A profile whose wrapper binary is missing
fails that smoke row.
