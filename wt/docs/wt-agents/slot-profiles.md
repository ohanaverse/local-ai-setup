# wt slot profiles: one launch, several models

A wt launch of Claude Code names one model, and Claude Code uses several:
a main model, a small fast model for background work, and whatever `/model`
or a subagent switches to. A single-model launch sends all of them to the
one model it names: wt sets `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for every slot
to the `--model` value (see
[claude-wt.md](claude-wt.md#model-slots-on-a-single-model-launch)), so no
slot keeps an Anthropic id for the LiteLLM proxy to reject with
`Invalid model name passed in model=claude-…`.

A **slot profile** gives each slot its own model. It is a named entry that assigns a registry model to each of an agent's model
slots. It appears as a row in the model picker, and you choose it like a
model. Claude Code is the only agent with slots today; its slots are
`fable`, `opus`, `sonnet` and `haiku`.

Slot profiles are not the automatic profiles of [profiles.md](profiles.md).
An automatic profile has no name, is never chosen, and is applied to a
single-model launch that matches it. A slot profile is its own launch: wt
never applies an automatic profile to it, and `wt profile on|off` does not
hide it. The two launches also treat the slot variables differently. On a
single-model launch they are process environment variables that an
automatic profile's `env` may override; on a slot-profile launch they
travel in `--settings`, and a profile may not set them.

Design: `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md`
(monorepo root). Anthropic does not support routing Claude Code to
non-Claude models through a gateway. Slot profiles, like every non-native wt
launch of Claude Code, are outside that support.

## Quick start

1. Configure the model server once, if you have not (`[model-server]` in
   `config.toml`; [wt-session.md](../wt-session.md)). A slot profile always
   goes through its gateway, which forwards to the LiteLLM proxy.
2. Add a profile: `wt profile slots add <name> --slot sonnet=<model-id> …`
   ([Managing the file](#managing-the-file)), or write
   `~/.config/agent-wt/slot-profiles.toml` by hand (next section). Take the
   ids from `wt model list --server`.
3. Check it: `wt profile list`, then `wt profile show -A claude -M profile/<name>`.
4. Launch it: pick the `profile/<name>` row in the claude model picker, or
   pin it with `claude-wt -M profile/<name>`.

## The file

`~/.config/agent-wt/slot-profiles.toml`, beside `profiles.toml`. Editing it
by hand is always valid. wt itself changes it only through
`wt profile slots add|set|rm` ([Managing the file](#managing-the-file));
every other command only reads it. A missing file means no profile
rows. The array is `[[profile]]`, singular; `profiles.toml` uses
`[[profiles]]`.

```toml
[[profile]]
name    = "mixed"
agent   = "claude"
default = "sonnet"

[profile.slots]
opus   = "openrouter/z-ai/glm-5.3-flash"
sonnet = "ollama/glm-5.3-flash:cloud"
haiku  = "omlx/mlx-community--Qwen3.8-27B-4bit"

[profile.env]
MAX_THINKING_TOKENS = "4096"

[profile.config_content.modelOverrides]
"claude-sonnet-4-6" = "ollama/deepseek-v4-pro:cloud"
```

The model ids above are illustrative. The same entry is the first profile
of the contract fixture `docs/contracts/slot-profiles.sample.toml`, which a
Go test reads, so this format and the parser cannot drift apart. Its
`modelOverrides` target is not one of `mixed`'s slots, so a launch of that
example warns that wt does not route it
([below](#a-modeloverrides-target-wt-does-not-route)); a target that is a
slot model raises no warning.

| Field | Rule |
|---|---|
| `name` | Required. Lowercase letters, digits and hyphens, starting with a letter or digit. Unique per agent |
| `agent` | Required. An agent whose driver supports slots (`claude`) |
| `slots` | Required table, slot name → registry id. At least one entry. Every key must be a slot the agent declares |
| `default` | Required. A slot that `slots` fills, or `opusplan` for a profile that fills `opus` and `sonnet` ([opusplan](#opusplan)). wt passes it as `--model`, so the session starts on it |
| `env` | Optional, string → string |
| `args` | Optional string list |
| `config_content` | Optional free-form table, merged into the agent's settings. `config_content.modelOverrides` is how a profile supplies Claude Code's `modelOverrides` setting. It must be a table whose every target is a string, or the profile is blocked; wt does not validate its keys, and a target that is not one of the profile's slot models is a warning ([below](#a-modeloverrides-target-wt-does-not-route)). Checked on 2026-10-13: `modelOverrides` is honoured behind the proxy; an id it names is sent as the id it maps to. |
| `wrapper` | Optional `{binary, args_template}` |

**Unfilled slots use the `default` slot's model.** The example fills three
of four slots; `fable` gets the `sonnet` model. No slot is ever left sending
an Anthropic id. With `default = "opusplan"` they use the `sonnet` slot's
model.

### opusplan

`default = "opusplan"` hands Claude Code its own `opusplan` model alias: the
`opus` slot's model while the session is in plan mode, the `sonnet` slot's
model for everything else. Switching plan mode on and off switches the
model. Use it when `opus` holds a stronger model than `sonnet`.

```toml
[[profile]]
name    = "planned"
agent   = "claude"
default = "opusplan"

