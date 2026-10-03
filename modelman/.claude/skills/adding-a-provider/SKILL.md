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
6. Add the provider's LiteLLM mapping (prefix, api_key rule, cloud flag) to the policy table in `wt/internal/litellm/policy.go` (Go, in this monorepo). That table is the single source of truth for LiteLLM routing: wt's route writer uses it, and a model of an unmapped provider is never routed (`provider "<id>" has no LiteLLM mapping`, and the picker refuses the row as "(not in LiteLLM)"). modelman reads nothing from it and holds no routing state of its own — since #179 it just asks wt for one `wt litellm sync` after its own state changes, so there is no modelman-side gate or flag to add here.

No changes to `main.py` are required unless a new CLI subcommand is also added.
