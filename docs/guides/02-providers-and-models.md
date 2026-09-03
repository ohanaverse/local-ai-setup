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

LiteLLM's `config.yaml` defaults to `~/.config/litellm/config.yaml` (`MODELMAN_LITELLM_CONFIG` overrides it).

## TL;DR

<!-- UNVERIFIED — not run as one block. The `sync` line and the command list were verified live; the `expose`/`unexpose` lines and both TUI launches were not driven from this session (expose/unexpose mutate the live LiteLLM config — see Steps §6–7 and Verification for how to confirm). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman                                # full TUI: browse families → add/edit/delete → queue changes → confirm on exit
uv run modelman sync                           # reconcile downloaded/disk_path/size_bytes in modelman.toml against providers; never adds models
uv run modelman expose ollama/gpt-oss:20b      # non-interactive: writes a model_list entry + sets litellm_exposed = true
uv run modelman unexpose ollama/gpt-oss:20b    # removes the entry and clears the flag
```

Scope split (verified via `uv run modelman --help` and the TUI key lists below): **TUI-only** = adding/editing/deleting providers and models (writes `registry.toml`), display-name edits, and downloads with live progress (`download` opens the TUI at a model screen). **CLI** = `expose`/`unexpose`, `sync`, `migrate`, `benchmark`, `usage`; bare `modelman` and `modelman download <family>` open the TUI.

## Steps

### 1. TUI orientation

<!-- UNVERIFIED — interactive TUI, not driven from this session. All keys/behavior below are copied verbatim from the modelman README (§TUI). -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman
```

Three screens (README, verbatim):

- **Family screen** — table of families with columns: family · display · variants · downloaded · size. Keys: `a` add, `e` edit display name, `d` delete (blocked if anything is downloaded), `enter` open, `q` quit. Reconcile runs automatically on mount/resume — no manual key. The `downloaded` column counts only local models; cloud entries are excluded from both the count and the `size` column.
- **Model screen** — single table scoped to one family (columns: family · provider · model · loc · status · exposed · cost · sub · size), with a details panel below showing the row's on-disk path. Keys: `a` add model, `e` edit (id/provider fixed), `d` queue delete (any model — apply skips on-disk removal if the artifact is already gone), `r` toggle ready (queues download/pull; a no-op with a notification for a local-artifact model already on disk — delete the file instead), `x` toggle exposed (cascades a ready toggle first if needed), `enter` edit, `escape` back / apply queue.
- **Status screen** — when you apply on exit, the model screen hands off to a status screen that streams per-item progress (`Deleting …`, `Downloaded …`, `Saving …`) into a scrollable log. `Escape` mid-run pops a Cancel-or-Wait dialog: `Cancel` kills any running subprocess (Ollama) and stops the queue; `Wait` keeps waiting. Once the run completes (or is cancelled), `Escape` returns to the family screen.

The model screen derives its provider pane from each family model-variant's `provider_id` field in `registry.toml`; the add flow raises `KeyError` on a provider id that has no `[[providers]]` entry (`src/modelman/screens/models.py:40-43`). Keep provider entries ahead of model entries.

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

Validate the file after editing (read-only registry load):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run python -c 'from modelman.registry import load_registry; print(sorted(p.id for p in load_registry().providers))'
```

```text
['ollama']
```

<!-- UNVERIFIED — post-add state not exercised (would mutate the live registry.toml). After adding the openrouter block the same one-liner prints: -->

```text
['ollama', 'openrouter']
```

Then register models under it with the same `[[models]]` shape as Step 3 (`provider_id = "openrouter"`, `location = "cloud"`) — a cloud model is exposed without being downloaded.

### 3. Add a local model (Ollama)

In the TUI: family screen `a` to add, or open a family (`enter`) and press `a` on the model screen — then edit (`enter`/`e`) to fill the fields. The add/edit dialog includes optional **Per-token pricing** and **Subscription pricing** sections; check each section to reveal its labeled fields (Input / Cache / Output, each priced per million tokens, for per-token; Amount / Period for subscription) and fill them in. For Ollama models, `model_info` is auto-populated on add by running `ollama show <name>` and translating known capabilities (e.g. `tools` → `supports_function_calling: true`) — no manual capability wiring needed.

Resulting `registry.toml` entry — real, as written on disk here (`~/.config/local-ai/registry.toml`, verified on this machine):

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

### 4. HF-backed model (llama.cpp / oMLX)

HF-backed providers pull from Hugging Face via the model's `[models.fetch]` block — exact TOML from the modelman README:

```toml
[models.fetch]               # optional, for HF-backed providers
repo = "org/repo"
files = ["model.gguf"]
quantizations = ["Q4_K_M"]
```

The add dialog collects exactly these fields (`provider`, `name`, `repo`, `files`, `model_info` — see the adapter in `src/modelman/screens/models.py`); `fetch` with a non-empty `repo`/`files`/`quantizations` makes it a download-able HF-backed variant.

### 5. Downloads

Downloads are **queued in the TUI and applied on exit** — nothing is written until you confirm the pending set on exit; confirming then runs **deletes first, then downloads, then exposure changes**, and writes `registry.toml` + `modelman.toml` once (README, verbatim).

Non-interactive entry point — `download` opens the TUI directly at that family's model screen (help text captured live):

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman download --help
```

