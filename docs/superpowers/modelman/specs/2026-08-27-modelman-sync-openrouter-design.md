# Spec: `modelman sync` — openrouter discovery (DROPPED)

**Date:** 2026-08-27
**Status:** dropped — no-op. Do not implement.

## Decision

OpenRouter models are **explicitly configured** in `registry.toml`; the
config already holds their model info. OpenRouter does not download
models, so there is no downloaded state to reconcile. `modelman sync`
does nothing for openrouter.

Per the reconcile-not-discover principle (see
`2026-08-28-modelman-sync-ollama-reconcile-design.md`), sync never lists
all available models for any provider — so there is no openrouter
discovery step either.

## What this means

- No `discover_openrouter()` in `sync.py`.
- No openrouter branch in the `sync()` orchestrator.
- The `openrouter` provider entry in `registry.toml` (added by `migrate`)
  is sufficient; its models are added/edited manually in the TUI.

## Sequence

This was PR 2 of the provider-sync effort. It is removed from the
roadmap (see `docs/ROADMAP.md`). The remaining provider-reconcile PRs are
ollama reconcile-only (PR #9) and modeldir reconcile (PR #10).
