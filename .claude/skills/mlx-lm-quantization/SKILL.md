---
name: mlx-lm-quantization
description: Produce a quantized MLX model variant with mlx-lm and register it (bin/mlx-quantize): by hand as a local_path entry, or with wt model add once it is in omlx's model directory. Use when the user wants to quantize a model locally or try a new quantization recipe.
---

## Quantizing and registering a local MLX model

1. Run `bin/mlx-quantize <convert|dynamic-quant|dwq> --model <hf-repo-or-path> [--mlx-path <out-dir>] ...` — a thin wrapper that resolves `mlx_lm.*` from the omlx Homebrew keg and forwards all flags verbatim.
2. Register it. Either move the output directory into omlx's model directory (`~/.omlx/models/` by default) and run `wt model add omlx <directory name> --family <family>`, or leave it where it is and hand-edit `registry.toml` — a `local_path` entry is one of the few things wt has no command for: add an `[[models]]` entry with `provider_id = "omlx"` and a `[models.fetch]` `local_path = "<out-dir>"` (absolute path); see `docs/guides/10-mlx-lm-quantization.md` for the exact snippet.
3. `modelman sync` to reconcile the new entry's ready flag, then `modelman start <id>` — there is no separate routing step: a local model is routed while it runs, and every start ends with the `wt litellm sync` that adds its route.

For a target+draft speculative-decoding pairing (`mlx_lm.server --draft-model`) instead of a single quantized model, register both sides by hand in one `[[models]]` block under provider `mlx_lm_server` (`[models.fetch]` = target, `[models.draft]` = draft; snippet in the guide's Step 3) and isolate with `uv run --directory llmbench llmbench provider isolate mlx_lm_server <target> --draft <draft>` — see `docs/guides/10-mlx-lm-quantization.md`.

## Gotchas

- `mlx_lm.*` binaries are never on PATH — they live inside the versioned omlx keg; set `MLX_LM_BIN_DIR` to override (e.g. a separate `pip install`ed mlx-lm).
- modelman never deletes a `local_path` artifact (it didn't create it, possibly hours of GPU time) — cleaning up a failed experiment is a manual `rm -rf`.
