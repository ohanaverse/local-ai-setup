---
name: adding-a-provider
description: Steps to add a new model provider to modelman (Provider class, registry.toml entry, LiteLLM policy). Use when asked to add support for a new local or cloud model provider to modelman.
---

## Adding a new provider

1. Create `src/modelman/providers/<name>.py` with a class extending `Provider`.
2. Call `ProviderRegistry.register(TheProvider)` at the bottom of the module.
3. Add a `[[providers]]` entry to `registry.toml` with `id = "<name>"`.
4. Reference it from models via `provider_id = "<name>"`.
5. (Optional) Override `size_of` so the size column is populated for downloaded variants.
6. Add a `ProviderPolicy` entry to `PROVIDER_POLICIES` in `src/modelman/litellm.py` (prefix, api_key, cloud flag). This table is the single source of truth for LiteLLM exposure — both the config writer and the TUI's expose gate read it, and an unmapped provider cannot be exposed.

No changes to `main.py` are required unless a new CLI subcommand is also added.