[profile.slots]
opus   = "openrouter/z-ai/glm-5.3"
sonnet = "ollama/glm-5.3-flash:cloud"
```

- **Both slots are required.** Claude Code reads
  `ANTHROPIC_DEFAULT_OPUS_MODEL` in plan mode and
  `ANTHROPIC_DEFAULT_SONNET_MODEL` otherwise. With either unset it falls back
  to a built-in Anthropic id, which the proxy rejects, so a profile that does
  not fill both is blocked.
- **The session works on `sonnet`.** The row is priced as the `sonnet`
  slot's model, `sonnet` is listed first and marked `*`, and the unfilled
  slots take its model.
- **wt sets `CLAUDE_CODE_DISABLE_1M_CONTEXT=1`.** In plan mode Claude Code
  promotes opusplan's opus model to its `[1m]` variant and assumes a
  1M-token context window, which a slot model rarely has; compaction would
  then start far too late. wt's value wins over one the profile sets.
- **`fable` and `haiku` are never opusplan's.** `haiku` still takes Claude
  Code's background requests; `fable` is reached only with `/model fable`.
- **Above 200K tokens of context, plan mode stays on `sonnet`**: Claude Code
  skips the switch to `opus` for a session that large.
- **`wt smoke` exercises `sonnet` only**: its one-shot prompt does not run in
  plan mode.

Checked on 2026-10-14 with Claude Code 2.1.299 (#391): plan mode reached the
`opus` slot's model, other requests the `sonnet` slot's, and resuming a
session in the other mode switched models. A native launch has its own
`claude/opusplan` row ([claude-wt.md](claude-wt.md#model-selection)).

**The optional fields follow the agent's allow-list**, the one automatic
profiles use ([profiles.md](profiles.md), "Mechanisms by agent"). Claude
allows `env` and `config_content`. `args` and `wrapper` are rejected for it.

**Reserved env keys.** A profile may not set a key wt sets for the launch,
in `env` or in `config_content.env`. For Claude: `ANTHROPIC_BASE_URL`,
`ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, and
`ANTHROPIC_DEFAULT_<SLOT>_MODEL` and `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME`
for each of the four slots.

**At most one local model.** A profile may mix cloud and local models and
models of different providers. It may name one distinct local model, which
may fill several slots.

