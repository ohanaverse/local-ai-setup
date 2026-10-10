# Providers and models — wt model, registry.toml, and LiteLLM routing

> Use this to: register cloud providers and local models in the shared `registry.toml` — with `wt model add`, `edit` and `rm`, or the Models tab of `wt config` — and download them with each provider's own tool. Routing through the LiteLLM proxy on :4000 follows on its own: **add a cloud model and it is routed**, and a local model is routed while it runs (an ollama model while it is pulled) — with or without a registry entry.
>
> Verified against: wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29 · revised 2026-10 (commands checked against `wt --help` and `llmbench --help`, not re-run live)

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: LiteLLM proxy running on :4000, Ollama up, services healthy. Guides 03–08 of this set ([03-model-families](03-model-families.md) onward) continue from here.
- `wt` on PATH (`make install` from the repo root): `wt model init`, `wt litellm sync`, `wt start` and `wt stop` do most of what follows.

wt's registry file under `~/.config/local-ai/` is this one:

| File | Purpose | Env override |
|------|---------|--------------|
| `registry.toml` | Canonical model/provider definitions (wt writes it; llmbench reads it) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |

Full map including wt and LaunchAgent surfaces: [00-config-map](00-config-map.md)

LiteLLM's `config.yaml` defaults to `~/.config/litellm/config.yaml` (`WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`, overrides it; wt is the writer).

## TL;DR

<!-- UNVERIFIED — not run as one block. The `start`/`stop` lines were not driven from this session (they load real models — see Step 6 and Verification for how to confirm). The `wt model` lines were run against a scratch registry, the tab from a pty. -->

```bash
wt model init                                  # create registry.toml if it is missing, add the default provider rows (never changes a row that exists)
wt model                                       # the Models tab of `wt config`: n add, enter edit, d remove (Step 1)
wt model add openrouter <model> --family <f>   # the same without a terminal; also `wt model edit <id> …`, `wt model rm <id>`
wt model list                                  # every registry model, and every local model found that has no entry
wt litellm sync --dry-run                      # read-only: what wt would route/unroute for the registry as edited
wt litellm sync                                # apply it — `wt model …` and the tab do this themselves; run it after a hand edit
wt litellm list                                # what is routed right now — the authoritative answer
ollama pull <name:tag>                         # download: the provider's own tool (Step 5 has omlx, mtplx, mlx_lm_server)
# ollama/gpt-oss:20b below is an example id — substitute any id from your registry.toml, or a discovered id (Step 6)
wt start ollama/gpt-oss:20b                    # load it; the start writes its LiteLLM route (`wt start` alone: a picker of what is on disk)
wt stop ollama/gpt-oss:20b                     # unload it (a pulled ollama model stays routed; on mtplx the route goes with it; on omlx only that model is unloaded and unrouted)
```

