# `wt-model model …`

The models in the registry (`~/.config/local-ai/registry.toml`): list them,
add, edit and remove them. The commands are `wt-model model …`, run on the
machine that runs wt-model; under `wt` the same subcommand names (`model
init`, `model list`, `model add`, `model edit`, `model rm`, their names
before phase 4 stage 2) print a one-line pointer here and exit 1, and only
`wt model list --server` stays in wt. wt never downloads a model and never
deletes weights; it registers what a provider already has, or will have.

```bash
wt-model model                    # the Models tab, on the host (needs a terminal); see below
wt-model model list [--json]      # every registry model, and every local model found on this machine
wt model list --server [--json]   # the model server's catalog, as a client session sees it
wt-model model init [--json]      # create the registry if it is missing; add the default provider rows
wt-model model add <provider> <name> --family F [--tags a,b] [--location local|cloud] [price flags] [--id ID]
wt-model model edit <id> [--family F] [--tags a,b] [--location local|cloud] [--source manual|curated] [price flags]
wt-model model rm <id>... [--yes]
wt-model alias list|set|rm ...    # registry aliases and priority groups; see "wt-model alias" below
wt-model model add mlx_lm_server <target> --draft <draft> --family F   # a target+draft pairing
```

`wt-model served` and `wt-model warm` (what a provider serves, and loading a model
before a run) also live in `wt-model`; see [wt-start-stop.md](wt-start-stop.md).

Each of the three writing commands makes one locked write of the registry and
then syncs the LiteLLM routes once. The exit code is 0 when the write
succeeded, even if the sync could only warn; it is 1 for a value that cannot
be used, an unknown or duplicate id, an id that more than one registry row
carries (`edit` and `rm`), or a registry that cannot be read; it is 3 for a
`rm` that needed approval and got none (no `--yes` and no terminal, or the
question was declined) and removed nothing.