```text
 Usage: modelman download [OPTIONS] {family}

 Open the TUI at a family's model screen (queued downloads on exit).

╭─ Arguments ──────────────────────────────────────────────────────────────────╮
│ *    family      <str>  Family name (filename under families dir) [required] │
╰──────────────────────────────────────────────────────────────────────────────╯
```

(Options box with --help elided.)

<!-- UNVERIFIED — interactive launch not driven from this session; run it and confirm downloads queue, then apply on exit. -->

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman download ornith-1.5:35b
```

(Family id comes from the `family` field of the model rows in `registry.toml` — here `ornith-1.5:35b`, verified on disk.)

### 6. Reconcile: `sync`

```bash
# from: ~/github/ohanaverse/local-ai-setup/modelman
uv run modelman sync
```

```text
Synced: 13 downloaded, 9 not downloaded.
```

(Run live 2026-08-29 — exactly this single line, exit 0. `sync` takes no options at all; `sync --help` shows only `--help`.)

What it did / didn't do, as observed:

- The counts match the registry exactly: 13 local Ollama models present on disk with sizes, and the 9 `:cloud` registry rows — `ollama list` shows cloud rows with size `-`, and sync marks them `downloaded = false`. In the TUI family screen, those `:cloud` rows do not count toward the `downloaded` column and do not contribute to the family's `size` total.
- Models in `ollama list` but absent from `registry.toml` (e.g. `glm-5.3:cloud`) were ignored — sync only reconciles **configured** models.
- `modelman.toml` was rewritten with identical values (mtime bumped; no field changed — nothing had drifted). It updated/reaffirmed `downloaded`/`disk_path`/`size_bytes` per model; `litellm_exposed` was left untouched (sync preserves exposure state — it's owned by the LiteLLM feature).
- It did **not** add models, did not touch `registry.toml`, and printed no `Added provider entries:` line — that line only appears when sync has to repair a missing provider entry.
- Cloud providers (OpenRouter) are never reconciled. Documented reconcilable set is `("ollama", "llamacpp", "omlx")` (`src/modelman/sync.py:31`).

Semantics summary: `sync` = read-only over providers (`ollama list`, HF cache, oMLX model dir), writes `~/.config/local-ai/modelman.toml` always; touches `registry.toml` only to repair missing provider entries (prints `Added provider entries: …`), never adds models.

### 7. Expose / unexpose through LiteLLM (CLI)

<!-- UNVERIFIED — mutating commands; not run on this machine (they rewrite the live `~/.config/litellm/config.yaml` + `modelman.toml`). Help text and success lines below are from live `--help` output and the command source (`src/modelman/main.py`). Local models must be downloaded to expose (cloud models exempt); errors go to stderr with exit 1. -->

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

On success `expose` writes a `model_list` entry into `~/.config/litellm/config.yaml` and flips the model's `litellm_exposed` flag in `modelman.toml`; `unexpose` removes the entry and clears the flag. modelman only touches the `model_list` section — `general_settings` and unrecognized rows are preserved — and restarts LiteLLM itself right after (`MODELMAN_LITELLM_RESTART_CMD`, falling back to `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`; see [01-initial-setup](01-initial-setup.md) §7). In the TUI the same toggle is `x` on a model row (queued, applied on exit — downloads/pulls first if the model isn't ready yet; the EXPOSED column shows `Y`).

## Verification

Confirm a model is exposed — two independent greps, and both must agree:

(Current machine fails this pair-check — see the drift note below.)

```bash
grep -n "model_name" ~/.config/litellm/config.yaml
```

```text
3:  - model_name: ollama/qwen3.8:27b-mlx
12:  - model_name: omlx/Qwen3.8-27B-4bit
19:  - model_name: openrouter/qwen/qwen3.8-27b
26:  - model_name: openrouter/qwen/qwen3.8-flash
32:  - model_name: openrouter/qwen/qwen3.8-2.4t-a95b
38:  - model_name: openrouter/qwen/qwen3.8-max
45:  - model_name: llama.cpp/local-llama
52:  - model_name: ollama/ornith-1.5:35b
60:  - model_name: omlx/Ornith-1.5-35B-A3B-MLX-4bit
67:  - model_name: omlx/Ornith-1.5-35B-A3B-MLX-6bit
74:  - model_name: llama.cpp/ornith-1.5-35b
```

(11 entries, live 2026-08-29. Never `cat` this file into chat/docs — its `api_key:` values include real `sk-or-v1-…` keys.)

```bash
grep -A4 '"ollama/gpt-oss:20b"' ~/.config/local-ai/modelman.toml
```

```text
[model_state."ollama/gpt-oss:20b"]
ready = true
disk_path = "ollama:gpt-oss:20b"
size_bytes = 13958643712
litellm_exposed = false
```

```bash
grep -c "litellm_exposed = true" ~/.config/local-ai/modelman.toml
```

```text
24
```

> **Historical note (2026-08-30, updated 2026-09-03):** `modelman.toml` flags were out of sync because the non-ollama entries were seeded outside modelman. The count above is now 24: thirteen ollama models — the two local MLX downloads `ollama/qwen3.8:27b-mlx` and `ollama/ornith-1.5:35b` plus eleven cloud-hosted ollama models — and eleven openrouter models exposed through the TUI/CLI since. Other in-registry ollama models like `ollama/gpt-oss:20b` above simply haven't been exposed, and the omlx/llamacpp entries remain hand-managed by design and keep `litellm_exposed = false`.

Registry-side probe for a newly added model (only applies after a TUI add — `sync` and `expose` never add model ids); expected output mirrors the Step-3 ornith entry shape (the `id` line plus the 3 lines after it):

```bash
grep -n 'id = "<new-model>"' ~/.config/local-ai/registry.toml
```

```text
id = "ollama/ornith-1.5:35b"
family = "ornith-1.5:35b"
provider_id = "ollama"
model_name = "ornith-1.5:35b"
```

End-to-end confirm: the model also answers through the proxy — `curl http://localhost:4000/v1/models` with the master key from the LaunchAgent plist (full steps in [01-initial-setup](01-initial-setup.md) §Verification).

