# wt slot profiles: one launch, several models

A wt launch of Claude Code names one model, and Claude Code uses several:
a main model, a small fast model for background work, and whatever `/model`
or a subagent switches to. wt redirects only the one it names. The others
keep their Anthropic ids, and the LiteLLM proxy rejects them with
`Invalid model name passed in model=claude-…`.

A **slot profile** fixes that. It is a named, hand-written entry that
assigns a registry model to each of an agent's model slots. It appears as a
row in the model picker, and you choose it like a model. Claude Code is the
only agent with slots today; its slots are `fable`, `opus`, `sonnet` and
`haiku`.

Slot profiles are not the automatic profiles of [profiles.md](profiles.md).
An automatic profile has no name, is never chosen, and is applied to a
single-model launch that matches it. A slot profile is its own launch: wt
never applies an automatic profile to it, and `wt profile on|off` does not
hide it.

Design: `docs/superpowers/specs/2026-10-13-wt-slot-profiles-design.md`
(monorepo root). Anthropic does not support routing Claude Code to
non-Claude models through a gateway. Slot profiles, like every non-native wt
launch of Claude Code, are outside that support.

## Quick start

1. Configure the proxy once, if you have not: `wt litellm set --url … --api-key …`.
   A slot profile always uses the LiteLLM proxy, whatever `wt litellm status`
   says about the on/off toggle.
2. Write `~/.config/agent-wt/slot-profiles.toml` (next section). Take the
   ids from `wt model list`.
3. Check it: `wt profile list`, then `wt profile show -A claude -M profile/<name>`.
4. Launch it: pick the `profile/<name>` row in the claude model picker, or
   pin it with `claude-wt -M profile/<name>`.

## The file

`~/.config/agent-wt/slot-profiles.toml`, beside `profiles.toml`. You write
it by hand. wt reads it and never writes it. A missing file means no profile
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
Go test reads, so this format and the parser cannot drift apart.

| Field | Rule |
|---|---|
| `name` | Required. Lowercase letters, digits and hyphens, starting with a letter or digit. Unique per agent |
| `agent` | Required. An agent whose driver supports slots (`claude`) |
| `slots` | Required table, slot name → registry id. At least one entry. Every key must be a slot the agent declares |
| `default` | Required. A slot that `slots` fills. wt passes it as `--model`, so the session starts on it |
| `env` | Optional, string → string |
| `args` | Optional string list |
| `config_content` | Optional free-form table, merged into the agent's settings. `config_content.modelOverrides` is how a profile supplies Claude Code's `modelOverrides` setting; wt does not validate its keys. Checked on 2026-10-13: `modelOverrides` is honoured behind the proxy; an id it names is sent as the id it maps to. |
| `wrapper` | Optional `{binary, args_template}` |

**Unfilled slots use the `default` slot's model.** The example fills three
of four slots; `fable` gets the `sonnet` model. No slot is ever left sending
an Anthropic id.

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

## When a profile is blocked

wt checks a profile twice. The file alone is checked when it is loaded
(static). The file against the registry, the live inventory and the
`[litellm]` table is checked every time rows are built, and again at launch
(resolved). A profile that fails either check still has a row. The row is
blocked, and its reason is the first failing rule.

Static reasons:

| Reason | Cause |
|---|---|
| `duplicate profile name` | Two profiles share a name for one agent. Neither can launch; the picker shows one blocked row |
| `slots is empty: assign at least one slot` | No `slots` entry |
| `slot "x" is not a slot of agent "claude" (slots: fable, opus, sonnet, haiku)` | A `slots` key the agent does not declare |
| `slot "opus" names no model` | An empty id |
| `default is missing` / `default "x" is not a slot this profile fills` | `default` must name a filled slot |
| `agent "claude" does not support mechanism "wrapper"` | A field outside the agent's allow-list |
| `mechanism "x" has no effect without "y" on the same profile` | The agent's driver requires field `y` whenever `x` is set, and the profile sets only `x`. No rule of this kind applies to Claude |
| `env sets ANTHROPIC_BASE_URL, which wt sets for the launch` | A reserved key (also `config_content.env sets …`) |
| `config_content.env must be a table` | `config_content.env` is a string, a number or an array. This is Claude's rule: Claude Code's settings take `env` as a table, and wt adds its own variables to it |
| `wrapper args_template [...] has no "{{args}}" placeholder` | The agent's own arguments would be dropped |

Resolved reasons:

