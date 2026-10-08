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
| `PATH` | the model's directory (omlx, mtplx) or its `fetch.local_path`; `-` otherwise, including when the only directory found belongs to another organization's model of the same name |

The text table has no borders and fits the terminal: `PATH` is shown only when
every row fits on one line with it, and `SIZE` only while it leaves `MODEL` at
least 20 columns. A model id is never cut — one too long for its column gets a
line of its own, above its cells. When a column is left out, a note on stderr
says so. Into a pipe every column is printed, one model per line.

`--json` always has everything: `registry` (the file), `models` (each with
`id`, `family`, `provider_id`, `model_name`, `location`, `tags`,
`registered`, `status`, `running`, `size_bytes` and `path` — the last two
`null` when wt does not know them — and `target` and `draft` for a pairing),
and `providers` (each probed provider's status: `ok`, `partial`,
`unreachable`).

It runs on a registry that has a gap in it (a model whose provider has no
row, a mistyped location): that is what the list is for. Only a registry that
cannot be read at all stops it.

## `wt model init [--json]`

Creates the registry when it is missing and adds the provider rows wt needs
and the registry lacks. It never changes a row that exists. `wt model init
--help` lists which rows, and why.
