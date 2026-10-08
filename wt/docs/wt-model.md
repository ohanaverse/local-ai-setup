# `wt model`

The models in the registry (`~/.config/local-ai/registry.toml`): list them,
add, edit and remove them. wt never downloads a model and never deletes
weights; it registers what a provider already has, or will have.

```bash
wt model list [--json]     # every registry model, and every local model found on this machine
wt model init [--json]     # create the registry if it is missing; add the default provider rows
wt model add <provider> <name> --family F [--tags a,b] [--location local|cloud] [price flags] [--id ID]
wt model edit <id> [--family F] [--tags a,b] [--location local|cloud] [price flags]
wt model rm <id>... [--yes]
```

Each of the three writing commands makes one locked write of the registry and
then syncs the LiteLLM routes once. The exit code is 0 when the write
succeeded, even if the sync could only warn; it is 1 for a value that cannot
be used, an unknown or duplicate id, a registry that cannot be read, or a
removal that was declined.

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

`--json` always has everything: `registry` (the file), `models` (each with
`id`, `family`, `provider_id`, `model_name`, `location`, `tags`,
`registered`, `status`, `running`, `size_bytes` and `path` — the last two
`null` when wt does not know them — `target` and `draft` for a pairing, and
`malformed`, an array of the phrases above (`"fetch is not a table"`,
`"draft.local_path is not a string"`) that is always there and empty for a
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
table with it. And a row still in modelman's old cost layout (`kind =
"per_token"` with `price_per_million_tokens`, or `kind = "subscription"` with
`price_per_period` and `period`) is moved to the current keys in the same
write: modelman reads such a table by its old keys alone, so a new key
beside them would be a price it ignores.

## `wt model rm <id>... [--yes]`

Removes the rows from the registry, and nothing else: it prints where each
removed model's weights are, because wt deletes none. A local model that is
still on disk shows up again in `wt model list` as `STATUS new`. It asks on
the terminal first (`--yes` skips; with no terminal and no `--yes` it refuses).
When one id is not in the registry, none is removed.

## What stays a hand edit of `registry.toml`

- A `[[providers]]` row wt has no default for (a cloud provider other than
  openrouter, a gateway of your own).
- A model served from a directory of your own, `[models.fetch] local_path =
  "…"` (the `bin/mlx-quantize` workflow). wt keeps the key, shows the path, and
  takes the model's presence from a stat of it.
- `[[families]]` display names. wt reads only each model's `family`.

Run `wt litellm sync` after a hand edit: no tool saw it.
