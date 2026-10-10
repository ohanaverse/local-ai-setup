# `wt model`

The models in the registry (`~/.config/local-ai/registry.toml`): list them,
add, edit and remove them. wt never downloads a model and never deletes
weights; it registers what a provider already has, or will have.

```bash
wt model                   # the Models tab of `wt config` (needs a terminal)
wt model list [--json]     # every registry model, and every local model found on this machine
wt model init [--json]     # create the registry if it is missing; add the default provider rows
wt model add <provider> <name> --family F [--tags a,b] [--location local|cloud] [price flags] [--id ID]
wt model edit <id> [--family F] [--tags a,b] [--location local|cloud] [price flags]
wt model rm <id>... [--yes]
wt model add mlx_lm_server <target> --draft <draft> --family F   # a target+draft pairing
```

Each of the three writing commands makes one locked write of the registry and
then syncs the LiteLLM routes once. The exit code is 0 when the write
succeeded, even if the sync could only warn; it is 1 for a value that cannot
be used, an unknown or duplicate id, an id that more than one registry row
carries (`edit` and `rm`), a registry that cannot be read, or a removal that
was declined.

## `wt model list [--json]`

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
`null` when wt does not know them — `target` and `draft` for a pairing, and
`malformed`, an array of the phrases above (`"fetch is not a table"`,
`"draft.local_path is not a string"`, `"cost.time_prices[0]: windows[0]:
start must be HH:MM, got 9:00"`) that is always there and empty for a
row with nothing malformed), and `providers` (each probed provider's status:
`ok`, `partial`, `unreachable`). Stdout is that one document; the stderr
lines for malformed rows are printed in this mode too.

It runs on a registry that has a gap in it (a model whose provider has no
row, a mistyped location): that is what the list is for. Only a registry that
cannot be read at all stops it.

## `wt model init [--json]`

Creates the registry when it is missing and adds the provider rows wt needs
and the registry lacks. It never changes a row that exists. `wt model init
--help` lists which rows, and why.

## `wt model add <provider> <name> --family <family>`

`<name>` is the model as its provider lists it: an ollama tag (`qwen3:8b`), the
name of an omlx model directory, an mtplx `org/name`, or a cloud provider's
model id. Pull or download a local model with its provider's own tool, before
or after adding it; `wt model list` shows what is on disk and unregistered as
`STATUS new`.

| Flag | Meaning |
|---|---|
| `--family` | required; what `-F` filters on |
| `--tags a,b` | what `-T` filters on |
| `--location local\|cloud` | only when the model differs from its provider's location |
| `--input-price`, `--cache-price`, `--output-price` | $ per million tokens |
| `--subscription-price`, `--subscription-period month\|year` | a subscription price needs a period |
| `--id` | the id, instead of the derived one: `<provider>/<name>`, with no spaces |

The id is derived: for a local provider it is the id wt already lists the
model under when it finds it unregistered (`ollama/qwen3:8b`,
`omlx/<directory>`, `mtplx/<org>/<name>`; an `omlx-6bit` model is `omlx/…`), so
registering a model does not rename it. For any other provider it is
`<provider>/<name>` with each `/` in the name written `--`
(`openrouter/qwen--qwen3.8-27b`). The id, the provider and the name cannot be
changed afterwards: usage history, rotation and launch profiles key on the id.

In the same write wt adds the default provider row the model needs when it is
missing — for ollama, omlx, mtplx and openrouter (openrouter's holds no key,
only `auth.secret_ref = "OPENROUTER_API_KEY"`, the environment variable wt
reads the key from) — and any other row `wt model init` would add. A provider
wt has no default row for is refused: add its `[[providers]]` block to
`registry.toml` first. A second entry for an artifact that is already
registered is refused too. A value that cannot be used is refused before
anything else happens.

For an ollama model wt runs `ollama show <name>` once, against the address of
the registry's ollama provider, and records `model_info.supports_function_calling`
and `model_info.supports_vision` when ollama lists the capability; wt copies
`model_info` into the model's LiteLLM route. If the lookup fails the model is
added without them and a warning says so.

### An mlx_lm_server pairing

