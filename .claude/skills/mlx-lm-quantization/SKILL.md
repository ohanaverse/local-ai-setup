---
name: mlx-lm-quantization
description: Produce a quantized MLX model variant with mlx-lm and register it (bin/mlx-quantize): by hand as a local_path entry, or with wt model add once it is in omlx's model directory. Use when the user wants to quantize a model locally or try a new quantization recipe.
---

## Quantizing and registering a local MLX model

1. Run `bin/mlx-quantize <convert|dynamic-quant|dwq> --model <hf-repo-or-path> [--mlx-path <out-dir>] ...` — a thin wrapper that resolves `mlx_lm.*` from the omlx Homebrew keg and forwards all flags verbatim.
2. Register it. Move the output directory into omlx's model directory (`~/.omlx/models/` by default) and run `wt model add omlx <directory name> --family <family>`: that is the way `wt start` can load it. Or leave it where it is and hand-edit `registry.toml` — a `local_path` entry is one of the few things wt has no command for: add an `[[models]]` entry with `provider_id = "omlx"` and a `[models.fetch]` `local_path = "<out-dir>"` (absolute path); see `docs/guides/10-mlx-lm-quantization.md` for the exact snippet. wt lists such an entry (`wt model list` shows its path) and cannot start it from there.
3. `wt start <id>` — there is no separate routing step: a local model is routed while it runs. After a hand edit run `wt litellm sync` first, because no tool saw the edit; a `wt model add` needs none (the add syncs). `wt start` loads an omlx model by the name omlx lists, so a `local_path` directory outside omlx's model directory answers `<id> is not on disk — pull or download it first`.

For a target+draft speculative-decoding pairing (`mlx_lm.server --draft-model`) instead of a single quantized model, register it with `wt model add mlx_lm_server <target> --draft <draft> --family <family>` (it writes one `[[models]]` block: `[models.fetch]` = target, `[models.draft]` = draft; the guide's Step 3 shows it) and start it with `uv run --directory llmbench llmbench provider isolate --solo mlx_lm_server <target> --draft <draft>` (without `--solo` the other local providers are stopped first, which is what a benchmark wants) — see `docs/guides/10-mlx-lm-quantization.md`.

## Gotchas

- `mlx_lm.*` binaries are never on PATH — they live inside the versioned omlx keg; set `MLX_LM_BIN_DIR` to override (e.g. a separate `pip install`ed mlx-lm).
- wt never deletes a `local_path` artifact (possibly hours of GPU time): `wt model rm` removes the registry entry and prints the path — cleaning up a failed experiment is a manual `rm -rf`.
