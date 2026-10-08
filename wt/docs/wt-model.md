# `wt model`

The models in the registry (`~/.config/local-ai/registry.toml`): list them,
add, edit and remove them. wt never downloads a model and never deletes
weights; it registers what a provider already has, or will have.

```bash
wt model list [--json]     # every registry model, and every local model found on this machine
wt model init [--json]     # create the registry if it is missing; add the default provider rows
```

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
omlx/mine: fetch is not a table; wt reads it as absent (fix the entry in ~/.config/local-ai/registry.toml)
mlx_lm_server/T+draft-D: fetch.repo is not a string, draft is not a table; wt reads it as absent (fix the entry in ~/.config/local-ai/registry.toml)
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
