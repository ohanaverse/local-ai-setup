# wt slot profiles: selectable multi-model profiles for Claude Code — design

Date: 2026-10-13
Research: a six-agent read of the wt profiles, picker, claude launch path and LiteLLM code, plus the Claude Code documentation, on 2026-10-13; the draft was then reviewed against the code by four reviewers, each finding re-checked (no issue tracks this work yet)

## Problem

A wt launch of Claude Code names exactly one model. Claude Code uses several.

- **Only the main model is redirected.** wt sets `ANTHROPIC_BASE_URL`, a token and one `--model`. It sets none of `ANTHROPIC_DEFAULT_{FABLE,OPUS,SONNET,HAIKU}_MODEL`, so the other tiers (background tasks, subagents, `/model` switches) keep their Anthropic ids.
- **The proxy rejects those ids.** Between 2026-10-01 and 2026-10-13 the LiteLLM spend table recorded 610 failed Anthropic-format requests for ids such as `claude-haiku-4-5-20251001`, `claude-haiku-5-5` and `claude-sonnet-5`, each answered `Invalid model name passed in model=…`.
- **Existing profiles cannot fix it.** A `[[profiles]]` entry in `profiles.toml` has no name, is never chosen by the user, and applies automatically when one launched model matches its location, provider or id. Its only placeholder, `{{model_name}}`, is the provider-side name, which the proxy does not know. `wt/docs/wt-agents/profiles.md` records this as the reason the slot mapping was left out.
- **Nothing in the picker is more than one model.** Every row in the selection screen wraps one registry model, and every step after it (route, start, rotation, refcount, stats) takes one model id.

## Goal

A second kind of profile, a **slot profile**: a named, hand-written collection that assigns a registry model to each of an agent's model slots. Slot profiles appear as rows in the wt selection screen for the matching agent and are chosen by the user. Claude Code is the first agent.

**Success criteria**

1. A valid slot profile for `claude` appears in the claude selection screen after the native rows and before every other row.
2. Selecting it launches Claude Code through the LiteLLM proxy with every slot mapped to a registry id. Slots may name models from different providers, and cloud and local models may be mixed.
3. After a profile session that switches slots with `/model` and runs a subagent, the proxy error log has no new `Invalid model name passed in model=claude-*` line.
4. A profile whose one local model is not ready starts it through the existing start flow, then launches.
5. A profile launch causes at most one proxy restart, and none when every slot's route already exists.
6. `-M profile/<name>` launches the same profile without choosing it in the picker. On the non-TUI path a pin of a blocked profile exits non-zero with the reason.
7. A slot profile that cannot be fully applied never launches. No failure degrades to an unprofiled launch.
8. wt writes no settings file for a Claude slot-profile launch.
9. Automatic profiles, single-model launches, rotation, `wt start` and `wt smoke` behave as before.
10. No test touches the real config home or restarts the real proxy.

## Decisions

Each of these was settled with the owner during design.

| Decision | Choice |
|---|---|
| Endpoint | Always the LiteLLM proxy, whatever the `[litellm]` toggle says |
| Local models | At most one distinct local model per profile; it may fill several slots |
| Local model not ready | The row is a start row: start it, then launch |
| Session model | Each profile names a `default` slot; wt passes that alias as `--model` |
| Reach | The picker and a `-M profile/<name>` pin. Rotation, `wt start` and `wt smoke` ignore slot profiles |
| Architecture | A profile row is its own launch unit, not a model with extras attached |
| Storage | A separate file, `slot-profiles.toml` |
| Delivery (claude) | One inline `--settings` JSON; nothing written to disk |
| Other mechanisms | `env`, `args`, `config_content` and `wrapper` are supported, under each agent's existing allow-list |
| Automatic profiles | Never applied to a slot-profile launch |
| Failure posture | Fail closed |

Two alternatives were rejected. Attaching a profile to one "anchor" registry model fails because no single anchor fits: the start flow needs the local model, `--model` needs an alias, and usage would be recorded as an ordinary launch of the anchor. A synthetic registry model with id `profile/<name>` fails in `ResolveRoute` with "unknown provider", and marking it native or id-less makes the launch path skip the profile layer.

## Terms

- **Slot.** One of an agent's named model tiers. Claude's slots, in the driver's declared order, are `fable`, `opus`, `sonnet` and `haiku`.
- **Slot profile.** A named entry in `slot-profiles.toml` assigning registry ids to slots. Called "profile" below where no confusion is possible.
- **Automatic profile.** An existing `[[profiles]]` entry in `profiles.toml`, matched to a launch by location, provider or model.
- **Registry id.** A model's `id` in `registry.toml`, such as `ollama/glm-5.3-flash:cloud`. It is the proxy's `model_name` for that model's route.
- **Non-TUI path.** A launch with `-W`, `--cwd` or outside a repository, where `resolveModel` picks the model and the picker never opens.

## The file

`~/.config/agent-wt/slot-profiles.toml`, resolved the same way as `profiles.toml`. It is hand-authored. wt reads it and never writes it. A missing file means no profile rows.

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

