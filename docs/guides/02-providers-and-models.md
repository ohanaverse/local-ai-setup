# Providers and models — modelman, registry.toml, and LiteLLM exposure

> Use this to: register cloud providers and local models in the shared `registry.toml`, download them, and expose them to the LiteLLM proxy on :4000 — with modelman's TUI or its non-interactive CLI.
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
| `modelman.toml` | Per-machine mutable state: download markers, family display names, LiteLLM exposure flags | `MODELMAN_STATE` |
| `settings.yaml` | User preferences (theme) | `MODELMAN_SETTINGS` |

Full map including wt and LaunchAgent surfaces: [00-config-map](00-config-map.md)

LiteLLM's `config.yaml` defaults to `~/.config/litellm/config.yaml` (`WT_LITELLM_CONFIG`, legacy alias `MODELMAN_LITELLM_CONFIG`, overrides it; wt is the writer).

## TL;DR

<!-- UNVERIFIED — not run as one block. The `sync` line and the command list were verified live; the `expose`/`unexpose` lines and both TUI launches were not driven from this session (expose/unexpose mutate the live LiteLLM config — see Steps §6–7 and Verification for how to confirm). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman                                # full TUI: browse the model table → add/edit/delete → queue changes → confirm on exit
uv run modelman sync                           # reconcile downloaded/disk_path/size_bytes in modelman.toml against providers; never adds models
# ollama/gpt-oss:20b below is an example id — substitute any id from your registry.toml
uv run modelman expose ollama/gpt-oss:20b      # non-interactive: wt writes a model_list entry; sets exposed = true
uv run modelman unexpose ollama/gpt-oss:20b    # removes the entry and clears the flag
```

Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and queuing ready-on/off (downloaded/applied only after you exit via Apply — see modelman/CLAUDE.md's "Downloads (queued, applied on exit)"). **CLI** = `expose`/`unexpose`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` opens the TUI.

## Steps

### 1. TUI orientation

<!-- UNVERIFIED — interactive TUI, not driven from this session. All keys/behavior below are copied verbatim from the modelman README (§TUI). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman
```

One screen (README, verbatim):

- **Model screen** — the app's only/root screen: a single table of every model across every family (columns: family · provider · model · loc · status · exposed · cost · sub · size), with a details panel below showing the row's on-disk path. Keys: `a` add model, `e` edit (id/provider fixed; location editable to correct mistakes), `d` queue delete (any model — apply skips on-disk removal if the artifact is already gone), `r` toggle ready (queues a download/pull for reconcilable providers, or a flag flip for cloud/native providers; a no-op with a notification for a local-artifact model already on disk — delete the file instead), `x` toggle exposed (cascades a ready toggle first if needed), `l` toggle LiteLLM routing on/off, `enter` edit, `escape` shows the apply/discard/cancel dialog if anything is queued, otherwise quits the app. Reconcile runs automatically on mount — no manual key.

All model changes (adds, edits, deletes, ready toggles, exposure toggles, moves) are queued in memory — nothing downloads or writes to disk while the TUI is open. `Escape`/`Ctrl+Q` with a pending queue shows a confirmation dialog; `Apply` or `Discard` both exit the app. On `Apply`, `main.py` runs the queue in the plain terminal after the TUI closes — deletes, then moves, then ready changes (downloads/clears/flag flips), then exposure changes — printing provider progress and a lifecycle line per operation to stdout, then writes `registry.toml` + `modelman.toml` once.

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

`secret_ref` is written verbatim as `api_key` into the LiteLLM `model_list` entry on expose (`src/modelman/litellm.py:110`) — put the key or a resolvable secret reference there, never a real `sk-or-v1-…` value into any repo; the README's `"sk-or-v1-..."` placeholder above is the shape. `location = "cloud"` is what exempts this provider's models from the "must be downloaded" expose gate.

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

Then register models under it with the same `[[models]]` shape as Step 3 (`provider_id = "openrouter"`, `location = "cloud"`) — a cloud model is exposed without being downloaded.

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

Downloads are **queued in the TUI and applied on exit** — nothing is written until you confirm the pending set on exit; confirming then runs **deletes, then moves, then ready changes (downloads/clears/flag flips), then exposure changes**, and writes `registry.toml` + `modelman.toml` once (README, verbatim).

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
- `modelman.toml` is rewritten on every run; drifted rows get corrected `ready`/`disk_path`/`size_bytes`. The `exposed` flag is left untouched (sync preserves exposure state — it's owned by the LiteLLM feature).
- Cloud-hosted rows (`location = "cloud"`, including ollama `:cloud` models) never count toward the TUI family screen's `downloaded` column or its `size` total. Pulling/removing ollama `:cloud` stubs is the job of `modelman ollama-catalog sync` (see the `ollama-catalog` skill), not `modelman sync`.
- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is `("ollama", "omlx")` — llamacpp was retired 2026-09-07 (`src/modelman/sync.py:31`, `registry.py` `DEFAULT_PROVIDER_IDS`).

Semantics summary: `sync` = read-only over providers (`ollama list`, HF cache, oMLX model dir), writes `~/.config/local-ai/modelman.toml` always; touches `registry.toml` only to repair missing provider entries (prints `Added provider entries: …`), never adds models.

### 7. Expose / unexpose through LiteLLM (CLI)

<!-- UNVERIFIED — mutating commands; not run on this machine (they rewrite the live `~/.config/litellm/config.yaml` + `modelman.toml`). Help text and success lines below are from live `--help` output and the command source (`src/modelman/main.py`). Local models must be downloaded to expose (cloud models exempt); errors go to stderr with exit 1. -->

Example (illustrative — your ids will differ; use any `<provider>/<model>` id from `registry.toml`):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman expose ollama/gpt-oss:20b
```

