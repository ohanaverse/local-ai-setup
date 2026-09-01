# opencode-wt Launcher Design

**Date:** 2026-06-11
**Status:** approved

## Goal

Add an `opencode-wt` launcher for [OpenCode](https://opencode.ai) (`anomalyco/opencode`), following the established `*-wt` patterns.

## Agent info

| Field | Value |
|-------|-------|
| Homepage | https://opencode.ai |
| GitHub | https://github.com/anomalyco/opencode |
| Install | `curl -fsSL https://opencode.ai/install \| bash` (also `npm i -g opencode-ai@latest`, brew) |
| Binary | `opencode` |
| Auth | API keys via `/connect` command or env vars (`{env:ANTHROPIC_API_KEY}` in config) |
| Config | `~/.config/opencode/opencode.json` (global), `opencode.json` (project) |
| Sessions | `~/.local/share/opencode/storage/session/<project-id>/<session-id>.json` |
| Model format | `provider/model` for direct providers; ollama models via `OPENCODE_CONFIG_CONTENT` |

## Launcher contract

| Global | Value |
|--------|-------|
| `WT_DEFAULT_CODE` | `native:opencode` |
| `WT_DEFAULT_DESIGN` | `native:opencode` |
| `WT_AGENT_NAME` | `opencode` |

OpenCode supports model rotation (code/design/native) like claude-wt, codex-wt, copilot-wt, and pi-wt.

## Model architecture

- **`native:opencode`** — use opencode's own configured default model; launcher passes no `--model` flag
- **All other rotation models** — assumed to be ollama models; launcher generates inline JSON via `OPENCODE_CONFIG_CONTENT` env var

No direct-provider models in rotation. The `native:X` mechanism already gates agent-specific subscription models.

## wt_exec

```bash
wt_exec() {
  local model="$1"
  if [[ "$model" == "native:opencode" || "$model" == "native" ]]; then
    exec opencode "$@"                # let opencode use its configured default
  else
    # ollama model — generate inline config
    local base_url="${PROVIDER_OLLAMA_BASE_URL:-http://localhost:11434}"
    OPENCODE_CONFIG_CONTENT='{"model":"ollama/'"$model"'","provider":{"ollama":{"options":{"baseURL":"'"$base_url"'/v1","apiKey":""}}}}' \
      exec opencode "$@"
  fi
}
```

Base URL read from `PROVIDER_OLLAMA_BASE_URL` in `~/.config/agent-wt/models.conf`, defaulting to `http://localhost:11434`.

## wt_yolo_flag

```
--dangerously-skip-permissions
```

## Session resume (wt_pre_exec)

Implemented like claude-wt:

1. Get repo root commit hash: `git rev-list --max-parents=0 --all | head -1`
2. Check `~/.local/share/opencode/storage/session/<hash>/` for `*.json` files
3. If sessions exist → fzf with "Resume" (appends `--continue`) or "Start fresh"
4. Skip when `--code`/`--design`/`--native` explicitly requested

## Agent init

`opencode-wt --init` seeds:
- `AGENTS.md` — shared instruction template (if missing)
- No pointer file — OpenCode reads `AGENTS.md` natively and has its own `/init` command

## wt_check_deps

Verify `opencode` binary exists; die with install hint if not.

## Files to create / modify

| File | Action |
|------|--------|
| `bin/opencode-wt` | New launcher script |
| `docs/wt-agents/opencode-wt.md` | New per-agent reference doc |
| `docs/wt-agents/README.md` | Add opencode-wt row to Agents table |
| `docs/wt-agents/supported-agents.md` | Add OpenCode row |

## Non-goals

- No changes to `wt-core.sh` — opencode-wt uses existing rotation infrastructure as-is
- No changes to rotation system — opencode handles ollama models like other agents
- No direct-provider model support in rotation (native only)
