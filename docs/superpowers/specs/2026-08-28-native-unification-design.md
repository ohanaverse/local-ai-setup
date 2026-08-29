# Native Unification

## Goal

Replace the three incompatible "native" definitions in the codebase with a single derived field — `Model.Native` — sourced from the registry's `auth.type == "native"`, which is already the single source of truth in the shared data model.

## Motivation

The codebase determines "native-ness" three different ways, and they can disagree:

| Definition | Where | What it means |
|---|---|---|
| `Model.IsNative()` = `ModelName == "native"` | `config.go:265` | "is this the sentinel `claude/native` model" |
| `ProviderID == "claude"` / `"copilot"` | `claude.go:28`, `copilot.go:23` | "is this a native-provider model (subscription)" |
| `IsNative()` in codex/pi | `codex.go:36`, `pi.go:31` | "is this a native-provider model" — but uses the model-name test |

The inconsistency is latent, not live: today the only native models are the sentinels `claude/native`, `copilot/native`, `codex/native`, `agy/native`, where `ModelName == "native"` and `ProviderID == <agent>` coincide. But the moment a named native-provider model is added (e.g. `claude/opus`), the definitions diverge:

- `claude/opus` has `ModelName == "opus"`, so `IsNative()` is false → codex/pi/resume-skip would treat it as non-native and route it through the ollama gateway (or try to resume it), even though it belongs to the claude subscription.

The registry already carries the correct answer: `Provider.Auth.Type == "native"` (see `AuthConfig.Type` in `config.go:46` and modelman's `registry.py:39`). This design makes that field the single source of truth.

## Design

### 1. Add `Model.Native` (derived field)

Add a `Native bool` field to `Model` (`config.go:54`), tagged `toml:"-"` so it is neither decoded from the registry nor persisted by `Save`:

```go
type Model struct {
    // ... existing fields ...
    Native bool `toml:"-"` // derived: provider auth.type == "native"; not persisted
}
```

### 2. `deriveNative()` helper

Add a helper that marks each model whose provider authenticates natively:

```go
// deriveNative marks each model whose provider authenticates natively
// (auth.type == "native") as Native. It runs after the registry join so the
// in-memory Native field reflects the registry's auth data — the single
// source of truth for native-ness. A model whose provider is missing (or has
// a non-native auth type) is left non-native.
func deriveNative(cfg *Config) {
    for i := range cfg.Models {
        p := cfg.ProviderByID(cfg.Models[i].ProviderID)
        cfg.Models[i].Native = p != nil && p.Auth.Type == "native"
    }
}
```

Call it at both registry-join points in `Load()` — immediately after each `cfg.Providers, cfg.Models = providers, models` assignment (`config.go:135` early-return path and `config.go:165` normal path). It must run after the join because the registry's providers are what `Native` is derived from.

### 3. Replace `IsNative()` call sites with `m.Native`

| File | Current | After |
|---|---|---|
| `agents.go:172` | `!m.IsNative()` | `!m.Native` |
| `codex.go:36` | `!m.IsNative()` | `!m.Native` |
| `pi.go:31` | `m.IsNative()` | `m.Native` |
| `pi_models.go:66` | `m.IsNative() \|\| m.ModelName == ""` | `m.Native \|\| m.ModelName == ""` |
| `claude.go:28` | `m.ProviderID == "claude"` | `m.Native` |
| `copilot.go:23` | `m.ProviderID == "copilot"` | `m.Native` |
| `launch.go:33` | `!m.IsNative()` | `!m.Native` |
| `app.go:695` | `!highlighted.model.IsNative()` | `!highlighted.model.Native` |

Then delete `IsNative()` (`config.go:263-267`).

The claude/copilot driver dispatch changes from `ProviderID == "claude"`/`"copilot"` to `m.Native`. These are equivalent for the drivers' eligible models (claude/copilot providers are always `auth.type == "native"`; ollama is `auth.type == "none"`), so the change is behavior-preserving today and makes the dispatch consistent with every other native check.

### 4. Keep the sentinel `ModelName == "native"` checks

Two `ModelName == "native"` checks are a *different* concept — "is this the sentinel bare-launch model" — and stay:

- **`claude.go:30`** — within the native-provider branch, `m.ModelName != "native"` decides bare launch vs `--model <name>`. This is the bare-vs-named distinction, not native-ness.
- **`migrate.go:122`** — the legacy `models.conf` migration constructs models manually (no `Native` field), so it keeps the raw `m.ModelName == "native"` test.

### 5. Behavior change: resume-skip now covers all native-provider models

Today resume-skip is `!m.IsNative()` = `ModelName != "native"`, so only the sentinel `claude/native` skips resume. With `m.Native`, any native-provider model (including a future `claude/opus`) skips resume. This fixes the latent bug where a named native-provider model would resume a session whose stored model is a gateway model, silently routing it at the real Anthropic API. No live model changes behavior today (only the sentinels exist, and they already skip resume).

The same latent fix applies to `pi_models.go:66`: a future `claude/opus` would today be synced into pi's `models.json` (it is not an ollama model); with `m.Native` it is skipped.

## modelman: no changes needed

The `Native` field reads `Provider.Auth.Type`, which wt already decodes from the registry (`registry.go:76-83` → `AuthConfig.Type`). modelman's schema already supports `auth.type = "native"` (`registry.py:39`), and `modelman migrate` already imports native providers from wt's legacy `config.toml` with the type preserved (`migrate.py:86-90`). Native providers are data, not code — modelman never creates them (its defaults cover only the reconcilable local providers `ollama`/`llamacpp`/`omlx`), so there is nothing to change on the modelman side.

**Dependency to document:** native correctness now rides on `auth.type` being right in the registry. `modelman migrate` guarantees this; a hand-edited registry with a native model whose provider lacks `auth.type = "native"` would flip that model to non-native. This is the intended single-source-of-truth trade-off — no `|| ModelName == "native"` fallback, which would reintroduce the dual definition.

## Tests

`Native` is derived at `Load()` time, so tests that construct `Model` values directly (bypassing `Load()`) must set `Native: true` on native models:

- **`agents_test.go:16`** — add `Native: true` to the `nativeModel()` helper (covers the claude/codex/copilot/pi/resume-skip tests).
- **`launch_test.go:82`** — add `Native: true` to the inline `claude/native` model.
- **`pi_models_test.go:44`** — add `Native: true` to the inline `claude/native` model.
- **`tui/agent_model_test.go:385`** — add `Native: true` to the inline `claude/native` model if it exercises resume-skip.
- **`config_test.go`** — remove any direct `IsNative()` test; add a `deriveNative` test asserting a native-provider model is marked `Native` and an ollama model is not.

## What doesn't change

- The sentinel `ModelName == "native"` checks (`claude.go:30`, `migrate.go:122`) — bare-vs-named and legacy migration.
- The `agy` driver — already launches bare and ignores the model.
- The `opencode` driver — ollama-only, no native branch.
- Rotation, eligible-models, and tag/family filtering — untouched.
- The registry is still read-only; wt still never writes providers/models.

## Notes

- **`pi.go:31` is now provably dead.** pi is ollama-only (no native provider after the Aug 21 alignment), so `m.Native` is always false for pi's eligible models. The check is kept for consistency with the other drivers; removing it is a separate dead-code cleanup, not part of this unification.
