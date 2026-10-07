# Local mlx-lm quantization + `mlx_lm_server` speculative decoding

> Use this to: produce your own quantized MLX model with mlx-lm's own tooling and register it in `registry.toml`, or serve a target+draft pairing through mlx-lm's generic speculative decoding — both without any training/distillation step.

Two independent features, both built on the same `mlx_lm.*` tooling bundled inside the omlx Homebrew keg:

- **Local quantization** — `bin/mlx-quantize` wraps `mlx_lm.convert`/`dynamic_quant`/`dwq`; you register the output directory as a `local_path` on the `omlx` provider by hand-editing `registry.toml` (Step 2). modelman deliberately never runs these tools itself — it's register-only.
- **`mlx_lm_server` speculative decoding** — a target model + a same-tokenizer draft model served together via `mlx_lm.server --draft-model`, isolated and routed through LiteLLM like any other local provider, with **zero `wt` code changes** (`wt`'s Go decoder already ignores fields it doesn't know about).

## Prerequisites

- omlx installed (`brew install omlx`) — this is where `mlx_lm.convert`/`dynamic_quant`/`dwq`/`server` actually live; none of them are on `PATH` directly, or a declared dependency of this repo. Override with `MLX_LM_BIN_DIR` if you maintain a separate `pip install`ed mlx-lm.
- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, `llmbench provider` CLI runnable from `llmbench/`).
- For speculative decoding: a target and draft model that **share a tokenizer** — mlx-lm's speculative decoding only works across same-tokenizer pairs (this is generic mlx-lm decoding, not omlx's MTP/DFlash/VLM-MTP mechanisms, none of which apply here).

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/mlx-quantize convert --model mlx-community/some-model -q --mlx-path /tmp/some-model-4bit
# → Output written to: /tmp/some-model-4bit
#   Next: register it in modelman by hand-editing registry.toml — add an
#   [[models]] entry with provider_id = "omlx" and a [models.fetch]
#   local_path = "/tmp/some-model-4bit" (absolute path).

uv run --directory llmbench llmbench provider isolate mlx_lm_server org/target-repo --draft org/draft-repo --json
# → {"provider":"mlx_lm_server","model":"org/target-repo (+draft org/draft-repo)",
#    "direct_url":"http://localhost:8001/v1/chat/completions","ok":true,"error":null}

curl -s http://localhost:8001/v1/models

uv run --directory llmbench llmbench provider restore
```

## Steps

### 1. Quantize (optional — skip if you already have an MLX directory)

```bash
bin/mlx-quantize convert --model <hf-repo-or-local-path> -q [--mlx-path <out-dir>]
bin/mlx-quantize dynamic-quant --model <hf-repo-or-local-path> [--mlx-path <out-dir>]
bin/mlx-quantize dwq --model <hf-repo-or-local-path> [--mlx-path <out-dir>]
```

`bin/mlx-quantize` is a thin forwarder — every flag after the subcommand goes straight to the resolved `mlx_lm.*` binary verbatim; it never reimplements mlx-lm's own flag surface. Without `--mlx-path`, mlx-lm writes to `./mlx_model` by default.

### 2. Register a local-path model (feature 1)

Register a `local_path`-sourced omlx model by hand-editing `registry.toml` — the way every model is added now that modelman's TUI is disabled ([02-providers-and-models](02-providers-and-models.md) Step 1 has the procedure):

```toml
[[models]]
id = "omlx/<name>"            # e.g. "omlx/some-model-4bit"
family = "<existing-family>"
provider_id = "omlx"
model_name = "<name>"         # basename you'll recognize in `modelman start`'s list
location = "local"

[models.fetch]
local_path = "/tmp/some-model-4bit"   # absolute path to Step 1's output directory
```

Then `modelman sync` to pick up the new entry, and `modelman start <id>` to load it — there is no separate routing step: a local model is routed while it runs, and the start's own `wt litellm sync` writes the route. From there it's usable through `wt` and `llmbench` exactly like any other omlx model.

### 3. Register a target+draft pairing (feature 2)

Add the pairing to `registry.toml` by hand ([02-providers-and-models](02-providers-and-models.md) Step 1) — one `[[models]]` block whose `[models.fetch]` names the target and whose `[models.draft]` names the draft, each as a `repo` (HF repo id) or a `local_path` (absolute directory, e.g. Step 1's output):

```toml
[[models]]
id = "mlx_lm_server/<target-basename>+draft-<draft-basename>"
family = "<existing-family>"
provider_id = "mlx_lm_server"
model_name = "<target-basename>+draft-<draft-basename>"
location = "local"

