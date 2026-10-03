# Providers and models — modelman, registry.toml, and LiteLLM routing

> Use this to: register cloud providers and local models in the shared `registry.toml`, download them, and get them routed through the LiteLLM proxy on :4000 — with modelman's TUI or its non-interactive CLI.
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
| `registry.toml` | Canonical model/provider definitions (shared, read-only by other tools) | `MODELMAN_REGISTRY` |
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
# ollama/gpt-oss:20b below is an example id — substitute any id from your registry.toml
uv run modelman start ollama/gpt-oss:20b       # load it; the start's own route sync makes it reachable through LiteLLM
uv run modelman stop ollama/gpt-oss:20b        # unload it (for a single-model provider, its route goes with it)
uv run --directory .. wt litellm list          # what is routed right now — the authoritative answer
```

Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and queuing ready-on/off (downloaded/applied only after you exit via Apply — see modelman/CLAUDE.md's "Downloads (queued, applied on exit)"). **CLI** = `start`/`stop`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` opens the TUI. There is no expose/unexpose command any more (#179) — `wt` routes what `registry.toml` configures plus what is running.

## Steps

### 1. TUI orientation

<!-- UNVERIFIED — interactive TUI, not driven from this session. All keys/behavior below are copied verbatim from the modelman README (§TUI). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman
```

One screen (README, verbatim):

- **Model screen** — the app's only/root screen: a single table of every model across every family (columns: family · provider · model · loc · status · running · cost · size), with a details panel below showing the row's on-disk path. Keys: `a` add model, `e` edit (id/provider fixed; location editable to correct mistakes), `d` queue delete (any model — apply skips on-disk removal if the artifact is already gone), `r` toggle ready (queues a download/pull for reconcilable providers, or a flag flip for cloud/native providers; a no-op with a notification for a local-artifact model already on disk — delete the file instead), `s` start/stop a ready local model (confirmed, runs immediately, syncs the routes afterwards), `l` toggle LiteLLM routing on/off, `enter` edit, `escape` shows the apply/discard/cancel dialog if anything is queued, otherwise quits the app. Reconcile runs automatically on mount — no manual key. There is no exposure column: ask `wt litellm list` what is routed.

All model changes (adds, edits, deletes, ready toggles, moves) are queued in memory — nothing downloads or writes to disk while the TUI is open. `Escape`/`Ctrl+Q` with a pending queue shows a confirmation dialog; `Apply` or `Discard` both exit the app. On `Apply`, `main.py` runs the queue in the plain terminal after the TUI closes — deletes, then moves, then ready changes (downloads/clears/flag flips) — printing provider progress and a lifecycle line per operation to stdout, then writes `registry.toml` + `modelman.toml` once and runs one `wt litellm sync` so the routes follow.

The model screen derives its provider pane from each model's `provider_id` field in `registry.toml`; the add flow raises `KeyError` on a provider id that has no `[[providers]]` entry (`src/modelman/screens/models.py:91`). Keep provider entries ahead of model entries.

### 2. Add a cloud provider (OpenRouter)

`registry.toml` is canonical — add the provider block to `~/.config/local-ai/registry.toml` (hand-edit, or via the TUI's add flow; both write this file). Documented TOML shape, copied from the modelman README:

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

`secret_ref` is written verbatim as `api_key` into the LiteLLM `model_list` entry when wt routes the model (`internal/litellm/entry.go`) — put the key or a resolvable secret reference there, never a real `sk-or-v1-…` value into any repo; the README's `"sk-or-v1-..."` placeholder above is the shape. `location = "cloud"` is what keeps this provider's models routable without a download.

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

Then register models under it with the same `[[models]]` shape as Step 3 (`provider_id = "openrouter"`, `location = "cloud"`) — a cloud model is routed without being downloaded.

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

(The `location`/`source`/`tags` keys appear on every TUI-written row and load fine — the README's minimal model example omits them.)

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
- `modelman.toml` is rewritten on every run; drifted rows get corrected `ready`/`disk_path`/`size_bytes`. A legacy `exposed` key is dropped here (there is no exposure flag any more); `running` is left alone.
- Cloud-hosted rows (`location = "cloud"`, including ollama `:cloud` models) never count toward the TUI family screen's `downloaded` column or its `size` total. Pulling/removing ollama `:cloud` stubs is the job of `modelman ollama-catalog sync` (see the `ollama-catalog` skill), not `modelman sync`.
- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is `("ollama", "omlx")` — llamacpp was retired 2026-09-07 (`src/modelman/sync.py:31`, `registry.py` `DEFAULT_PROVIDER_IDS`).

Semantics summary: `sync` = read-only over providers (`ollama list`, HF cache, oMLX model dir), writes `~/.config/local-ai/modelman.toml` always; touches `registry.toml` only to repair missing provider entries (prints `Added provider entries: …`), never adds models.

### 7. Routing through LiteLLM (no expose step)

<!-- UNVERIFIED — the `start` below was not run from this session (it loads a real local model); the success line is from the command source (`src/modelman/main.py`). Errors go to stderr with exit 1. -->

There is no `expose`/`unexpose` command any more (#179). `wt` derives the routes
from `registry.toml` plus live probes: every configured cloud model, and a local
model while it runs (for ollama, while it is pulled). modelman changes state and
then asks wt to reconcile — one `wt litellm sync` after a queue apply, `sync`,
`migrate`, `refresh-prices`, `ollama-catalog sync`, every start/stop, and a TUI
mount that found a stale `running` flag.

So to route a local model, start it (illustrative — use any `<provider>/<model>`
id from `registry.toml`):

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

`modelman start` refuses up front when the ollama daemon isn't answering: the
sync that follows would otherwise read the refused probe as "nothing is pulled"
and prune every ollama route.

**`wt` must be on PATH** (`make install` from the repo root). It writes the
`model_list` entry into `~/.config/litellm/config.yaml`, touching only
`model_list` plus a few launcher-required `litellm_settings` — `general_settings`,
other sections and comments survive — and restarts LiteLLM itself, but *only
when the file actually changed* (`WT_LITELLM_RESTART_CMD`, legacy
`MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k
gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7
and [04-litellm-config](04-litellm-config.md)). In the TUI the equivalent is `s`
on a model row: confirmed first, run immediately (not queued), routes synced
when it finishes. A model removed from `registry.toml` — or, for a local model,
stopped on a single-model provider — loses its route on the next sync.

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

