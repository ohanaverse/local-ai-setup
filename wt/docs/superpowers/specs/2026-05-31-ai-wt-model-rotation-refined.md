# AI-WT Model Rotation Enhancement — Refined Design

## Problem Statement
When using `source <(ai-code)>` followed by a `*-wt` launcher (e.g., `pi-wt`), if the ai-code rotation selects a native model like `native:claude`, the `pi-wt` launcher skips it (because it's not `native:pi`) and runs Pi with its default model. However, the ai-code rotation has already advanced its state, so the skipped model is effectively "used up" even though it wasn't actually used by Pi. This wastes rotation slots and breaks the intended model rotation sequence.

## Design Goals
1. Solve the model skip compensation problem — rotation advances only when a model is actually used
2. Allow `*-wt` scripts to work without any model rotation configuration (models.conf)
3. Maintain backward compatibility with existing `source <(ai-code)>; *-wt` workflow
4. Preserve shared rotation state and cross-rotation skip coordination
5. Provide explicit intent via `--code`/`--design` flags
6. **Agent-aware defaults** — each launcher defaults to its own native model when config is missing

## Solution Overview
Modify `*-wt` launchers to accept optional `--code` and `--design` flags that enable internal model selection using the shared rotation state from `~/.config/ai-shell/models.conf`. When these flags are present:
- Launcher handles model selection internally (no reliance on `AI_CODING_MODEL` environment variable)
- Model selection and usage occur in same process — eliminates state mismatch
- Rotation advances only after successful model selection and usage
- If models.conf is missing or rotations are empty, fall back to **agent-specific native defaults**
- When flags are absent, fall back to existing `AI_CODING_MODEL` behavior (backward compatibility)

## Refined Design

### 1. Architecture Changes
Each `*-wt` launcher (`claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`) gains:
- Optional `--code` flag: use code rotation for model selection
- Optional `--design` flag: use design rotation for model selection
- Internal model selection logic that reads/writes rotation state files
- **Agent-specific model compatibility checking**
- **Agent-specific default models** when config is missing or empty

**agy-wt**: No changes needed. agy doesn't support model passthrough, so rotation integration would add dead code.

### 2. Model Selection Logic (Shared Pattern)

All launchers use similar internal model selection when `--code` or `--design` is specified:

```bash
get_model_from_rotation() {
  local mode="$1"  # "code" or "design"
  local config_file="${HOME}/.config/ai-shell/models.conf"
  local state_file="${HOME}/.config/ai-shell/rotation-${mode}.state"
  local other_state_file="${HOME}/.config/ai-shell/rotation-$( [[ "$mode" == "code" ]] && echo "design" || echo "code" ).state"
  
  # Agent-specific defaults — defined BEFORE config check (fixes scope bug)
  local default_code default_design
  case "$(basename "$0")" in
    *claude-wt*)
      default_code="native:claude"
      default_design="native:claude"
      ;;
    *codex-wt*)
      default_code="native:codex"
      default_design="native:claude"  # codex has no design-native, use claude
      ;;
    *copilot-wt*)
      default_code="native:copilot"
      default_design="native:copilot"
      ;;
    *pi-wt*)
      default_code="native:pi"      # CHANGED: use native:pi instead of native:claude
      default_design="native:pi"    # CHANGED: use native:pi instead of native:copilot
      ;;
  esac
  
  local DEFAULT_MODEL
  if [[ "$mode" == "design" ]]; then
    DEFAULT_MODEL="$default_design"
  else
    DEFAULT_MODEL="$default_code"
  fi

  # Handle missing config file - use agent-specific defaults
  if [[ ! -f "$config_file" ]]; then
    echo "$DEFAULT_MODEL"
    return
  fi
  
  source "$config_file"

  local model_var
  if [[ "$mode" == "code" ]]; then
    model_var="CODE_MODELS[@]"
  else
    model_var="DESIGN_MODELS[@]"
  fi

  local models=("${!model_var}")
  local num_models=${#models[@]}

  # Handle empty rotation array - fall back to agent-specific default
  if [[ "$num_models" -eq 0 ]]; then
    echo "$DEFAULT_MODEL"
    return
  fi

  # Read current index for this mode (default 0 if missing/invalid)
  local current_index=0
  if [[ -f "$state_file" ]]; then
    current_index=$(sed -n '1p' "$state_file" 2>/dev/null || echo 0)
    if ! [[ "$current_index" =~ ^[0-9]+$ ]] || [[ "$current_index" -ge "$num_models" ]]; then
      current_index=0
    fi
  fi

  # Read other mode's last selected model for cross-rotation skip
  local other_last=""
  if [[ -f "$other_state_file" ]]; then
    other_last=$(sed -n '2p' "$other_state_file" 2>/dev/null || echo "")
  fi

  # Cache ollama models list for performance (avoid repeated calls)
  local ollama_models
  ollama_models=$(ollama list 2>/dev/null | awk 'NR>1 {print $1}')

  local selected=""
  local attempts=0
  while [[ $attempts -lt $num_models ]]; do
    selected="${models[$current_index]}"
    
    # Cross-rotation skip: skip if matches other mode's last selection
    if [[ -n "$other_last" ]] && [[ "$selected" == "$other_last" ]]; then
      current_index=$(( (current_index + 1) % num_models ))
      selected="${models[$current_index]}"
      attempts=$((attempts + 1))
      continue
    fi

    # Agent-specific usability check
    local agent_usable=1
    case "$(basename "$0")" in
      *claude-wt*)
        [[ "$selected" == native:* && "$selected" != "native:claude" ]] && agent_usable=0
        ;;
      *codex-wt*)
        [[ "$selected" == native:* && "$selected" != "native:codex" ]] && agent_usable=0
        ;;
      *copilot-wt*)
        [[ "$selected" == native:* && "$selected" != "native:copilot" ]] && agent_usable=0
        ;;
      *pi-wt*)
        [[ "$selected" == native:* && "$selected" != "native:pi" ]] && agent_usable=0
        ;;
    esac
    
    if [[ $agent_usable -eq 0 ]]; then
      # Not usable by this agent, skip to next
      current_index=$(( (current_index + 1) % num_models ))
      attempts=$((attempts + 1))
      continue
    fi
    
    if [[ "$selected" != native:* ]]; then
      # Cloud model - check if available in ollama (using cached list)
      if ! echo "$ollama_models" | grep -qFx "$selected"; then
        printf '%s: model "%s" not in ollama — skipping\n' "$(basename "$0")" "$selected" >&2
        current_index=$(( (current_index + 1) % num_models ))
        attempts=$((attempts + 1))
        continue
      fi
    fi
    
    # Found a usable model
    break
  done

  if [[ $attempts -ge $num_models ]]; then
    printf '%s: no usable model found in %s rotation\n' "$(basename "$0")" "$mode" >&2
    return 1  # No usable model found
  fi

  # Advance index for next invocation (only called when model will be used)
  local next_index=$(( (current_index + 1) % num_models ))
  printf '%s\n%s\n' "$next_index" "$selected" > "$state_file"
  
  echo "$selected"
}
```

**Key refinements:**
- `DEFAULT_MODEL` defined before config file check (fixes scope bug)
- Agent-specific defaults for both code and design modes
- pi-wt defaults to `native:pi` (not `native:claude`) — avoids immediate skip
- Ollama models list cached once per invocation (performance improvement)

### 3. pi-wt Implementation (Corrected)

**Argument Parsing** (added to `parse_wt_args()`):
```bash
WT_MODEL_MODE=""  # "" for none, "code" or "design"
PI_WT_ARGS=()
while [[ $# -gt 0 ]]; do
  case "$1" in
    --code) WT_MODEL_MODE="code"; shift ;;
    --design) WT_MODEL_MODE="design"; shift ;;
    *) PI_WT_ARGS+=("$1"); shift ;;
  esac
done
```

**Execution Logic** (modified `wt_exec()`):
```bash
wt_exec() {
  local model_to_use=""
  
  if [[ -n "$WT_MODEL_MODE" ]]; then
    # Use internal model selection with --code/--design
    model_to_use=$(get_model_from_rotation "$WT_MODEL_MODE") || exit 1
  elif [[ -n "${AI_CODING_MODEL:-}" ]]; then
    # Fallback to old behavior for backward compatibility
    model_to_use="$AI_CODING_MODEL"
  fi
  
  # Launch pi with selected model
  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:pi" ]]; then
      printf '%s: skipping "%s" (not a pi model)\n' "$WT_NAME" "$model_to_use" >&2
      exec pi "${PI_WT_ARGS[@]}"
    fi
    if [[ "$model_to_use" == "native" || "$model_to_use" == "native:pi" ]]; then
      exec pi "${PI_WT_ARGS[@]}"
    fi
    if grep -qF "\"id\": \"$model_to_use\"," "$HOME/.pi/agent/models.json" 2>/dev/null; then
      exec pi --model "$model_to_use" "${PI_WT_ARGS[@]}"
    else
      printf '%s: model "%s" not configured for pi, using default\n' \
        "$WT_NAME" "$model_to_use" >&2
      exec pi "${PI_WT_ARGS[@]}"
    fi
  else
    exec pi "${PI_WT_ARGS[@]}"
  fi
}
```

**Key refinements:**
- `|| exit 1` pattern for clearer error handling
- Uses `PI_WT_ARGS` array consistently (not `$@`)

### 4. Other Launcher Implementations

#### claude-wt
```bash
# Add --code/--design argument parsing (same as pi-wt)
# Add get_model_from_rotation() function (shared pattern above)

wt_exec() {
  local model_to_use=""
  
  if [[ -n "$WT_MODEL_MODE" ]]; then
    model_to_use=$(get_model_from_rotation "$WT_MODEL_MODE") || exit 1
  elif [[ -n "${AI_CODING_MODEL:-}" ]]; then
    model_to_use="$AI_CODING_MODEL"
  fi
  
  # Launch claude with selected model
  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:claude" ]]; then
      printf '%s: skipping "%s" (not a claude model)\n' "$WT_NAME" "$model_to_use" >&2
      exec claude "$@"
    fi
    if [[ "$model_to_use" == "native" || "$model_to_use" == "native:claude" ]]; then
      exec claude "$@"
    fi
    # Cloud model - pass through to claude
    exec claude --model "$model_to_use" "$@"
  else
    exec claude "$@"
  fi
}
```

#### codex-wt
```bash
# Add --code/--design argument parsing
# Add get_model_from_rotation() function
# Add fix_codex_ollama_profile() (existing function, keep as-is)

wt_exec() {
  local model_to_use=""
  
  if [[ -n "$WT_MODEL_MODE" ]]; then
    model_to_use=$(get_model_from_rotation "$WT_MODEL_MODE") || exit 1
  elif [[ -n "${AI_CODING_MODEL:-}" ]]; then
    model_to_use="$AI_CODING_MODEL"
  fi
  
  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:codex" ]]; then
      printf '%s: skipping "%s" (not a codex model)\n' "$WT_NAME" "$model_to_use" >&2
      exec codex "$@"
    fi
    if [[ "$model_to_use" == "native:codex" ]]; then
      exec codex "$@"
    fi
    # Cloud model - check ollama and use profile
    if ollama list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qFx "$model_to_use"; then
      fix_codex_ollama_profile
      exec codex --profile ollama-launch -m "$model_to_use" "$@"
    else
      printf '%s: model "%s" not available, using default\n' \
        "$WT_NAME" "$model_to_use" >&2
      exec codex "$@"
    fi
  else
    exec codex "$@"
  fi
}
```

#### copilot-wt
```bash
# Add --code/--design argument parsing
# Add get_model_from_rotation() function

wt_exec() {
  local model_to_use=""
  
  if [[ -n "$WT_MODEL_MODE" ]]; then
    model_to_use=$(get_model_from_rotation "$WT_MODEL_MODE") || exit 1
  elif [[ -n "${AI_CODING_MODEL:-}" ]]; then
    model_to_use="$AI_CODING_MODEL"
  fi
  
  if [[ -n "$model_to_use" ]]; then
    if [[ "$model_to_use" == native:* && "$model_to_use" != "native:copilot" ]]; then
      printf '%s: skipping "%s" (not a copilot model)\n' "$WT_NAME" "$model_to_use" >&2
      exec copilot "$@"
    fi
    if [[ "$model_to_use" == "native:copilot" ]]; then
      exec copilot --model auto "$@"
    fi
    # Cloud model - set env vars and launch
    if ! ollama list 2>/dev/null | awk 'NR>1 {print $1}' | grep -qFx "$model_to_use"; then
      printf '%s: model "%s" not in ollama — trying --model passthrough\n' \
        "$WT_NAME" "$model_to_use" >&2
      exec copilot --model "$model_to_use" "$@"
    fi
    local copilot_base_url="${COPILOT_PROVIDER_BASE_URL:-${ANTHROPIC_BASE_URL:-http://localhost:11434}}"
    exec env \
      COPILOT_PROVIDER_BASE_URL="${copilot_base_url%/}/v1" \
      COPILOT_PROVIDER_API_KEY="" \
      COPILOT_PROVIDER_WIRE_API="responses" \
      COPILOT_MODEL="$model_to_use" \
      copilot "$@"
  fi
  exec copilot "$@"
}
```

> Historical spec note (added 2026-09-01): `WIRE_API="responses"` above is
> outdated. Copilot CLI's `responses` wire drops leading characters through
> the OpenAI-compatible bridge; wt now ships `completions` in both gateway
> modes. See `docs/wt-agents/copilot-wt.md` for the current contract.

#### agy-wt
**No changes.** agy doesn't support model passthrough, so adding rotation logic would be dead code.

### 5. Default Behavior When models.conf is Missing

| Launcher | `--code` default | `--design` default |
|----------|------------------|-------------------|
| `claude-wt` | `native:claude` | `native:claude` |
| `codex-wt` | `native:codex` | `native:claude` |
| `copilot-wt` | `native:copilot` | `native:copilot` |
| `pi-wt` | `native:pi` | `native:pi` |

**Rationale:** Each launcher defaults to its own native model, ensuring the first invocation works without skips.

### 6. Backward Compatibility
- Existing workflow `source <(ai-code); pi-wt` unchanged (uses `AI_CODING_MODEL` environment variable)
- `ai-shell` script unchanged for users who source it for direct tool usage (`claude`, `codex`, etc.)
- New `--code/--design` flags are optional enhancements
- When neither flag is present, launchers behave exactly as before

### 7. Error Handling and Edge Cases
- **No usable model in entire rotation**: Show error and exit (do not silently fall back to default)
  - Prevents silent failures when all models in rotation are incompatible/unavailable
  - User can investigate why no models work (missing ollama pull, configuration issue, etc.)
- **Invalid state files**: Corrupted or missing state files treated as index 0 (safe reset)
- **Ollama not available**: Clear error messages when cloud models are requested but not available
- **Cross-rotation skip**: Still functional even when defaulting due to missing config

### 8. Performance Optimization
- **Ollama models caching**: Call `ollama list` once per invocation, cache result in variable
- Avoids repeated subprocess calls when iterating through rotation models
- Especially important with large model lists

## Benefits
1. **Solves skip compensation**: Rotation advances only when model is actually used by the agent
2. **Explicit intent**: `--code` means "use code rotation", `--design` means "use design rotation"
3. **Works without configuration**: Sensible fallbacks when models.conf is missing
4. **Backward compatible**: Existing workflows continue to function unchanged
5. **Centralized logic**: Model selection complexity contained within each launcher
6. **Preserves coordination**: Cross-rotation skip behavior maintained through shared state files
7. **Agent-aware defaults**: Each launcher defaults to its own native model (no immediate skips)
8. **Performance**: Ollama models list cached once per invocation

## Drawbacks
1. **Requires launcher modifications**: All `*-wt` scripts need updates (claude-wt, codex-wt, copilot-wt)
2. **Slightly more complex launchers**: Each gains internal model selection logic
3. **New user pattern**: Users adopt `pi-wt --code` instead of `source <(ai-code)>; pi-wt` (but old way still works)

## Documentation Updates Required

### bin/README.md
Add section documenting `--code`/`--design` flags for each launcher:

```markdown
## Model Rotation Flags

Each `*-wt` launcher accepts optional `--code` and `--design` flags for integrated model rotation:

| Launcher | `--code` | `--design` | Default (no flag) |
|----------|----------|------------|-------------------|
| `claude-wt` | Use code rotation | Use design rotation | `AI_CODING_MODEL` env |
| `codex-wt` | Use code rotation | Use design rotation | `AI_CODING_MODEL` env |
| `copilot-wt` | Use code rotation | Use design rotation | `AI_CODING_MODEL` env |
| `pi-wt` | Use code rotation | Use design rotation | `AI_CODING_MODEL` env |

Examples:
```bash
pi-wt --code        # Select from code rotation, advance only on use
claude-wt --design  # Select from design rotation
copilot-wt          # Use AI_CODING_MODEL (backward compatible)
```

When `models.conf` is missing, each launcher defaults to its native model.
```

Update the model-passthrough table to reflect the new flags.

## Migration Path

Users currently using `source <(ai-code)>; pi-wt` can migrate to:
```bash
pi-wt --code   # Equivalent, but rotation advances only when model is used
```

The old workflow continues to work for backward compatibility.

## Testing Checklist

- [ ] pi-wt --code with models.conf present
- [ ] pi-wt --code with models.conf missing (default to native:pi)
- [ ] pi-wt --code with empty CODE_MODELS array
- [ ] pi-wt --design with design rotation
- [ ] pi-wt without flags (backward compatible with AI_CODING_MODEL)
- [ ] pi-wt --code when all models unavailable (error + exit)
- [ ] Cross-rotation skip works (code avoids design's last pick)
- [ ] Repeat above for claude-wt, codex-wt, copilot-wt
- [ ] Verify rotation state files advance correctly
- [ ] Verify ollama models caching (performance)