Bare `model` under wt opens nothing: it prints `wt: model moved to wt-model — run
'wt-model model'; nothing was changed` and exits 1. The Models tab it used to
open is `wt-model model` ([below](#the-models-tab-wt-model-model)).

## `wt-model model list [--json]`

One row per registry model, then one per local model a provider has that the
registry does not (`STATUS new`), from one live probe of the providers.

| Column | Values |
|---|---|
| `STATUS` | `ok` — a cloud model, or local weights the probe found; `missing` — a registered local model that is not on disk; `unknown` — the provider could not be asked, wt has no probe for it, or the row's location does not resolve; `new` — on disk or pulled, not in the registry; `-` — an mlx_lm_server pairing, which is never enumerated |
| `RUNNING` | `run`; `load` (omlx is still loading it); blank; `?` when the probe could not tell |
| `SIZE` | the weights' size when the probe reports it (ollama); `-` otherwise |
| `PATH` | the model's directory (omlx, mtplx) or its `fetch.local_path`; `-` otherwise, including when the only directory found belongs to another organization's model of the same name, and when omlx has that name in more than one directory |

The text table has no borders and fits the terminal: `PATH` is shown only when
every row fits on one line with it, and `SIZE` only while it leaves `MODEL` at
least 20 columns (or all its ids need, when that is less). A model id is never cut — one too long for its column gets a
line of its own, above its cells. When a column is left out, a note on stderr
says so. Into a pipe every column is printed, one model per line.

A row whose `fetch` or `draft` is malformed in the registry — the value is not
a table, or its `repo` or `local_path` is not a string — is still listed, and
wt reads that value as absent: the row has no path (so a model meant to be
found at a `local_path` reads `missing`), or a pairing has no target or draft.
After the table, one line per such row on stderr says so, in the registry's
order:

```text
omlx/mine: fetch is not a table; wt reads it as absent (fix the entry in /Users/you/.config/local-ai/registry.toml)
mlx_lm_server/T+draft-D: fetch.repo is not a string, draft is not a table; wt reads it as absent (fix the entry in /Users/you/.config/local-ai/registry.toml)
```

The path is the registry wt read, in full. The table on stdout is the same
with or without the line, and the command still exits 0. A hand-typed
`fetch = "~/models/mine"` is the usual cause; what was meant is

```toml
[models.fetch]
local_path = "~/models/mine"
```

A `cost.time_prices` row that breaks the registry's rules is named the same
way, after the `fetch` and `draft` phrases of its model (illustrative):

```text
openrouter/mine: cost.time_prices[0]: windows[0]: start must be HH:MM, got 9:00; wt reads it as absent (fix the entry in /Users/you/.config/local-ai/registry.toml)
```

The model picker does not apply such a row, at any hour: it shows the
model's own price
([wt-cloud-sync.md](wt-cloud-sync.md#a-row-you-write-by-hand) has the rules;
a window whose `end` is before its `start` breaks none, it runs past
midnight). The listing itself shows no price.

`--json` always has everything: `registry` (the file), `models` (each with
`id`, `family`, `provider_id`, `model_name`, `location`, `tags`,
`registered`, `status`, `running`, `size_bytes` and `path` — the last two
`null` when wt does not know them — `target` and `draft` for a pairing,
`serve_args` for an mtplx model that carries them (left out otherwise), and
`malformed`, an array of the phrases above (`"fetch is not a table"`,
`"draft.local_path is not a string"`, `"cost.time_prices[0]: windows[0]:
start must be HH:MM, got 9:00"`) that is always there and empty for a
row with nothing malformed), and `providers` (each probed provider's status:
`ok`, `partial`, `unreachable`). Stdout is that one document; the stderr
lines for malformed rows are printed in this mode too.

The text listing names a model's `serve_args` under the table, one
`<id>: serve_args <args>` line each, since the table has no column for them.

It runs on a registry that has a gap in it (a model whose provider has no
row, a mistyped location): that is what the list is for. Only a registry that
cannot be read at all stops it.

### `wt model list --server [--json]`

With `--server`, `model list` (in wt, on a session machine) reads the model server's catalog
(`[model-server]` in `config.toml`, or `WT_MODEL_URL`) instead of the
registry: the models a client session can launch or start, in the server's
order, with their state. Nothing is read from the registry and no provider is
probed. Without `--server`, the same `model list` only points at `wt-model model
list` (exit 1).

| Column | Values |
|---|---|
| `MODEL` | the model id |
| `STATE` | `running`, `loading`, `stopped`, `blocked` or `unknown` (the full state words; the registry listing's `run`/`load` are its own) |
| `LOC` | `local` or `cloud` |
| `TIER` | the advisory tier, `-` when the model has none |
| `CLIENT SESSIONS` | how many client sessions have registered the model |

A blocked row's reason is not in the table; `--json` has it.

**`--server --json` is not the registry's `model list --json`.** The registry's
document (`registry`, `models`, `providers`; `docs/contracts/model-list.sample.json`)
is what modeltiers reads and is unchanged. `--server --json` prints the
session's catalog view, `{"version": 1, "schema": N, "generated": "…",
"providers": {…}, "rows": […]}`, where each row is the server's own row (state,
action, `block_reason`, tier, price, client session count);
`docs/contracts/model-list.server.sample.json` is its fixture. When the
command fails it prints `{"version": 1, "error": {"message": "…", "code":
"…"}}` on stdout instead, writes nothing to stderr and exits 1.

## `wt-model model init [--json]`

Creates the registry when it is missing and adds the provider rows wt needs
and the registry lacks. It never changes a row that exists. `wt-model model init
--help` lists which rows, and why. The `sushi` row is written when the `sushi`
command is on PATH:

```toml
[[providers]]
id = "sushi"
name = "Sushi"
location = "local"
model_dir = "~/.sushi/models"

[providers.auth]
type = "none"
base_url = "http://127.0.0.1:12345/v1"
```

It has no `protocols` and no `secret_ref`.

## `wt-model model add <provider> <name> --family <family>`

`<name>` is the model as its provider lists it: an ollama tag (`qwen3:8b`), the
name of an omlx model directory, an mtplx `org/name`, a Sushi pack `org/pack`
(`beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, written to `model_name`
exactly as given), or a cloud provider's model id. Pull or download a local model with its provider's own tool, before
or after adding it; `wt-model model list` shows what is on disk and unregistered as
`STATUS new`.

| Flag | Meaning |
|---|---|
| `--family` | required; what `-F` filters on |
| `--tags a,b` | what `-T` filters on |
| `--location local\|cloud` | only when the model differs from its provider's location |
| `--input-price`, `--cache-price`, `--output-price` | $ per million tokens |
| `--subscription-price`, `--subscription-period month\|year` | a subscription price needs a period |
| `--id` | the id, instead of the derived one: `<provider>/<name>`, with no spaces |
| `--context-window N` | the model's context window in tokens (#626); see [the window](#a-models-context-window) |

A cloud model that `wt-model cloud-sync` covers (any `openrouter` model, an ollama
model with `--location cloud` or a cloud tag) is written with `source =
"manual"`: cloud-sync keeps it even if the source it mirrors does not list it,
and sets it to `curated` once it does ([wt-cloud-sync.md](wt-cloud-sync.md#models-you-add-by-hand)).

The id is derived: for a local provider it is the id wt already lists the
model under when it finds it unregistered (`ollama/qwen3:8b`,
`omlx/<directory>`, `mtplx/<org>/<name>`, `sushi/<org>/<pack>`; an `omlx-6bit` model is `omlx/…`), so
registering a model does not rename it. For any other provider it is
`<provider>/<name>` with each `/` in the name written `--`
(`openrouter/qwen--qwen3.8-27b`). The id, the provider and the name cannot be
changed afterwards: usage history, rotation and launch profiles key on the id.
`wt-model model add sushi beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw --family <family>`
registers `sushi/beamster/Qwen3.8-Flash-Next-Sushi-STQ-2.75bpw`, the id
llmbench's `eval-sushi-vs-ollama` suite names; only a cloud id spells its slash
`--`.

In the same write wt adds the default provider row the model needs when it is
missing — for ollama, omlx, mtplx, rec, sushi and openrouter (openrouter's holds no key,
only `auth.secret_ref = "OPENROUTER_API_KEY"`, the environment variable wt
reads the key from) — and any other row `wt-model model init` would add. A provider
wt has no default row for is refused: add its `[[providers]]` block to
`registry.toml` first. A second entry for an artifact that is already
registered is refused too. A value that cannot be used is refused before
anything else happens.

For an ollama model wt runs `ollama show <name>` once, against the address of
the registry's ollama provider, and records `model_info.supports_function_calling`
and `model_info.supports_vision` when ollama lists the capability; wt copies
`model_info` into the model's LiteLLM route. If the lookup fails the model is
added without them and a warning says so. wt looks nothing up for any other
provider, a Sushi pack included: a `model_info` table (such as
`supports_function_calling`) is written by hand in `registry.toml`.

### Claude subscription models

The `claude-subscription` provider serves Claude models on the user's own
Claude subscription, through the gateway's header mode (#597;
[wt-model-server.md](wt-model-server.md#subscription-passthrough) has the
host switch, the hazards and the install order). Add one with
`wt-model model add`:

```bash
wt-model model add claude-subscription claude-haiku-5-5 --family claude --context-window 1000000
```

- **The id is the bare Claude id**, equal to the Anthropic API id
  (`claude-haiku-5-5`): no provider prefix, no date suffix, no `[1m]`. Only
  this provider takes a slash-less id; a name with `/`, `[`, `]`, a space or
  a control character is refused.
- **`--context-window` is required** for this provider (refused otherwise:
  `a claude-subscription model needs --context-window (Claude Code's window
  for it)`), and `wt-model model edit` refuses to clear it. 1000000 is the
  window of each of the current ids `claude-fable-5-2`, `claude-opus-5-5`,
  `claude-sonnet-5-5` and `claude-haiku-5-5`.
- **`--location local` is refused**, on `add` and on `edit`
  (`a claude-subscription model is served over a cloud route, so it cannot be
  local: leave --location off, or pass cloud`): the provider is cloud and its
  route the keyless `anthropic/` row, so a local mark could never be served.
- **`--family` is required as for any model.** Anthropic publishes no
  family for these ids; `claude` is only this page's choice, and any family
  of your own works.
- The provider row (`Claude subscription`, `auth.type = "client-login"`, no
  `base_url`, no key) is added in the same write. It is left out of the
  Models tab's form, which has no context-window field.
- No price and no `source` key are written, and `wt-model cloud-sync` never
  touches the provider. LiteLLM books its own list price for such a row
  (the route's zero cost is not honoured by LiteLLM 1.109.1; seen on a live
  check), and Claude Code shows list price too; neither is what your plan
  charges.
- The route is a keyless `anthropic/<id>` row in `config.yaml` (a cloud
  row, so adding or removing one restarts the proxy), routed only while the
  proxy shows none of the hazards the host checks. An alias over one is a
  database row, like any alias.
- The rows are offered only to `claude`, and an alias with a subscription
  target is claude-only. The subscription is the user's own login only.
- **Usage credits.** Depending on the plan and seat tier, a Fable row (and
  other large models) may need usage credits or count against the plan's
  limits; Claude Code's `/model` picker can show "Requires usage credits".
  Check the plan before pointing a slot at one.

See what is registered with `wt-model model list` on the host, or
`wt model list --server` from a session.

### An mlx_lm_server pairing

An mlx_lm_server model is a target and a draft served together by
`mlx_lm.server --draft-model`. `wt-model model add mlx_lm_server <target> --draft
<draft> --family <family>` registers one: each side is a Hugging Face repo
(`org/name`) or a local path (starting `/`, `~`, `./` or `../`), written to the
row's `[models.fetch]` and `[models.draft]`. Anything else is taken for a
repo, a bare relative directory such as `out/Big-4bit` included — when a
directory of that name is in the working directory, a `note:` on stderr says
what it was registered as and how to register the directory instead
(`./out/Big-4bit`). A `./` or `../` path is stored as the absolute path it
names from the directory the command ran in, so the row means the same
directory to llmbench, which runs from `llmbench/`. A `~` path is stored as
typed. The id is
`mlx_lm_server/<target>+draft-<draft>`, from the last segment of each; when
that would not be a usable id (a directory name with a space in it), the add
is refused and `--id mlx_lm_server/<name>` names it.

A pairing is its two sides, not their names. The same target and draft
again are refused, whatever `--id` says and however a local path is spelled
(`~/quant/Big-4bit`, the absolute path of the same directory, a trailing
slash). A different pairing whose sides end
in the same two names — a target you quantized into `/quant/Big-4bit` beside
`mlx-community/Big-4bit`, with one draft — would get the id that is taken:
the add says so, and `--id mlx_lm_server/<name>` registers it.

wt registers a pairing and cannot start one. `add` prints the command that
does, and so does a launch or `wt start` of a pairing that is not running:

```bash
llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>
```

(from the repository: `uv run --directory llmbench llmbench provider isolate
--solo …`; `--solo` leaves the other local providers running; a path with a
space in it is printed quoted, so the line can be pasted). A pairing is a
row of `wt-model model list` and of the Models tab with `STATUS -`, since no probe
can enumerate it; its family, tags and prices are edited like any model's,
and the tab's form does not create one.

A pairing row's `RUNNING` is not proof that this pairing is the one being
served ([#299](https://github.com/ohanaverse/local-ai-setup/issues/299), open).
The server lists the Hugging Face repo or path it serves, never a pairing's
name, and nothing records which target and draft a running `mlx_lm.server`
was started with:

- With exactly one pairing registered, anything served on the mlx_lm_server
  port marks that pairing `run` — a different, unregistered pairing started
  with `llmbench provider isolate` included. `wt-model model add mlx_lm_server`
  makes this case easy to reach: one add, and the registry has exactly one.
- A pairing whose name is the end of a served repo id reads `run` as well.
- With two or more registered and no name matching, `RUNNING` is `?`.

wt shows the state and syncs the pairing's LiteLLM route by it, as it did
before this command existed; `wt-model model add`, `edit` and `rm` decide nothing by
it. When it matters which pairing is up, check
`/tmp/local-ai-setup-mlx-lm-server.log` or the server's `/v1/models`.

## `wt-model model edit <id> [flags]`

Changes the fields named by the flags, and nothing else in the row: every
other key (off-peak prices, `model_info`, `fetch`, keys wt does not know) is
kept as it is. A flag left out leaves its field alone; an empty value clears
it (`--tags ""`, `--input-price ""`), and an empty `--location` makes the
model inherit its provider's. `--source manual|curated` sets the row's
source; it is for models `wt-model cloud-sync` covers (an `openrouter` model, an
ollama cloud model) and is refused for any other. Use `--source manual` on a
cloud model you added by hand before cloud-sync knew the mark, so the next sync
does not offer to remove it. `--context-window N` sets the window (`0` removes the key). No flag at all is a usage error. It does not
change `pricing_updated_at`. When nothing changes it says `no change` and
syncs nothing.

### A model's context window

`context_window` is an optional whole number of tokens on a `[[models]]` row
(`wt-model model add|edit --context-window N`; `0` on `edit` removes it; a
negative is refused, and a hand-written value that is not a whole number reads
as absent, it never fails the load). It is the one way to give a **local**
model a window, which `tiers.toml` never records. The model server sends the
window as the catalog row's `context`: the registry value when set (any
location), else, for a cloud model, the tiers file's window when it is the
model's own host window. Claude Code reads it to declare
`CLAUDE_CODE_MAX_CONTEXT_TOKENS` ([claude-wt.md](wt-agents/claude-wt.md#context-window-of-a-gateway-model)).
Other agents do not use it yet.

### `serve_args`: extra flags for an mtplx model

`--serve-arg` sets the extra arguments `mtplx serve` gets for one model, after
the ones wt passes itself; repeat it, **one argv element per flag**, so a flag
and its value are two: `--serve-arg --profile --serve-arg turbo` (or
`--serve-arg=--profile --serve-arg=turbo`). `--clear-serve-args` removes them.
They are stored as `serve_args = ["--profile", "turbo"]` on the model's row,
and `wt-model model add mtplx … --serve-arg …` writes them with the row.

- **mtplx models only.** Any other provider refuses them, naming the provider.
- **wt's own flags are refused**, alone or as `--flag=value`: `--model`,
  `--port`, `--host` and `--model-id`. wt's route, probes and stop path assume
  the port, host and model name it passed, and it reads the model's name back
  from `--model-id` of a server still loading. An empty argument is refused
  too.
- **They apply from the next start.** wt cannot see the flags a running server
  was started with, so a changed `serve_args` does not make it a different
  server; the edit says so. `wt stop` then `wt start` picks them up.
- **A row whose `serve_args` is unusable** (hand-edited: not an array of
  strings, or one of wt's flags) loads, `wt-model model list` names the problem like
  any other malformed value, and `wt start` of that model stops before
  spawning, naming the row, rather than ignoring it.
- **`--model-id` and turbo.** wt always passes `--model-id <name>`, which makes
  mtplx skip its per-model promotion to the turbo profile, so a model that
  mtplx would run in turbo runs in `sustained` under wt. Ask for it with
  `--serve-arg --profile --serve-arg turbo`. llmbench starts mtplx from its own
  spec and does not apply `serve_args`.

Two things follow from an edit of a price or of a subscription field, and
only from one. Clearing a model's last price removes its `[models.cost]`
table with it. And a row still in the old cost layout (`kind =
"per_token"` with `price_per_million_tokens`, or `kind = "subscription"` with
`price_per_period` and `period`) is moved to the current keys in the same
write: a table that held both layouts would carry two prices for one
model.

An id that more than one registry row carries is refused, with nothing
written and no sync: wt cannot tell which row you mean, and there is no flag
to choose one. The message names each row's provider, in the file's order,
and the file to repair:

```text
wt: model "ollama/qwen3:8b" is in the registry twice (providers ollama, openrouter); wt cannot tell which one you mean — fix the entry in /Users/you/.config/local-ai/registry.toml
```

Give one of the rows another id, or delete it, by hand. The rows beside it,
each with an id of its own, are edited as usual.

## `wt-model model rm <id>... [--yes]`

Removes the rows from the registry, and nothing else: it prints where each
removed model's weights are, because wt deletes none. A local model that is
still on disk shows up again in `wt-model model list` as `STATUS new`. It asks on
the terminal first (`--yes` skips; with no terminal and no `--yes` it refuses,
removing nothing, with `approval missing: pass --yes (there is no terminal to
ask); nothing was removed` and exit 3).
When one id is not in the registry, none is removed.

An id that more than one registry row carries is refused in the same way,
before the question and with the message `wt-model model edit` gives: none of the
ids named is removed, whichever of them it is. Removing one of two rows that
share an id is a hand edit — only there can you see which row is which.

A model a registry alias targets is refused too, before the question: the
message names the aliases (`model is an alias target: model "<id>" is a
target of alias/<x>; change or remove the alias first (wt-model alias
set|rm)`), and nothing is removed. The Models tab's `d` says the same.

## `wt-model alias`

A registry alias is a gateway id `alias/<name>` (`<name>` matches
`^[a-z0-9][a-z0-9.-]*$`) that a slot profile, pi or codex names like a model
and that routes to one model, or to several in priority order (a group: a
lower `order` is tried first). Aliases live in a top-level `[[aliases]]`
array of `registry.toml`, never in `[[models]]`: `id` and
`targets = [{ model = "<registry id>", order = N }, ...]`, and nothing else
(a `notes` key, or any unknown key, marks the alias malformed, and wt freezes
it: reserved words are not accepted yet). `order` left out is the target's
position, from 1. A target names exactly one registry model; no alias or native
model is a target; at most one target is local. An alias id never equals a
model id, and provider id `alias` and model ids under `alias/` are refused.

```bash
wt-model alias list [--json]
wt-model alias set <alias/name> --target ID[:ORDER]... [--accept-smaller-window] [--dry-run]
wt-model alias rm <alias/name>... [--yes] [--force-in-use]
```

`list` needs neither a model server nor the proxy: ALIAS, TARGETS
(`order:model`), CONTEXT (the smallest window over all targets, `-` when any
is unknown), SESSIONS (client sessions of the running model server; `-` when
no server record answers; a record that cannot be read, or one whose server
is not running, is one note, `wt-model: note: could not read client sessions
from the model server (<why>) — no alias is counted as in use`, and the
command goes on with no counts, so `rm` then needs no `--force-in-use`; a
server that is running but cannot be asked is one note ending `it is running
(pid N), so an alias is not removed without --force-in-use`, and `rm` then
needs `--force-in-use`, exit 3 without it) and ROUTED (`yes`, `no`, or `?` when the proxy
pair is not set or the database cannot be read). `--json` prints one
document, [alias-list.sample.json](../../docs/contracts/alias-list.sample.json),
with `null` for an unknown `context`, `sessions` or `routed`. An alias the
registry cannot use is listed with a warning. No output carries a route's
parameters.

`set` creates the alias or replaces its **whole** target list. A `--target`
whose id is a registry model is read whole (an ollama tag such as
`ollama/qwen3:8b` holds a colon); otherwise a digit suffix after the last
colon is the order. It prints `set alias: <id>` or `no change: <id>`. A set
that makes the alias's window smaller, or unknown where it was known, asks
y/N (default No) and names the client sessions using the alias (they keep the
window they launched with); `--accept-smaller-window` answers yes. Without a
terminal it refuses: `approval missing: pass --accept-smaller-window (there is
no terminal to ask); nothing was changed`, exit 3. `--dry-run` prints the
targets, the window change and each database row the write would plan (row
id, target, order and the 8-character hash `h8`, never the parameters),
changes nothing and writes no row at all when no proxy pair is set. The rows
and the targets left without one (a stopped local model, a Claude subscription
model while its hazard check is blocked) come from the same planner as the
write, so the preview says what the write does. When the database half cannot
run (the proxy does not answer, `store_model_in_db` is off) the preview says
so instead of rows, in the write's own words.

`rm` removes the aliases and, through the sync that follows, their rows; the
models they target are not touched. It asks on the terminal (`--yes` skips;
without either it refuses, exit 3). An alias that client sessions use also
needs `--force-in-use`. When one id is not an alias none is removed.

Exit codes of `set` and `rm`: 0 done (also when the routes could not be
written: a warning says so), 1 a plain error (a bad target, a refused alias,
a failed registry write), 3 approval missing, nothing changed.

**The routes.** After the registry write one route sync runs. Each alias
becomes one LiteLLM database row per live target (a cloud target always, a
local one while it runs), `model_name` the alias id and the target's own
parameters plus `order`; rows are added before the old ones are deleted, so a
re-point has no gap and no proxy restart. Targets with equal `order` share
the requests (measured on LiteLLM 1.109.1: two equal-order targets each served
part of 30 requests, no even split promised); a lower order is tried first. A cloud target's resolved key is stored, encrypted by LiteLLM,
in its alias row (never printed). Aliases are never written to `config.yaml`.
Detail: [internals/litellm-routes.md](internals/litellm-routes.md#alias-rows).

**When the database cannot be reached** (no proxy pair, `store_model_in_db`
off, a failed list, the deadline) the registry write still happens, exit stays
0, nothing is written to the file, and one warning says:

```text
alias routes unavailable (<reason>); registry written, LiteLLM not updated; run 'wt-model litellm sync' when the proxy answers
```

`alias rm` prints it too, when the rows could not be deleted. Existing alias
rows survive in the database and keep routing. The repair is any full sync:
`wt-model litellm sync` (serve also runs one at start). Until then, while the
gateway does not list the alias id, its catalog row reads `start` when its
lowest-order local target runs (the alias is unrouted) or must start (the start
re-runs that target's route write, which adds the alias's rows); else `block`
when any target is servable but the alias is not routed; else `start` when no
target is servable but any target can start; else `block`. The one
exception to the warning: with no `config.yaml` at all (no LiteLLM set up on
this machine) the write runs no sync and prints nothing.

**Renaming** is not a command. The sequence: `wt-model alias set alias/new
--target ...` with the same targets, then `wt profile slots set` on each
session machine to name the new id, and later `wt-model alias rm alias/old`
once `alias list` shows no SESSIONS on it (rm refuses an alias in use without
`--force-in-use`).

**Upgrade order.** An older `wt-model` refuses every registry write once
`[[aliases]]` exists, and ignores alias rows (its start, stop and sync neither
add nor remove them). Run `make install` and restart the serve LaunchAgent
before the first `alias set`; edit the plist if its program changed, then
`launchctl bootout` and `bootstrap` it: a `kickstart` alone keeps the old
loaded definition. Update the server before the clients: an older server sends
a local row's `context` from the tiers file, so a newer client against it may
declare a window the registry never set.

An alias with no live target has no rows: LiteLLM answers its unknown-model
error and a warning says `alias/<x> has no live target`. A target that is
only unresolvable (a cloud key missing from serve's environment) keeps its
existing row and warns. Rows are compared by the hash stamped on them, so a
hand edit in the LiteLLM UI that keeps the stamp is not detected. Two aliases
with one id fail the registry load, the one case where a bad alias does.

**Launching.** `-M alias/<name>`, the picker and slot profiles list and launch an alias like a model. A launch with no pin (and rotation) never picks an alias: it walks the registered models only.

The Models tab has no alias screen yet: use `wt-model alias`.

## `wt-model gateway`

```text
wt-model gateway
wt-model gateway subscription on|off [--dry-run]
```

The host's `[gateway]` table in the wt-model home's `config.toml`
(`~/.config/wt-model/config.toml`), which `wt-model serve` reads once, at
start (#693). Bare `gateway` shows it and changes nothing: whether the Claude
subscription header mode is switched on, and which TLS pair serve speaks with
(paths only). The Setup tab (`wt-model setup`) shows the same switch as its
`gateway` status row.

`gateway subscription on|off` writes `subscription_passthrough`, the one key
that has a writer; `tls_cert` and `tls_key` stay hand edits. It changes that
key and leaves the rest of the file (the `[litellm]` table, the TLS pair) as
decoded, under the lock the proxy pair's writer uses, keeps the file's mode,
and writes through a symlink. A `config.toml` that does not parse is refused and
left as it is. The file it replaces is first copied, byte for byte, to
`backups/config.toml.bak-<date>-<time>` beside it (a 0700 directory, a 0600
file), and the command prints that path. Like `litellm set`, a real write drops
the file's hand comments. Backups are never pruned, so a scripted toggle
leaves one file per change in `backups/`; delete old ones by hand. Asking for what the file already says prints
`already on; nothing was changed` and writes nothing, and `--dry-run` says what
would change and writes nothing.

A change takes effect when serve is restarted (`wt-model setup`, then `R`, or
`launchctl kickstart -k gui/<uid>/local.wt-model.serve`); the command says so.
With the mode on, serve checks the proxy for subscription hazards at start and
then every minute, and turns the mode off at the first hazard (it stays off
until serve is restarted)
([What keeps it off](wt-model-server.md#what-keeps-it-off)).

Exit codes: `0` done (also when nothing needed changing), `1` any failure: a word
other than `on` or `off`, a `config.toml` that cannot be read or written.

## What stays a hand edit of `registry.toml`

- A `[[providers]]` row wt has no default for (a cloud provider other than
  openrouter, a gateway of your own).
- A model served from a directory of your own, `[models.fetch] local_path =
  "…"` (the `bin/mlx-quantize` workflow). wt keeps the key, shows the path, and
  takes the model's presence from a stat of it.
- `[[families]]` display names. wt reads only each model's `family`.
- The two sides of an mlx_lm_server pairing that is already registered: no
  command changes a row's `[models.fetch]` or `[models.draft]`. (`wt-model model
  add mlx_lm_server … --draft …` writes them once; to change a side, remove
  the pairing and add it again, or edit the two tables.)
- Two `[[models]]` rows with one id. Every launch refuses such a registry
  (`duplicate model id`), and `wt-model model edit`, `wt-model model rm` and the Models
  tab refuse the id rather than pick a row; `wt-model model list` and the tab still
  show both rows, each with its provider's status.
- A `fetch` or `draft` that is not a table (or whose `repo` or `local_path`
  is not a string). wt lists the row, and refuses to write it — `wt-model model
  edit` and the form say `fetch must be a table` — until the value is fixed.

Run `wt-model litellm sync` after a hand edit: no tool saw it.

## The Models tab (`wt-model model`)

`wt-model model` opens it on the host that holds the registry, the screen's
one tab; it needs a terminal (without one it exits 1 and names `wt-model model
list`, `add`, `edit` and `rm`). Bare `model` under wt only points here.

The same rows as `wt-model model list`, as a table: FAMILY, MODEL, LOC, STATUS,
RUNNING, SIZE. On a narrow terminal it gives up SIZE, then LOC, then FAMILY —
never MODEL, STATUS or RUNNING. When an id is still too long for the MODEL
column it loses its middle to an ellipsis
(`omlx/Qwen3.8-35B-A3B-Instruct-abliterat…-dynamic-quant-6bit`): the start
says which provider, the end which variant.

Under the table is the selected row's detail: its id, whole, then its status,
`running` / `loading` / `running?`, and its tags (for a pairing, its target
and draft); and the path of its weights on a line of its own, written from
`~`. A short terminal drops the path before it drops table rows, and a
pairing's target and draft before its key hints and the id.

A row whose `fetch` or `draft` is malformed in `registry.toml` has one more
line under its id, in the words `wt-model model list` prints on stderr: `fetch is
not a table; read as absent`. wt reads such a value as absent, which is why
the row has no path, or reads `missing` when its weights are there. A
`cost.time_prices` row that breaks a rule is on that line too
(`cost.time_prices[0]: windows[0]: start must be HH:MM, got 9:00; read as
absent`): the model picker does not apply it.

| Key | Does |
|---|---|
| `↑`/`↓`, `j`/`k` | move |
| `enter` | edit the selected registry model; on a `new` row, register it (the form, below) |
| `n` | add a model (the form, below) |
| `d` | remove the selected registry model, after a `y/N` prompt that shows where its weights are (on a terminal too short for a long id and path the prompt drops its blank lines, then its closing sentence, before any of the path); the path is repeated in the status afterwards |
| `r` | probe the providers again; the status says `probing providers...` until they answer — also after a save or a removal, which probe again — and `n`, `enter`, `r` and `d` wait for it |
| `/` | filter by id or family: type, `Enter` to keep the filter, `Esc` to clear it |
| `Tab` | nothing: the screen has one tab |
| `q`, `Ctrl+C` | quit — but on the remove prompt `q` cancels it, as `Esc` and `n` do (`Ctrl+C` also quits while a filter is being typed, where `q` is text) |

`Esc` does not quit, and `Ctrl+S` does nothing here: there is nothing to save.
The providers are probed when the tab is first shown; `q` works while that
first probe is still out.

### The form

`n`, and `enter`, open a form with the fields `wt-model model add` and `wt-model model
edit` take: Provider, Model name, Family, Tags, Location, the three
per-token prices, and the subscription price and period.

| Key | Does |
|---|---|
| `Tab`, `↓`, `Enter` | next field |
| `Shift+Tab`, `↑` | previous field |
| `←` / `→` | change Provider, Location or Subscription period |
| `→` at the end of Family | replace what was typed with the family the registry already has (`Ctrl+N` / `Ctrl+P`: the next and the previous one that matches); with no family matching, nothing changes |
| `Ctrl+S` | save |
| `Esc` | cancel |

Provider offers every provider in the registry and the ones wt adds a row
for by itself (ollama, omlx, mtplx, openrouter), but not mlx_lm_server: its
model is a target+draft pairing, which takes two artifacts and is added on
the command line (`wt-model model add mlx_lm_server <target> --draft <draft>`,
above). While the add form's cursor is on Provider, a dim line above the key
hints names that command (a shorter spelling at 40 columns; on a terminal too
short to spare the row it is left out). `enter` on a pairing row opens the form like any other, to edit its
family, tags, location and prices; its target and draft are not fields, and a
save leaves `[models.fetch]` and `[models.draft]` as they are. When editing, and when registering a `new` row, the provider
and the model name are fixed, and the title names the id. An edit writes only
the fields you changed; saving an untouched form writes nothing and says `no
change`. Adding an ollama model runs the `ollama show` lookup `wt-model model add`
runs, after the values have been checked; if it fails the model is added
without its capabilities and the status says so.

A value that cannot be used keeps the form open, with the reason above the
key hints and the cursor on that field. Two refusals are about the registry
row, not about a field, and move nothing; nothing is written and no sync is
owed:

- an id that more than one row carries — the message `wt-model model edit` gives
  (`model "<id>" is in the registry twice (providers A, B); wt cannot tell
  which one you mean — fix the entry in <registry path>`). The form opens on
  the row under the cursor, but wt will not choose a row to write;
- a row whose `fetch` or `draft` is malformed — `invalid registry entry:
  model "<id>": fetch must be a table (fix the entry in <registry path>)`.
  wt reads the value as absent but does not write the row back around it.
  The form adds `(fix the entry in <registry path>)` only for such a row,
  where no field can repair what is wrong. A row with a `cost.time_prices`
  row that breaks a rule is refused the same way (`… cost: time_prices[0]:
  windows[0]: start must be HH:MM, got 9:00`), with the same hint.

Both are repaired in `registry.toml`; `r` on the table reads it again. A
hand-written row the registry writer refuses for a key the form has a field
for — a row with no `family` — is shown the writer's message as it is
(`invalid registry entry: model "<id>": family is required`), with no file to
fix: filling the field in the same form and saving repairs it.

On a terminal too short for every field the fields scroll, and `↑ N more` /
`↓ N more` count the ones off the screen. The title wraps; a fixed value too
long for its row loses its middle to an ellipsis, and a field that is not
being edited shows the start of its value. A refusal too long to leave the
fields three rows — the field being edited and the two markers — (a
duplicated 72-column id, at 40 columns and 12 lines) is shown whole without
them, and the next key brings the form back and does nothing else.

A saved form and a removal are each written to `registry.toml` at once. The LiteLLM routes are not
synced per change: the status says `LiteLLM routes pending (sync on quit)`,
and that one sync runs when `wt-model model` closes, only if the registry changed. A
quit typed while a change is still being written waits for it, on this tab
(the form's title says `saving, then quitting...`). A
removal the registry refuses is reported and the table is read again: nothing
is written and no sync is owed. An id that more than one row carries is
refused that way, with the message `wt-model model rm` gives (`model "<id>" is in
the registry twice (providers A, B); wt cannot tell which one you mean — fix
the entry in <registry path>`); both rows are listed, and the cursor stays on
the one it was on. If wt is
killed before the sync, the next `wt start`, `wt stop` or launch through
LiteLLM repairs the routes, and `wt-model litellm sync` does it at once. A sync that
leaves `config.yaml` unchanged does not restart the proxy.

What the last action said stays above the table until the next key. On a
terminal too short for both (a refusal that ends with a long path, at 12
lines) it is shown whole without the table, and the next key brings the table
back. A registry that does not load is reported the same way, with the repair
that fits the error (`fix that file by hand`), and stays until `r` reads it
again.
