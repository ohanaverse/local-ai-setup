# wt-agents reference

Per-agent reference docs for the `*-wt` launchers in [`bin/`](../../bin/). Each file documents how the underlying agent is configured on disk, how it authenticates, and how the launcher invokes it.

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, and `agy-wt`.

The launcher contract itself (flags, rotation behavior, install commands) lives in this README. These per-agent docs add per-agent context that does not fit in the table above.

## Agents

| Agent | File | Verification status |
|---|---|---|
| Claude Code | [claude-wt.md](claude-wt.md) | Verified on this machine, 2026-06-01 |
| OpenAI Codex CLI | [codex-wt.md](codex-wt.md) | Convention only (used on other machines) |
| GitHub Copilot CLI | [copilot-wt.md](copilot-wt.md) | Convention only (not installed on this machine) |
| pi-coding-agent | [pi-wt.md](pi-wt.md) | Verified on this machine, 2026-06-01 — includes NYT LiteLLM worked example |
| Antigravity CLI | [agy-wt.md](agy-wt.md) | Convention only (not installed on this machine) |

## Verification convention

Each per-agent file ends with a "Verified on this machine" section. Verified files carry a `Verified on this machine, YYYY-MM-DD` stamp inside that section; convention-only files state non-verification explicitly. Re-verifying a file is a single-file edit, not a rename.

## Common launcher flags

All `*-wt` launchers support:

| Flag | Description |
|------|-------------|
| `-w <name>`, `--worktree <name>` | Use or create a worktree for the given branch name |
| `--yolo` | Skip permission prompts (agent-specific) |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |
| `--code` | Use code model rotation (default if neither flag given) |
| `--design` | Use design model rotation |
| `--native` | Use the agent's configured native model (requires `NATIVE_<AGENT>` in models.conf) |
| `--no-guard` | Remove the main-branch commit guard |
| `--check-guard` | Report whether the guard is installed |

With no flags, launchers present an fzf picker showing available worktrees and branches.

## Native model flag

The `--native` flag bypasses model rotation and uses the agent's dedicated native model:

| Command | Config variable | Example value |
|---------|-----------------|---------------|
| `claude-wt --native` | `NATIVE_CLAUDE` | `claude-sonnet-4-5` |
| `pi-wt --native` | `NATIVE_PI` | `claude-sonnet-4-5` |
| `copilot-wt --native` | `NATIVE_COPILOT` | `claude-sonnet-4-5` |
| `codex-wt --native` | `NATIVE_CODEX` | `claude-sonnet-4-5` |

The native model is read from `~/.config/ai-shell/models.conf`. If the variable is not configured, the launcher errors:
```bash
claude-wt: --native requires NATIVE_CLAUDE to be configured in models.conf
```

## Model rotation

All model-rotating launchers share a single `get_model_from_rotation()` function implemented in `wt-core.sh`. The wrapper sets three globals before sourcing `wt-core.sh` to configure behavior:

| Wrapper | `WT_DEFAULT_CODE` | `WT_DEFAULT_DESIGN` | `WT_AGENT_NAME` |
|---|---|---|---|
| `claude-wt` | `native:claude` | `native:claude` | `claude` |
| `pi-wt` | `claude-sonnet-4-6` | `claude-sonnet-4-6` | `pi` |
| `codex-wt` | `native:codex` | `native:codex` | `codex` |
| `copilot-wt` | `native:copilot` | `native:copilot` | `copilot` |

The function handles:
- Missing config file → use agent-specific defaults
- Empty rotation array → use agent-specific defaults
- Cloud models → verify they are available in ollama; skip and retry if not
- `native:X` where X ≠ `WT_AGENT_NAME` → skip (model not usable by this agent)
- Cross-rotation skip → avoid picking the same model the other mode last used
