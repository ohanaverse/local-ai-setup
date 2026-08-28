# Spec: `modelman sync` — openrouter discovery (PLACEHOLDER)

**Date:** 2026-08-27
**Status:** placeholder — NOT yet brainstormed. Do not implement.

## What this will do

Extend `modelman sync` to discover cloud models from the OpenRouter
REST API (`https://openrouter.ai/api/v1/models`), mirroring wt's
`OpenRouter.Discover()`. Adds a `discover_openrouter()` to `sync.py`
and wires it into the `sync()` orchestrator.

## Open questions (brainstorm before implementing)

- API key handling — OpenRouter's `/models` endpoint is public, but
  which models to include (all? filtered by capability?) is undecided.
- Pagination / rate limiting.
- Family assignment for cloud models (wt uses the last `/`-segment).
- Whether discovery failures are non-fatal (multi-provider sync needs
  this, unlike the ollama-only first PR).

## Sequence

This is PR 2 of the provider-sync effort, after the ollama PR
(`2026-08-27-modelman-sync-ollama-design.md`).