(A running local model, e.g. an omlx one, adds its wt-written row while it runs. Never `cat` this file into chat/docs — its `api_key:` values may hold literal keys.)

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

A block like this with no line in the first grep means exactly one thing: the model is downloaded but not running, so it has no route. `uv run modelman start ollama/gpt-oss:20b` fixes it — the start's own `wt litellm sync` writes the row. (A `ready = false` block means modelman does not have the artifact; on an ollama `:cloud` stub or a cloud provider, `ready` is permanently false by design.)

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

- **`registry.toml` is canonical + read-only to wt.** Model visibility for agents changes HERE — edit `~/.config/local-ai/registry.toml`, not wt's config. `modelman.toml` is per-machine state (`[model_state]` blocks: `ready`, `disk_path`, `size_bytes`, `running`; `[families]` display names); never treat it as the model catalog. It carries **no routing field** — a legacy `exposed` key is ignored and dropped on write (#179), and wt reads it only as an opaque value for its picker's EXPOSED column.
- **Run modelman from the `modelman/` directory.** modelman is not installed as a global `uv tool`. Always run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`.
- **`sync` semantics:** reconcile only (`ollama`/`omlx`; llamacpp retired 2026-09-07), unconfigured models ignored, no models added, then one `wt litellm sync` so the routes follow whatever it changed; ollama `:cloud` stubs are managed by `modelman ollama-catalog sync`, not `sync`. If a run prints `Added provider entries: …`, it repaired `registry.toml`.
- **Providers before models.** The model screen resolves each variant's `provider_id` against `[[providers]]`; a model referencing a missing provider breaks the add flow with `KeyError` (`src/modelman/screens/models.py:91`).
- **TUI changes apply on exit only.** Adds/edits/deletes/downloads/ready toggles sit in an in-memory queue until you confirm the pending set; deletes run before downloads, then one write of both files and one route sync.
- **Secrets:** `secret_ref` is copied verbatim into the LiteLLM entry's `api_key`. If a `secret_ref` holds a literal key, the live `config.yaml` will too (e.g. `sk-or-v1-…`) — check and redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- modelman README (TUI keys, TOML shapes, all commands): `~/github/ohanaverse/local-ai-setup/modelman/README.md`
- TUI screens and apply-queue design: `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-26-modelman-tui-design.md`
- LiteLLM exposure design (provider policies, `model_list` writes): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`
- Model-dir sync/reconcile design (sync semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