[models.fetch]                       # the target
repo = "<org>/<target repo>"         # or: local_path = "/abs/path/to/target"

[models.draft]                       # the draft
repo = "<org>/<draft repo>"          # or: local_path = "/abs/path/to/draft"
```

The id convention `<target-basename>+draft-<draft-basename>` is the one modelman's TUI used, so the pairing reads clearly in wt's picker. `wt model init` adds the `mlx_lm_server` provider row once a model references it; then `wt litellm sync`.

### 4. Isolate and serve the pairing

```bash
uv run --directory llmbench llmbench provider isolate mlx_lm_server <target> --draft <draft>
```

Unlike ollama/omlx, **`mlx_lm_server` has no baked-in default pairing** — you must always pass the target and draft explicitly (`<target>` positional + `--draft <draft>` — note `--draft` is a flag, not a second positional, since the port from the old bash isolation helper — or `LLM_ISOLATE_MLXLM_MODEL`/`LLM_ISOLATE_MLXLM_DRAFT_MODEL`). This isolates on port 8001, backgrounded with a pidfile at `/tmp/local-ai-setup-mlx-lm-server.pid` (log at `/tmp/local-ai-setup-mlx-lm-server.log`) — not a LaunchAgent, since a plist would bake in one fixed pairing and defeat sweeping many pairings per session.

### 5. Route and use it

The isolated pairing is live on port 8001; provided the pairing is registered (Step 3 — `mlx_lm_server` is never discovered, so an unregistered pairing gets no row and no route), `wt litellm sync` (or the `modelman start` you'd use for a registered pairing) then adds its route, so it shows up in `wt`'s model picker with `api_base http://localhost:8001/v1`, same as any other local provider. `llmbench run` sweeps `mlx_lm_server` targets like any other local provider too — isolation resolves the pairing from the registry automatically.

## Verification

```bash
curl -s http://localhost:8001/v1/models
curl -s http://localhost:8001/v1/chat/completions -d '{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}'
```

Check `/tmp/local-ai-setup-mlx-lm-server.log` for mlx_lm.server's draft/acceptance reporting to confirm speculative decoding is actually engaging (a tokenizer-mismatched pairing still serves, it just never speculates).

```bash
uv run --directory llmbench llmbench provider isolate omlx   # isolate something else
```

Confirms `mlx_lm_server` was stopped, port 8001 closed, pidfile removed — `llmbench provider restore` does the same unconditionally at the end of every `llmbench` run, even if `mlx_lm_server` was the last isolated provider.

## Gotchas

- **modelman never deletes a `local_path` artifact.** A directory from `mlx_lm.convert`/`dwq` is user-produced (possibly hours of GPU time), not something modelman downloaded — deleting the registry entry (or a ready-off toggle) leaves the directory on disk. Clean up failed experiments with a manual `rm -rf`.
- **No default target/draft pairing exists anywhere in this repo.** Every `mlx_lm_server` isolate call — manual or from `llmbench` — must supply both sides; there's no fallback to guess from.
- **`mlx_lm_server` is one-model-per-process**, unlike ollama (single daemon, any model) or omlx (one daemon, both 4-bit/6-bit variants). Sweeping multiple pairings in one benchmark run restarts the process between them.
- **The omlx keg version drifts on `brew upgrade omlx`.** `bin/mlx-quantize` and the `llmbench provider isolate` lifecycle backends (`src/llmbench/providers/lifecycle/binaries.py`) resolve `mlx_lm.*` by globbing the keg and taking the newest match — never hardcode a version path.

## Going deeper

- Provider/isolation artifact reference: [provider-artifacts.md](../reference/provider-artifacts.md)
- Benchmark isolation mechanics: [05-benchmarks](05-benchmarks.md)
- Module map: `modelman/CLAUDE.md` (Provider plugin system), `llmbench/CLAUDE.md` (Provider lifecycle, Benchmark subsystem)
