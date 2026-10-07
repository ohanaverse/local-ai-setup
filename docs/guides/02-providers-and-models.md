# Providers and models — modelman, registry.toml, and LiteLLM routing

> Use this to: register cloud providers and local models in the shared `registry.toml` and download them, with modelman's TUI or its non-interactive CLI. Routing through the LiteLLM proxy on :4000 follows on its own: **add a cloud model and it is routed**, and a local model is routed while it runs (an ollama model while it is pulled) — with or without a registry entry.
>
> Verified against: modelman 0.1.0, wt 0.1.0, LiteLLM 1.98.0, Ollama 0.33.2 on 2026-08-29

## Prerequisites

- [01-initial-setup](01-initial-setup.md) complete: LiteLLM proxy running on :4000, Ollama up, services healthy. Guides 03–08 of this set ([03-model-families](03-model-families.md) onward) continue from here.
- modelman runnable from its repo (it is not installed globally; run from the `modelman/` directory):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv sync
```

Then run any modelman command with `uv run modelman …` from that same directory.

```text
Resolved 51 packages in 4ms
Checked 50 packages in 2ms
```

modelman reads three files under `~/.config/local-ai/` (table copied from the modelman README — each file is overridable by env var):

| File | Purpose | Env override |
|------|---------|--------------|
| `registry.toml` | Canonical model/provider definitions (shared; `wt model init` also writes it) | `WT_REGISTRY` (legacy alias `MODELMAN_REGISTRY`) |
| `modelman.toml` | Per-machine mutable state: download markers, family display names, the `running` hint | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |

Full map including wt and LaunchAgent surfaces: [00-config-map](00-config-map.md)

LiteLLM's `config.yaml` defaults to `~/.config/litellm/config.yaml` (`WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`, overrides it; wt is the writer).

## TL;DR

<!-- UNVERIFIED — not run as one block. The `sync` line and the command list were verified live; the `start`/`stop` lines and both TUI launches were not driven from this session (they load real models — see Steps §6–7 and Verification for how to confirm). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman                                # full TUI: browse the model table → add/edit/delete → queue changes → confirm on exit
uv run modelman sync                           # reconcile downloaded/disk_path/size_bytes in modelman.toml against providers; never adds models, then syncs the LiteLLM routes
# ollama/gpt-oss:20b below is an example id — substitute any id from your registry.toml, or a discovered id (Step 7)
uv run modelman start ollama/gpt-oss:20b       # load it; the start's own route sync makes it reachable through LiteLLM
uv run modelman stop ollama/gpt-oss:20b        # unload it (a pulled ollama model stays routed; on mtplx the route goes with it; on omlx it unloads just that model, as `wt stop <model>` does, and removes only its route)
wt litellm sync --dry-run                      # read-only: what wt would route/unroute for the current registry
wt litellm list                                # what is routed right now — the authoritative answer
```

Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and queuing ready-on/off (downloaded/applied only after you exit via Apply — see modelman/CLAUDE.md's "Downloads (queued, applied on exit)"). **CLI** = `start`/`stop`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` opens the TUI. There is no per-model routing command: `wt` routes the cloud models `registry.toml` configures plus the local models it finds running (Step 7).

## Steps

### 1. TUI orientation

<!-- UNVERIFIED — interactive TUI, not driven from this session. All keys/behavior below are copied verbatim from the modelman README (§TUI). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman
```

One screen (README, verbatim):

- **Model screen** — the app's only/root screen: a single table of every model across every family (columns: family · provider · model · loc · status · running · cost · size), with a details panel below showing the row's on-disk path. Keys: `a` add model, `e` edit (id/provider fixed; location editable to correct mistakes), `d` queue delete (any model — apply skips on-disk removal if the artifact is already gone), `r` toggle ready (queues a download/pull for reconcilable providers, or a flag flip for cloud/native providers; a no-op with a notification for a local-artifact model already on disk — delete the file instead), `s` start/stop a ready local model (confirmed, runs immediately, syncs the routes afterwards), `l` toggle LiteLLM routing on/off, `enter` edit, `escape` shows the apply/discard/cancel dialog if anything is queued, otherwise quits the app. Reconcile runs automatically on mount — no manual key. No column shows routing: ask `wt litellm list` what is routed.

All model changes (adds, edits, deletes, ready toggles, moves) are queued in memory — nothing downloads or writes to disk while the TUI is open. `Escape`/`Ctrl+Q` with a pending queue shows a confirmation dialog; `Apply` or `Discard` both exit the app. On `Apply`, `main.py` runs the queue in the plain terminal after the TUI closes — deletes, then moves, then ready changes (downloads/clears/flag flips) — printing provider progress and a lifecycle line per operation to stdout, then writes `registry.toml` + `modelman.toml` once and runs one `wt litellm sync` so the routes follow.

The model screen derives its provider pane from each model's `provider_id` field in `registry.toml`; the add flow raises `KeyError` on a provider id that has no `[[providers]]` entry (`src/modelman/screens/models.py:91`). Keep provider entries ahead of model entries.

### 2. Add a cloud provider (OpenRouter)

`registry.toml` is canonical — add the provider block to `~/.config/local-ai/registry.toml` (hand-edit, or via the TUI's add flow; both write this file). If `wt model init` already added an `openrouter` row (it does when a configured agent lists openrouter), do not add a second one: add `secret_ref` to the row that is there. Documented TOML shape, copied from the modelman README:

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

The key is the `auth.api_key` value in `~/.omlx/settings.json`. This is a hand edit — modelman has no screen for a provider's auth, but it keeps the line when it rewrites the file. Nothing needs setting while omlx has no models loaded, or all of them loaded with `/v1/models` still listing the whole pool: its `/health` answers those without a key. A hidden model is counted in the pool but not listed, so "everything loaded" with one hidden is provable by the status endpoint alone — that case wants the key.

Providers also declare a `protocols` field — the list of wire protocols the provider serves (`"anthropic"`, `"openai-chat"`, `"openai-responses"`; default `["openai-chat"]`). Ollama's discovered entry serves `["anthropic","openai-chat"]`. wt compares an agent's protocols against the provider's to pick direct-vs-LiteLLM routing ([06-wt-agents-and-models](06-wt-agents-and-models.md) §4).

Validate the file after editing (read-only registry load):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run python -c 'from modelman.registry import load_registry; print(sorted(p.id for p in load_registry().providers))'
```

It prints a sorted Python list of every `[[providers]]` id in the registry; a load error means the TOML is malformed. After adding the openrouter block, `'openrouter'` should appear in that list alongside your existing providers. Example (illustrative — your ids will differ):

```text
['ollama', 'openrouter']
```

Then register models under it with the same `[[models]]` shape as Step 3 (`provider_id = "openrouter"`, `location = "cloud"`). That is all a cloud model needs: there is no download and no ready gate, and the next `wt litellm sync` routes it (Step 7).

### 3. Add a local model (Ollama)

In the TUI: press `a` on the model screen to add — then edit (`enter`/`e`) to fill the fields. The add/edit dialog includes optional **Per-token pricing** and **Subscription pricing** sections; check each section to reveal its labeled fields (Input / Cache / Output, each priced per million tokens, for per-token; Amount / Period for subscription) and fill them in. For Ollama models, `model_info` is auto-populated on add by running `ollama show <name>` and translating known capabilities (e.g. `tools` → `supports_function_calling: true`) — no manual capability wiring needed.

Resulting `registry.toml` entry shape. Example (illustrative — your ids will differ):

```toml
[[models]]
id = "ollama/ornith-1.5:35b"
family = "ornith-1.5:35b"
provider_id = "ollama"
model_name = "ornith-1.5:35b"
location = "local"
source = "discovered"
tags = []

[models.model_info]
supports_function_calling = true
supports_vision = true
```

**`location` must be exactly `local` or `cloud`**, on a provider entry and on a model that sets its own. Anything else — `Local`, `Cloud`, a stray space — is a validation error, not a location, and it never falls back the way a missing one does: a model with no location of its own inherits its provider's, while a model whose own value is mistyped has no valid location at all (a provider entry may likewise leave its location out, but a value that is set must be one). A model caught by it is not offered, and `wt litellm sync` keeps its existing route until the registry is repaired. When the family could not be probed — always the case for a mistyped provider entry, since the inventory probes only an exact `local` — the whole family stays frozen and sync warns, naming the gap; on a healthy probed provider only the mistyped model's own row stays and it is simply left unrouted, with no warning. `wt`, `wt start`, `wt stop` and `wt smoke` stop with `config error: provider "<id>" has location "<value>"; expected "local" or "cloud" (fix the entry in <registry path>)` for a mistyped provider entry, or the same with `model "<id>"` for a model's own value, until it is corrected. One mistyped entry therefore stops every launch, not only that model's. modelman does not check the value: it reads anything that is not exactly `local` as not local.

(The `location`/`source`/`tags` keys appear on every TUI-written row and load fine — the README's minimal model example omits them. `source = "discovered"` marks a registry entry modelman registered from an on-disk artifact; it is an overlay like any other, not to be confused with a *discovered model* below, which has no entry at all.)

**`id` is the route name; `model_name` is what the provider is asked for.** The LiteLLM row wt builds uses the registry `id` as its `model_name` (the client-facing id) and the provider prefix plus the registry `model_name` as the upstream model. Keep the two in agreement: when they disagree (a stale suffix left on the id, a typo in either), the route table shows exactly that — wt rebuilds its rows from the registry on every sync, so a hand-correction in `config.yaml` no longer hides the mismatch. Fix it here, in the registry entry. Renaming an `id` renames the route, so anything that references the old id (agent configs, scripts) must follow.

**A local registry entry is an optional overlay** (#179). `wt` lists the local models its live inventory finds — on disk, or running — whether or not `registry.toml` names them; it probes each local provider family (ollama, omlx, mtplx, and `mlx_lm_server` for running state) the registry has a local `[[providers]]` row (`location = "local"`) or a local model for. An entry adds family, tags, cost and `model_info` to the model whose provider family and `model_name` match it (ollama: the same name, with the implicit `:latest`; omlx and mtplx: the same name with or without its `org/` prefix). Without an entry the model still appears in `wt`'s picker — status `new`, no family or tags, so any `-T`/`-F` hides it — and is routed under its discovered id, `<family>/<artifact>`: `ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` (omlx and omlx-6bit are one server and share the `omlx` prefix). An `mlx_lm_server` pairing is the exception: it cannot be discovered, so it needs its entry. An entry whose artifact is confirmed missing from disk is not listed by wt at all; modelman's TUI still lists it, so you can download it (Step 5).

Removing only the entry — a hand-edit of `registry.toml`; the TUI's `d` deletes the artifact too — does not unroute a model that is still on disk and running: the next sync drops the row named after the old id and writes one under the discovered id (when the two ids are equal, the row simply stays). The exception is an `mlx_lm_server` pairing: it cannot be discovered, so with its entry gone nothing names it and the next sync removes its route even while the server is still up. Convention for a new local entry: `id = "<provider family>/<model_name>"` (the family is the provider id, except that `omlx-6bit` belongs to `omlx`). With `model_name` spelled the way the provider lists the artifact, that is the id wt already gives the model without an overlay, so its usage and survey history (keyed by id) carries over when the entry is added or removed. An entry whose id spells a `/` as `--` — the shape the TUI's `a` add form derives for a name containing `/` on every provider except mtplx and the native ones (an omlx repo name becomes `omlx/mlx-community--<name>`) — works, but adding or removing it starts a separate history key. (The `+`-row form of Step 7 registers under the discovered id, so it never has that cost.)

### 4. HF-backed model (oMLX; llama.cpp retired — see [provider-artifacts.md](../reference/provider-artifacts.md))

HF-backed providers pull from Hugging Face via the model's `[models.fetch]` block — exact TOML from the modelman README:

```toml
[models.fetch]               # optional, for HF-backed providers
repo = "org/repo"
files = ["model.gguf"]
quantizations = ["Q4_K_M"]
```

The add dialog collects exactly these fields (`provider`, `name`, `repo`, `files`, `model_info` — see the adapter in `src/modelman/screens/models.py`); `fetch` with a non-empty `repo`/`files`/`quantizations` makes it a download-able HF-backed variant.

### 5. Downloads

Downloads are **queued in the TUI and applied on exit** — nothing is written until you confirm the pending set on exit; confirming then runs **deletes, then moves, then ready changes (downloads/clears/flag flips)**, writes `registry.toml` + `modelman.toml` once, and closes with a single `wt litellm sync` so the routes match what the queue just changed (README, verbatim).

There is no non-interactive `download` subcommand — `modelman download <family>` was retired along with the old family/model/status three-screen TUI. To queue a download, open the plain TUI and press `r` on the model's row:

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman
```

<!-- UNVERIFIED — interactive launch not driven from this session; run it, press `r` on the target model row to queue ready-on, and confirm the download queues, then apply on exit. -->

Press `r` on the row you want (model id comes from the `family`/`provider`/`model_name` fields of the model rows in `registry.toml` — e.g. a row whose `model_name` is `ornith-1.5:35b`; your ids will differ); `escape` shows the apply/discard/cancel dialog, and the queued download runs in the plain terminal once you choose `Apply`.

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
- Cloud-hosted rows (`location = "cloud"`, including ollama `:cloud` models) never count toward the TUI family screen's `downloaded` column or its `size` total. Pulling/removing ollama `:cloud` stubs is the job of `modelman ollama-catalog sync` (see the `ollama-catalog` skill), not `modelman sync`.
- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is `("ollama", "omlx")` — llamacpp was retired 2026-09-07 (`src/modelman/sync.py:31`, `registry.py` `DEFAULT_PROVIDER_IDS`).

Semantics summary: `sync` = read-only over providers (`ollama list`, HF cache, oMLX model dir), writes `~/.config/local-ai/modelman.toml` always; touches `registry.toml` only to repair missing provider entries (prints `Added provider entries: …`), never adds models.

### 7. Routing: what is routed, and when

<!-- UNVERIFIED — the `start` below was not run from this session (it loads a real local model); the success line is from the command source (`src/modelman/main.py`). `wt litellm sync --dry-run` and `wt litellm list` are read-only and were run live 2026-10-03. Errors go to stderr with exit 1. -->

Nothing in this guide's steps asks you to "turn routing on" for a model, because there is nothing to turn on. `wt` derives the LiteLLM routes from `registry.toml` plus live probes:

- **A cloud model** (OpenRouter, ollama `:cloud`) is routed as soon as it is configured — no download, no ready flag.
- **A local model** is routed while it runs; **an ollama model while it is pulled** (ollama loads it on the first request, so stopping it only unloads it and the route stays). That holds with or without a registry entry: a model with none is routed under its discovered id, `<family>/<artifact>` (Step 3).
- **A native model** (`claude/native`, `copilot/native`) never needs a route.

The writer is `wt litellm sync`. modelman runs it once after anything that can change routing — a TUI exit that changed `registry.toml` (add/edit write it immediately), a queue applied on exit, `sync`, `migrate`, `refresh-prices`, `ollama-catalog sync`, every `start`/`stop`, and a TUI mount that found a stale `running` flag — so the usual flow needs no routing command at all. Run it yourself after **hand-editing** `registry.toml` (no modelman command saw the change):

```bash
wt litellm sync --dry-run   # preview: "<id>: would route" / "would unroute" lines, plus probe warnings; writes nothing
wt litellm sync             # apply: writes config.yaml, restarts the proxy only if the file changed
```

To route a local model, start it (illustrative — use any `<provider>/<model>` id from `registry.toml`, or the discovered id of an on-disk model with no entry):

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

`modelman start <name>` also takes an on-disk model that has **no registry entry** — by its native name, or by its discovered id `<family>/<artifact>` (`ollama/<name:tag>`, `omlx/<model directory name>`, `mtplx/<org>/<name>` — mtplx keeps the `/`). It starts the model without registering it, the closing sync routes it under that id, and `modelman stop <that id>` stops it. `modelman start` with no argument prints the live inventory — registered and on disk, registered but not downloaded, and a `Discovered` section listing each unregistered artifact under its discovered id, with `(running)` beside one modelman started and a probe confirms. In the TUI such a model is a `+` row (STATUS `+`; RUNNING `●` while it runs). `s` does not act on a `+` row — start it from the CLI; `enter`/`e` on it opens the registration form, which stays the explicit way to give the model an overlay. That `+`-row form registers the model under its discovered id (`<family>/<artifact>` — the convention of Step 3), so registering a running model keeps its route, its running flag and its usage history. The `a` add form derives its own id: it keeps the slash for mtplx and spells `/` as `--` for every other non-native provider. On a registry whose only omlx-family row is `omlx-6bit`, such a model is still listed and started as `omlx/<directory name>`, through that row.

`modelman start` refuses up front when the ollama daemon isn't answering: the sync that follows would otherwise read the refused probe as "nothing is pulled" and prune every ollama route. In the TUI the equivalent is `s` on a model row: confirmed first, run immediately (not queued), routes synced when it finishes.

**Taking a model off the proxy**: a cloud model — delete it from the registry (TUI `d`) and the sync that closes the apply drops its row. A local model — stop it: `wt stop <omlx model>` unloads just that model (the service and its other loaded models stay up) and removes only its route, `wt stop omlx` halts the service and clears the family's routes, and stopping an mtplx model clears that provider family's wt-written routes; `modelman stop <omlx model>` does what `wt stop <omlx model>` does (and `modelman start <omlx model>` what `wt start` does: it loads the model beside the ones already loaded, and unloads others only when omlx needs the memory, printing which), while `modelman stop --all` halts the omlx service. When wt cannot tell which omlx model is loaded — the server wants its API key and the registry names no `auth.secret_ref` (Step 2) — `modelman stop` says so and changes nothing; set `auth.secret_ref` (or run `modelman stop --all`); an `mlx_lm_server` pairing has no wt stop hook, so `modelman` stops it and the sync that follows drops its route once a probe no longer finds it running; a pulled ollama model keeps its route until the model is removed from ollama and a sync runs. Deleting a local registry entry alone does not unroute a model that is still on disk and running (Step 3). A row you wrote by hand in `config.yaml` is never removed or rewritten, even when its name equals a discovered model's id — wt then leaves that name to your row, so delete the row yourself if you want wt's.

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

Registry-side probe for a newly added model (only applies after a TUI add — `sync` never adds model ids); expected output mirrors the Step-3 entry shape (the `id` line plus the 3 lines after it). Example (illustrative — your ids will differ):

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

- **`registry.toml` is canonical + read-only to wt.** Which cloud models agents see, and the family, tags and cost of every model, change HERE — edit `~/.config/local-ai/registry.toml`, not wt's config. Which *local* models agents see is what wt's probes find on disk or running (Step 3), with or without an entry. `modelman.toml` is per-machine state (`[model_state]` blocks: `ready`, `disk_path`, `size_bytes`, `running`; `[families]` display names); never treat it as the model catalog. It carries **no routing field** — a legacy `exposed` key from before #179 is ignored by both modelman and wt, and modelman drops it on its next write.
- **Run modelman from the `modelman/` directory.** modelman is not installed as a global `uv tool`. Always run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`.
- **`sync` semantics:** reconcile only (`ollama`/`omlx`; llamacpp retired 2026-09-07), unconfigured models ignored, no models added, then one `wt litellm sync` so the routes follow whatever it changed; ollama `:cloud` stubs are managed by `modelman ollama-catalog sync`, not `sync`. If a run prints `Added provider entries: …`, it repaired `registry.toml`.
- **Providers before models.** The model screen resolves each variant's `provider_id` against `[[providers]]`; a model referencing a missing provider breaks the add flow with `KeyError` (`src/modelman/screens/models.py:91`).
- **TUI changes apply on exit only.** Adds/edits/deletes/downloads/ready toggles sit in an in-memory queue until you confirm the pending set; deletes run before downloads, then one write of both files and one route sync.
- **Secrets:** wt resolves `secret_ref` (Step 2) and writes the resulting key into the LiteLLM entry's `api_key` — whatever form the ref takes, the live `config.yaml` holds the literal key (e.g. `sk-or-v1-…`) — check and redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- modelman README (TUI keys, TOML shapes, all commands): `~/github/ohanaverse/local-ai-setup/modelman/README.md`
- TUI screens and apply-queue design: `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-26-modelman-tui-design.md`
- Routing design — configured is routed, the ownership marker, `wt litellm sync`: `~/github/ohanaverse/local-ai-setup/docs/superpowers/specs/2026-10-02-configured-is-exposed-design.md` (it supersedes the original per-model expose design, `modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`, kept as history)
- Model-dir sync/reconcile design (sync semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
