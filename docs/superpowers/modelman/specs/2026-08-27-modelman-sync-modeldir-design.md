# Spec: `modelman sync` — llamacpp/omlx model_dir scan (SUPERSEDED)

**Date:** 2026-08-27
**Status:** superseded by
`2026-08-28-modelman-sync-modeldir-reconcile-design.md` — sync is
reconcile-only, not discover. Do not implement.

## What this would have done

Extend `modelman sync` to discover local models by scanning `model_dir`
(omlx) and the HF cache (llamacpp), producing registry `ModelEntry`
definitions rather than `LocalModel` state.

## Why superseded

The reconcile-not-discover principle (see the ollama reconcile spec) means
sync never adds new models. The modeldir PR instead reconciles the
downloaded state of already-configured llamacpp/omlx models.
