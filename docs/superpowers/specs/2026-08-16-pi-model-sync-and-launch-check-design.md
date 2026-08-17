# Pi Model Sync and Launch Check (Go) — Design

## Summary

Port the pi launcher fixes from PRs #25, #26, and #27 (originally implemented in
the legacy bash `bin/pi-wt`) into the Go `wt` tool. The Go `pi` driver currently
passes `--model <id>` unconditionally for non-native models, with no
`models.json` sync and no `_launch` verification. This design brings the Go
driver to parity with the bash behavior, using native Go JSON parsing instead of
`jq`.

## Background

The three PRs fixed the following in the bash `bin/pi-wt`:

- **#25** — verify the selected model is present in `~/.pi/agent/models.json`
  **and** marked `_launch: true` (using `jq`) before passing `--model`, so
  orphaned/manual entries are not launched.
- **#26** — docs only: clarify the `jq` dependency.
- **#27** — auto-sync **all** non-native rotation models (cloud + local/MLX)
  into `models.json`, not just `:cloud`-suffixed ones.

The Go `internal/agents/pi.go` driver (created in Lesson 06, PR #23) is a
minimal stub that appends `--model <id>` for non-native models with no sync and
no check. It also passes `m.ID` (e.g. `ollama/deepseek-v4-pro:cloud`) where pi
expects the bare name `deepseek-v4-pro:cloud` (the `ModelName` field).

## Decisions

- **Native Go JSON parsing** (no `jq` dependency) for both sync and check.
- **Fall back to pi's default** (launch `pi` with no `--model`) when a model
  cannot be verified, with a warning — faithful to bash, never blocks a launch.
- **Optional `Syncer` interface** on the `Driver` to host the pre-launch sync
  step (Approach A).

## Components

### 1. `Syncer` interface (`internal/agents/agents.go`)

```go
// Syncer is an optional Driver capability: a pre-launch step that needs the
// full config (e.g. pi syncing its model catalog). Launch paths call it once
// before Build.
type Syncer interface {
    SyncModels(cfg *config.Config) error
}
```

### 2. `internal/agents/pi.go` — implement `Syncer` + native check

- `piDriver` gains a `SyncModels(cfg *config.Config) error` method.
- `Build(m, yolo)` is rewritten to do the `_launch` check natively (no `jq`).

### 3. `internal/agents/pi_models.go` — `models.json` read/write logic

Kept separate from `pi.go` so the JSON parsing/sync is unit-testable in
isolation. Defines the on-disk shape:

```go
type piModelsFile struct {
    Providers struct {
        Ollama struct {
            Models []piModel `json:"models"`
        } `json:"ollama"`
    } `json:"providers"`
}

type piModel struct {
    Launch        bool     `json:"_launch"`
    ContextWindow int      `json:"contextWindow"`
    ID            string   `json:"id"`
    Input         []string `json:"input"`
    Reasoning     bool     `json:"reasoning"`
}
```

### 4. Launch paths thread `cfg` and invoke the sync

`cmd/wt/launch.go` (`buildLaunch`) and `internal/tui/launch.go` (`launchAgent`)
both gain a `cfg *config.Config` parameter and, before `agents.Command`, do:

```go
if s, ok := d.(agents.Syncer); ok {
    if err := s.SyncModels(cfg); err != nil {
        return nil, err
    }
}
```

### 5. `LaunchCmd.Warn` field (`internal/agents/agents.go`)

Add a `Warn string` field to `LaunchCmd` (empty for all other drivers).
`agents.Command` prints it to stderr when non-empty. This is how the pi driver
surfaces the "using default model" warning without writing to `os.Stderr`
directly.

### 6. Model ID mapping fix

The sync and check both use `m.ModelName` (bare name, e.g.
`deepseek-v4-pro:cloud`), **not** `m.ID` (e.g. `ollama/deepseek-v4-pro:cloud`).
This corrects the latent bug in the current `Build` and matches pi's
`models.json` catalog.

## Data Flow

### Sync flow (`SyncModels(cfg)`) — runs once per launch, before `Build`

1. Resolve `~/.pi/agent/models.json` (honoring `$HOME`).
2. If the file doesn't exist → return `nil` (nothing to sync; matches bash
   `[[ -f ]] || return 0`).
3. Read + parse the JSON. On parse error → return the error (abort launch).
4. Collect all non-native models from `cfg.Models` (`!m.IsNative()`), keyed by
   `m.ModelName`.
