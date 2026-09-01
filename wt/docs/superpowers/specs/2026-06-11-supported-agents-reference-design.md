# Supported Agents Reference Doc

**Date:** 2026-06-11
**Status:** approved

## Goal

Add a consolidated quick-reference doc listing all AI coding agents supported by the `*-wt` launchers, with homepage, GitHub repo (if public), and installation command.

## File

New file: `docs/wt-agents/supported-agents.md`

## Content

### Title + intro

One-sentence intro: "Quick reference for the AI coding agents supported by the `*-wt` launchers."

### Reference table

| Agent | Launcher | Homepage | GitHub | Install |
|-------|----------|----------|--------|---------|
| Claude Code | `claude-wt` | https://claude.ai/code | — (closed source) | `curl -fsSL https://claude.ai/install.sh \| bash` |
| OpenAI Codex CLI | `codex-wt` | https://developers.openai.com/codex | https://github.com/openai/codex | `npm install -g @openai/codex` |
| GitHub Copilot CLI | `copilot-wt` | https://github.com/github/copilot-cli | https://github.com/github/copilot-cli | `npm install -g @github/copilot` |
| pi-coding-agent | `pi-wt` | https://github.com/mariozechner/pi-coding-agent | https://github.com/mariozechner/pi-coding-agent | `npm install -g @mariozechner/pi-coding-agent` |
| Antigravity CLI | `agy-wt` | https://antigravity.google/cli | — (closed source) | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |

### Notes section

Brief callouts for anything unusual:
- pi's npm scope (`@mariozechner/pi-coding-agent` vs the GitHub org `mariozechner/pi-coding-agent`)
- agy's install URL pattern (curl-to-bash, like Claude Code)
- copilot's npm scope (`@github/copilot`)

## Integration

Add a link to `supported-agents.md` from `docs/wt-agents/README.md` — either as a new row in the existing Agents table or as a "See also" line after it.

## Non-goals

- Not duplicating the detailed per-agent docs (config, auth, model selection, verification)
- Not adding install instructions for the `*-wt` launchers themselves (that's in the main README)