The model ids above are illustrative.

| Field | Rule |
|---|---|
| `name` | Required. Lowercase letters, digits and hyphens. Unique per agent |
| `agent` | Required. Must be an agent whose driver supports slots |
| `slots` | Required table, slot name → registry id. At least one entry. Every key must be a slot the agent declares |
| `default` | Required. A slot name that `slots` fills |
| `env` | Optional, string → string, as in `profiles.toml` |
| `args` | Optional string list, as in `profiles.toml` |
| `config_content` | Optional free-form table, as in `profiles.toml` |
| `wrapper` | Optional `{binary, args_template}`, as in `profiles.toml` |

**Unfilled slots fall back to the `default` slot's model.** A profile that fills two of four slots is valid, and no slot is left sending an Anthropic id.

**Mechanisms follow the agent's allow-list.** The four optional fields are checked against the driver's `ProfileMechanisms()`, exactly as automatic profiles are. Claude declares `env` and `config_file`, so a Claude slot profile may use `env` and `config_content`; `args` and `wrapper` are rejected for it. This design does not widen any driver's list.

**Reserved env keys.** A profile's `env` may not set a key wt owns for the launch. For Claude these are `ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, and every `ANTHROPIC_DEFAULT_<SLOT>_MODEL` and `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME`. The driver declares the list. The same keys are rejected under `config_content.env`.

## Validation

Validation runs in two stages, because half the rules need live state.

**Static, at load** (the file alone):

- the file parses;
- `name` is well formed and unique per agent;
- `agent` is known and its driver supports slots;
- every `slots` key is a declared slot, and `slots` is not empty;
- `default` names a filled slot;
- every used mechanism is allowed for the agent, and a mechanism's required partner is present;
- no reserved env key is set;
- a `wrapper` has `{{args}}` in its `args_template`.

**Resolved, when rows are built** (the file plus the registry, the live inventory and the wt config):

- every slot id is a model in the registry;
- no slot model is native;
- every slot model's provider has a LiteLLM route policy;
- the slots name at most one distinct local model;
- LiteLLM is configured: `[litellm]` has both a url and an api key, the test `ResolveRoute` applies. The blocked reason reuses the existing `wt litellm set` wording;
- the local slot model, if any, is launchable or startable (next paragraph).

**The local slot model is judged by the catalog's own row policy.** wt builds a `catalog.Row` for it from the same inventory snapshot the picker uses, independent of the agent's eligible list and of `-T`/`-F`, and reads the verdict from that row:

| The local model's own row | Profile row |
|---|---|
| No row (confirmed not on disk, a stopped `mlx_lm_server` pairing, no inventory entry) | Blocked, with `catalog.MissingReason`, or `slot <s>: <id> is not on disk or running` when that is empty |
| `Action()` is `ActionBlock` (its provider has no start backend) | Blocked, with `slot <s>: ` and the row's `BlockReason()` |
| `Action()` is `ActionStart` (not running, still loading, or presence unknown) | Start row |
| `Action()` is `ActionLaunch` | Launch row |

An all-cloud profile that passes the other rules is a launch row.

**A failure blocks one row, not the feature.** A profile with a well-formed name and a known, slot-capable agent that fails either stage still produces a row, blocked, with the first failing rule as its reason, for example `slot haiku: ollama/gemma4:31b-cloud is not in the registry`. This covers a slot left dangling after `wt cloud-sync` or `wt model rm` removes a model. Three cases cannot produce a usable row:

- **No usable name or agent.** A profile whose `name` is missing or malformed, or whose `agent` is missing, unknown or without slot support, produces no row and cannot be pinned. `wt profile list` reports it by its position in the file, and wt prints one warning line when rows are built.
- **Duplicate names.** When two or more profiles share a name for one agent, none is launchable. The catalog emits one blocked row `profile/<name>` with the reason `duplicate profile name`, so row ids stay unique.
- **A file that does not parse** yields no rows; wt prints one warning line with the parse error.

A static error in one profile never affects another profile or any automatic profile.

## The catalog and its consumers

**Row shape.** `catalog.Row` gains an optional profile field. A profile row has the id `profile/<name>`, carries the profile and its resolved slot models, and holds no `config.Model`. Its launch, start or block verdict comes from resolved validation, not from `Row.Action()`'s location and provider logic.

**Rows are opt-in.** `catalog.Build` emits profile rows only when its input carries the loaded slot profiles, and then only those whose `agent` equals the input's agent. Two callers set that input: the TUI agent picker (`buildRows`) and `resolveModel`. `wt start` (`localRowsSnap`) and `wt smoke` (`smoke.walk`, which builds one catalog per agent) never set it, so they see no profile rows. Gating on the agent alone would not be enough, since smoke passes one.

**What each consumer does with a profile row:**

| Consumer | Rule |
|---|---|
| `renderTable`, `pickerBlockedReason` | Do not call `ResolveRoute` on a profile row. Its block reason is the validation reason |
| `launchableModels` | Excludes profile rows, so rotation and the single-launchable auto-launch never return one |
| `catalog.Find` | Resolves `profile/<name>` to the profile row |
| `resolveModel` | Uses a profile row only to resolve a `-M profile/<name>` pin |