## Gotchas

- **`registry.toml` is canonical + read-only to wt.** Model visibility for agents changes HERE — edit `~/.config/local-ai/registry.toml`, not wt's config. `modelman.toml` is per-machine state (`[model_state]` blocks: `downloaded`, `disk_path`, `size_bytes`, `litellm_exposed`; `[families]` display names); never treat it as the model catalog.
- **Run modelman from the `modelman/` directory.** modelman is not installed as a global `uv tool`. Always run it from `~/github/ohanaverse/local-ai-setup/modelman` with `uv run modelman …`.
- **`sync` semantics as observed:** reconcile only (`ollama`/`llamacpp`/`omlx`), `:cloud` rows land `downloaded = false`, unconfigured models ignored, no models added, `litellm_exposed` preserved. If a run prints `Added provider entries: …`, it repaired `registry.toml`.
- **Providers before models.** The model screen resolves each variant's `provider_id` against `[[providers]]`; a model referencing a missing provider breaks the add flow with `KeyError` (`src/modelman/screens/models.py:40-43`).
- **TUI changes apply on exit only.** Adds/edits/deletes/downloads/exposure toggles sit in an in-memory queue until you confirm the pending set; deletes run before downloads, downloads before exposure changes, then one write of both files.
- **Secrets:** `secret_ref` is copied verbatim into the LiteLLM entry's `api_key`. The live `config.yaml` currently holds literal `sk-or-v1-…` keys — redact before pasting config anywhere.

## Going deeper

- Family concepts and per-provider variants: [03-model-families](03-model-families.md) (next in this set)
- modelman README (TUI keys, TOML shapes, all commands): `~/github/ohanaverse/local-ai-setup/modelman/README.md`
- TUI screens and apply-queue design: `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-26-modelman-tui-design.md`
- LiteLLM exposure design (provider policies, `model_list` writes): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-litellm-exposure-design.md`
- Model-dir sync/reconcile design (sync semantics): `~/github/ohanaverse/local-ai-setup/modelman/docs/superpowers/specs/2026-08-28-modelman-sync-modeldir-reconcile-design.md`