| Reason | Cause |
|---|---|
| `slot haiku: <id> is not in the registry` | The id is not a registry model. This is what a profile shows after `wt cloud-sync` or `wt model rm` removed a model it names |
| `slot opus: <id> is a native model, which has no LiteLLM route` | A slot cannot use the native Anthropic subscription |
| `slot opus: <id> names provider "x", which is not in the registry` | The model's `provider_id` has no provider row |
| `slot opus: model "<id>" has location "…"; expected "local" or "cloud"` | The model's location does not resolve |
| `slot opus: <id> cannot be routed: provider "x" has no LiteLLM mapping` | wt has no route policy for the provider |
| `slot opus: <id> cannot be routed: wt has no LiteLLM route for this model` | The launch's route write would refuse the model, for a reason no row above names: for example a cloud model under a provider whose auth is `native`. The row asks the same function the route write does, so the two cannot disagree |
| `slots name two local models (<id> and <id>); a slot profile may name one` | More than one distinct local model |
| `a slot profile always uses the LiteLLM proxy, but no URL is configured — run 'wt litellm set --url ... --api-key ...'` | `[litellm]` has no url (or, with `no API key`, no key) |
| `slot haiku: <id> is not on disk or running`, or the pull hint `wt model list` gives | The local model has no row |
| `slot haiku: …` and the local model's own block reason | The local model cannot be started by wt |

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
  is taken when the registry has a provider with id `profile`, or a model
  whose id begins with `profile/` under any provider. wt then shows no
  profile rows and prints
  `wt: the registry has a provider with id "profile" — slot profiles are unavailable`,
  or
  `wt: the registry has a model whose id begins with "profile/" (<id>) — slot profiles are unavailable`.
  `wt profile list` prints the same sentence as a `warning:` line.
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

**Where the warning lines appear.** wt prints them to stderr once per launch
of an agent that has slots, before any picker opens. The picker's screen
then covers them, so a launch that went through the picker prints the same
lines again when the picker has closed: after the session, or when you quit.

## In the picker

A valid profile for the agent appears after the native rows and before every
other row, in file order. The screen below is an illustrative mock-up with
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
| LOC | `cloud`, `local`, or `mixed` |
| STATUS | `ok`. A start or blocked profile is shown as a start or blocked model row is, by its action and note |
| RUNNING | The local model's state (`run`, `load` or `-`), on a blocked row too, whichever rule blocked it; `-` for an all-cloud profile, for a profile that names two local models, for one whose local model is not in the registry or has no row of its own, and for the one row of profiles sharing a name |
| COST | The `default` slot model's price in force, with `~` when it is time-priced |
| 1D / 7D / 30D | Launches of this profile |
| In-use prefix, SURVEY | Blank |

- **The line under the table** lists the highlighted profile's slots in
  place of the `LiteLLM: on|off` line: the default first, marked `*`, then
  the rest in the agent's order, each with the model's provider-side name.
  It shows whole entries only; what does not fit becomes `+N more`.
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
claude-wt -W feat -M profile/mixed
```

- A pinned profile whose local model is idle starts it first, as a pinned
  model does. `--replace` lets that start stop a running model.
- A blocked pin: with `-W`, `--cwd` or outside a repository, wt exits
  non-zero with the reason. When the picker screens are showing, wt returns
  to the agent picker with the reason.
- A profile pinned under another agent, an unusable one, or any profile
  under `-T`/`-F` gets `model "profile/<name>" is not in the eligible list for agent "<agent>"`.

Without `-M`, a launch that skips the picker never chooses a profile.
`wt start`, `wt smoke` and rotation ignore slot profiles.

## What a launch does

In order. A failure at any step stops the launch before Claude Code starts,
and nothing is recorded.

1. Check the profile again against fresh state.
2. Refuse passthrough arguments that would replace the slot mapping.
3. Write the LiteLLM route of each cloud slot model that has none.
4. Start the local model if it is not running. The start writes the model's
   route and restarts the proxy.
5. Write the local model's route if it has none: for a model that was already
   running, and as a check after a start, whose own route write is not allowed
   to fail the start.
6. Restart the proxy once, if a route was written and no start restarted it.
7. Wait for any proxy restart still in flight, then check that the proxy
   answers. The check refuses the launch if the most recent restart wt
   attempted in this process failed.
8. Build the command (next section).
9. Apply the profile's `args` and `wrapper`, for an agent that allows them.
10. Run the agent.

**The proxy check.** Both launch paths call `lifecycle.WaitPendingRoutes`
immediately before `lifecycle.ProbeProxy`, so the probe never runs while a
restart is still in flight. `internal/lifecycle` remembers the outcome of the
most recent proxy restart attempt in the wt process. When that restart failed
(its command failed, or the proxy did not answer afterwards), the probe
refuses the launch with `the last LiteLLM proxy restart failed, so routes
written since may not be live: <cause>`. The proxy that was not restarted is
usually still up and answers its liveness check, while it rejects every route
written since it booted, so the plain probe cannot see this. The refusal
stands until a later restart succeeds or a new wt process starts.

**A profile launch restarts the proxy at most once**, and not at all when
every slot's route already exists. A local model started in step 4 stays
running if a later step fails.

**Quitting mid-launch.** Ctrl+C in the picker while a profile launch is
under way quits wt, starts no agent and records nothing. A route the launch
had already written is published before wt exits: wt restarts the proxy once
and waits for it, so no route is left in `config.yaml` that the proxy never
loaded. That holds while the routes are being written, on the replace
dialog, and while the profile's local model is starting. Quitting with a
second Ctrl+C while a cancelled start is still stopping does not wait for
that start; wt restarts the proxy itself, and the proxy can restart a second
time if the start also gets that far.

**After a restart that failed.** The refusal in step 7 lasts for the wt
process that saw the failure. A new wt process finds every route in place,
writes nothing, restarts nothing, and the old proxy answers the check; the
slot whose route was written but never loaded then answers
`Invalid model name`. Restart the proxy by hand before trying again:
`launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`, or your
`WT_LITELLM_RESTART_CMD`.

**Passthrough arguments.** wt refuses `--settings` and `--model` after
`--`, in either form (`--model opus` or `--model=opus`). Either would
replace the slot mapping. Everything else after `--` is passed on unchanged.

## What Claude Code is given

Process environment:

| Variable | Value |
|---|---|
| `ANTHROPIC_BASE_URL` | The `[litellm]` url |
| `ANTHROPIC_AUTH_TOKEN` | The `[litellm]` api key |
| `ANTHROPIC_API_KEY` | Empty |

Removed from the inherited environment: every `ANTHROPIC_DEFAULT_*`
variable, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL`. A
stray shell export of one of them could send a tier to a model the profile
never chose. A profile that wants a subagent model sets
`CLAUDE_CODE_SUBAGENT_MODEL` in its own `env`. Checked on 2026-10-13 with
Claude Code 2.1.296: an inherited `ANTHROPIC_SMALL_FAST_MODEL` does not
override `ANTHROPIC_DEFAULT_HAIKU_MODEL`; wt removes it all the same.