**The non-TUI path reaches profiles by pin only.** With no `-M`, that path is unchanged: it auto-launches a single launchable model or advances rotation, and never considers a profile.

**Filters.** Under `-T` or `-F` profile rows are hidden, as discovered models are.

**`profile` is a reserved id prefix.** If the registry has a provider with id `profile`, wt emits no profile rows and prints one warning; the registry rows keep their ids.

## The picker row

**Order.** `sortRows` gains a middle band: native rows, then profile rows in file order, then every other row in today's order.

**Columns.** A profile row uses the table's existing vocabulary. No existing row changes.

| Column | Profile row |
|---|---|
| In-use prefix | Blank. Refcount lines are recorded under slot model ids only |
| FAMILY | `profile` |
| MODEL | `profile/<name>` |
| LOC | `cloud` when every slot model is cloud, `local` when every one is local, otherwise `mixed` |
| STATUS | `ok`. A start or blocked profile is shown the way a start or blocked model row is: through the row's action and its trailing note, not a new STATUS word |
| RUNNING | The local model's `runningText` value (`run`, `load` or `-`); `-` for an all-cloud profile |
| COST | The `default` slot model's prices in force, formatted as `costCell` formats them, including the `~` suffix when that model is time-priced |
| 1D / 7D / 30D | Launches of this profile |
| SURVEY | Blank |
| Trailing note | The start or block note only. No `(via proxy)` note, since every profile uses the proxy |

**Slots on highlight.** While a profile row is highlighted, the slot list replaces the mode line under the table. It adds no line to the frame.

- It lists the slots the profile fills, the default first and marked `*`, the rest in the driver's declared order, each with the model's provider-side name.
- It shows whole entries only. Entries that do not fit the inner width are replaced by `+N more`, following the mode line's "whole or not at all" rule. `wt profile show` gives the full list.
- The `LiteLLM: on|off` text and the price note are not shown for a profile row.
- On a blocked row the line shows the reason.

```
  FAMILY   MODEL                 LOC    STATUS  RUNNING  COST            7D
  claude   claude/native         cloud  ok      -        -               41
> profile  profile/ollama-cloud  cloud  ok      -        0.40/0.04/1.60   6
  profile  profile/mixed         mixed  ok      -        0.15/0.02/0.60   2
  profile  profile/broken        cloud  ok      -        -
  glm      ollama/glm-5.3-fl...  cloud  ok      -        0.40/0.04/1.60  12

  opus* glm-5.2:cloud · fable kimi-k3:cloud · sonnet glm-5.3-flash:cloud · +1 more
```

The screen above is an illustrative mock-up with some columns omitted.

**Enter.** A launch row launches. A start row runs the existing start flow for the profile's local model, including the replace dialog when its server is occupied, then launches. A blocked row shows its reason and stays on the picker.

**Marker and starting cursor.**

- The `> ` marker sits on the last-picked row, which may be a profile row.
- When the last-picked row is a profile row that is present and not blocked, the cursor starts on it. Profiles do not rotate.
- Otherwise the cursor follows today's rule, read from the rotation pointer: the next actionable registry model after the pointer, else the first actionable row in sort order.

**The single-row shortcut.** The TUI's auto-launch fires only when the table has exactly one row. It counts every row, so any profile row, blocked or not, keeps the picker open.

**The pin.** `-M profile/<name>` and the picker read the same row and reach the same verdict. What a blocked pin does depends on the path, as for a blocked model pin today:

- on the non-TUI path, wt exits non-zero with the reason;
- when the TUI handles the pin, it returns to the agent picker with the reason as its status.

A pin may start the profile's local model, as a pin of a single idle model may today. A profile pinned under a different agent gets the existing "not in the eligible list" message.

## The launch flow

A profile launch runs these steps in order. A failure at any step aborts: the TUI returns to the picker with the reason, and the non-TUI path exits non-zero. A local model started in step 4 is left running after a later abort.

