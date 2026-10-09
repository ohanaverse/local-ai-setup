# Providers and models — modelman, registry.toml, and LiteLLM routing

> Use this to: register cloud providers and local models in the shared `registry.toml` — with `wt model add`, `edit` and `rm`, or the Models tab of `wt config` — and download them with each provider's own tool. modelman's TUI, which used to do both, is disabled. Routing through the LiteLLM proxy on :4000 follows on its own: **add a cloud model and it is routed**, and a local model is routed while it runs (an ollama model while it is pulled) — with or without a registry entry.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: LiteLLM proxy running on :4000, Ollama up, services healthy. Guides 03–08 of this set ([03-model-families](03-model-families.md) onward) continue from here.
- `wt` on PATH (`make install` from the repo root): `wt model init`, `wt litellm sync`, `wt start` and `wt stop` do most of what follows.
- modelman runnable from its repo, for the subcommands that still work — `sync`, `start`/`stop`, `usage`, and `refresh-prices` and `ollama-catalog sync`, which `wt cloud-sync` replaces (it is not installed globally; run from the `modelman/` directory):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv sync
```

Then run a modelman subcommand with `uv run modelman <subcommand> …` from that same directory. Bare `uv run modelman` used to open the TUI; it now prints where to go in wt and exits 1.

```text
Resolved 51 packages in 4ms
Checked 50 packages in 2ms
```

modelman reads three files under `~/.config/local-ai/` (table copied from the modelman README — each file is overridable by env var):

| File | Purpose | Env override |
|------|---------|--------------|
| `registry.toml` | Canonical model/provider definitions (shared; `wt model init` also writes it) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |
| `modelman.toml` | Per-machine mutable state: download markers, the `running` hint (and a legacy `[families]` display-name table, read as a fallback only) | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |

Full map including wt and LaunchAgent surfaces: [00-config-map](00-config-map.md)

LiteLLM's `config.yaml` defaults to `~/.config/litellm/config.yaml` (`WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`, overrides it; wt is the writer).

## TL;DR

<!-- UNVERIFIED — not run as one block. The `sync` line and the command list were verified live; the `start`/`stop` lines were not driven from this session (they load real models — see Steps §6–7 and Verification for how to confirm). The `wt model` lines were run against a scratch registry, the tab from a pty. -->

```bash
wt model init                                  # create registry.toml if it is missing, add the default provider rows (never changes a row that exists)
wt model                                       # the Models tab of `wt config`: n add, enter edit, d remove (Step 1)
wt model add openrouter <model> --family <f>   # the same without a terminal; also `wt model edit <id> …`, `wt model rm <id>`
wt model list                                  # every registry model, and every local model found that has no entry
wt litellm sync --dry-run                      # read-only: what wt would route/unroute for the registry as edited
wt litellm sync                                # apply it — `wt model …` and the tab do this themselves; run it after a hand edit
wt litellm list                                # what is routed right now — the authoritative answer
ollama pull <name:tag>                         # download: the provider's own tool (Step 5 has omlx, mtplx, mlx_lm_server)
# ollama/gpt-oss:20b below is an example id — substitute any id from your registry.toml, or a discovered id (Step 7)
wt start ollama/gpt-oss:20b                    # load it; the start writes its LiteLLM route (`wt start` alone: a picker of what is on disk)
wt stop ollama/gpt-oss:20b                     # unload it (a pulled ollama model stays routed; on mtplx the route goes with it; on omlx only that model is unloaded and unrouted)
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman sync                           # reconcile ready/disk_path/size_bytes in modelman.toml against providers; never adds models, then syncs the LiteLLM routes
```

Who does what now (verified via `uv run modelman --help` and `wt --help`): **wt** = `wt model` (the Models tab of `wt config`) and `wt model add|edit|rm|list` (models: add, edit, remove, list), `wt model init` (the file and its provider rows), `wt cloud-sync` (OpenRouter prices, and ollama's cloud models with their prices), `wt litellm …` (routes), `wt start`/`wt stop` (local models), `wt served <provider>` (what a server is serving). **The provider's own tool** = downloads (Step 5). **modelman subcommands** = `sync`, `start`/`stop`, `migrate`, `usage`, and `refresh-prices` and `ollama-catalog sync` (both replaced by `wt cloud-sync`) — all still work; bare `modelman` (the TUI) does not. **llmbench** = benchmarks. **By hand** = a provider row wt has no default for, a `local_path` entry for a single model ([10-mlx-lm-quantization](10-mlx-lm-quantization.md)), `[[families]]` display names, and the repair of a row wt refuses to write (Step 1). There is no per-model routing command: `wt` routes the cloud models `registry.toml` configures plus the local models it finds running (Step 7).

## Steps

### 1. Add, edit and remove models with `wt model`

modelman's TUI used to add, edit and delete models. It is disabled: bare `modelman` prints where to go and exits 1. wt does it now, two ways that write the same rows:

```bash
wt model init                    # 1. the file and its provider rows (once)
wt model                         # 2a. the Models tab of `wt config`: n add, enter edit, d remove
wt model add ollama qwen3:8b --family qwen3 --tags code      # 2b. the same from the command line
wt model edit ollama/qwen3:8b --tags code,design
wt model rm ollama/qwen3:8b
wt model list                    # 3. what the registry has, and what is on disk without an entry (STATUS new)
```

1. **`wt model init`** creates `registry.toml` when it is missing and appends the default `[[providers]]` row for each provider it finds a use for (an installed `ollama`/`omlx`/`mtplx`, a provider a model or a configured agent names, a native row per configured agent — `wt model init --help` has the full list). It never changes a row that exists, so running it again is safe. `wt model add` adds a missing default row too, in the same write as the model.
2. **Add, edit, remove.** `wt model add <provider> <name> --family <family>` registers a model under a derived id — the id wt already shows an unregistered local model under, or `<provider>/<name>` with `/` written `--` for a cloud model — and `--id` overrides it. `wt model edit <id>` changes family, tags, location and prices, and nothing else in the row; the id, the provider and the name are fixed. `wt model rm <id>` removes the row and prints where the weights are: wt never deletes them (Step 5). The Models tab does the same from a form. Every flag and key: `wt/docs/wt-model.md`.
3. **Routes follow by themselves.** Each command syncs the LiteLLM routes once after its write, and the tab once when you quit (Step 7). A sync that could not run is a warning; the registry change stands.

What to keep in mind:

- **A model's provider must have a `[[providers]]` row.** `wt model add` refuses a provider that has none and that it has no default row for, and names the file to add it to. A registry that already holds such a model makes `wt`, `wt start` and `wt stop` stop with `config error: model "<id>": unknown provider "<provider>"`, and `wt litellm sync` warns that it cannot route the model; `wt model list` and `wt model rm` still work, so the row can be seen and removed; `wt model edit` refuses the row until its `[[providers]]` block is added, which is a hand edit (below).
- **A few things stay a hand edit of `registry.toml`:** a `[[providers]]` row for a provider wt has no default for (Step 2), a model served from a directory of your own (`[models.fetch] local_path`, guide 10), `[[families]]` display names (guide 03), and the repair of a row wt will not write: two `[[models]]` rows that share an id (`wt model edit`, `wt model rm` and the tab refuse the id rather than pick a row, and name both rows' providers), or a `fetch` that is not a table (`fetch must be a table`). `wt model list` shows such rows, so you can see what to fix. The procedure for one: edit the file; run `wt litellm sync --dry-run`, a read-only check that prints `config error: parse …` when the file is no longer valid TOML and otherwise the route changes a sync would make; then `wt litellm sync`, because no tool saw the edit. Steps 2–4 show a block per provider kind; every field name there is one the shared fixture `docs/contracts/registry.sample.toml` pins for both wt and modelman.
- **Comments do not survive.** The first tool that changes the file — a `wt model` command, `wt cloud-sync`, or a modelman subcommand that writes it (`migrate`, `ollama-catalog sync`, `refresh-prices`, `sync` when it repairs a provider row) — lays the whole file out again without them. Keys neither tool knows are kept.
- **Do not edit while a modelman subcommand is running.** It saves the copy it loaded; when the file changed underneath it, it refuses (`registry.toml changed on disk; reload`) — run that command again.
- **What else the TUI did:** downloads are the provider's own tool now (Step 5), starting and stopping is `wt start`/`wt stop` (Step 7), and its ready toggle has no replacement because none is needed — wt probes the providers for what is on disk.

### 2. Add a cloud provider (OpenRouter)

`registry.toml` is canonical. For OpenRouter the provider block is written for you: `wt model init` adds it when a model or a configured agent names openrouter, and the first `wt model add openrouter …` adds it in the same write as the model. The block of any other cloud provider is a hand edit — a provider row is one of the things wt has no command for (Step 1). Do not add a second `openrouter` row: the one wt writes has `secret_ref = "OPENROUTER_API_KEY"`, the name of the environment variable wt reads the key from — export the variable, or change `secret_ref` on the row that is there to where you keep the key. Documented TOML shape, copied from the modelman README:

```toml
[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "sk-or-v1-..."
```

wt resolves `secret_ref` — an env-var name (or `os.environ/NAME`), an `exec:` helper command, or a literal key — and writes the result as `api_key` into each of the provider's LiteLLM `model_list` entries (`wt/internal/litellm/entry.go`); never commit a real `sk-or-v1-…` value to any repo — the README's `"sk-or-v1-..."` placeholder above is the shape. An env-var ref must be set in the shell that runs the sync, or wt reports `resolved empty` and keeps the existing rows. `location = "cloud"` is what makes this provider's models routable without a download.

**A key for a local omlx server.** omlx can require an API key for its management endpoints while leaving inference open, and wt needs one of those endpoints to tell which omlx models are loaded when only some of them are ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), Gotchas). Give wt the key by adding a `secret_ref` to the omlx provider — one key for the server, however many models it holds; `omlx` and `omlx-6bit` are the same server, so the `omlx` entry is enough. `type` stays `"none"`. wt sends the key on that probe and on the one-token warmup request that loads a model (`wt start`, which `modelman start` runs for an omlx model, and `wt warm`, which `llmbench provider isolate omlx` and the benchmarks use) — an omlx that also wants the key for inference refuses a keyless warmup, and the start fails at once naming this setting. It is not written into the model's LiteLLM route, which keeps `api_key: not-needed` ([04-litellm-config](04-litellm-config.md)): requests through LiteLLM reach omlx keyless, so they are served only while omlx allows unauthenticated inference (`auth.allow_unauthenticated_inference` in `~/.omlx/settings.json`).

```toml
[[providers]]
id = "omlx"
# ...
[providers.auth]
type = "none"
base_url = "http://localhost:8000"
secret_ref = "OMLX_API_KEY"   # env-var name, os.environ/NAME, an exec: helper, or the literal key
```

The key is the `auth.api_key` value in `~/.omlx/settings.json`. This is a hand edit of the provider row — wt has no command for one (Step 1); a modelman subcommand that rewrites the file keeps the line. Nothing needs setting while omlx has no models loaded, or all of them loaded with `/v1/models` still listing the whole pool: its `/health` answers those without a key. A hidden model is counted in the pool but not listed, so "everything loaded" with one hidden is provable by the status endpoint alone — that case wants the key.

Providers also declare a `protocols` field — the list of wire protocols the provider serves (`"anthropic"`, `"openai-chat"`, `"openai-responses"`; default `["openai-chat"]`). Ollama's discovered entry serves `["anthropic","openai-chat"]`. wt compares an agent's protocols against the provider's to pick direct-vs-LiteLLM routing ([06-wt-agents-and-models](06-wt-agents-and-models.md) §4).

A provider may also set `openrouter_priced` (`true` or `false`, unquoted). Left out, modelman and wt infer it: an `openrouter` model, or a model of a non-native cloud provider, takes its price from OpenRouter — `wt cloud-sync` refreshes it (`modelman refresh-prices` still does too), and after a launch wt reminds you when the newest refresh is more than a week old. Set `openrouter_priced = false` on a cloud provider whose model names are not OpenRouter ids (a corporate LiteLLM gateway, say) to take its models out of the refresh and stop the reminder; `true` opts a provider in. This is a hand edit that wt and modelman keep when they rewrite the file.

Validate the file after editing (read-only registry load):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run python -c 'from modelman.registry import load_registry; print(sorted(p.id for p in load_registry().providers))'
```

It prints a sorted Python list of every `[[providers]]` id in the registry; a load error means the TOML is malformed. After adding the openrouter block, `'openrouter'` should appear in that list alongside your existing providers. Example (illustrative — your ids will differ):

```text
['ollama', 'openrouter']
```

Then add models under it — one block each. `id` is the route name clients ask LiteLLM for, `model_name` is what OpenRouter is asked for:

```toml
[[models]]
id = "openrouter/<org>/<model>"
family = "<family>"
provider_id = "openrouter"
model_name = "<org>/<model>"
location = "cloud"
tags = []                      # e.g. ["code"] — rotation groups, guide 03

[models.model_info]            # optional: capabilities copied into the LiteLLM route
supports_function_calling = true
```

The command for it is `wt model add openrouter <org>/<model> --family <family>` (Step 1), which adds the `openrouter` provider row when it is missing, writes a block like this one and syncs the route. The id it derives spells the `/` of the name as `--` (`openrouter/<org>--<model>`); `--id openrouter/<org>/<model>` gives the spelling above. That is all a cloud model needs: there is no download and no ready gate (Step 7). Prices go in a `[models.cost]` table (`input_price_per_million`, `cache_price_per_million`, `output_price_per_million`, and `subscription_price` + `subscription_period`); for OpenRouter models leave it out and run `wt cloud-sync --only prices`, which fetches the per-token prices, shows what it would change and asks before writing them. Ollama's `:cloud` models and their prices are added and removed by `wt cloud-sync`'s catalog flow — no hand edit. Start with `wt cloud-sync --dry-run`, which prints both plans and changes nothing; a plan that removes a model is applied only after you approve it. Flags, exit codes and recovery: [wt/docs/wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md).

### 3. Add a local model (Ollama)

Pull the model first (Step 5), then register it with `wt model add ollama <name:tag> --family <family>` (Step 1) — or leave it out: an entry for a local model is optional (see *A local registry entry is an optional overlay* below). `wt model add` fills `model_info` from `ollama show <name>` as the TUI's add dialog did: `tools` → `supports_function_calling = true`, `vision` → `supports_vision = true`. If that lookup fails it says so and adds the model without them; set the keys in the block yourself then. A local model needs no `[models.cost]`.

Resulting `registry.toml` entry shape. Example (illustrative — your ids will differ):

```toml
[[models]]
id = "ollama/ornith-1.5:35b"
family = "ornith-1.5:35b"
provider_id = "ollama"
model_name = "ornith-1.5:35b"
location = "local"
tags = []

[models.model_info]
supports_function_calling = true
supports_vision = true
```

**`location` must be exactly `local` or `cloud`**, on a provider entry and on a model that sets its own. Anything else — `Local`, `Cloud`, a stray space — is a validation error, not a location, and it never falls back the way a missing one does: a model with no location of its own inherits its provider's, while a model whose own value is mistyped has no valid location at all (a provider entry may likewise leave its location out, but a value that is set must be one). A model caught by it is not offered, and `wt litellm sync` keeps its existing route until the registry is repaired. When the family could not be probed — always the case for a mistyped provider entry, since the inventory probes only an exact `local` — the whole family stays frozen and sync warns, naming the gap; on a healthy probed provider only the mistyped model's own row stays and it is simply left unrouted, with no warning. `wt`, `wt start`, `wt stop` and `wt smoke` stop with `config error: provider "<id>" has location "<value>"; expected "local" or "cloud" (fix the entry in <registry path>)` for a mistyped provider entry, or the same with `model "<id>"` for a model's own value, until it is corrected. One mistyped entry therefore stops every launch, not only that model's. modelman does not check the value: it reads anything that is not exactly `local` as not local.

(`location` may be left out — the model then inherits its provider's. Rows modelman's TUI wrote also carry `source = "discovered"`, which marks an entry modelman registered from an on-disk artifact; it loads fine, a hand-written block does not need it, and it is not to be confused with a *discovered model* below, which has no entry at all.)

**`id` is the route name; `model_name` is what the provider is asked for.** The LiteLLM row wt builds uses the registry `id` as its `model_name` (the client-facing id) and the provider prefix plus the registry `model_name` as the upstream model. Keep the two in agreement: when they disagree (a stale suffix left on the id, a typo in either), the route table shows exactly that — wt rebuilds its rows from the registry on every sync, so a hand-correction in `config.yaml` no longer hides the mismatch. Fix it here, in the registry entry. Renaming an `id` renames the route, so anything that references the old id (agent configs, scripts) must follow.

**A local registry entry is an optional overlay** (#179). `wt` lists the local models its live inventory finds — on disk, or running — whether or not `registry.toml` names them; it probes each local provider family (ollama, omlx, mtplx, and `mlx_lm_server` for running state) the registry has a local `[[providers]]` row (`location = "local"`) or a local model for. An entry adds family, tags, cost and `model_info` to the model whose provider family and `model_name` match it (ollama: the same name, with the implicit `:latest`; omlx and mtplx: the same name with or without its `org/` prefix). Without an entry the model still appears in `wt`'s picker — status `new`, no family or tags, so any `-T`/`-F` hides it — and is routed under its discovered id, `<family>/<artifact>`: `ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` (omlx and omlx-6bit are one server and share the `omlx` prefix). An `mlx_lm_server` pairing is the exception: it cannot be discovered, so it needs its entry. An entry whose artifact is confirmed missing from disk is not listed by wt at all until you download it (Step 5); `uv run modelman start` with no argument still lists it, as registered but not downloaded.

Removing only the entry — deleting its block; the artifact stays on disk until you remove it with the provider's tool (Step 5) — does not unroute a model that is still on disk and running: the next sync drops the row named after the old id and writes one under the discovered id (when the two ids are equal, the row simply stays). The exception is an `mlx_lm_server` pairing: it cannot be discovered, so with its entry gone nothing names it and the next sync removes its route even while the server is still up. Convention for a new local entry: `id = "<provider family>/<model_name>"` (the family is the provider id, except that `omlx-6bit` belongs to `omlx`). With `model_name` spelled the way the provider lists the artifact, that is the id wt already gives the model without an overlay, so its usage and survey history (keyed by id) carries over when the entry is added or removed. An entry whose id spells a `/` as `--` — the shape modelman's TUI derived for a name containing `/` on every provider except mtplx and the native ones (an omlx repo name became `omlx/mlx-community--<name>`), so an older registry may hold some — works, but adding or removing it starts a separate history key.

### 4. Add a model from Hugging Face (oMLX, MTPLX, mlx_lm_server; llama.cpp retired — see [provider-artifacts.md](../reference/provider-artifacts.md))

Download the artifact first (Step 5); the block is the optional overlay of Step 3, except for an `mlx_lm_server` pairing, which must have one. For oMLX and MTPLX, `wt model add omlx <model directory name> --family <family>` and `wt model add mtplx <org>/<name> --family <family>` (or `enter` on the model's `new` row in the Models tab) write the block shown, and `wt model add mlx_lm_server <target> --draft <draft> --family <family>` writes the pairing's (each side a Hugging Face repo id or a local path; the Models tab's form does not create a pairing, and edits the family, tags and prices of one that is there). `model_name` is spelled the way the provider lists the artifact, and `id` is `<provider family>/<model_name>`.

**oMLX** — the artifact is a directory under omlx's model directory, and its name is the model's name:

```toml
[[models]]
id = "omlx/<model directory name>"
family = "<family>"
provider_id = "omlx"                 # or "omlx-6bit" — same server, and the id still starts "omlx/"
model_name = "<model directory name>"
tags = []

[models.fetch]                       # optional; wt ignores it, `modelman sync` uses it to find the directory
repo = "<org>/<repo>"
```

A model you quantized yourself takes `local_path = "<absolute directory>"` in `[models.fetch]` instead of `repo` — [10-mlx-lm-quantization](10-mlx-lm-quantization.md) Step 2.

**MTPLX** — the artifact is an `<org>--<name>` directory, listed as `<org>/<name>`:

```toml
[[models]]
id = "mtplx/<org>/<name>"
family = "<family>"
provider_id = "mtplx"
model_name = "<org>/<name>"
tags = []
```

**mlx_lm_server** — a target+draft pairing; each side is a `repo` or a `local_path`:

```toml
[[models]]
id = "mlx_lm_server/<target name>+draft-<draft name>"
family = "<family>"
provider_id = "mlx_lm_server"
model_name = "<target name>+draft-<draft name>"
tags = []

[models.fetch]                       # the target
repo = "<org>/<target repo>"         # or: local_path = "<absolute directory>"

[models.draft]                       # the draft
repo = "<org>/<draft repo>"          # or: local_path = "<absolute directory>"
```

`[models.fetch]` also takes `files` and `quantizations` (lists) — they selected GGUF files for the retired llama.cpp provider; leave them out for the providers above.

### 5. Downloads

Neither tool downloads a model now. wt has no download command, and modelman's — a queue in its TUI, applied on exit — went with the TUI; there is no `modelman download` subcommand. Use the provider's own tool, into the place that provider reads:

| Provider | Download | Where it must land | Remove |
|---|---|---|---|
| ollama | `ollama pull <name:tag>` | ollama's own store (wt asks the daemon, not the disk) | `ollama rm <name:tag>` |
| omlx | `hf download <org>/<repo> --local-dir ~/.omlx/models/<repo>` | a directory holding `config.json`, directly under the omlx provider row's `model_dir` (default `~/.omlx/models`) or one organization folder down; the directory name is the model's name | `rm -rf` that directory |
| mtplx | `mtplx pull <org>/<name>` | `<org>--<name>` under the mtplx provider row's `model_dir` (default `~/.mtplx/models`) | `rm -rf` that directory |
| mlx_lm_server | `hf download <org>/<repo> --local-dir <directory>`, once per side | anywhere — name each directory as that side's `local_path` (Step 4) | `rm -rf` that directory |

<!-- UNVERIFIED — none of these downloads was run from this session (each fetches gigabytes). `ollama pull` and the omlx `hf download` line are the commands 01-initial-setup already uses; `mtplx pull` is from `mtplx pull --help` (mtplx 2.12.0), and the directories are the ones wt's inventory scans (`wt/internal/localmodels/inventory.go`). That `mlx_lm.server` fetches a `repo` side by itself is from the isolation code, which passes the repo id through unchanged (`llmbench/src/llmbench/benchmark/isolation.py`), not from a run. -->

```bash
ollama pull qwen3.8:27b-mlx                                                              # example tag — substitute yours
hf download mlx-community/Qwen3.8-27B-4bit --local-dir ~/.omlx/models/Qwen3.8-27B-4bit   # example repo — substitute yours
grep -n 'model_dir' ~/.config/local-ai/registry.toml                                     # a provider row that moved its model directory says so here
```

- **omlx needs a flat directory.** `--local-dir` gives one; a bare `hf download <repo>` fills the nested Hugging Face cache instead, which wt does not list.
- **A side of an mlx_lm_server pairing given as `repo`** needs no download step: `mlx_lm.server` is handed the repo id and fetches it into the Hugging Face cache itself the first time the pairing starts.
- **Afterwards** the model is on disk, which is all wt asks: `wt start` lists it (Step 7) with or without a registry entry. `uv run modelman sync` (Step 6) brings modelman's own `ready`/`disk_path`/`size_bytes` record up to date; wt does not read it.

### 6. Reconcile: `sync`

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman sync
```

It prints a single summary line, `Synced: <N> downloaded, <M> not downloaded.` (preceded by `Added provider entries: …` only if it repaired `registry.toml`), and exits 0. The two numbers are whatever your registry and disks hold — they change every time you add, pull or delete a model. `sync` takes no options at all; `sync --help` shows only `--help`.

What it does / doesn't do:

- The counts span every reconcilable provider (ollama plus the model-dir providers such as omlx, per `DEFAULT_PROVIDER_IDS`), not just ollama. "Downloaded" = a configured model whose provider reports an on-disk artifact with a size (an `ollama list` row with a size, an oMLX model dir, an HF-cache entry); "not downloaded" = every other configured model of those providers. A model whose files were on disk but whose state had drifted gets flipped back to `ready = true` (the Bug 1 fixed in PR #24).
- Only **configured** models are reconciled. A configured model with no `model_state` block gets one; models not in `registry.toml` are ignored — sync never adds models to the registry.
- `modelman.toml` is rewritten on every run; drifted rows get corrected `ready`/`disk_path`/`size_bytes`. A legacy pre-#179 `exposed` key is dropped here — nothing in this file records routing; `running` is left alone.
- Cloud-hosted rows (`location = "cloud"`, including ollama `:cloud` models) are never counted as downloaded. Pulling/removing ollama `:cloud` stubs is the job of `wt cloud-sync` ([wt/docs/wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md)), not `modelman sync`.
- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is `("ollama", "omlx")` — llamacpp was retired 2026-09-07 (`src/modelman/sync.py:31`, `registry.py` `DEFAULT_PROVIDER_IDS`).

Semantics summary: `sync` = read-only over providers (`ollama list`, HF cache, oMLX model dir), writes `~/.config/local-ai/modelman.toml` always; touches `registry.toml` only to repair missing provider entries (prints `Added provider entries: …`), never adds models.

### 7. Routing: what is routed, and when

<!-- UNVERIFIED — the `start` below was not run from this session (it loads a real local model); the success line is from the command source (`src/modelman/main.py`). `wt litellm sync --dry-run` and `wt litellm list` are read-only and were run live 2026-10-03. Errors go to stderr with exit 1. -->

Nothing in this guide's steps asks you to "turn routing on" for a model, because there is nothing to turn on. `wt` derives the LiteLLM routes from `registry.toml` plus live probes:

- **A cloud model** (OpenRouter, ollama `:cloud`) is routed as soon as it is configured — no download, no ready flag.
- **A local model** is routed while it runs; **an ollama model while it is pulled** (ollama loads it on the first request, so stopping it only unloads it and the route stays). That holds with or without a registry entry: a model with none is routed under its discovered id, `<family>/<artifact>` (Step 3).
- **A native model** (`claude/native`, `copilot/native`) never needs a route.

The writer is `wt litellm sync`. `wt start`, `wt stop`, `wt model init` and `wt cloud-sync` update the routes themselves, and modelman runs one sync after each subcommand that can change routing — `sync`, `migrate`, `refresh-prices`, `ollama-catalog sync`, every `start`/`stop`. `wt model add`, `edit` and `rm` sync once after their write, and the Models tab once when you quit (Step 1). A **hand edit** of `registry.toml` is the case no tool sees, so run it yourself after one:

```bash
wt litellm sync --dry-run   # preview: "<id>: would route" / "would unroute" lines, plus probe warnings; writes nothing
wt litellm sync             # apply: writes config.yaml, restarts the proxy only if the file changed
```

To route a local model, start it (illustrative — use any `<provider>/<model>` id from `registry.toml`, or the discovered id of an on-disk model with no entry):

```bash
wt start ollama/gpt-oss:20b    # ollama, omlx, mtplx; `wt start` with no argument is a picker of every local model on disk
```

`wt start` has no backend for an `mlx_lm_server` pairing: it prints the command that starts one, `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` with the pairing's own target and draft ([10-mlx-lm-quantization](10-mlx-lm-quantization.md) Step 4). modelman starts one too, and also still starts the others:

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman start ollama/gpt-oss:20b
```

```text
Started ollama/gpt-oss:20b.
```

```bash
wt litellm list    # the routes config.yaml now holds — the authoritative answer
```

`modelman start <name>` also takes an on-disk model that has **no registry entry** — by its native name, or by its discovered id `<family>/<artifact>` (`ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` — mtplx keeps the `/`). It starts the model without registering it, the closing sync routes it under that id, and `modelman stop <that id>` stops it. `modelman start` with no argument prints the live inventory — registered and on disk, registered but not downloaded, and a `Discovered` section listing each unregistered artifact under its discovered id, with `(running)` beside one modelman started and a probe confirms. `wt start` takes the same ids, and `wt served <provider>` prints what an omlx, mtplx or mlx_lm_server server is serving now. To give such a model a family and tags, register it with `wt model add <provider> <name> --family <family>` (or `enter` on its row in the Models tab), which keeps its discovered id (`<family>/<artifact>` — the convention of Step 3): registering a running model that way keeps its route and its usage history. On a registry whose only omlx-family row is `omlx-6bit`, such a model is still listed and started as `omlx/<directory name>`, through that row.

`modelman start` refuses up front when the ollama daemon isn't answering: the sync that follows would otherwise read the refused probe as "nothing is pulled" and prune every ollama route.

**Taking a model off the proxy**: a cloud model — `wt model rm <id>` (Step 1), whose sync drops its row. A local model — stop it: `wt stop <omlx model>` unloads just that model (the service and its other loaded models stay up) and removes only its route, `wt stop omlx` halts the service and clears the family's routes, and stopping an mtplx model clears that provider family's wt-written routes; `modelman stop <omlx model>` does what `wt stop <omlx model>` does (and `modelman start <omlx model>` what `wt start` does: it loads the model beside the ones already loaded, and unloads others only when omlx needs the memory, printing which), while `modelman stop --all` halts the omlx service. When wt cannot tell which omlx model is loaded — the server wants its API key and the registry names no `auth.secret_ref` (Step 2) — `modelman stop` says so and changes nothing; set `auth.secret_ref` (or run `modelman stop --all`); an `mlx_lm_server` pairing has no wt stop hook, so `modelman` stops it and the sync that follows drops its route once a probe no longer finds it running; a pulled ollama model keeps its route until the model is removed from ollama and a sync runs. Deleting a local registry entry alone does not unroute a model that is still on disk and running (Step 3). A row you wrote by hand in `config.yaml` is never removed or rewritten, even when its name equals a discovered model's id — wt then leaves that name to your row, so delete the row yourself if you want wt's.

**`wt` must be on PATH** (`make install` from the repo root). It writes the `model_list` entry into `~/.config/litellm/config.yaml`, stamping it `model_info.wt_managed: true` and touching only rows it owns plus a few launcher-required `litellm_settings` — hand-written rows, `general_settings`, other sections and comments survive — and restarts LiteLLM itself, but *only when the file actually changed* (`WT_LITELLM_RESTART_CMD`, legacy `MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7). How sync decides — the ownership marker, adoption of unmarked rows, what a rebuild keeps from a hand-edited row, `--dry-run` — is in [04-litellm-config](04-litellm-config.md) §2.

## Verification

One check, because there is one record: `config.yaml`'s `model_list` holds every model the proxy serves.

```bash
wt litellm list        # wt's view, from config.yaml — needs no master key
grep -n "model_name" ~/.config/litellm/config.yaml
```

One line per LiteLLM `model_list` entry, each `<line>:  - model_name: <model-id>` — the model you just started should appear. Example (illustrative — your ids will differ):

```text
3:  - model_name: ollama/qwen3.8:27b-mlx
19:  - model_name: openrouter/qwen/qwen3.8-27b
```

(A running local model, e.g. an omlx one, adds its wt-written row while it runs; `wt litellm list` prints a row without wt's marker — including one a pre-#179 wt wrote, see [04-litellm-config](04-litellm-config.md) §2 *Upgrading from a pre-#179 wt* — with a `(hand-written)` suffix. Never `cat` this file into chat/docs — its `api_key:` values may hold literal keys.)

There is deliberately **no second check in `modelman.toml`**. That file records what modelman owns — whether a model is downloaded and whether it is loaded — and nothing about routing:

```bash
grep -A4 '"<model-id>"' ~/.config/local-ai/modelman.toml
```

```text
[model_state."ollama/gpt-oss:20b"]
ready = true
disk_path = "ollama:gpt-oss:20b"
size_bytes = 13958643712
running = false
```

A block like this with no line in the first grep means the model has no route. For a non-ollama local model that means it is downloaded but not running — or it was started outside wt and modelman and no sync has run since (`wt litellm sync` writes the row, and so does launching it through wt or `wt start <id>`). For ollama it means the model is not *pulled* (a pulled one is routed whether or not it is loaded) or no sync has run since the pull — `wt litellm sync --dry-run` tells the two apart. `uv run modelman start ollama/gpt-oss:20b` fixes either — the start's own `wt litellm sync` writes the row. (A `ready = false` block means modelman does not have the artifact; on an ollama `:cloud` stub or a cloud provider, `ready` is permanently false by design.)

Registry-side probe for a newly added model (after `wt model add` — `modelman sync` never adds model ids); expected output mirrors the Step-3 entry shape (the `id` line plus the 3 lines after it). Example (illustrative — your ids will differ):

```bash
grep -A3 'id = "<new-model>"' ~/.config/local-ai/registry.toml
```

```text
id = "ollama/ornith-1.5:35b"
family = "ornith-1.5:35b"
provider_id = "ollama"
model_name = "ornith-1.5:35b"
```

End-to-end confirm: the model also answers through the proxy — `curl http://localhost:4000/v1/models` with the master key from the LaunchAgent plist (full steps in [01-initial-setup](01-initial-setup.md) §Verification).

## Gotchas

- **`registry.toml` is canonical.** Which cloud models agents see, and the family, tags and cost of every model, change HERE — with `wt model add|edit|rm` or the Models tab of `wt config` (Step 1), which write `~/.config/local-ai/registry.toml`; wt's own `config.toml` holds no model. Which *local* models agents see is what wt's probes find on disk or running (Step 3), with or without an entry. `modelman.toml` is per-machine state (`[model_state]` blocks: `ready`, `disk_path`, `size_bytes`, `running`; a legacy `[families]` table — display names live in `registry.toml`'s `[[families]]` now); never treat it as the model catalog. It carries **no routing field** — a legacy `exposed` key from before #179 is ignored by both modelman and wt, and modelman drops it on its next write.
- **Run modelman from the `modelman/` directory.** modelman is not installed as a global `uv tool`. Always run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman <subcommand> …`; bare `uv run modelman` (the TUI) is disabled.
- **`sync` semantics:** reconcile only (`ollama`/`omlx`; llamacpp retired 2026-09-07), unconfigured models ignored, no models added, then one `wt litellm sync` so the routes follow whatever it changed; ollama `:cloud` stubs are managed by `wt cloud-sync`, not `sync`. If a run prints `Added provider entries: …`, it repaired `registry.toml`.
- **A provider row for every model.** A `[[models]]` block whose `provider_id` names no `[[providers]]` row stops every wt launch with `config error: … unknown provider`, and `wt litellm sync` warns that it cannot route the model and leaves it unrouted; `wt model init` adds the default row for a provider a model references (Step 1).
- **A hand edit is not routed until you sync.** Nothing watches `registry.toml`: `wt model …` and the Models tab sync for themselves, but after an edit of the file run `wt litellm sync` (Step 7). The same goes for a download — nothing records it; wt finds the artifact the next time it probes.
- **Secrets:** wt resolves `secret_ref` (Step 2) and writes the resulting key into the LiteLLM entry's `api_key` — whatever form the ref takes, the live `config.yaml` holds the literal key (e.g. `sk-or-v1-…`) — check and redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- modelman README (TOML shapes, all commands; its TUI section describes the disabled screen): `~/github/ohanaverse/local-ai-setup/modelman/README.md`
- TUI screens and apply-queue design (history — the TUI is disabled): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-26-modelman-tui-design.md`
- Routing design — configured is routed, the ownership marker, `wt litellm sync`: `~/github/ohanaverse/local-ai-setup/docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md` (it supersedes the original per-model expose design, `modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`, kept as history)
- Model-dir sync/reconcile design (sync semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