```text
Exposed ollama/gpt-oss:20b through LiteLLM.
```

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman unexpose ollama/gpt-oss:20b
```

```text
Unexposed ollama/gpt-oss:20b.
```

On success `expose` has **wt** write a `model_list` entry into `~/.config/litellm/config.yaml` (modelman applies its ready/cloud gates, then runs `wt litellm expose --json --skip-ready-gate`, so **`wt` must be on PATH** — `make install`) and flips the model's `exposed` flag in `modelman.toml`; `unexpose` removes the entry and clears the flag. wt only touches the `model_list` section (plus a few launcher-required `litellm_settings`) — `general_settings`, other sections and comments are preserved — and restarts LiteLLM itself right after (`WT_LITELLM_RESTART_CMD`, legacy `MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7 and [04-litellm-config](04-litellm-config.md)). Local models are also routed automatically by `wt start`/`wt stop` without touching the `exposed` flag, so for a local model this grep pair can disagree — `wt litellm list` is the authoritative view. In the TUI the same toggle is `x` on a model row (queued, applied on exit — downloads/pulls first if the model isn't ready yet; the EXPOSED column shows `Y` if both the flag is set and the model is ready, with cloud models — `openrouter/*` or `location = "cloud"` rows — exempt from the ready gate).

## Verification

Confirm a model is exposed — two independent greps, and both must agree (for cloud/native models; for local models see the note after the greps):

```bash
grep -n "model_name" ~/.config/litellm/config.yaml
```

One line per LiteLLM `model_list` entry, each `<line>:  - model_name: <model-id>` — the model you just exposed should appear. Example (illustrative — your ids will differ):

```text
3:  - model_name: ollama/qwen3.8:27b-mlx
19:  - model_name: openrouter/qwen/qwen3.8-27b
```

(A running local model, e.g. an omlx one, adds its wt-written row while it runs. Never `cat` this file into chat/docs — its `api_key:` values may hold literal keys.)

```bash
grep -A4 '"<model-id>"' ~/.config/local-ai/modelman.toml
```

The model's `[model_state]` block. Example (illustrative — your ids will differ): suppose `ollama/gpt-oss:20b` is pulled but not exposed —

```text
[model_state."ollama/gpt-oss:20b"]
ready = true
disk_path = "ollama:gpt-oss:20b"
size_bytes = 13958643712
exposed = false
```

— then the first grep has no line for it, and after `modelman expose` both greps flip together (`exposed = true` plus a `model_name` line).

```bash
grep -c "exposed = true" ~/.config/local-ai/modelman.toml
```

Prints how many models carry the `exposed` flag; the number tracks your own expose/unexpose history and has no fixed expected value.

> **Note:** the `exposed` flag is only authoritative for cloud/native models. For local models it isn't the routing truth — `wt start`/`wt stop`/`wt litellm sync` add and remove their `config.yaml` rows without touching the flag, so a local model can be routed with `exposed = false` (or vice versa). `wt litellm list` is the authoritative view of what LiteLLM actually routes. (Historical: the flags were once out of sync because some entries were seeded outside modelman; hand-written omlx rows were removed 2026-09-30, #168; the llama.cpp rows were retired 2026-09-07.)

Registry-side probe for a newly added model (only applies after a TUI add — `sync` and `expose` never add model ids); expected output mirrors the Step-3 entry shape (the `id` line plus the 3 lines after it). Example (illustrative — your ids will differ):

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

- **`registry.toml` is canonical + read-only to wt.** Model visibility for agents changes HERE — edit `~/.config/local-ai/registry.toml`, not wt's config. `modelman.toml` is per-machine state (`[model_state]` blocks: `ready`, `disk_path`, `size_bytes`, `exposed` — legacy `downloaded`/`litellm_exposed` keys are still read as fallbacks; `[families]` display names); never treat it as the model catalog.
- **Run modelman from the `modelman/` directory.** modelman is not installed as a global `uv tool`. Always run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`.
- **`sync` semantics:** reconcile only (`ollama`/`omlx`; llamacpp retired 2026-09-07), unconfigured models ignored, no models added, `exposed` preserved; ollama `:cloud` stubs are managed by `modelman ollama-catalog sync`, not `sync`. If a run prints `Added provider entries: …`, it repaired `registry.toml`.
- **Providers before models.** The model screen resolves each variant's `provider_id` against `[[providers]]`; a model referencing a missing provider breaks the add flow with `KeyError` (`src/modelman/screens/models.py:91`).
- **TUI changes apply on exit only.** Adds/edits/deletes/downloads/exposure toggles sit in an in-memory queue until you confirm the pending set; deletes run before downloads, downloads before exposure changes, then one write of both files.
- **Secrets:** `secret_ref` is copied verbatim into the LiteLLM entry's `api_key`. If a `secret_ref` holds a literal key, the live `config.yaml` will too (e.g. `sk-or-v1-…`) — check and redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- modelman README (TUI keys, TOML shapes, all commands): `~/github/ohanaverse/local-ai-setup/modelman/README.md`
- TUI screens and apply-queue design: `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-26-modelman-tui-design.md`
- LiteLLM exposure design (provider policies, `model_list` writes): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`
- Model-dir sync/reconcile design (sync semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