1. **Re-resolve.** Run resolved validation again against fresh state.
2. **Check passthrough args.** If the user's arguments after `--` contain `--settings` or `--model`, abort naming the flag. Either could replace the slot mapping.
3. **Write the cloud slots' missing routes, restart deferred.** Each distinct cloud slot model gets an add-if-missing change. Record whether any write changed `config.yaml`.
4. **Start the local model if its row is a start row.** The TUI uses the existing start flow; the pin path uses the existing pin start. Both go through `lifecycle.Start` with one target. `Start` writes the local model's route and restarts the proxy itself; when step 3 changed `config.yaml`, it is told a restart is owed, so its one restart also publishes the cloud routes.
5. **Ensure the local route when no start ran.** A local model that was already running gets its add change, restart deferred. (Amended after the whole-branch review, 2026-10-13: the same ensure also runs after a step-4 start, because the start's own route write only prints a failure; a row the start already wrote is no change and costs no restart.)
6. **Restart once if still owed.** When steps 3 or 5 changed `config.yaml` and no start restarted the proxy, wt restarts it once and waits.
7. **Probe the proxy.** wt checks that the proxy answers, on every launch, including one that restarted nothing. No answer aborts, naming the proxy url. (Amended after the task and whole-branch reviews, 2026-10-13: wt first waits for any proxy restart still in flight, and the probe also refuses the launch while the most recent restart attempt in this wt process failed, since routes written since may not be live.)
8. **Build the command** through the driver's slot build (below).
9. **Apply `args` and `wrapper`**, for agents that allow them, through the slot-profile applier (below).
10. **Run the agent**, then do the post-exit bookkeeping.

### New lifecycle seams

The existing launch-time route check cannot do steps 3 to 7. It is single-model, always restarts on change, and by contract never fails a launch. The profile path needs three additions to `internal/lifecycle`:

- **A multi-model route ensure.** It takes the distinct slot models, builds each change with `modelRouteChange`, applies each with `restartDeferred`, and returns errors and whether anything changed. Three results the existing check treats as non-failures must be returned as errors: a missing `config.yaml` (`litellm.ErrMissing`), a row's own write error (`Outcome.Err`), and a model `modelRouteChange` declines to route. `litellm.ErrRegistryRedirected` and a lock timeout are errors too. The changes cannot be folded into one `litellm.Change`, because `AddMissingOnly` is a flag on the whole change and cloud and local slots need different values.
- **A restart-owed option on `lifecycle.Start`.** A new `Options` field seeds the internal `restartOwed` state. The profile launch sets it only when step 3 changed `config.yaml`; setting it always would force a restart when every route already exists.
- **A restart that reports failure.** The existing `bounceProxyAsync` prints a failed restart command or a readiness timeout as a warning. The profile path needs a call that returns them, and a proxy probe that returns its result.

In the TUI the route writes, the restart wait and the probe run off the update goroutine, as the start flow's waits do.

### The driver seam

Slot support is an optional interface on an agent driver, beside `ProfileCapable`:

- the slot names the agent accepts, in display order;
- the env keys the agent reserves;
- a slot build. It takes the resolved slot models (unfilled slots already set to the default's model), the default slot, the proxy origin and key, the `yolo` flag, and the profile's `env` and `config_content`, and returns the command.

The launch flow gives the result the treatment `BuildLaunchCmdInfo` gives a single-model launch: binary lookup, the driver's `ClearEnv` filtering, the worktree directory and `PWD`, and the user's passthrough args appended unchanged.

An agent without the interface cannot have slot profiles. Adding slot profiles to another agent means implementing this interface, with no change to the catalog, the picker or the launch flow.

### The Claude slot build

- **Process env:** `ANTHROPIC_BASE_URL` set to the proxy origin, `ANTHROPIC_AUTH_TOKEN` set to the `[litellm]` api key, `ANTHROPIC_API_KEY` set empty. The proxy route is built here directly and does not consult the `[litellm]` toggle.
- **Removed from the inherited env:** every `ANTHROPIC_DEFAULT_*` variable, `ANTHROPIC_SMALL_FAST_MODEL` and `CLAUDE_CODE_SUBAGENT_MODEL`. A stray shell export of any of these could send a tier to an id the profile never chose. A profile that wants a subagent model sets `CLAUDE_CODE_SUBAGENT_MODEL` in its own `env`.
- **The yolo flag**, when set, as the single-model build emits it.
- **`--model <default slot>`**, for example `--model opus`. Because it is an alias, `/model` inside the session switches between the profile's slots.
- **`--settings '<json>'`**, one inline JSON object:
  - an `env` block holding `ANTHROPIC_DEFAULT_<SLOT>_MODEL` for all four slots, each a registry id;
  - in the same block, `ANTHROPIC_DEFAULT_<SLOT>_MODEL_NAME` for each slot, set to the model's provider-side name, so Claude Code's own `/model` picker shows a readable name;
  - in the same block, `ANTHROPIC_BASE_URL` and an empty `ANTHROPIC_API_KEY`, so an `env` block in the user's own settings files cannot redirect the session;
  - the profile's `env` and `config_content`, merged as described next.

**Merge rule.** `config_content` merges into the settings object by the rule automatic profiles use (`profiles.mergeInto`): tables merge key by key, recursively; a scalar or an array replaces the value whole. Within the `env` block, precedence from low to high is the profile's `env`, then `config_content.env`, then wt's own variables. wt's variables cannot collide with the others, since their keys are reserved.

**The token stays out of argv.** `ANTHROPIC_AUTH_TOKEN` is set in the process env only, not in the `--settings` payload, so the proxy key does not appear in the process list. A user settings file whose `env` block sets `ANTHROPIC_AUTH_TOKEN` would override it, as it would for every non-native wt launch today.

`config_content.modelOverrides` is how a profile supplies Claude Code's `modelOverrides` setting. wt gives it no special treatment and does not validate its keys.

**wt writes no file.** For Claude the profile's `config_content` travels only in `--settings`. `settings.local.json` is never written, so an interrupted session leaves no proxy-only ids behind for a later bare `claude` run in that worktree. The existing self-heal of a leftover `settings.local.json` from an earlier automatic-profile session still runs before a profile launch.

**Claude Code itself may write one.** Per its documentation, choosing a model with `/model` during a session saves that choice as the `model` field of `~/.claude/settings.json`. wt does not prevent this. Profile launches are unaffected, because `--model` outranks the saved value, but a later bare `claude` run starts on the saved alias.

### The slot-profile applier

A slot-profile launch never reaches `applyProfileForLaunch`. Both existing run functions call it unconditionally (`runAgentCmd` in `cmd/wt`, and `runAndWaitCmd` through the `tui.ProfileApplier` closure), so the profile launch has its own applier:

- `cmd/wt` calls it directly; the TUI receives it as a second closure beside `ProfileApplier`, since `internal/tui` cannot import `cmd/wt`.
- It applies a hand-built `ResolvedProfile` that carries only `ExtraArgs` and `Wrapper`. Its `ConfigContent` stays empty, because `config_content` is delivered by the slot build.
- It fails closed: an apply error aborts the launch. The existing `applyResolvedProfile` degrades to an unprofiled launch on error, so the applier is a fail-closed variant of it, not a direct call.
- It prints no `apply? [Y/n]` prompt and resolves no automatic profile.

### What must be checked on real Claude Code first

Several things the design relies on are documented but untested on this setup. The implementation plan starts by checking them against the installed Claude Code:

1. An `env` block inside an inline `--settings` payload is honoured.
2. `--model <alias>` with `ANTHROPIC_DEFAULT_<SLOT>_MODEL` set sends that registry id to the proxy.
3. After a session that switches slots and runs a subagent, the proxy log has no new `Invalid model name … claude-*` line.
4. What two `--settings` flags on one command line do: merge, or the last one wins.
5. Whether an inherited `ANTHROPIC_SMALL_FAST_MODEL` overrides `ANTHROPIC_DEFAULT_HAIKU_MODEL`.
6. What `/model` writes to `~/.claude/settings.json` during a profile session.

**If check 1 fails**, the slot variables, the `_NAME` variables and the profile's `env` are set as process env instead, and `--settings` carries only `config_content`. Nothing else in the design changes.

**If check 3 fails**, the ids that still escape are recorded, and the plan decides whether a wt-written `modelOverrides` entry or another variable closes the gap before the feature is built on it.

**Check 4** decides whether step 2 of the launch flow must refuse a passthrough `--settings`. If the two merge, the refusal is relaxed to `--model` only.

The same session records whether `modelOverrides` has any effect behind the proxy. The Claude Code documentation describes it for Bedrock, Vertex and Foundry and does not say. The result decides how the docs describe `config_content.modelOverrides`; the feature does not depend on it.

Anthropic states that it does not support routing Claude Code to non-Claude models through a gateway. This feature, like every non-native wt launch of Claude Code, is outside that support.

## Bookkeeping

| Record | What a profile launch writes |
|---|---|
| Usage event | One event under `profile/<name>`, written with `usage.Store.RecordFor` directly. It feeds the row's 1D / 7D / 30D columns |
| Rotation pointer | Nothing. `rotation.state` stays one bare registry id |
| Last-picked row | `profile/<name>`, in a new one-line file `last-picked.state` beside `rotation.state` |
| Refcount | One line per distinct slot model, for the session's pid |
| Spend | Nothing new. LiteLLM logs each request under the slot model's registry id |

**The last-picked file is the one addition to existing state.** `rotation.state` is a single plain-text line that `Last()` returns whole, so it cannot hold a second value without breaking an older wt binary reading it. Today that one value is both the rotation position and the picker's marker. A profile launch must move the marker without moving the rotation position.

- A profile launch writes `last-picked.state` and the usage event, and does not call `rotation.RecordFor`, which writes the pointer and the usage event together.
- A single-model launch writes both files, so a stale profile marker never survives it.
- A missing `last-picked.state` falls back to `rotation.state`, so existing state keeps working.

**`wt stats` shows profile launches and model spend separately.** A profile appears as its own row with its launch count and no spend. Its spend lands on the slot models' rows. This version does not attribute spend back to a profile.

**After the session exits.** The post-exit functions take one `config.Model` today; a profile launch does this instead:

- the summary line names the profile and the duration;
- the refcount lines are released, then the stop picker runs, as it does after any non-native launch. It is not filtered to the profile: it offers every running local model no live session uses;
- the per-model price notice is skipped;
- the survey is skipped. This is a new skip condition beside the existing native and id-less ones.

## Failure handling

Every one of these stops before the agent starts.

| Failure | Result |
|---|---|
| `slot-profiles.toml` does not parse | No profile rows; one warning line with the parse error |
| A profile has no usable name or agent | No row for it; one warning line; reported by `wt profile list` |
| Two profiles share a name for one agent | One blocked row, `duplicate profile name` |
| A profile fails static or resolved validation | Blocked row with the reason |
| The registry has a provider with id `profile` | No profile rows; one warning line |
| LiteLLM has no url or no api key | Blocked row |
| Passthrough args contain `--settings` or `--model` | Abort naming the flag |
| A slot's route cannot be written, or `config.yaml` is missing | Abort naming the slot and the cause |
| The local model's start is refused or fails | Back to the picker with the start flow's error |
| The proxy restart fails or times out | Abort. Written routes stay for the next attempt |
| The proxy does not answer the probe | Abort naming the proxy url |
| `args` or `wrapper` cannot be applied | Abort with the apply error |

**Not handled in this version.**

- If the profile's local model is stopped during a session despite the refcount, requests to that slot fail at the proxy until the model runs again. wt does not detect or repair this.
- A profile launch, being a proxy launch, skips the ollama availability pre-check, as single-model proxy launches do. An ollama slot model that is registered but not pulled therefore launches, and that slot fails at ollama until the model is pulled.

## Commands

- **`wt profile list`** gains a slot-profile section: agent, name, and `ok` or the static error. The section is produced independently of the automatic section, so it prints when `profiles.toml` is missing, empty or invalid. It needs no running provider.
- **`wt profile show -A <agent> -M profile/<name>`** keeps the command's existing shape. A `-M` value with the `profile/` prefix selects the slot-profile branch, which does not depend on `profiles.toml` loading. It builds the agent's catalog rows from one inventory probe and looks the row up with `catalog.Find`, so it reports the picker's verdict. It prints the resolved slots, the env and the `--settings` payload, with the proxy token redacted. A blocked profile prints its reason and exits non-zero. It starts no model, writes no route and restarts nothing.
- **`wt profile on|off` and `wt profile status`** govern automatic profiles only. Turning automatic profiles off does not hide slot profiles.

## Out of scope

- Slot profiles for agents other than Claude Code.
- Rotation over slot profiles, and slot profiles in `wt start` and `wt smoke`.
- More than one distinct local model in a profile.
- A slot served by the native Anthropic subscription. Claude Code takes one base URL, and native models have no proxy route.
- Direct (non-proxy) profile launches.
- Attributing spend to a profile in `wt stats`.
- Creating or editing slot profiles from `wt` (a command or a `wt config` tab).
- Allowing `args` or `wrapper` for Claude.
- Setting a context window for slot models. Claude Code assumes 200K for an id it does not recognise; a profile can set `CLAUDE_CODE_AUTO_COMPACT_WINDOW` in its `env`.

## Testing

**Unit tests**, table-driven, with no live provider:

| Area | What is pinned |
|---|---|
| Load and static validation | Every rule, one case per error message; a parse error; a profile with no usable name or agent; duplicate names |
| Resolved validation | Unknown id, native slot, provider without a route policy, two distinct local models, LiteLLM missing url or key, and each row of the local-model verdict table |
| Catalog rows | Rows only with the opt-in input; agent match; hidden under `-T`/`-F`; the reserved prefix; `catalog.Find` on a pin |
| Catalog consumers | `smoke.Candidates` returns no profile row; `launchableModels` excludes them; `renderTable` does not call `ResolveRoute` on one; the non-TUI path without a pin is unchanged |
| Sort and cursor | The native → profile → rest bands; the three starting-cursor cases. The existing native-first tests pass unchanged |
| Claude slot build | Process env, removed inherited variables, yolo, the `--model` alias, passthrough args appended, and the `--settings` JSON as a golden file: fallback fill, `_NAME` variables, merge precedence, no token in the payload |
| Route ensuring | A fake restarter counts restarts: zero when every route exists; exactly one for several missing cloud routes; exactly one for an idle local model plus a missing cloud route; each new error case is returned |
| No file written | A Claude profile with `config_content`, run through the build and the applier: no `settings.local.json` in the worktree, and the scratch config home unchanged |
| Automatic profiles skipped | With a `profiles.toml` entry matching a slot model, a profile launch neither resolves it nor prompts |
| Bookkeeping | Usage event under the profile id; `rotation.state` byte-identical; `last-picked.state` written; a single-model launch writes both; a missing `last-picked.state` falls back; one refcount line per distinct slot model |
| Commands | `wt profile list` with `profiles.toml` missing and invalid; `wt profile show` for an ok, a start and a blocked profile, with the token redacted |
| Fail closed | Each row of the failure table ends with no agent command run |

**Screen fit, automated.** In-process tests through `Update` and `View()`, as the existing ones are: profile rows are added to the fixtures `TestEveryListPhaseFitsTheTerminal` measures, and a test pins the slot line (including the `+N more` cut) and a blocked row's reason at 80x24, 40x12 and 120x50.

**Real screens, manual, before merge.** Captured at 80x24 from a throwaway pty driver that answers the OSC 11 query: profile rows in place, the slot line, a blocked row, and a start row going through the start flow.

**Contract fixture.** `docs/contracts/slot-profiles.sample.toml`, read by a Go contract test, so the documented format and the parser cannot drift apart.

**Isolation.** Tests run under a scratch config home. The existing CI step `bin/check-config-dirs-untouched` confirms nothing touched the real `agent-wt` or `litellm` directories. No test restarts the real proxy. Any ad-hoc run against a scratch registry sets `WT_LITELLM_CONFIG` as well.

**Acceptance on the real stack**, run once at the start (the checks above) and again at the end:

1. Launch an all-cloud profile, switch slots with `/model`, and run a subagent. The proxy log gains no `Invalid model name … claude-*` line, and the spend table shows requests under each slot's registry id.
2. Launch a mixed profile whose local model is idle, with one cloud route removed beforehand. The model starts, the proxy restarts once, and the session works.
3. With that session open, a second session's stop picker does not offer the local model.
4. With `-W`, `-M profile/<name>` launches the same profile, and a pin of a blocked profile exits non-zero with the reason.

## Documentation

In the same change:

- a slot-profiles page under `wt/docs/wt-agents/`, with a pointer from `profiles.md` and from the claude page; the `profiles.md` caveat about the slot mapping is replaced by that pointer;
- the internals notes in `wt/docs/internals/` for the launch flow, the new lifecycle seams and the picker row;
- the usage playbooks in `docs/guides/`, including the lines that describe `rotation.state`, with examples labelled illustrative, since guides must not embed live model state;
- `wt/CLAUDE.md` and the root `CLAUDE.md`, for the two new files and their owner.

## Check results

Run on 2026-10-13 with Claude Code 2.1.296 (Claude Code) against the LiteLLM proxy at http://localhost:4000.

| # | Check | Result | Evidence | Decision |
|---|---|---|---|---|
| 1 | `env` in an inline `--settings` payload is honoured | pass | Slot variables only in the payload: `--model sonnet` gave `ollama/glm-5.3:cloud \| anthropic_messages \| success \| 1`, `--model opus` gave `ollama/glm-5.2:cloud \| anthropic_messages \| success \| 1`; `new rejections: 0` in both runs | primary path |
| 2 | `--model <alias>` sends the slot's registry id | pass | Slot variables in the process env, one `success` row each and `new rejections: 0`: `fable` reached `ollama/kimi-k3:cloud`, `opus` `ollama/glm-5.2:cloud`, `sonnet` `ollama/glm-5.3:cloud`, `haiku` `ollama/deepseek-v4.1-flash:cloud` | proceed |
| 3 | No new `Invalid model name … claude-*` line | pass | One interactive session, 4 slot switches (`/model opus`, `/model haiku`, `/model fable`, then `sonnet` from the `/model` list) and one subagent run (`Explore`, answered 2 files): `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 4`, `ollama/glm-5.2:cloud \| anthropic_messages \| success \| 1`, `ollama/glm-5.3:cloud \| anthropic_messages \| success \| 9`, `ollama/kimi-k3:cloud \| anthropic_messages \| success \| 1`; `new rejections: 0`. Each switch asked `Switch model?` and needed a confirmation | primary path |
| 4 | Two `--settings` flags | last wins | First payload maps only sonnet, second only haiku. `--model sonnet`: exit 1, `(no route) \| acompletion \| failure \| 1`, `new rejections: 1`, id `claude-sonnet-5-5`. `--model haiku`: `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 1`, `new rejections: 0` | refuse both |
| 5 | Inherited `ANTHROPIC_SMALL_FAST_MODEL` | does not | Process env set it to `ollama/glm-5.3-flash:cloud`, which no run reached. Print mode `--model haiku`: `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 1`; print mode `--model sonnet`: `ollama/glm-5.3:cloud \| anthropic_messages \| success \| 1` only (no background request); interactive `--model sonnet`: `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 2` and `ollama/glm-5.3:cloud \| anthropic_messages \| success \| 1`; `new rejections: 0` throughout | documentation only |
| 6 | What `/model` writes to `~/.claude/settings.json` | writes `"model": "sonnet"` | The file had no `model` field before; the `diff` after the session was the one line `>   "model": "sonnet",` (the alias picked last, not a registry id; it read `"model": "fable"` mid-session after `/model fable`). Each switch printed `Set model to <registry id> and saved as your default for new sessions`. The `/model` list showed the payload's display names (`glm-5.2:cloud` Custom Opus model, `kimi-k3:cloud` Custom Fable model, `glm-5.3:cloud` Custom Sonnet model, `deepseek-v4.1-flash:cloud` Custom Haiku model), plus a `Default (recommended)` row reading `currently ollama/glm-5.2:cloud[1m]`, which was not picked. One other Claude Code session was open during the run | documentation only |
| – | `modelOverrides` behind the proxy | effective | With no slot mapping `--model sonnet` was rejected: exit 1, `new rejections: 1`, id `claude-sonnet-5-5`. With `modelOverrides: {"claude-sonnet-5-5": "ollama/glm-5.3-flash:cloud"}`: exit 0, `ollama/glm-5.3-flash:cloud \| anthropic_messages \| success \| 1`, `new rejections: 0` | documentation only |

### Acceptance

Run on 2026-10-13 with wt built from feat/wt-slot-profiles at 0352d94 and Claude Code 2.1.296 (Claude Code), against the LiteLLM proxy at http://localhost:4000. The model ids are those of the run date. The sessions were driven through pseudo-terminals from scrubbed shells (no `ANTHROPIC_*` or `CLAUDE*` variable), 120x40 for the sessions and 80x24 for the screen captures; one other wt session (`claude/native`, not through the proxy) stayed open throughout and holds one refcount line of its own. The local model was `ollama/gpt-oss:20b`.

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | An all-cloud profile: `/model` switches and a subagent | pass | Picked `profile/accept-cloud` in the picker; no `apply?` prompt; argv held `--model sonnet --settings {"env":{` and no token; no `settings.local.json`. Four switches (`opus`, `haiku`, `fable`, `sonnet`, each confirmed at `Switch model?`) and one subagent run (`Explore`, answered 2 files): `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 3`, `ollama/glm-5.2:cloud \| anthropic_messages \| success \| 1`, `ollama/glm-5.3-flash:cloud \| anthropic_messages \| success \| 8`, `ollama/kimi-k3:cloud \| anthropic_messages \| success \| 1`; `new rejections: 0`; `restarts=0`; `last-picked.state` read `profile/accept-cloud`; `rotation.state` unchanged (`claude/native`). Exit line `wt: claude · profile/accept-cloud · 2m46s`, no survey, no price notice |
| 2 | A mixed profile, local model idle, one cloud route removed | pass | The route of `ollama/glm-5.3-flash:cloud` was removed from `config.yaml` and the proxy restarted by hand (`NOT ROUTED`, proxy pid 83697 to 68623). Enter on `profile/accept-mixed` showed `Starting ollama/gpt-oss:20b — warming the model`, then `updating LiteLLM routes`, then Claude Code, 16 seconds in all. `restarts=1` (proxy pid 68623 to 68819); both ids `routed`; RUNNING went from blank to `run`. `ollama/glm-5.3-flash:cloud \| anthropic_messages \| success \| 1`, `ollama/gpt-oss:20b \| anthropic_messages \| success \| 2`; `new rejections: 0`. Whether the running proxy served an id was read from these rows, not from `/v1/models`, because the run did not read the proxy key |
| 3 | A second session's stop picker does not offer the in-use local model | pass | The mixed session held two refcount lines with one pid: `{"pid":68741,"model_id":"ollama/glm-5.3-flash:cloud"}` and `{"pid":68741,"model_id":"ollama/gpt-oss:20b"}`. A second session (`-M profile/accept-cloud`, no picker, four lines of its own) exited with no stop picker: the next line was `wt: claude · profile/accept-cloud · 21s`. The first session's exit then asked `Stop running local models? (none are in use by another wt session)` and listed `1  ollama/gpt-oss:20b`; choosing it printed `Stopping ollama/gpt-oss:20b... done` and `wt: claude · profile/accept-mixed · 1m44s`. Afterwards RUNNING was blank and no refcount line of either session was left |
| 4 | `-W -M profile/<name>` launches; a blocked pin exits non-zero | pass | `wt -W accept-pin -A claude -M profile/accept-cloud` opened Claude Code in `.worktrees/accept-pin` with no picker: `ollama/deepseek-v4.1-flash:cloud \| anthropic_messages \| success \| 2`, `ollama/glm-5.3-flash:cloud \| anthropic_messages \| success \| 1`; `new rejections: 0`; `restarts=0`; no `settings.local.json`; `wt: claude · profile/accept-cloud · 55s`. The blocked pin's last stderr line was `wt: slot haiku: ollama/wt-accept-not-in-registry is not in the registry`, `exit=1`, no spend row, `new rejections: 0` |
| – | Real screens at 80x24: rows, slot line, blocked row, start flow | pass | `picker/01-rows`, `02-slot-line`, `03-start-row`, `04-blocked-row`, `05-blocked-enter`; `start/01-before` to `05-after`; `start-fine/01-t0` to `09-t18`. Every frame is 24 lines and no line wraps. The native row is first, then the three profile rows in file order, with `> ` on `profile/accept-cloud`. At 80 columns the table keeps MODEL, STATUS and RUNNING and drops FAMILY and LOC, as it does for every row, so `profile` and `mixed` were read in the 120-column sessions of checks 1 and 2. Cut with an ellipsis: the RUNNING heading (`RUNN` and an ellipsis) and the `(via proxy)` note of three openrouter rows. Slot line: `sonnet* glm-5.3-flash:cloud · fable kimi-k3:cloud · +2 more` for `accept-cloud`, `sonnet* glm-5.3-flash:cloud · haiku gpt-oss:20b` for `accept-mixed`, the whole reason `slot haiku: ollama/wt-accept-not-in-registry is not in the registry` for `accept-blocked`, which Enter left on the picker. Start flow: `Starting ollama/gpt-oss:20b — warming the model (1s)` with `[esc] cancel`, then Claude Code's input box; the model was warm from check 2 and both routes existed, so the start screen lasted under two seconds and the `updating LiteLLM routes` stage was seen only at 120x40 in check 2 |
