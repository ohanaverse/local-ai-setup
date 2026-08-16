# wt-agents reference

Per-agent reference docs for the agents launched by `wt` (via the `*-wt` shims in [`bin/`](../../bin/)). Each file documents how the underlying agent is configured on disk and how it authenticates. The `*-wt` files are now thin shims that forward to `wt` (e.g. `claude-wt` → `wt --agent claude`); the launch logic lives in the Go tool (`cmd/wt/`, `internal/agents/`).

## Scope

These docs cover the agents launched by `claude-wt`, `codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`, and `opencode-wt`.

The launcher contract (flags, rotation, install) now lives in the Go tool — see the root [`CLAUDE.md`](../../CLAUDE.md). These per-agent docs add per-agent context (config files, auth, model selection) that does not fit there.

## Agents

Quick reference (homepage, GitHub, install): [supported-agents.md](supported-agents.md)

| Agent | File | Verification status |
|---|---|---|
| Claude Code | [claude-wt.md](claude-wt.md) | Verified on this machine, 2026-06-01 |
| OpenAI Codex CLI | [codex-wt.md](codex-wt.md) | Convention only (used on other machines) |
| GitHub Copilot CLI | [copilot-wt.md](copilot-wt.md) | Convention only (not installed on this machine) |
| pi-coding-agent | [pi-wt.md](pi-wt.md) | Verified on this machine, 2026-06-01 — includes NYT LiteLLM worked example |
| Antigravity CLI | [agy-wt.md](agy-wt.md) | Convention only (not installed on this machine) |
| OpenCode | [opencode-wt.md](opencode-wt.md) | Verified on this machine, 2026-06-11 |
| Shell command | [shell-wt.md](shell-wt.md) | Verified on this machine, 2026-07-22 |

## Verification convention

Each per-agent file ends with a "Verified on this machine" section. Verified files carry a `Verified on this machine, YYYY-MM-DD` stamp inside that section; convention-only files state non-verification explicitly. Re-verifying a file is a single-file edit, not a rename.

## Common launcher flags

The `wt` tool (and therefore every `*-wt` shim) supports:

| Flag | Description |
|------|-------------|
| `-w <name>`, `--worktree <name>` | Use or create a worktree for the given branch name. For branches with slashes (e.g., `feature/my-branch`, `origin/feature`), the last path component is used as the worktree directory name (`.worktrees/my-branch`, `.worktrees/feature`). Remote tracking branches are checked out as new local branches. |
| `--cwd` | Launch in the current repo root, no picker |
| `--agent <name>` | Pin the agent to launch (defaults to the first configured agent) |
| `--yolo` | Skip permission prompts (agent-specific) |
| `--init` | Seed agent instruction files (AGENTS.md + agent-specific pointer if applicable) and exit |

With no flags, `wt` presents a Bubble Tea TUI picker showing available worktrees and branches, then an agent+model screen.

### Legacy bash flags

The original bash launchers supported `--code`, `--design`, `--native`, `--no-guard`, and `--check-guard`. These are not supported by `wt`: model rotation is now tag-based (the `r` key in the TUI, or `wt rotate <tag>`), and the main guard is managed by `internal/guard`. The bash model-rotation and pi `models.json` auto-sync behavior described in earlier versions of this doc is not yet ported to Go.