Arguments: the yolo flag when `--yolo` is given, `--model <default slot>`
(an alias such as `sonnet`, so `/model` inside the session switches between
the profile's slots), and `--settings '<json>'`: one inline JSON object.
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
| `refcount.jsonl` | One line per distinct slot model, for the session's process |
| LiteLLM spend | Nothing new. The proxy logs each request under the slot model's registry id |

`wt stats` shows a profile as a row of its own, with its launches and `-`
in the spend columns. Its spend is on the slot models' rows. wt does not
attribute spend back to a profile.

After the session: wt prints the summary line naming the profile, releases
the refcount lines, and offers the stop picker as after any non-native
launch. The stop picker is not limited to the profile's models. There is no
survey and no price notice for a profile session.

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
profile it lists as `ok` has a row then. A file that does
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

The proxy key is masked as `wt litellm status` masks it. A blocked profile
prints `reason: …` and the command exits non-zero.

`wt profile on|off` and `wt profile status` govern automatic profiles only.

## Failures

Each of these stops before the agent starts. Nothing degrades to a launch
without the profile.

| Failure | What you see |
|---|---|
| The file does not parse, or a value has the wrong TOML type | No profile rows; one warning line with the TOML error |
| The file has a key wt does not know | The file loads without it; one warning line per key |
| The registry has a provider `profile`, or a model id that begins with `profile/` | No profile rows; one warning line |
| A profile has no usable name or agent | No row for it; one warning line; listed by `wt profile list` |
| Two profiles share a name | One blocked row, `duplicate profile name` |
| A profile fails a check | A blocked row with the reason |
| LiteLLM has no url or key | A blocked row with the `wt litellm set` hint |
| `--settings` or `--model` after `--` | `a slot profile sets --model itself: remove it from the arguments after --` |
| A route cannot be written, or `config.yaml` is missing | `slot <s>: LiteLLM route for <id> not written: <cause>` |
| The local model's start is refused or fails | The start's own error |
| Ctrl+C with `-M profile/<name>`, too late to stop the local model's start | `started <id>; launch cancelled`. The model stays running; Claude Code does not start |
| The proxy restart fails or times out | The restart's error. Routes already written stay for the next attempt |
| The most recent proxy restart failed | `the last LiteLLM proxy restart failed, so routes written since may not be live: <cause>`. The launch is refused until a later restart succeeds or a new wt process starts |
| The proxy does not answer | `the LiteLLM proxy does not answer at <url>: <cause>` |
| `args` or `wrapper` cannot be applied | The apply error |

In the picker the reason is the status line and you stay on the picker. With
a pin and no picker, wt exits non-zero with it.

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
