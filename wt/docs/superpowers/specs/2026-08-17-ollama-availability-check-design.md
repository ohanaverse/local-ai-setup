# Ollama Availability Check Design

## Overview

Before launching an agent with an Ollama model, verify the model is locally available via `ollama list`. If unavailable, warn the user and offer choices.

## Motivation

The legacy bash engine skipped cloud models not present in `ollama list` output. The Go rewrite selects from config without verifying that the model is currently available. This can lead to agents failing to start or producing confusing errors when the user hasn't pulled the model.

## Goals

- Prevent launching an agent with an unavailable Ollama model
- Give users clear, actionable feedback when a model is missing
- Support both TUI and non-TUI launch paths
- Match the bash engine's behavior of skipping unavailable models in rotation

## Non-Goals

- Check OpenRouter or other cloud providers (they are API-based, no local pull needed)
- Auto-pull missing models (out of scope, security and bandwidth concerns)
- Verify model file integrity (just presence in `ollama list`)

## Architecture

### New Package: `internal/ollamacheck`

A small, focused package that knows how to check if an Ollama model is available locally.

```go
package ollamacheck

// IsOllamaModel returns true if the model is from the ollama provider.
func IsOllamaModel(m config.Model) bool

// Available checks whether modelName appears in `ollama list` output.
// Returns false when ollama is not installed or the model is not present.
func Available(modelName string) (bool, error)
```

- `IsOllamaModel` checks `m.ProviderID == "ollama"`
- `Available` runs `ollama list`, parses output, checks for exact match on `modelName`
- If `ollama` binary is not in PATH, returns `(false, nil)` — no error, just unavailable
- If `ollama list` exits non-zero, returns the error so callers can decide

### TUI Integration

New phase `phaseOllamaWarn` in `internal/tui/app.go`:

```go
const (
    phaseList       phase = iota
    phaseModel
    phaseBrowser
    phaseResume
    phaseGuardWarn
    phaseOllamaWarn  // confirm before launching with unavailable ollama model
)
```

New fields on `model` struct:

```go
ollamaWarnModel list.Model   // confirmation choices for unavailable model
ollamaWarnModelName string   // the model name being warned about
```

**Flow:**

1. User presses `enter` in `phaseModel`
2. Before any launch/resume logic, check `ollamacheck.IsOllamaModel(m.current)`
3. If yes, call `ollamacheck.Available(m.current.ModelName)`
4. If available: continue to existing launch/resume flow
5. If unavailable: build choices, transition to `phaseOllamaWarn`
6. In `phaseOllamaWarn`, user selects:
   - **Proceed anyway** — continue to launch/resume flow
   - **Skip to next model** — rotate to next model (advances `rotation-<tag>.state` like the `r` key), return to `phaseModel`
   - **Cancel** — return to `phaseModel`

   If all models in the tag group are unavailable, Skip cycles back to the same model. The warning is shown again with "(all models unavailable)" in the description.

### Non-TUI Integration

In `cmd/wt/launch.go`, the `launch()` function:

1. Resolve default model via `defaultModel(cfg, agent)`
2. Check `ollamacheck.IsOllamaModel(m)`
3. If yes, call `ollamacheck.Available(m.ModelName)`
4. If unavailable: return error with `ollama pull` suggestion
5. If available or not Ollama: continue to `buildLaunch()`

Error message format:
```
wt: model "gemma4:9b" is not available locally. Run: ollama pull gemma4:9b
```

### Choice List UI

In `internal/tui/launch.go`, add:

```go
type ollamaChoice int

const (
    ollamaProceedChoice ollamaChoice = iota
    ollamaSkipChoice
    ollamaCancelChoice
)

type ollamaItem struct {
    choice ollamaChoice
    title  string
    desc   string
}

func buildOllamaChoices(modelName string) []list.Item
```

Choices:
- Proceed anyway — "Launch with unavailable model (may fail)"
- Skip to next — "Rotate to the next model in the tag group"
- Cancel — "Return to the agent+model screen"

## Data Flow

### TUI Path

```
User presses Enter in phaseModel
  → Check if current model is Ollama (IsOllamaModel)
    → Yes: Check availability (Available)
      → Available: Continue to existing launch/resume flow
      → Unavailable: Transition to phaseOllamaWarn
        → User selects Proceed: Continue to launch/resume flow
        → User selects Skip: Rotate to next model, return to phaseModel
        → User selects Cancel: Return to phaseModel
    → No: Continue to launch/resume flow
```

### Non-TUI Path

```
launch() called
  → Resolve default model
  → Check if Ollama model
    → Yes: Check availability
      → Unavailable: Return error ("run 'ollama pull <model>'")
    → No: Continue
  → Build and run command
```

## Error Handling

| Scenario | Behavior |
|---|---|
| `ollama` not in PATH | Treat as unavailable, show warning |
| `ollama list` fails | Return error to caller |
| User selects "Proceed anyway" | Continue despite warning |
| User selects "Skip" | Rotate to next model (persist state), warn if all unavailable |
| Non-TUI, model unavailable | Fail fast with clear error |

## Testing

### `internal/ollamacheck/ollamacheck_test.go`

- `TestIsOllamaModel` — true for ollama provider, false for others
- `TestAvailable` — with fake `ollama list` output
- `TestAvailableNotInstalled` — when ollama binary not found
- `TestAvailableError` — when `ollama list` exits non-zero

### `internal/tui/app_test.go`

- `TestOllamaWarnShownWhenUnavailable` — phase transitions to ollama warn
- `TestOllamaWarnProceed` — continues to launch
- `TestOllamaWarnSkip` — rotates and returns to phaseModel
- `TestOllamaWarnCancel` — returns to phaseModel
- `TestNoOllamaWarnForNonOllamaModel` — skips check for non-ollama models

### `cmd/wt/launch_test.go`

- `TestLaunchFailsWhenOllamaModelUnavailable` — non-TUI error path

## Files Changed

| File | Change |
|---|---|
| `internal/ollamacheck/ollamacheck.go` | New package |
| `internal/ollamacheck/ollamacheck_test.go` | New tests |
| `internal/tui/app.go` | Add phaseOllamaWarn, integration logic |
| `internal/tui/app_test.go` | Add TUI tests |
| `internal/tui/launch.go` | Add ollama choice list helpers |
| `cmd/wt/launch.go` | Add non-TUI availability check |
| `cmd/wt/launch_test.go` | Add non-TUI test |

## References

- `bin/wt-core.sh` lines 544-575 — legacy ollama availability check
- `internal/registry/discover.go` — `parseOllamaList` for parsing `ollama list` output
- `internal/config/config.go` — `Model` struct with `ProviderID` and `ModelName`
