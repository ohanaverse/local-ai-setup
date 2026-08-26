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

## Use

Author a family manifest at `~/.config/local-ai/families/<family>.yaml`, then:

```bash
modelman download <family>
```