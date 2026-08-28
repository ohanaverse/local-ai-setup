# Spec: `modelman sync` — llamacpp/omlx model_dir scan (PLACEHOLDER)

**Date:** 2026-08-27
**Status:** placeholder — NOT yet brainstormed. Do not implement.

## What this will do

Extend `modelman sync` to discover local models by scanning
`model_dir` (omlx) and the HF cache (llamacpp), mirroring the existing
`list_local()` methods but producing registry `ModelEntry` definitions
rather than `LocalModel` state.

## Open questions (brainstorm before implementing)

- What counts as a "model" on disk (`.gguf` files? directories?).
- How to derive `repo`/`files` (the `fetch` block) from a scanned path.
- Family assignment for scanned models.

## Sequence

This is PR 3 of the provider-sync effort, after the openrouter PR
(`2026-08-27-modelman-sync-openrouter-design.md`).
