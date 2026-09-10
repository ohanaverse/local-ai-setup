# Local mlx-lm quantization + `mlx_lm_server` speculative decoding

> Use this to: produce your own quantized MLX model with mlx-lm's own tooling and register it in modelman, or serve a target+draft pairing through mlx-lm's generic speculative decoding — both without any training/distillation step.

This guide, like [09-agent-benchmarks](09-agent-benchmarks.md), embeds no `exposed` snapshots — nothing here goes stale when a model is exposed/unexposed.

Two independent features, both built on the same `mlx_lm.*` tooling bundled inside the omlx Homebrew keg:

- **Local quantization** — `bin/mlx-quantize` wraps `mlx_lm.convert`/`dynamic_quant`/`dwq`; modelman registers the output directory as a `local_path` on the `omlx` provider. modelman deliberately never runs these tools itself — it's register-only.
- **`mlx_lm_server` speculative decoding** — a target model + a same-tokenizer draft model served together via `mlx_lm.server --draft-model`, isolated and exposed through LiteLLM like any other local provider, with **zero `wt` code changes** (`wt`'s Go decoder already ignores fields it doesn't know about).

## Prerequisites

- omlx installed (`brew install omlx`) — this is where `mlx_lm.convert`/`dynamic_quant`/`dwq`/`server` actually live; none of them are on `PATH` directly, or a declared dependency of this repo. Override with `MLX_LM_BIN_DIR` if you maintain a separate `pip install`ed mlx-lm.
- Everything in [05-benchmarks](05-benchmarks.md)'s Prerequisites (no other local model loaded, backends healthy, isolation helpers on `PATH`).
- For speculative decoding: a target and draft model that **share a tokenizer** — mlx-lm's speculative decoding only works across same-tokenizer pairs (this is generic mlx-lm decoding, not omlx's MTP/DFlash/VLM-MTP mechanisms, none of which apply here).

## TL;DR

```bash
# from: /Users/keith/github/ohanaverse/local-ai-setup
bin/mlx-quantize convert --model mlx-community/some-model -q --mlx-path /tmp/some-model-4bit
# → Output written to: /tmp/some-model-4bit
#   Next: register it in modelman — TUI → Add model → provider 'omlx' →
#   local-path field → paste '/tmp/some-model-4bit' (or its absolute path).

bin/llm-isolate-provider mlx_lm_server org/target-repo org/draft-repo
# → {"provider":"mlx_lm_server","model":"org/target-repo (+draft org/draft-repo)",
#    "direct_url":"http://localhost:8001/v1/chat/completions","ok":true,"error":null}

curl -s http://localhost:8001/v1/models

bin/llm-restore-providers
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

TUI → Add model → provider `omlx` → local-path field → paste the output directory from Step 1 (or any mlx-lm directory you already produced). `modelman expose` it, and it's usable through `wt` and `modelman benchmark` exactly like any other omlx model.

### 3. Register a target+draft pairing (feature 2)

TUI → Add model → provider `mlx_lm_server` → dual-model form → target (repo or local path) + draft (repo or local path). The pairing's model id follows `<target-basename>+draft-<draft-basename>`.

### 4. Isolate and serve the pairing

```bash
bin/llm-isolate-provider mlx_lm_server <target> <draft>
```

Unlike ollama/omlx, **`mlx_lm_server` has no baked-in default pairing** — you must always pass the target and draft explicitly (positional args, or `LLM_ISOLATE_MLXLM_MODEL`/`LLM_ISOLATE_MLXLM_DRAFT_MODEL`). This isolates on port 8001, backgrounded with a pidfile at `/tmp/local-ai-setup-mlx-lm-server.pid` (log at `/tmp/local-ai-setup-mlx-lm-server.log`) — not a LaunchAgent, since a plist would bake in one fixed pairing and defeat sweeping many pairings per session.

### 5. Expose and use it

`modelman expose <id>` makes the pairing show up in `wt`'s model picker with `api_base http://localhost:8001/v1`, same as any other local provider. `modelman benchmark run` sweeps `mlx_lm_server` targets like any other local provider too — isolation resolves the pairing from the registry automatically.

## Verification

```bash
curl -s http://localhost:8001/v1/models
curl -s http://localhost:8001/v1/chat/completions -d '{"model":"default","messages":[{"role":"user","content":"hi"}],"max_tokens":8}'
```

Check `/tmp/local-ai-setup-mlx-lm-server.log` for mlx_lm.server's draft/acceptance reporting to confirm speculative decoding is actually engaging (a tokenizer-mismatched pairing still serves, it just never speculates).

```bash
bin/llm-isolate-provider omlx   # isolate something else
```

Confirms `mlx_lm_server` was stopped, port 8001 closed, pidfile removed — `bin/llm-restore-providers` does the same unconditionally at the end of every `modelman benchmark` run, even if `mlx_lm_server` was the last isolated provider.

## Gotchas

- **modelman never deletes a `local_path` artifact.** A directory from `mlx_lm.convert`/`dwq` is user-produced (possibly hours of GPU time), not something modelman downloaded — deleting the registry entry (or a ready-off toggle) leaves the directory on disk. Clean up failed experiments with a manual `rm -rf`.
- **No default target/draft pairing exists anywhere in this repo.** Every `mlx_lm_server` isolate call — manual or from `modelman benchmark` — must supply both sides; there's no fallback to guess from.
- **`mlx_lm_server` is one-model-per-process**, unlike ollama (single daemon, any model) or omlx (one daemon, both 4-bit/6-bit variants). Sweeping multiple pairings in one benchmark run restarts the process between them.
- **The omlx keg version drifts on `brew upgrade omlx`.** `bin/mlx-quantize` and the isolation helpers resolve `mlx_lm.*` by globbing the keg and taking the newest match — never hardcode a version path.

## Going deeper

- Provider/isolation artifact reference: [provider-artifacts.md](../reference/provider-artifacts.md)
- Benchmark isolation mechanics: [05-benchmarks](05-benchmarks.md)
- Module map: `modelman/CLAUDE.md` (Provider plugin system, Benchmark subsystem)
