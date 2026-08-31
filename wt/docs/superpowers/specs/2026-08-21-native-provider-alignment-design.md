# Native Provider Alignment

## Goal

Align every agent's native provider with the agent's own name, add validation that all agents have at least one provider, and migrate existing configs to the new scheme.

## Motivation

The `agy` agent was seeded with a `google` provider and `google/native` model — the only case where the provider name doesn't match the agent name. `opencode` had a native provider/model but never actually uses native login (it's ollama-only in practice). This creates inconsistency and dead code.

## Agent → Provider Mapping (after change)

| Agent   | Providers         | Native model      |
|---------|-------------------|-------------------|
| `agy`   | `agy` only        | `agy/native`      |
| `claude`| `claude`, `ollama`| `claude/native`   |
| `copilot`| `copilot`, `ollama`| `copilot/native`|
| `codex` | `codex`, `ollama` | `codex/native`    |
| `opencode`| `ollama` only   | (none)            |
| `pi`    | `ollama` only     | (none)            |

## Design

### 1. Config schema migration (`migrateConfigSchema`)

New function in `internal/config/migrate.go`, called from `Load()` after the config is decoded into memory. It applies idempotent fixups to the in-memory config and saves if anything changed. Each fixup is self-extinguishing — once applied, the old pattern no longer exists so it won't fire again.

**Fixup 1 — Rename `google` → `agy`:**
- Provider with ID `google`: change ID to `agy`, Name to `Antigravity`.
- All models with `provider_id = "google"`: change to `provider_id = "agy"`.
- Model with ID `google/native`: change ID to `agy/native`, family to `agy`.
- All agents with `"google"` in `supported_providers`: replace with `"agy"`.
- All agents with `default_provider = "google"`: change to `"agy"`.

**Fixup 2 — Ensure `agy` provider/model/agent:**
- If no `agy` provider exists, create it: ID `agy`, Name `Antigravity`, Location `cloud`, Auth type `native`.
- If no `agy/native` model exists, create it: ID `agy/native`, family `agy`, provider_id `agy`, model_name `native`, location `cloud`, tags `["code", "design"]`.
- If `agy` agent exists: set `supported_providers` to `["agy"]`, `default_provider` to `"agy"`. This removes any stale `ollama` or other entries.
- If no `agy` agent exists, create it with `supported_providers = ["agy"]`, `default_provider = "agy"`.

**Fixup 3 — Remove `opencode` native:**
- Remove the `opencode` provider from `cfg.Providers` if present.
- Remove all models with `provider_id = "opencode"` from `cfg.Models`.
- If `opencode` agent exists: set `supported_providers` to `["ollama"]`, `default_provider` to `"ollama"`.

**Save & log:**
If any fixup made a change, call `Save(cfg)` and write a summary to stderr (e.g. `wt: migrated config: renamed google→agy, removed opencode native`).

**Integration point:**
In `Load()`, after `toml.Decode` succeeds and before returning the config, call `migrateConfigSchema(cfg)`. The function returns `(bool, error)` matching the `Migrate()` pattern. If it returns `true`, the in-memory config is already updated — just return it.

### 2. Legacy migration seed (`Migrate()`)

Update the fresh-install seed logic in `migrate.go`:

**Agy special case:**
Change from `google` provider / `google/native` model to `agy` provider / `agy/native` model:
- Provider: ID `agy`, Name `Antigravity`, Location `cloud`, Auth type `native`.
- Model: ID `agy/native`, family `agy`, provider_id `agy`, model_name `native`, location `cloud`, tags `["code", "design"]`.
- Agent: `supported_providers = ["agy"]`, `default_provider = "agy"`.

**Opencode — no native:**
Define a `noNativeAgents` set containing `{"pi", "opencode"}`. Two things happen for agents in this set:

1. **Skip in the native processing loop:** When `native:X` entries are found in legacy `CODE_MODELS`/`DESIGN_MODELS`, skip creating native provider/model for any agent in `noNativeAgents`. This prevents `opencode` from getting a native provider if it appears as `native:opencode` in a legacy config.

2. **Ensure agent-only entries after the loop:** After the native processing loop completes, for each agent in `noNativeAgents`, check if an agent entry already exists in `cfg.Agents`. If it does, fix it to `supported_providers = ["ollama"]`, `default_provider = "ollama"` (strip any native provider references). If it doesn't, add a new agent entry with `supported_providers = ["ollama"]`, `default_provider = "ollama"`.

This replaces the existing standalone `pi` special case block with the general `noNativeAgents` handling. `pi` never appears as `native:pi` in legacy configs, so step 1 is a no-op for it, but step 2 ensures the `pi` agent entry is always created.

### 3. Validation (`config.go`)

In `validate()`, inside the agent loop, add after the existing checks:

```go
if len(a.SupportedProviders) == 0 {
    errs = append(errs, fmt.Errorf("agent %q: must have at least one supported provider", a.Name))
}
```

### 4. Agent driver changes

**`opencode.go`:**
Remove the `m.ProviderID == "opencode"` native branch (ClearEnv path). The driver becomes ollama-only — it always sets `OPENCODE_CONFIG_CONTENT` env with the inline JSON config. No `ClearEnv` path remains.

Before:
```go
if m.ProviderID == "opencode" {
    lc.ClearEnv = []string{"OPENCODE_CONFIG_CONTENT"}
    return lc
}
// ollama gateway path...
```

After:
```go
// ollama gateway path only (no native provider for opencode)
lc.Env = append(lc.Env, ...)
```

**`agy.go`:** No change — already launches bare, ignores model entirely.

**`claude.go`, `copilot.go`, `codex.go`:** No change — provider IDs already match agent names.

### 5. Tests

**`migrate_test.go`:**
- Update `TestMigrate` assertions: `google` → `agy` for provider, model, and agent references.
- Update `TestMigrate_AgyGoogleSeedingIsIdempotent` → `TestMigrate_AgySeedingIsIdempotent`: check for `agy` provider (not `google`), `agy/native` model, `agy` agent with `["agy"]` supported providers.
- Update any assertions referencing `opencode` provider or `opencode/native` model — opencode should have `["ollama"]` only.

**New test — `TestMigrateConfigSchema`:**
- Test fixup 1: load a config with `google` provider/model, verify it becomes `agy`.
- Test fixup 2: load a config with `agy` agent having `["ollama"]` supported providers, verify it becomes `["agy"]`.
- Test fixup 3: load a config with `opencode` provider and `opencode/native` model, verify they're removed and agent becomes `["ollama"]`.
- Test idempotency: run twice, verify second run is a no-op.

**`agents_test.go`:**
- `TestOpenCode`: Remove native-model assertions (the `nativeModel("opencode")` build that checks for no env and ClearEnv). Keep the ollama path test.
- Remove `TestOpenCodeNativeProviderNamed` entirely — this tests a code path that no longer exists.

### 6. Docs

**`docs/wt-agents/agy-wt.md`:**
- Update model selection section: `google` provider → `agy` provider, `google/native` → `agy/native`.
- Update any references to the `google` provider name.

## What doesn't change

- The `Model.IsNative()` check (`ModelName == "native"`) — this is the existing mechanism for bare launches and stays the same.
- The `buildCommandForModel` session-skip logic for native models — unchanged.
- The `claude`, `copilot`, `codex` driver dispatch logic — unchanged.
- The rotation and eligible-models logic — unchanged.
- The `pi` driver and its `SyncModels`/`models.json` flow — unchanged.