An mlx_lm_server model is a target and a draft served together by
`mlx_lm.server --draft-model`. `wt model add mlx_lm_server <target> --draft
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
row of `wt model list` and of the Models tab with `STATUS -`, since no probe
can enumerate it; its family, tags and prices are edited like any model's,
and the tab's form does not create one.

A pairing row's `RUNNING` is not proof that this pairing is the one being
served ([#299](https://github.com/ohanaverse/local-ai-setup/issues/299), open).
The server lists the Hugging Face repo or path it serves, never a pairing's
name, and nothing records which target and draft a running `mlx_lm.server`
was started with:

- With exactly one pairing registered, anything served on the mlx_lm_server
  port marks that pairing `run` — a different, unregistered pairing started
  with `llmbench provider isolate` included. `wt model add mlx_lm_server`
  makes this case easy to reach: one add, and the registry has exactly one.
- A pairing whose name is the end of a served repo id reads `run` as well.
- With two or more registered and no name matching, `RUNNING` is `?`.

wt shows the state and syncs the pairing's LiteLLM route by it, as it did
before this command existed; `wt model add`, `edit` and `rm` decide nothing by
it. When it matters which pairing is up, check
`/tmp/local-ai-setup-mlx-lm-server.log` or the server's `/v1/models`.

## `wt model edit <id> [flags]`

Changes the fields named by the flags, and nothing else in the row: every
other key (off-peak prices, `model_info`, `fetch`, keys wt does not know) is
kept as it is. A flag left out leaves its field alone; an empty value clears
it (`--tags ""`, `--input-price ""`), and an empty `--location` makes the
model inherit its provider's. No flag at all is a usage error. It does not
change `pricing_updated_at`. When nothing changes it says `no change` and
syncs nothing.

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

## `wt model rm <id>... [--yes]`

Removes the rows from the registry, and nothing else: it prints where each
removed model's weights are, because wt deletes none. A local model that is
still on disk shows up again in `wt model list` as `STATUS new`. It asks on
the terminal first (`--yes` skips; with no terminal and no `--yes` it refuses).
When one id is not in the registry, none is removed.

An id that more than one registry row carries is refused in the same way,
before the question and with the message `wt model edit` gives: none of the
ids named is removed, whichever of them it is. Removing one of two rows that
share an id is a hand edit — only there can you see which row is which.

## What stays a hand edit of `registry.toml`

- A `[[providers]]` row wt has no default for (a cloud provider other than
  openrouter, a gateway of your own).
- A model served from a directory of your own, `[models.fetch] local_path =
  "…"` (the `bin/mlx-quantize` workflow). wt keeps the key, shows the path, and
  takes the model's presence from a stat of it.
- `[[families]]` display names. wt reads only each model's `family`.
- The two sides of an mlx_lm_server pairing that is already registered: no
  command changes a row's `[models.fetch]` or `[models.draft]`. (`wt model
  add mlx_lm_server … --draft …` writes them once; to change a side, remove
  the pairing and add it again, or edit the two tables.)
- Two `[[models]]` rows with one id. Every launch refuses such a registry
  (`duplicate model id`), and `wt model edit`, `wt model rm` and the Models
  tab refuse the id rather than pick a row; `wt model list` and the tab still
  show both rows, each with its provider's status.
- A `fetch` or `draft` that is not a table (or whose `repo` or `local_path`
  is not a string). wt lists the row, and refuses to write it — `wt model
  edit` and the form say `fetch must be a table` — until the value is fixed.

Run `wt litellm sync` after a hand edit: no tool saw it.

## The Models tab (`wt model`, or `Tab` in `wt config`)

The same rows as `wt model list`, as a table: FAMILY, MODEL, LOC, STATUS,
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
line under its id, in the words `wt model list` prints on stderr: `fetch is
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
| `Tab` | the Agents tab |
| `q`, `Ctrl+C` | quit — but on the remove prompt `q` cancels it, as `Esc` and `n` do (`Ctrl+C` also quits while a filter is being typed, where `q` is text) |

`Esc` does not quit, and `Ctrl+S` does nothing here: there is nothing to save.
The providers are probed when the tab is first shown, not on every visit;
`Tab` and `q` work while that first probe is still out.

### The form

`n`, and `enter`, open a form with the fields `wt model add` and `wt model
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
the command line (`wt model add mlx_lm_server <target> --draft <draft>`,
above). While the add form's cursor is on Provider, a dim line above the key
hints names that command (a shorter spelling at 40 columns; on a terminal too
short to spare the row it is left out). `enter` on a pairing row opens the form like any other, to edit its
family, tags, location and prices; its target and draft are not fields, and a
save leaves `[models.fetch]` and `[models.draft]` as they are. When editing, and when registering a `new` row, the provider
and the model name are fixed, and the title names the id. An edit writes only
the fields you changed; saving an untouched form writes nothing and says `no
change`. Adding an ollama model runs the `ollama show` lookup `wt model add`
runs, after the values have been checked; if it fails the model is added
without its capabilities and the status says so.

A value that cannot be used keeps the form open, with the reason above the
key hints and the cursor on that field. Two refusals are about the registry
row, not about a field, and move nothing; nothing is written and no sync is
owed:

- an id that more than one row carries — the message `wt model edit` gives
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
and that one sync runs when the editor closes, only if the registry changed. A
quit typed while a change is still being written waits for it, on this tab
(the form's title says `saving, then quitting...`). A
removal the registry refuses is reported and the table is read again: nothing
is written and no sync is owed. An id that more than one row carries is
refused that way, with the message `wt model rm` gives (`model "<id>" is in
the registry twice (providers A, B); wt cannot tell which one you mean — fix
the entry in <registry path>`); both rows are listed, and the cursor stays on
the one it was on. If wt is
killed before the sync, the next `wt start`, `wt stop` or launch through
LiteLLM repairs the routes, and `wt litellm sync` does it at once. A sync that
leaves `config.yaml` unchanged does not restart the proxy.

What the last action said stays above the table until the next key. On a
terminal too short for both (a refusal that ends with a long path, at 12
lines) it is shown whole without the table, and the next key brings the table
back. A registry that does not load is reported the same way, with the repair
that fits the error (`fix that file by hand`), and stays until `r` reads it
again.
