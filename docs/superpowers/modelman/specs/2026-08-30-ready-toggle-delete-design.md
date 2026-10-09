# Ready Toggle Delete Design

**Date**: 2026-08-30  
**Status**: Approved  
**Author**: modelman

## Problem

The "r" (ready toggle) keybinding does not work for local models. When pressing "r" on a downloaded model, the user sees:
> "Reconcile controls local-model ready state; delete the file to mark not ready."

This is confusing because:
1. The binding is labeled "Toggle ready" but doesn't toggle for local models
2. The message references "Reconcile" which has no keybinding
3. Users expect "r" to manage file presence (download/delete)

## Design: Cascading Dependencies

Modelman manages four state layers with clear dependencies:

```
registry (config) ← expose (LiteLLM) ← ready (file) ← disk (artifact)
```

Higher layers depend on lower layers. Changes cascade to maintain consistency.

### State Cascade Rules

| Action | Direction | Cascade |
|--------|-----------|---------|
| **Expose ON** (`x` on not-ready) | expose → ready | Queues download if needed |
| **Expose OFF** (`x` on exposed) | expose only | Removes from LiteLLM config |
| **Ready ON** (`r` on not-ready) | ready only | Queues download if needed |
| **Ready OFF** (`r` on ready) | ready → expose | Deletes file, unexposes if was exposed |
| **Delete** (`d` on any model) | expose → ready → registry | Unexpose → delete file → remove from registry |
| **Add/Edit** | registry only | No cascade (metadata only) |
| **Reconcile** (auto) | disk → ready → expose | If file missing: ready=off, expose=off |

### Key Principles

1. **"r" toggles file presence**: Download on ready=ON, delete on ready=OFF
2. **"d" removes config + cascades**: Removes registry entry, but first cleans up expose + ready
3. **Expose depends on ready**: Cannot expose a not-ready model; unexpose when ready goes off
4. **Reconcile enforces truth**: Auto-syncs state to disk reality

## Implementation Changes

### 1. Provider `delete()` Methods

**ollama** (already implemented):
- `ollama rm <name>` removes from ollama registry

**llamacpp** (new):
- Delete specific GGUF file(s) from HF cache snapshot
- Clean up orphaned blob from `blobs/` directory
- Leave snapshot directory intact (may have other files)

**omlx** (new):
- Delete entire model directory (`~/.omlx/models/<repo-basename>`)

### 2. `action_toggle_ready()` in `models.py`

Remove the block that prevents `ready=False` for local models (lines 414-423). The ready toggle now:
- **target=True**: Queue download (existing behavior)
- **target=False**: Queue file deletion + cascade unexpose (new behavior)

### 3. `action_delete_model()` in `models.py`

Update to queue in dependency order:
1. Queue expose=False (if currently exposed)
2. Queue ready=False (triggers file delete)
3. Queue registry removal

### 4. `PendingChanges.apply()` in `queue.py`

Update the `ready=False` branch to:
1. Call `provider.delete()` to remove file
2. Set `state.ready = False`
3. If `state.litellm_exposed` was True: queue expose=False

### 5. HF Cache Cleanup (llamacpp)

When deleting a GGUF file from HF cache:
1. Delete the file from `snapshots/<commit>/<file>`
2. Compute the blob hash (file content SHA256)
3. Check if blob is referenced by other snapshots
4. If orphaned: delete from `blobs/` directory

## Testing

### Unit Tests
- `test_toggle_ready_downloads()`: Verify "r" on not-ready queues download
- `test_toggle_ready_deletes_file()`: Verify "r" on ready deletes file + unexposes
- `test_delete_cascades()`: Verify "d" unexposes → deletes → removes registry
- `test_reconcile_missing_file()`: Verify reconcile unsets ready + expose when file absent

### Provider Tests
- `test_ollama_delete()`: Verify `ollama rm` is called
- `test_llamacpp_delete_file()`: Verify GGUF file deleted from HF cache
- `test_llamacpp_cleanup_blob()`: Verify orphaned blob removed
- `test_omlx_delete_directory()`: Verify model directory removed

### Integration Tests
- `test_expose_cascades_to_ready()`: Expose ON downloads if needed
- `test_ready_off_unexposes()`: Ready OFF removes LiteLLM entry
- `test_full_delete_cascade()`: Delete removes from all layers

## Migration Notes

This change makes "r" a true toggle for all models. Users who were accustomed to the notification message will now see the file deleted. This is the intended behavior — "r" manages readiness (file presence), not just a flag.

The reconcile auto-sync behavior remains unchanged: if a file is manually deleted outside modelman, reconcile will set `ready=False` and `expose=False` on next mount/resume.