**A slot may name an alias** (`alias/<name>`, [wt-model.md](../wt-model.md#wt-model-alias);
`wt-model alias list` on the model-server host shows them). An alias is a
catalog row like a model, so `wt profile slots add|set` accepts its id and the
launch needs no other change. For the one-local-model rule an alias counts as
the local model it loads: an alias and its own local target are ONE local model,
as are two aliases that share a local target, and a start through the profile
runs the alias's start (which also adds the alias's routes). An alias counts
as local only when its lowest-order targets include its local target (the
server sends any other alias as a cloud row, so a cloud-first group with a local
fallback is not the profile's local model). A group is eligible for an agent only
when every target's provider is one that agent can run. To rename an alias,
add the new one, `wt profile slots set` each profile on each session machine,
then `wt-model alias rm` the old one.

## When a profile is blocked

wt checks a profile twice. The file alone is checked when it is loaded
(static). The file against the model server's catalog is checked every time
rows are built, and again at launch (resolved). A profile that fails either check still has a row. The row is
blocked, and its reason is the first failing rule.

Static reasons:

| Reason | Cause |
|---|---|
| `duplicate profile name` | Two profiles share a name for one agent. Neither can launch; the picker shows one blocked row |
| `slots is empty: assign at least one slot` | No `slots` entry |
| `slot "x" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)` | A `slots` key the agent does not declare |
| `slot "opus" names no model` | An empty id |
| `default is missing` / `default "x" is not a slot this profile fills` | `default` must name a filled slot, or an alias of the agent's |
| `default "opusplan" needs the opus slot filled` | An alias over a slot the profile leaves unfilled; the first such slot is named |
| `agent "claude" does not support mechanism "wrapper"` | A field outside the agent's allow-list |
| `mechanism "x" has no effect without "y" on the same profile` | The agent's driver requires field `y` whenever `x` is set, and the profile sets only `x`. No rule of this kind applies to Claude |
| `env sets ANTHROPIC_BASE_URL, which wt sets for the launch` | A reserved key (also `config_content.env sets …`) |
| `config_content.env must be a table` | `config_content.env` is a string, a number or an array. This is Claude's rule: Claude Code's settings take `env` as a table, and wt adds its own variables to it |
| `config_content.modelOverrides must be a table` | `config_content.modelOverrides` is a string, a number, a boolean or an array. Claude's rule: Claude Code's `modelOverrides` maps model ids to model ids (#412) |
| `config_content.modelOverrides["<key>"] must be a model id string` | A `modelOverrides` target is a number, a boolean, an array or a table; the first such key in key order is named, its value is not shown. Claude's rule (#418); an empty string is a string and only warns |
| `config_content cannot be encoded as the --settings JSON` | A number in `config_content` is nan or inf, which the `--settings` payload cannot hold (the reason names what the value is). Also Claude's rule |
| `wrapper args_template [...] has no "{{args}}" placeholder` | The agent's own arguments would be dropped |

Resolved reasons (against the model server's catalog):

| Reason | Cause |
|---|---|
| `slot haiku: <id> is not in the model server's catalog` | The id is not a model the server lists. This is what a profile shows after `wt-model cloud-sync` or `wt-model model rm` removed a model it names |
| `slot opus: <id> is a native model, which has no gateway route` | A slot cannot use the native Anthropic subscription (the gateway's origin and token never reach a native model) |
| `slot opus: ` and the server's own block reason | The server blocks that model (no LiteLLM mapping, not on disk, no engine to start it, …) |
| `slots name two local models (<id> and <id>); a slot profile may name one` | More than one distinct local model; an alias counts as its local target, so an alias and its own target are one |

Three problems produce no usable row, and a fourth loses part of the file:

- **No usable name or agent.** A profile whose `name` is missing or
  malformed, or whose `agent` is missing, unknown or without slots, has no
  row and cannot be pinned. wt prints one line per launch,
  `wt: slot-profiles.toml[<n>]: <reason> — skipped (see 'wt profile list')`,
  where `<n>` is the profile's position in the file, from 0.
- **A file that does not parse.** No profile rows; one line,
  `wt: parse slot-profiles.toml: … — slot profiles are unavailable`, which
  carries the TOML error and its line number. A value of the wrong TOML type
  is a parse failure too, and it fails the whole file: with `default = 3` or
  `args = "x"` in one profile, the valid profiles beside it lose their rows
  as well. Fix the line the warning names.
- **The `profile/` prefix is taken.** `profile/` is a reserved id prefix. It
  is taken when the model server's catalog has a provider with id `profile`,
  or a model whose id begins with `profile/` under any provider. A profile
  launch, pin or `wt profile show` is then blocked with a reason that names
  the holder.
- **A key wt does not know.** The file still loads, without what the key
  holds, and wt prints one line per key:
  `wt: slot-profiles.toml: unknown key "profiles" — ignored`. The key is a
  dotted path: `profiles` for an array written `[[profiles]]`,
  `profile.envs` for a misspelt table, `profile.wrapper.args` for a misspelt
  wrapper field. A file whose only content is under an unknown key gives no
  profile and this warning. Keys under `config_content` are free-form and
  are never reported.

None of these affects a single-model launch or an automatic profile. Only
the parse failure affects another profile.

### A modelOverrides target wt does not route

A launch makes sure the profile's slot models are routed: the distinct
registry ids its slots name (the default's model among them). A
`config_content.modelOverrides` target outside that set may be an id you
route yourself, so wt warns and never blocks the row (#412). For each such
target of a profile that passed validation, a launch prints before any
picker opens, and again after a picker has closed, so a launch that opens
no picker prints them once,

```
wt: profile/mixed (claude): config_content.modelOverrides["claude-sonnet-4-6"] targets "ollama/deepseek-v4-pro:cloud", which is not one of the profile's slot models, so wt does not make sure it is routed
```

`wt profile list` prints the same lines after `warning:`, and the profile
stays `ok`; `wt profile show` prints the shown profile's own lines after
`warning:`, under its slot lines (#419). The keys are Claude Code's own
model ids and are never judged.

**Where the warning lines appear.** wt prints them to stderr once per launch
of an agent that has slots, before any picker opens. The picker's screen
then covers them, so a launch that went through the picker prints the same
lines again when the picker has closed: after the session, or when you quit.

## In the picker

A valid profile for the agent appears after the native rows and before every
other row (running local models come next), alphabetical by `profile/<name>`, not in file order. The screen below is an illustrative mock-up with
some columns omitted.

```
  FAMILY   MODEL                 LOC    STATUS  RUNNING  COST            7D
  claude   claude/native         cloud  ok      -        -               41
> profile  profile/ollama-cloud  cloud  ok      -        0.40/0.04/1.60   6
  profile  profile/mixed         mixed  ok      -        0.15/0.02/0.60   2
  profile  profile/broken        cloud  ok      -        -
  glm      ollama/glm-5.3-fl...  cloud  ok      -        0.40/0.04/1.60  12

  opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · +1 more
```

| Column | A profile row shows |
|---|---|
| FAMILY | `profile` |
| MODEL | `profile/<name>` |
| TIER | `-`; the slots' tiers are on the line under the table |
| LOC | `cloud`, `local`, or `mixed` |
| STATUS | `ok`. A start or blocked profile is shown as a start or blocked model row is, by its action and note |
| RUNNING | The local model's state (`run`, `load` or `-`), on a blocked row too, whichever rule blocked it; `-` for an all-cloud profile, for a profile that names two local models, for one whose local model is not in the registry or has no row of its own, and for the one row of profiles sharing a name |
| COST | The `default` slot model's price in force (the `sonnet` slot's for `opusplan`), with `~` when it is time-priced |
| 1D / 7D / 30D | Launches of this profile |
| In-use prefix | Blank |

- **The line under the table** lists the highlighted profile's slots: the
  default first, marked `*`, then the rest in the agent's order, each with
  the model's provider-side name and, when `tiers.toml` rates that model,
  its tier code and score
  (`opus* glm-5.3:cloud S44 · haiku qwen3.8:27b-mlx S34`, an illustrative
  line). There is no LiteLLM mode to name: the model server routes.
  An `opusplan` profile's line starts with `opusplan`, then
  `sonnet*`. It shows whole entries only, giving up the tiers before an entry;
  what does not fit becomes `+N more`.
  `wt profile show` gives the full list. On a blocked row the line is the
  reason.
- **Enter** on a launch row launches. On a start row it starts the
  profile's local model through the usual start screen, replace dialog
  included, then launches. On a blocked row it shows the reason.
- **The `> ` marker** sits on the row you launched last, a profile row
  included. When that row is a profile that is present and not blocked, the
  cursor starts on it. Profiles do not rotate: rotation still walks the
  registry models, from where the last single-model launch left it.
- **Filters.** Under `-T` or `-F` profile rows are hidden.
- A table with a profile row never auto-launches its single row.

## Pinning a profile

`-M profile/<name>` launches the profile without the picker. The pin reads
the same row the picker shows.

```bash
claude-wt -W feat --new -M profile/mixed
```

- A pinned profile whose local model is idle starts it first, as a pinned
  model does. `--replace` lets that start stop a running model.
- A blocked pin: with `-W`, `--cwd` or outside a repository, wt exits
  non-zero with the reason. When the picker screens are showing, wt returns
  to the agent picker with the reason.
- Slot profiles are hidden under `-T`/`-F`. Pinning one of the launching
  agent's own profiles there gets
  `model "profile/<name>" is a slot profile, excluded by -T` (or `-F`, or
  `-T and -F`, naming the flags you set), whether or not the profile is
  blocked.
- A profile of another agent gets
  `model "profile/<name>" is a slot profile for agent "<its agent>", not "<agent>"`,
  with or without `-T`/`-F`.
- An unknown name, a profile `wt profile list` reports as skipped, or any
  pin while `slot-profiles.toml` does not load goes to the model server as
  any model id does, which answers with its own unknown-model text. A pin
  while the catalog holds the reserved `profile/` prefix is blocked with a
  reason that names the holder.

Without `-M`, a launch that skips the picker never chooses a profile.
`wt start`, `wt smoke` and rotation ignore slot profiles.

## What a launch does

Every slot profile is resolved against the model server's catalog and
launched through its gateway (see [On a client session](#on-a-client-session-model-server)).
In order — a failure at any step stops the launch before Claude Code starts,
and nothing is recorded:

1. One catalog read; the profile is resolved against it.
2. Refuse passthrough arguments that would replace the slot mapping.
3. Register the client session with every distinct slot model.
4. Start the local slot model with a start job on the model server, only
   when it is stopped or known not to be routed.
5. Build the command against the gateway (next section).
6. Apply the profile's `args` and `wrapper`, for an agent that allows them.
7. Run the agent, with the client session's heartbeat; end the client
   session when it exits.

wt writes no route and restarts no proxy for a profile: the model server
owns LiteLLM. A local model started in step 4 stays running if a later step
fails; the server stops it after its idle grace when no client session uses
it. Ctrl+C in the picker while a profile launch is under way quits wt,
starts no agent and records nothing.

**Passthrough arguments.** wt refuses `--settings` and `--model` after
`--`, in either form (`--model opus` or `--model=opus`). Either would
replace the slot mapping. Everything else after `--` is passed on unchanged.

## What Claude Code is given

Process environment:

| Variable | Value |
|---|---|
| `ANTHROPIC_BASE_URL` | The model server's gateway origin |
| `ANTHROPIC_AUTH_TOKEN` | The model server's bearer token |
| `ANTHROPIC_API_KEY` | Empty |

Removed from the inherited environment: every `ANTHROPIC_DEFAULT_*`
variable, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL`. A
stray shell export of one of them could send a tier to a model the profile
never chose. A profile that wants a subagent model sets
`CLAUDE_CODE_SUBAGENT_MODEL` in its own `env`. Checked on 2026-10-13 with
Claude Code 2.1.296: an inherited `ANTHROPIC_SMALL_FAST_MODEL` does not
override `ANTHROPIC_DEFAULT_HAIKU_MODEL`; wt removes it all the same.

The `--settings` env also carries `CLAUDE_CODE_MAX_CONTEXT_TOKENS` when the
default slot's model has a window: its registry `context_window` (any
location, [wt-model.md](../wt-model.md#a-models-context-window)), else for a
cloud model the host `context` in `tiers.toml` (its own host row; a local build
has none, and a window that is only models.dev's largest row is not declared);
an alias's window is the smallest of its targets', none when any is unknown, so Claude Code does
not assume 200K for an id it does not recognize. For `opusplan` the anchor is
the smaller of the `opus` and `sonnet` slots' windows, and nothing when
either has none. The variable is one value per session and `/model` can move
the session onto any slot, so the value is then lowered to the smallest window
of any other slot that has one; a slot with none is ignored, not a veto. A
value in the profile's `env` or
`config_content.env`, or exported in your shell, is left as it is. See
[claude-wt.md](claude-wt.md#context-window-of-a-gateway-model).

Arguments: the yolo flag when `--yolo` is given, `--model <default slot>`
(an alias such as `sonnet`, so `/model` inside the session switches between
the profile's slots; `opusplan` for an [opusplan](#opusplan) profile, whose
payload also sets `CLAUDE_CODE_DISABLE_1M_CONTEXT=1`), and
`--settings '<json>'`: one inline JSON object.
For the example profile, with the proxy at `http://localhost:4000`:

```json
{
  "env": {
    "ANTHROPIC_API_KEY": "",
    "ANTHROPIC_BASE_URL": "http://localhost:4000",
    "ANTHROPIC_DEFAULT_FABLE_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_FABLE_MODEL_NAME": "glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "omlx/mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL_NAME": "mlx-community--Qwen3.8-27B-4bit",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "openrouter/z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_OPUS_MODEL_NAME": "z-ai/glm-5.3-flash",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "ollama/glm-5.3-flash:cloud",
    "ANTHROPIC_DEFAULT_SONNET_MODEL_NAME": "glm-5.3-flash:cloud",
    "MAX_THINKING_TOKENS": "4096"
  },
  "modelOverrides": {
    "claude-sonnet-4-6": "ollama/deepseek-v4-pro:cloud"
  }
}
```

The payload is illustrative and shown indented; wt passes it on one line.
`wt profile show` prints the real one.

- Each `ANTHROPIC_DEFAULT_<SLOT>_MODEL` is a registry id, the name the proxy
  routes. Each `_MODEL_NAME` is the provider-side name, which Claude Code's
  own `/model` picker shows.
- `ANTHROPIC_BASE_URL` and the empty `ANTHROPIC_API_KEY` are repeated in the
  payload so an `env` block in your own Claude Code settings cannot redirect
  the session.
- **Merge order.** `config_content` is merged into the object as automatic
  profiles merge it: tables key by key, a scalar or an array replaced whole.
  In the `env` block the profile's `env` is lowest, `config_content.env`
  next, wt's own variables highest. `config_content.env` must be a table; a
  profile where it is anything else is a blocked row
  ([When a profile is blocked](#when-a-profile-is-blocked)).
- **The token stays out of the command line.** `ANTHROPIC_AUTH_TOKEN` is set
  in the process environment only, so the proxy key does not appear in the
  process list. An `env` block in your own Claude Code settings that sets
  `ANTHROPIC_AUTH_TOKEN` overrides it, as it does for every non-native wt
  launch.

**wt writes no file.** A Claude profile's `config_content` travels only in
`--settings`. `.claude/settings.local.json` is never written, so an
interrupted session leaves nothing behind for a later bare `claude` run in
that worktree. A leftover from an earlier automatic-profile session is still
restored before a profile launch.

**Claude Code may write its own settings.** Choosing a model with `/model`
during a session saves the alias you picked as the `model` field of
`~/.claude/settings.json`, and prints `Set model to <registry id> and saved as
your default for new sessions`. wt does not prevent it. Profile launches are
unaffected, because `--model` outranks the saved value. A later bare
`claude` run starts on the saved alias, for example `"model": "sonnet"`. It
is the alias, not a registry id.

What the 2026-10-13 run with Claude Code 2.1.296 showed of the session:

- Every mid-session `/model` switch asks `Switch model?` and needs a
  confirmation.
- Each launch prints a warning that the registry id is not described by this
  version's model catalog. The ids still work.
- The `/model` list shows the profile's provider-side names, one per slot,
  and a `Default (recommended)` row that shows the opus slot's registry id
  with a `[1m]` suffix. Picking that row was not tested, so this page does
  not say what it does.

## What wt records

| Record | A profile launch writes |
|---|---|
| `usage.jsonl` | One event under `profile/<name>`. It feeds the row's 1D / 7D / 30D columns and `wt stats` |
| `rotation.state` | Nothing. It stays the id of the last single-model launch |
| `last-picked.state` | `profile/<name>`. This file holds the picker's `> ` marker; a single-model launch writes it too |
| LiteLLM spend | Nothing new. The proxy logs each request under the slot model's registry id |

`wt stats` shows a profile as a row of its own, with its launches and `-`
in the spend columns. Its spend is on the slot models' rows. wt does not
attribute spend back to a profile.

After the session: wt ends the client session and prints the summary line
naming the profile. There is no stop picker (the model server stops an idle
model after its grace), and no survey and no price notice for a profile
session.

## Managing the file

Five subcommands of `wt profile slots` manage the file. Editing it by hand
stays valid; use the commands when you want a validated, backed-up edit, or
let the `wt-manage-slot-profiles` skill (`.claude/skills/wt-manage-slot-profiles/`)
drive them. Every write leaves a `.bak-<stamp>` backup beside the file; old
backups are not pruned, so delete them by hand.

```bash
wt profile slots add <name> --slot <slot>=<model-id> [--slot …] [--default <slot>] [--env KEY=VALUE] [--comment "<text>"]
wt profile slots set <name> [--slot <slot>=<model-id>] [--default <slot>] [--env KEY=VALUE] [--unset-env KEY]
wt profile slots rm <name>
wt profile slots usage [--json]
wt profile slots candidates [--json] [--open-only] [--provider <id>[,<id>]] [--near <model-id>] [--per-provider N]
```

- `add` creates a profile for claude (`--default` defaults to `sonnet`) and
  creates the file when it is missing. `set` changes only what you name;
  `--slot` replaces that slot's model, `--env` sets a key, `--unset-env`
  removes one. At least one of the four must be given. `rm` removes the
  entry and the comment block directly above it. Each prints the backup path
  (or `created <path>`), then the `wt profile list` slot section.
- `usage` lists each profile in the file with its launches in the last 1, 7
  and 30 days, its last launch and its leading comment. Usage history is
  pruned at 30 days, so zero in the 30-day column means "not launched in 30
  days", not "never".
- `candidates` ranks registry models by `tiers.toml` score (open weights
  first, then score, then price) and prints how old `tiers.toml` is. With
  `--near <model-id>` it lists, per provider, the models scoring nearest to
  that one. Advisory only: nothing routes on a rank. `--json` also reports
  `tiers_generated_at` and `stale_sources`.

What a write guarantees:

- The old file is copied to `slot-profiles.toml.bak-<YYYYMMDD-HHMMSS>`
  beside it first (a `-2`, `-3` suffix on a collision). The read-edit-write
  holds `slot-profiles.toml.lock` beside the file, so two concurrent
  `wt profile slots` commands serialize instead of silently dropping one
  edit.
- Every other entry and comment is kept byte for byte, and the file keeps
  its mode. A symlinked file is written through the link.
- The result is validated before it replaces the file. wt refuses, and
  leaves the file and its backups alone, when the new text does not parse,
  when the edited profile would not be usable, when a slot names an id that
  is not in the registry, when a profile that was usable would stop being
  usable, or when the edit introduces an unknown key.
- `add` writes only `name`, `agent`, `default`, `slots` and `env`. A profile
  that needs `args`, `wrapper` or `config_content` is written by hand.

Where the editor will not follow the file, it stops with a message that says
to edit by hand: a multi-line string (`"""` or `'''`) anywhere in the
file, a profile whose `slots` or `env` are written as dotted keys or an
inline table, a value that is not a one-line string, a name used by two
profiles, or a name that does not exist (`set`, `rm`).

## Commands

```bash
wt profile list                              # automatic profiles, then slot profiles
wt profile show -A claude -M profile/mixed   # the picker's verdict and what would be passed
```

`wt profile list` prints the slot section whatever state `profiles.toml` is
in, and needs no running provider:

```
slot profiles:
  claude: profile/mixed ok
  claude: profile/broken error: default "x" is not a slot this profile fills
  slot-profiles.toml[2]: error: name is missing
```

With no profile it prints `slot profiles: (none defined)`. After the
profiles it prints one `warning:` line per unknown key
(`warning: slot-profiles.toml: unknown key "profiles" — ignored`), also when
there is no profile, and one when the `profile/` prefix is taken, since no
profile it lists as `ok` has a row then, and one per `modelOverrides`
target a passing profile does not route
([above](#a-modeloverrides-target-wt-does-not-route)). A file that does
not parse is one line, `slot profiles: <error>`, and the command exits
non-zero. `list` reports the static check only. A profile it calls `ok` can
still be blocked by the registry or the proxy; `show` says.

`wt profile show -A <agent> -M profile/<name>` reports what the picker
would: `launch`, `start` or `blocked`. It probes the local providers once.
It starts no model, writes no route and restarts nothing. Output for the
example (illustrative):

```
profile/mixed (claude): start
slot fable: ollama/glm-5.3-flash:cloud (glm-5.3-flash:cloud) [default's model]
slot opus: openrouter/z-ai/glm-5.3-flash (z-ai/glm-5.3-flash)
slot sonnet*: ollama/glm-5.3-flash:cloud (glm-5.3-flash:cloud)
slot haiku: omlx/mlx-community--Qwen3.8-27B-4bit (mlx-community--Qwen3.8-27B-4bit)
warning: profile/mixed (claude): config_content.modelOverrides["claude-sonnet-4-6"] targets "ollama/deepseek-v4-pro:cloud", which is not one of the profile's slot models, so wt does not make sure it is routed
env: ANTHROPIC_BASE_URL=http://localhost:4000
env: ANTHROPIC_AUTH_TOKEN=***1234
env: ANTHROPIC_API_KEY=
unset: ANTHROPIC_SMALL_FAST_MODEL
unset: CLAUDE_CODE_SUBAGENT_MODEL
unset: ANTHROPIC_DEFAULT_*
args: --model sonnet --settings <settings>
settings:
  { …the payload shown above… }
```

The proxy key is masked as `wt litellm status` masks it. Under the slot
lines comes one `warning:` line per `modelOverrides` target of this profile
that wt does not route, worded as `wt profile list` words it
([above](#a-modeloverrides-target-wt-does-not-route)); they sit there, next
to the slot models the targets are compared with and above the long
settings payload, and the command still exits 0. A blocked profile prints
`reason: …`, no warning line, and the command exits non-zero. An `opusplan` profile
prints `default: opusplan — the opus slot in plan mode, the sonnet slot
otherwise` under the first line and marks `sonnet*`.

`wt profile on|off` and `wt profile status` govern automatic profiles only.

## On a client session (model server)

Every session is a client of the model server (`[model-server]` `url` and
`token_file` in `config.toml`, or `WT_MODEL_URL` and
`WT_MODEL_TOKEN_FILE`/`WT_MODEL_TOKEN`; with none configured a profile
launch refuses, exit 1): slot profiles are read against the server's catalog,
never the registry, and the launch goes through the server's gateway
([wt-model-server.md](../wt-model-server.md)). `slot-profiles.toml` stays in
the client's own config home.

| Command | Needs the model server |
|---|---|
| `wt profile slots candidates` | yes: the ranking runs on the model-server host, which holds the registry and `tiers.toml`. Same `--json` document (`tiers_generated_at`, `stale_sources`, `candidates`) |
| `wt profile slots add`, `set` | yes: a slot id must be a row of the server's catalog, a model or an alias (otherwise `profile "<name>" slot <slot> names "<id>", which is not a model or alias on the model server`). A server that does not answer fails the command (exit 1, `…; nothing was changed`) and the file is not touched |
| `wt profile show -A <agent> -M profile/<name>` | yes: the verdict is resolved against the catalog. The preview shows the gateway origin and the client token masked (`***` plus its last four characters); the `[litellm]` url and key are never read |
| `wt profile slots rm`, `usage` | no: both work offline, with no server and no registry |
| `wt profile list` | no: it reports the static check only |

`modeltiers run` (which refreshes `tiers.toml`) runs on the model-server
host, not on the client's machine.

A launch (`-M profile/<name>`, or Enter on the profile's row in the picker)
does, in order: one catalog read; the profile resolved against it; one
register of the client session with every distinct slot model; a start job
for the one local slot model, only when it is stopped or known not to be
routed (a running model whose routing the server could not ask about is
never started); the command built against the gateway; the agent run with
the client session's heartbeat; and the client session ended when the agent
exits. There is no route write, proxy restart or stop picker: the server stops a model nobody uses after its idle grace
(`wt-model serve --idle-grace`). A failed register is one `wt: note:` line
and the launch goes on. Ctrl-C during the start detaches from the job, which
keeps running on the server (exit 2, `nothing was launched`).

The resolved rules that are the catalog's own:

- a slot id the catalog does not list blocks with
  `slot <slot>: <id> is not in the model server's catalog`;
- a native model blocks with
  `slot <slot>: <id> is a native model, which has no gateway route` (the
  gateway's origin and token never reach a native model);
- a slot model the server blocks blocks the profile with the server's own
  reason, after `slot <slot>: `.

When the catalog holds the reserved `profile/` prefix, a profile launch or
`show` stops with `the model server's catalog has … — slot profiles are
unavailable`, and the picker lists no profile row.

## Failures

Each of these stops before the agent starts. Nothing degrades to a launch
without the profile.

| Failure | What you see |
|---|---|
| The file does not parse, or a value has the wrong TOML type | No profile rows; one warning line with the TOML error |
| The file has a key wt does not know | The file loads without it; one warning line per key |
| The model server's catalog holds the `profile/` prefix | The profile launch is blocked with the holder named |
| A profile has no usable name or agent | No row for it; one warning line; listed by `wt profile list` |
| Two profiles share a name | One blocked row, `duplicate profile name` |
| A profile fails a check | A blocked row with the reason |
| No model server is configured | The launch refuses before anything starts (exit 1) |
| `--settings` or `--model` after `--` | `a slot profile sets --model itself: remove it from the arguments after --` |
| The local model's start is refused or fails | The start's own error |
| Ctrl+C with `-M profile/<name>` during the local model's start | Detaches from the start job, which keeps running on the server (exit 2, `nothing was launched`) |
| `args` or `wrapper` cannot be applied | The apply error |

In the picker the reason is the status line and you stay on the picker. With
a pin and no picker, wt exits non-zero with it.

## A local slot model and memory

This section is advisory. wt does not check or enforce any of it.

**What was seen, dated.** On 2026-10-14, Claude Code 2.1.299 ran through the
LiteLLM proxy on a 64 GB Mac (#393). One mtplx build of a 27B model
(`Qwen3.8-27B-MTPLX-Optimized-Quality`) served the `sonnet` and `haiku`
slots of a profile. Claude Code's first request carries a prompt of about
30k tokens, and prompts in the tests reached 41k. mtplx refused some of
these requests with HTTP 507:

```
insufficient memory: the Mac ran out of memory it can hand out without compressing during this prefill
```

Refusals recurred across the profile test and follow-up runs of two-turn
sessions; no per-run count is recorded here. Claude Code retried each
refused request, and every turn finished, but slowly: a one-line reply took
78 s on the `sonnet` slot and 163 s on `haiku`. In these tests omlx and
ollama gave no 507s on the same task, and neither did a different mtplx
build (a 35B-A3B model). These are observations from one machine and a few
sessions, not a benchmark. A follow-up run on 2026-10-15 showed that mtplx's
memory guard can refuse when the system's free memory is low even while
mtplx's own engine is within its limit, so how much else the machine holds
matters.

**What to do.** For a slot Claude Code sends its full prompt to, prefer an
omlx or ollama model unless you have measured the mtplx model on your own
machine. This is a warning, not a rule: wt does not stop an mtplx slot,
and in these tests every turn finished, only slowly. It is a preference,
not a guarantee about omlx: in the same spike it rejected prompts of its
own (400 `prefill_memory_exceeded`) while abandoned generations held its
memory (#399).

**Measured again on an idle machine, 2026-10-26 (#393).** Same Mac (64 GB),
mtplx 2.12.2, the same 27B build on the `sonnet` and `haiku` slots, and
nothing else local loaded (87 to 90 percent of memory free at the start of each
run). Each run started a cold mtplx server and ran two Claude Code turns
non-interactively: run the tests and fix a failing one, then add a test. The
heavy variant first made Claude read 118 KB of notes, so prompts reached 40k
to 47k tokens. One machine and a handful of runs, not a benchmark:

| mtplx profile | runs | refused requests | tests passed | largest prompt served |
|---|---|---|---|---|
| `sustained` (what wt starts) | 1 light, 3 heavy | none | all | 46.6k tokens |
| `turbo` | 1 light, 3 heavy | none | all | 45.6k tokens |
| `stable` | 2 light | every request | none | none |

- **`sustained` and `turbo` behaved alike**, with comparable turn times. The
  refusals above did not recur on an idle machine, at larger prompts than
  before. What produced them was most likely memory used by other things on
  the Mac at the time, not the size of the prompt alone. That is a hypothesis:
  it was not tested under memory pressure, so these runs do not show whether
  `turbo` copes better than `sustained` when memory is tight.
- **Do not use `stable` for a slot.** It does not chunk the prefill (mtplx's
  log shows `prefill_chunk_requested: null`), and a 15k to 17k token prompt
  needed 17 to 18.5 GiB of scratch memory on top of the 27.9 GiB of weights.
  mtplx refused it before any reply, with `a retry cannot succeed at this memory
  limit`. `sustained` and `turbo` chunk the prefill at 2048 tokens, and their
  scratch stayed between 0.4 and 1.2 GiB even at 46k tokens.
- wt starts every mtplx model with `--model-id`, which makes mtplx run
  `sustained`; to try `turbo` for one model, give it the flag
  ([serve_args](../wt-model.md#serve_args-extra-flags-for-an-mtplx-model)).

**How to check your own sessions.** Most refused requests leave no
failure row in the spend logs (only a few did in the runs above), and
`wt stats` has no failure column: it counts every logged request alike, so it does not
show refusals either way. Look in the proxy's error log, `~/.litellm.err.log`
(on the machine above the refusals were there and not in `~/.litellm.log`; see
[litellm-troubleshooting.md](litellm-troubleshooting.md#debugging-playbook-what-actually-worked)),
for `insufficient memory`, and in the log of the mtplx server wt started,
`/tmp/local-ai-setup-mtplx.log`, for its `memory guard` lines (`action`
`prefill_admission_shed`, `allocation_failure_shed`, `prefill_system_abort`).
Under each `Runtime contract verified — profile:` line the log says which
profile that server ran. `wt-model litellm list` shows which local models the proxy
routes.

## Limits

- Claude Code only. Another agent gets slot profiles when its driver
  implements `profiles.SlotCapable`.
- One distinct local model per profile.
- No slot can use the native Anthropic subscription: Claude Code takes one
  base URL.
- wt does not create or edit the file.
- If the profile's local model is stopped during a session, requests to
  that slot fail at the proxy until it runs again. wt does not detect this.
- A profile launch skips the `ollama list` pre-check, as every proxy launch
  does. An ollama slot model that is registered but not pulled launches, and
  that slot fails at ollama until the model is pulled.
- Claude Code assumes a 200K context window for an id it does not know. A
  profile can set `CLAUDE_CODE_AUTO_COMPACT_WINDOW` in its `env`.
