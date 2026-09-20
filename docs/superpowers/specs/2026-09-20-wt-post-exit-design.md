# wt Post-Exit Flow Redesign

## Overview

Two changes to wt's post-exit behavior:

1. **Model-stopping prompt** (issue #115): When wt exits, offer to stop running local models that have zero refcount.
2. **Skip survey for native models** (issue #116): Skip the survey phase when the launched model's provider matches the agent name (e.g., `claude/*` agent launched with an `anthropic/` provider model).

Both changes share the same post-exit interaction flow. The flow is reordered so user actions come first, followed by informational output.

## Post-Exit Flow (New Order)

```
survey → model-stopping picker → model-stopping progress/stats → summary → survey stats → pricing notice
```

**Rules:**
- If no local models are running: skip model-stopping picker and all its output entirely.
- If native model (provider == agent): skip survey phase entirely, but still show model-stopping if applicable.
- Pricing notice always last (informational).
- Summary line is deferred until after user interaction (currently printed immediately after agent exits).

## Model-Stopping Picker

### Input

Checkbox-style multi-select using the same stdin/stdout as the survey:

```
Stop running local models?
  [x] claude (ollama/qwen3.8:27b-mlx)
  [x] opencode (omlx/ornith-1.5:35b)
  [ ] claude (mtplx/qwen3.8:30b)

  [ ] none    [x] all

Enter to confirm, Esc to skip
```

- Each row shows the model label (`family/model-id`)
- `[x]` = selected for stopping, `[ ]` = not selected
- `none` / `all` toggle buttons at the bottom
- Navigation: `↑`/`↓` for rows, `Space` to toggle, `n`/`a` for none/all, `Esc` to skip

### Output

Progress lines after confirmation:

```
Stopping ollama/qwen3.8:27b-mlx... done
Stopping omlx/ornith-1.5:35b... done
```

### Implementation

- Queries `localmodels.Inventory(cfg)` for the list of running local models
- Calls stop methods per model family (reverse of `lifecycle.Start` backends)
- `ollamaBackend.Stop(modelID)` → `ollama stop <modelID>`
- `omlxBackend.Stop(modelID)` → `omlx stop` (single-model provider)
- `mtplxBackend.Stop(modelID)` → `mtplx stop --port 8003 --grace-seconds 10` (single-model provider)

## Native Model Detection

A model is "native" when its provider family matches the agent name:

- Agent `claude` + model `anthropic/claude-opus-4-1` → native (skip survey)
- Agent `claude` + model `ollama/qwen3.8:27b-mlx` → not native (show survey)
- Agent `opencode` + model `anthropic/claude-opus-4-1` → not native (show survey)

Detection: extract the family (first component before `/` in `m.ID`), compare to agent name.

## Files to Modify

### New file

- **`wt/internal/survey/stop.go`** — model-stopping picker:
  - `Picker(cfg, store, agent, m, r, w) error` — runs the checkbox picker + stop execution
  - `renderPicker(r, w, snapshot)` — renders the checkbox UI
  - `executeStops(cfg, selected, w)` — calls stop methods per model, prints progress

### Modified files

- **`wt/internal/survey/prompt.go`** — `PromptRun` becomes phased runner:
  - Delegates to `runSurveyPhase()` then `runStopPhase()` (new)
  - Early-exit guard `m.ID == ""` stays (command agents skip everything)
  - Native model check: if true, skip survey phase but proceed to stop phase

- **`wt/internal/agents/launch.go`** — reorder flow:
  - `runAgentCmd`: defer summary printing until after survey+stop
  - TUI path: same reordering (already defers via `pendingSummary`)

- **`wt/internal/config/modelman.go`** — add `isNativeModel(agent, m)` helper

- **`wt/internal/lifecycle/backends.go`** — add `Stop(modelID)` to each backend:
  - `ollamaBackend.Stop`, `omlxBackend.Stop`, `mtplxBackend.Stop`
