# Supported Agents

Quick reference for the AI coding agents supported by the `*-wt` launchers.

| Agent | Launcher | Homepage | GitHub | Install |
|-------|----------|----------|--------|---------|
| Claude Code | `claude-wt` | [claude.ai/code](https://claude.ai/code) | — (closed source) | `curl -fsSL https://claude.ai/install.sh \| bash` |
| OpenAI Codex CLI | `codex-wt` | [developers.openai.com/codex](https://developers.openai.com/codex) | [openai/codex](https://github.com/openai/codex) | `npm install -g @openai/codex` |
| GitHub Copilot CLI | `copilot-wt` | [github.com/github/copilot-cli](https://github.com/github/copilot-cli) | [github/copilot-cli](https://github.com/github/copilot-cli) | `npm install -g @github/copilot` |
| pi-coding-agent | `pi-wt` | [github.com/mariozechner/pi-coding-agent](https://github.com/mariozechner/pi-coding-agent) | [mariozechner/pi-coding-agent](https://github.com/mariozechner/pi-coding-agent) | `npm install -g @mariozechner/pi-coding-agent` |
| Antigravity CLI | `agy-wt` | [antigravity.google/cli](https://antigravity.google/cli) | — (closed source) | `curl -fsSL https://antigravity.google/cli/install.sh \| bash` |
| OpenCode | `opencode-wt` | [opencode.ai](https://opencode.ai) | [anomalyco/opencode](https://github.com/anomalyco/opencode) | `curl -fsSL https://opencode.ai/install \| bash` |
| Shell command | `shell-wt` | — | [ohanaverse/local-ai-setup](https://github.com/ohanaverse/local-ai-setup) | Copy from this repo (`wt/`) |

## Notes

- **pi** — npm scope is `@mariozechner/pi-coding-agent`; the GitHub repo is `mariozechner/pi-coding-agent`.
- **agy** — curl-to-bash installer, same pattern as Claude Code.
- **copilot** — npm scope is `@github/copilot`; the GitHub repo is `github/copilot-cli`.
- **opencode** — npm scope is `opencode-ai`; GitHub org is `anomalyco`.

## See also

Per-agent deep-dives: [claude-wt](claude-wt.md) · [codex-wt](codex-wt.md) · [copilot-wt](copilot-wt.md) · [opencode-wt](opencode-wt.md) · [pi-wt](pi-wt.md) · [agy-wt](agy-wt.md) · [shell-wt](shell-wt.md)