5. For each not already present (by `id`), append a new entry:
   ```json
   { "_launch": true, "contextWindow": 262144, "id": "<ModelName>",
     "input": ["text", "image"], "reasoning": true }
   ```
6. If anything was added, write back atomically (temp file + rename). If nothing
   added, leave the file untouched.
7. Idempotent — only adds, never removes.

### Check flow (`Build(m, yolo)`) — per selected model

1. If `m.IsNative()` → return `LaunchCmd{Bin: "pi"}` (no args).
2. Read + parse `models.json`. If missing or unparseable → treat as "not
   verified" (fall back, don't crash).
3. Look for any entry where `id == m.ModelName && _launch == true`.
4. **Verified** → append `--model <m.ModelName>`.
5. **Not verified** → return no `--model` arg (pi uses its own default), and
   surface a warning.

### Warning surfacing

Add a `Warn string` field to `LaunchCmd` (empty for all other drivers);
`agents.Command` prints it to stderr when non-empty. This keeps `Build`
pure/testable rather than writing to `os.Stderr` directly.

### Ordering guarantee

Sync always runs before `Build`, so a rotation-selected model is present in
`models.json` by the time the check runs — the fallback only triggers on genuine
failures (missing file, manual removal, parse error).

## Error Handling

| Scenario | Behavior |
|---|---|
| `models.json` missing (sync) | Return `nil` — nothing to sync, launch proceeds |
| `models.json` missing (check) | Treat as "not verified" → fall back to pi default + warning |
| `models.json` unparseable (sync) | Return error → **abort launch** (config is corrupt; don't silently proceed) |
| `models.json` unparseable (check) | Treat as "not verified" → fall back + warning (don't crash on a read-only check) |
| Write failure during sync (temp/rename) | Return error → abort launch |
| Model not in `models.json` after sync | Fall back to pi default + warning (never blocks launch) |
| `$HOME` unset | `os.UserHomeDir()` error → return error (can't locate `models.json`) |

**Rationale for the asymmetry:** the sync is a *mutation* — if it can't read or
write the file, something is genuinely wrong and we should stop. The check is a
*read* — if it can't verify, the safe default is to fall back to pi's own model
rather than crash, matching the bash "using default" behavior.

**No partial writes:** the sync writes via temp-file + `os.Rename`, so a failed
write never corrupts the existing `models.json`.

## Testing

**Testability seam:** the sync and check functions take the `models.json` path
as a parameter, so tests use a temp file instead of the real
`~/.pi/agent/models.json`. The `piDriver` methods resolve the real path via
`os.UserHomeDir()` and delegate.

```go
// pi_models.go — internal, path-injected for tests
func syncModels(cfg *config.Config, path string) error
func isLaunchable(name, path string) (bool, error)
```

**Unit tests (following the repo's what/why comment convention):**

| Test | What it verifies |
|---|---|
| `TestPiSyncModelsAddsMissing` | Non-native models absent from `models.json` get added with `_launch: true` |
| `TestPiSyncModelsIdempotent` | Running twice doesn't duplicate entries |
| `TestPiSyncModelsSkipsNative` | `native` models are never synced |
| `TestPiSyncModelsUsesModelName` | Uses `ModelName` (bare), not `ID` (prefixed) — guards the mapping fix |
| `TestPiSyncModelsPreservesExisting` | Existing entries (incl. `_launch: false`) are left untouched |
| `TestPiSyncModelsMissingFile` | Missing `models.json` returns `nil`, no error |
| `TestPiBuildVerified` | `_launch: true` model → `--model <ModelName>` |
| `TestPiBuildNotVerified` | `_launch: false` / absent model → no args + `Warn` set |
| `TestPiBuildNative` | Native model → no args (existing behavior preserved) |
| `TestPiBuildMissingFile` | Missing `models.json` → no args + `Warn` set |
| `TestBuildLaunchSyncsPi` | `buildLaunch` invokes `SyncModels` for pi (wiring) |
| `TestLaunchAgentSyncsPi` | `launchAgent` invokes `SyncModels` for pi (wiring) |

**Existing test to update:** `TestPi` in `agents_test.go` currently asserts
`--model` is passed unconditionally for a cloud model — it will be updated to
reflect the new verified/fallback behavior.

## Out of Scope

- No changes to the legacy bash `bin/pi-wt` (already a shim forwarding to `wt`).
- No changes to other agent drivers.
- No changes to `models.json` entries that already exist (sync only adds).
