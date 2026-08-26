# modelman

A CLI for managing local LLM model families across providers (Ollama, llama.cpp, oMLX).

## Install

```bash
uv tool install ~/ohanaverse/modelman
```

## Configure

Create `~/.config/local-ai/config.yaml`:

```yaml
providers:
  ollama:
    type: ollama
  llamacpp:
    type: llamacpp
  omlx:
    type: omlx
    model_dir: ~/.omlx/models
```

## Usage

### Add a new family

Create `~/.config/local-ai/families/<name>.yaml`:

```yaml
family: <name>
display_name: "<Display Name>"
variants:
  - id: <variant-id>
    provider: ollama
    name: <ollama-tag>
  - id: <another-id>
    provider: llamacpp
    repo: <org>/<hf-repo>
    files: [<file.gguf>]
  - id: <omlx-id>
    provider: omlx
    repo: <org>/<hf-repo>
```

### Download missing variants

```bash
modelman download <family>
```

A multi-select picker shows which variants are already on disk and which are missing. Pre-selected are the missing ones. Press Enter to accept, or toggle individual rows.

### Download all missing without prompting

```bash
modelman download <family> --all -y
```

## Development

```bash
uv sync
uv run pytest
uv run mypy src/
```

## Architecture

- `src/modelman/providers/` — one module per backend, each ~50–80 lines. Register with `ProviderRegistry.register()` at import time.
- `src/modelman/commands/` — one module per subcommand. Each uses `Config` + `FamilyManifest` + `ProviderRegistry` to do work.
- `src/modelman/config.py` — loads `~/.config/local-ai/config.yaml`. `Config` is a dataclass with a `providers: dict[str, dict]` field.
- `src/modelman/manifest.py` — loads/saves `~/.config/local-ai/families/*.yaml`. `FamilyManifest` is a dataclass with `variants: list[VariantSpec]` and `downloaded: dict[str, dict]`.

To add a new provider (e.g. vLLM):
1. Create `src/modelman/providers/vllm.py` with a class extending `Provider`
2. Call `ProviderRegistry.register(VLLMProvider)` at module bottom
3. Add `vllm: { type: vllm }` to `~/.config/local-ai/config.yaml`
4. Use `provider: vllm` in any family manifest variant