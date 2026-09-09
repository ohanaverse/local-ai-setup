---
name: mlx-lm-quantization
description: Produce a quantized MLX model variant with mlx-lm and register it in modelman (bin/mlx-quantize, registry.toml). Use when the user wants to quantize a model locally or try a new quantization recipe.
---

## Quantizing and registering a local MLX model

1. Run `bin/mlx-quantize <convert|dynamic-quant|dwq> --model <hf-repo-or-path> [--mlx-path <out-dir>] ...` — a thin wrapper that resolves `mlx_lm.*` from the omlx Homebrew keg and forwards all flags verbatim.
2. Register the output directory in modelman: TUI → Add model → provider `omlx` → local-path field → paste the output dir.
3. `modelman expose` the model so `wt` picks it up.

For a target+draft speculative-decoding pairing (`mlx_lm.server --draft-model`) instead of a single quantized model, register both sides under provider `mlx_lm_server` (dual-model form) and isolate with `bin/llm-isolate-provider mlx_lm_server <target> <draft>` — see `docs/guides/10-mlx-lm-quantization.md`.

## Gotchas

- `mlx_lm.*` binaries are never on PATH — they live inside the versioned omlx keg; set `MLX_LM_BIN_DIR` to override (e.g. a separate `pip install`ed mlx-lm).
- modelman never deletes a `local_path` artifact (it didn't create it, possibly hours of GPU time) — cleaning up a failed experiment is a manual `rm -rf`.