Who does what now (verified via `wt --help`): **wt** = `wt model` (the Models tab of `wt config`) and `wt model add|edit|rm|list` (models: add, edit, remove, list), `wt model init` (the file and its provider rows), `wt cloud-sync` (OpenRouter prices, and ollama's cloud models with their prices), `wt litellm …` (routes), `wt start`/`wt stop` (local models), `wt served <provider>` (what a server is serving). **The provider's own tool** = downloads (Step 5). **llmbench** = benchmarks. **By hand** = a provider row wt has no default for, a `local_path` entry for a single model ([10-mlx-lm-quantization](10-mlx-lm-quantization.md)), `[[families]]` display names, and the repair of a row wt refuses to write (Step 1). There is no per-model routing command: `wt` routes the cloud models `registry.toml` configures plus the local models it finds running (Step 6).

## Steps

### 1. Add, edit and remove models with `wt model`

A model is added, edited and removed with wt, two ways that write the same rows:

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
3. **Routes follow by themselves.** Each command syncs the LiteLLM routes once after its write, and the tab once when you quit (Step 6). A sync that could not run is a warning; the registry change stands.

What to keep in mind:

- **A model's provider must have a `[[providers]]` row.** `wt model add` refuses a provider that has none and that it has no default row for, and names the file to add it to. A registry that already holds such a model makes `wt`, `wt start` and `wt stop` stop with `config error: model "<id>": unknown provider "<provider>"`, and `wt litellm sync` warns that it cannot route the model; `wt model list` and `wt model rm` still work, so the row can be seen and removed; `wt model edit` refuses the row until its `[[providers]]` block is added, which is a hand edit (below).
- **A few things stay a hand edit of `registry.toml`:** a `[[providers]]` row for a provider wt has no default for (Step 2), a model served from a directory of your own (`[models.fetch] local_path`, guide 10), `[[families]]` display names (guide 03), and the repair of a row wt will not write: two `[[models]]` rows that share an id (`wt model edit`, `wt model rm` and the tab refuse the id rather than pick a row, and name both rows' providers), or a `fetch` that is not a table (`fetch must be a table`). `wt model list` shows such rows, so you can see what to fix. The procedure for one: edit the file; run `wt litellm sync --dry-run`, a read-only check that prints `config error: parse …` when the file is no longer valid TOML and otherwise the route changes a sync would make; then `wt litellm sync`, because no tool saw the edit. Steps 2–4 show a block per provider kind; every field name there is one the shared fixture `docs/contracts/registry.sample.toml` pins for wt and llmbench.
- **A local provider's `base_url` names the port its server listens on.** The rows `wt model init` writes do. wt reads the url as written, and one with no port means the scheme's own (80 for `http`, 443 for `https`): omlx and ollama listen where their own settings say, and wt cannot know that port. The one exception is mtplx, which wt itself starts with a port: an `http` url for the `mtplx` provider on this machine (`localhost`, `127.0.0.1`, `[::1]`, or `0.0.0.0`) with no port means 8003, where `wt start` serves mtplx, for everything wt does with the address — the status it shows, `wt start` and `wt stop`, the LiteLLM route and a direct launch. If your mtplx really is on port 80, write `:80`. wt goes by the url's text, so a host name that only resolves to this machine (an `/etc/hosts` alias) is not recognised: name the port there too.
- **Comments do not survive.** The first tool that changes the file — a `wt model` command or `wt cloud-sync` — lays the whole file out again without them. Keys wt does not know are kept.
- **Downloads, starts and stops:** downloads are the provider's own tool (Step 5), starting and stopping is `wt start`/`wt stop` (Step 6), and nothing records whether a model is ready — wt probes the providers for what is on disk.
- **Cloud rows are never downloads.** Cloud-hosted rows (`location = "cloud"`, ollama `:cloud` models included) are never treated as downloads; `wt cloud-sync` pulls and removes the ollama cloud stubs.
- **No per-model state.** wt stores no per-model state: `wt model list` shows what is on disk (STATUS) and what is running (RUNNING) from a live probe.

### 2. Add a cloud provider (OpenRouter)

`registry.toml` is canonical. For OpenRouter the provider block is written for you: `wt model init` adds it when a model or a configured agent names openrouter, and the first `wt model add openrouter …` adds it in the same write as the model. The block of any other cloud provider is a hand edit — a provider row is one of the things wt has no command for (Step 1). Do not add a second `openrouter` row: the one wt writes has `secret_ref = "OPENROUTER_API_KEY"`, the name of the environment variable wt reads the key from — export the variable, or change `secret_ref` on the row that is there to where you keep the key. The TOML shape:

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

wt resolves `secret_ref` — an env-var name (or `os.environ/NAME`), an `exec:` helper command, or a literal key — and writes the result as `api_key` into each of the provider's LiteLLM `model_list` entries (`wt/internal/litellm/entry.go`); never commit a real `sk-or-v1-…` value to any repo — the `"sk-or-v1-..."` placeholder above is the shape. An env-var ref must be set in the shell that runs the sync, or wt reports `resolved empty` and keeps the existing rows. `location = "cloud"` is what makes this provider's models routable without a download.

**A key for a local omlx server.** omlx can require an API key for its management endpoints while leaving inference open, and wt needs one of those endpoints to tell which omlx models are loaded when only some of them are ([08-maintenance-and-troubleshooting](08-maintenance-and-troubleshooting.md), Gotchas). Give wt the key by adding a `secret_ref` to the omlx provider — one key for the server, however many models it holds; `omlx` and `omlx-6bit` are the same server, so the `omlx` entry is enough. `type` stays `"none"`. wt sends the key on that probe and on the one-token warmup request that loads a model (`wt start`, and `wt warm`, which `llmbench provider isolate omlx` and the benchmarks use) — an omlx that also wants the key for inference refuses a keyless warmup, and the start fails at once naming this setting. It is not written into the model's LiteLLM route, which keeps `api_key: not-needed` ([04-litellm-config](04-litellm-config.md)): requests through LiteLLM reach omlx keyless, so they are served only while omlx allows unauthenticated inference (`auth.allow_unauthenticated_inference` in `~/.omlx/settings.json`).

```toml
[[providers]]
id = "omlx"
# ...
[providers.auth]
type = "none"
base_url = "http://localhost:8000"
secret_ref = "OMLX_API_KEY"   # env-var name, os.environ/NAME, an exec: helper, or the literal key
```

The key is the `auth.api_key` value in `~/.omlx/settings.json`. This is a hand edit of the provider row — wt has no command for one (Step 1). Nothing needs setting while omlx has no models loaded, or all of them loaded with `/v1/models` still listing the whole pool: its `/health` answers those without a key. A hidden model is counted in the pool but not listed, so "everything loaded" with one hidden is provable by the status endpoint alone — that case wants the key.

Providers also declare a `protocols` field — the list of wire protocols the provider serves (`"anthropic"`, `"openai-chat"`, `"openai-responses"`; default `["openai-chat"]`). Ollama's discovered entry serves `["anthropic","openai-chat"]`. wt compares an agent's protocols against the provider's to pick direct-vs-LiteLLM routing ([06-wt-agents-and-models](06-wt-agents-and-models.md) §4).

The models of the `openrouter` provider, and no others, take their price from OpenRouter: `wt cloud-sync` refreshes it, and after a launch wt reminds you when the newest refresh is more than a week old. A model under any other cloud provider (a gateway of your own, say) is not refreshed and not reminded about; its price is the one you give it (`wt model add` / `wt model edit` with `--input-price`, `--output-price`). An older registry may carry a provider key `openrouter_priced` (`true` or `false`), which used to change that rule; wt no longer reads it. Such a registry still loads, the key decides nothing, and wt keeps it when it rewrites the file, so you can delete it by hand at any time.

List the provider ids in the file:

```bash
grep -A1 '^\[\[providers\]\]' ~/.config/local-ai/registry.toml | grep '^id'
```

It prints one `id = "<provider>"` line per `[[providers]]` row whose `id` is the first key under the header, which is where wt writes it. After adding the openrouter block, `id = "openrouter"` should appear alongside your existing providers. Whether the file is still valid TOML is the `wt litellm sync --dry-run` check of Step 1. Example (illustrative — your ids will differ):

```text
id = "ollama"
id = "openrouter"
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

The command for it is `wt model add openrouter <org>/<model> --family <family>` (Step 1), which adds the `openrouter` provider row when it is missing, writes a block like this one and syncs the route. The id it derives spells the `/` of the name as `--` (`openrouter/<org>--<model>`); `--id openrouter/<org>/<model>` gives the spelling above. That is all a cloud model needs: there is no download and no ready gate (Step 6). Prices go in a `[models.cost]` table (`input_price_per_million`, `cache_price_per_million`, `output_price_per_million`, and `subscription_price` + `subscription_period`); for OpenRouter models leave it out and run `wt cloud-sync --only openrouter`, which fetches the per-token prices, shows what it would change and asks before writing them. For a model OpenRouter prices by time of day it stores the whole schedule (the dearest level as the price, each other level as a `cost.time_prices` row labelled `openrouter`), and the model picker shows the level in force when it opens. For a model several providers serve, the price OpenRouter lists is one provider's quote and can move within minutes, so two syncs a few minutes apart can each show a price update for it although no provider changed a price; that is expected ([wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md#a-listed-price-that-moves-between-syncs)). Ollama's `:cloud` models and their prices are added and removed by `wt cloud-sync`'s ollama flow — no hand edit. Start with `wt cloud-sync --dry-run`, which prints both plans and changes nothing; a plan that removes a model is applied only after you approve it. Flags, exit codes and recovery: [wt/docs/wt-cloud-sync.md](../../wt/docs/wt-cloud-sync.md).

### 3. Add a local model (Ollama)

Pull the model first (Step 5), then register it with `wt model add ollama <name:tag> --family <family>` (Step 1) — or leave it out: an entry for a local model is optional (see *A local registry entry is an optional overlay* below). `wt model add` fills `model_info` from `ollama show <name>`: `tools` → `supports_function_calling = true`, `vision` → `supports_vision = true`. If that lookup fails it says so and adds the model without them; set the keys in the block yourself then. A local model needs no `[models.cost]`.

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

**`location` must be exactly `local` or `cloud`**, on a provider entry and on a model that sets its own. Anything else — `Local`, `Cloud`, a stray space — is a validation error, not a location, and it never falls back the way a missing one does: a model with no location of its own inherits its provider's, while a model whose own value is mistyped has no valid location at all (a provider entry may likewise leave its location out, but a value that is set must be one). A model caught by it is not offered, and `wt litellm sync` keeps its existing route until the registry is repaired. When the family could not be probed — always the case for a mistyped provider entry, since the inventory probes only an exact `local` — the whole family stays frozen and sync warns, naming the gap; on a healthy probed provider only the mistyped model's own row stays and it is simply left unrouted, with no warning. `wt`, `wt start`, `wt stop` and `wt smoke` stop with `config error: provider "<id>" has location "<value>"; expected "local" or "cloud" (fix the entry in <registry path>)` for a mistyped provider entry, or the same with `model "<id>"` for a model's own value, until it is corrected. One mistyped entry therefore stops every launch, not only that model's.

(`location` may be left out — the model then inherits its provider's. An older row may also carry `source = "discovered"`, which marks an entry registered from an on-disk artifact; it loads fine, a hand-written block does not need it, and it is not to be confused with a *discovered model* below, which has no entry at all.)

**`id` is the route name; `model_name` is what the provider is asked for.** The LiteLLM row wt builds uses the registry `id` as its `model_name` (the client-facing id) and the provider prefix plus the registry `model_name` as the upstream model. Keep the two in agreement: when they disagree (a stale suffix left on the id, a typo in either), the route table shows exactly that — wt rebuilds its rows from the registry on every sync, so a hand-correction in `config.yaml` no longer hides the mismatch. Fix it here, in the registry entry. Renaming an `id` renames the route, so anything that references the old id (agent configs, scripts) must follow.

**A local registry entry is an optional overlay** (#179). `wt` lists the local models its live inventory finds — on disk, or running — whether or not `registry.toml` names them; it probes each local provider family (ollama, omlx, mtplx, and `mlx_lm_server` for running state) the registry has a local `[[providers]]` row (`location = "local"`) or a local model for. An entry adds family, tags, cost and `model_info` to the model whose provider family and `model_name` match it (ollama: the same name, with the implicit `:latest`; omlx and mtplx: the same name with or without its `org/` prefix). Without an entry the model still appears in `wt`'s picker — status `new`, no family or tags, so any `-T`/`-F` hides it — and is routed under its discovered id, `<family>/<artifact>`: `ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` (omlx and omlx-6bit are one server and share the `omlx` prefix). An `mlx_lm_server` pairing is the exception: it cannot be discovered, so it needs its entry. An entry whose artifact is confirmed missing from disk is not listed by wt at all until you download it (Step 5); `wt model list` still shows it, with STATUS `missing`.

Removing only the entry — deleting its block; the artifact stays on disk until you remove it with the provider's tool (Step 5) — does not unroute a model that is still on disk and running: the next sync drops the row named after the old id and writes one under the discovered id (when the two ids are equal, the row simply stays). The exception is an `mlx_lm_server` pairing: it cannot be discovered, so with its entry gone nothing names it and the next sync removes its route even while the server is still up. Convention for a new local entry: `id = "<provider family>/<model_name>"` (the family is the provider id, except that `omlx-6bit` belongs to `omlx`). With `model_name` spelled the way the provider lists the artifact, that is the id wt already gives the model without an overlay, so its usage and survey history (keyed by id) carries over when the entry is added or removed. An entry whose id spells a `/` as `--` — the shape older registries hold for a name containing `/` on every provider except mtplx and the native ones (an omlx repo name became `omlx/mlx-community--<name>`), so an older registry may hold some — works, but adding or removing it starts a separate history key.

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

[models.fetch]                       # optional; wt shows the path (wt model list --json) and never deletes it
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

wt does not download models and has no download command. Use the provider's own tool, into the place that provider reads:

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
- **Afterwards** the model is on disk, which is all wt asks: `wt start` lists it (Step 6) with or without a registry entry.

### 6. Routing: what is routed, and when

<!-- UNVERIFIED — the `start` below was not run from this session (it loads a real local model); the success line is from `wt/cmd/wt/model_cmds.go` (`runStart`). `wt litellm sync --dry-run` and `wt litellm list` are read-only and were run live 2026-10-03. Errors go to stderr with exit 1. -->

Nothing in this guide's steps asks you to "turn routing on" for a model, because there is nothing to turn on. `wt` derives the LiteLLM routes from `registry.toml` plus live probes:

- **A cloud model** (OpenRouter, ollama `:cloud`) is routed as soon as it is configured — no download, no ready flag.
- **A local model** is routed while it runs; **an ollama model while it is pulled** (ollama loads it on the first request, so stopping it only unloads it and the route stays). That holds with or without a registry entry: a model with none is routed under its discovered id, `<family>/<artifact>` (Step 3).
- **A native model** (`claude/native`, `copilot/native`) never needs a route.

The writer is `wt litellm sync`. `wt start`, `wt stop`, `wt model init` and `wt cloud-sync` update the routes themselves. `wt model add`, `edit` and `rm` sync once after their write, and the Models tab once when you quit (Step 1). A **hand edit** of `registry.toml` is the case no tool sees, so run it yourself after one:

```bash
wt litellm sync --dry-run   # preview: "<id>: would route" / "would unroute" lines, plus probe warnings; writes nothing
wt litellm sync             # apply: writes config.yaml, restarts the proxy only if the file changed
```

To route a local model, start it (illustrative — use any `<provider>/<model>` id from `registry.toml`, or the discovered id of an on-disk model with no entry):

```bash
wt start ollama/gpt-oss:20b    # ollama, omlx, mtplx; `wt start` with no argument is a picker of every local model on disk
```

`wt start` has no backend for an `mlx_lm_server` pairing: it prints the command that starts one, `llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` with the pairing's own target and draft ([10-mlx-lm-quantization](10-mlx-lm-quantization.md) Step 4). wt starts the others:

```bash
wt start ollama/gpt-oss:20b
```

```text
wt: ollama/gpt-oss:20b is running
```

```bash
wt litellm list    # the routes config.yaml now holds — the authoritative answer
```

`wt start <id>` also takes an on-disk model that has **no registry entry**, by its discovered id `<family>/<artifact>` (`ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` — mtplx keeps the `/`). It starts the model without registering it and routes it under that id, and `wt stop <that id>` stops it. `wt model list` prints the live inventory — every registry model with its STATUS (`ok`, or `missing` for a local one that is not on disk), and each unregistered artifact under its discovered id with STATUS `new` — with RUNNING `run` beside a model a probe finds serving. `wt served <provider>` prints what an omlx, mtplx or mlx_lm_server server is serving now. To give such a model a family and tags, register it with `wt model add <provider> <name> --family <family>` (or `enter` on its row in the Models tab), which keeps its discovered id (`<family>/<artifact>` — the convention of Step 3): registering a running model that way keeps its route and its usage history. On a registry whose only omlx-family row is `omlx-6bit`, such a model is still listed and started as `omlx/<directory name>`, through that row.

`wt start` of an ollama model fails at once when the ollama daemon is not answering (`ollama is not answering at <origin> — start it first`) and writes no route: start the ollama app or service and run it again. A `wt litellm sync` run while the daemon refuses the connection reads it as "nothing is listening" and drops the ollama routes, with a warning; the next sync after the daemon is back writes them again.

**Taking a model off the proxy**: a cloud model — `wt model rm <id>` (Step 1), whose sync drops its row. A local model — stop it: `wt stop <omlx model>` unloads just that model (the service and its other loaded models stay up) and removes only its route, `wt stop omlx` halts the service and clears the family's routes, and stopping an mtplx model clears that provider family's wt-written routes; `wt stop --all` stops every running local model and then halts the omlx service. When omlx wants its API key for the unload and the registry names no `auth.secret_ref` (Step 2), `wt stop <omlx model>` says so (`omlx will not unload <model> without its API key …`) and changes nothing; set `auth.secret_ref` (or run `wt stop omlx`); an `mlx_lm_server` pairing has no wt stop hook, and `wt stop --all` leaves it running: `uv run --directory llmbench llmbench provider stop mlx_lm_server` stops it, and the next `wt litellm sync` drops its route; a pulled ollama model keeps its route until the model is removed from ollama and a sync runs. Deleting a local registry entry alone does not unroute a model that is still on disk and running (Step 3). A row you wrote by hand in `config.yaml` is never removed or rewritten, even when its name equals a discovered model's id — wt then leaves that name to your row, so delete the row yourself if you want wt's.

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

There is no second place to check. Routing is derived: `wt litellm list` is the only answer to 'is it routed?', and `wt model list` shows whether a local model is on disk and running. A local model with no line in `wt litellm list` is stopped, or was started outside wt and no sync has run since (`wt litellm sync`).

Registry-side probe for a newly added model (after `wt model add`); expected output mirrors the Step-3 entry shape (the `id` line plus the 3 lines after it). Example (illustrative — your ids will differ):

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

- **`registry.toml` is canonical.** Which cloud models agents see, and the family, tags and cost of every model, change HERE — with `wt model add|edit|rm` or the Models tab of `wt config` (Step 1), which write `~/.config/local-ai/registry.toml`; wt's own `config.toml` holds no model. Which *local* models agents see is what wt's probes find on disk or running (Step 3), with or without an entry. wt stores no per-model state and no routing flag: `wt model list` and `wt litellm list` read both live.
- **A provider row for every model.** A `[[models]]` block whose `provider_id` names no `[[providers]]` row stops every wt launch with `config error: … unknown provider`, and `wt litellm sync` warns that it cannot route the model and leaves it unrouted; `wt model init` adds the default row for a provider a model references (Step 1).
- **A hand edit is not routed until you sync.** Nothing watches `registry.toml`: `wt model …` and the Models tab sync for themselves, but after an edit of the file run `wt litellm sync` (Step 6). The same goes for a download — nothing records it; wt finds the artifact the next time it probes.
- **Secrets:** wt resolves `secret_ref` (Step 2) and writes the resulting key into the LiteLLM entry's `api_key` — whatever form the ref takes, the live `config.yaml` holds the literal key (e.g. `sk-or-v1-…`) — check and redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- TUI screens and apply-queue design (history — modelman and its TUI are deleted): `~/github/ohanaverse/local-ai-setup/docs/superpowers/modelman/specs/2026-08-26-modelman-tui-design.md`
- Routing design — configured is routed, the ownership marker, `wt litellm sync`: `~/github/ohanaverse/local-ai-setup/docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md` (it supersedes the original per-model expose design, `docs/superpowers/modelman/specs/2026-08-28-modelman-litellm-exposure-design.md`, kept as history)
- Model-dir sync/reconcile design (history — `modelman sync` is deleted): `~/github/ohanaverse/local-ai-setup/docs/superpowers/modelman/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
