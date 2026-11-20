# Agent Configuration

Configuration systems for Claude Code and Codex CLI.

## Claude Code Configuration

### Configuration Locations

- **Global config:** `~/.claude/`
- **Settings:** `~/.claude/settings.json`
- **Hooks:** `~/.claude/hooks/`
- **CLAUDE.md:** `~/.claude/CLAUDE.md` (deployed from agent-toolkit)

### Settings.json Format

#### Hooks

Claude Code supports several hook types:

- `SessionStart` - Runs when a new session begins
- `UserPromptSubmit` - Runs when a user submits a prompt
- `PostToolUse` - Runs after tool execution

Hook structure (must be wrapped in `hooks` array):

```json
{
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "/Users/user_name/.claude/hooks/welcome.sh"
          }
        ]
      }
    ]
  }
}
```

Hook output format:

```json
{"systemMessage": "Your message here"}
```

### Environment Filtering

Components support environment-specific content using markers:

```markdown
<!-- ENV: work -->
Work-specific content here
<!-- /ENV -->
```

Or in shell files:

```bash
# <!-- ENV: work -->
WORK_SETTING=1
# <!-- /ENV -->
```

## Codex CLI Configuration

### Configuration Locations

- **Global config:** `~/.codex/`
- **AGENTS.md:** `~/.codex/AGENTS.md` (deployed from agent-toolkit)

### Sandbox Network Access

Codex CLI runs in a sandboxed environment with restricted network access. Commands requiring external network calls need user approval.

### Approved Prefix Rules

Prefix rules persist across sessions:

| Prefix Rule | Allows |
|-------------|--------|
| `["gh", "pr"]` | `gh pr view`, `gh pr create`, `gh pr checkout` |
| `["git", "push"]` | `git push origin`, `git push --set-upstream` |
| `["git", "fetch"]` | `git fetch origin`, `git fetch --all` |

### Environment Filtering

Same marker system as Claude Code:

```markdown
<!-- ENV: work -->
Work-specific content here
<!-- /ENV -->
```

## Deployment

Use `agent-toolkit` to deploy configurations:

```bash
agent-toolkit deploy home              # Deploy home configuration
agent-toolkit deploy work              # Deploy work configuration
agent-toolkit deploy home --dry-run    # Preview changes
```

### What Gets Deployed

1. **Markdown components** from `components/` → `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md`
2. **Static files** from `components/<name>/static/` → `~/` (e.g., hooks)
3. **Hook registration** - Automatically updates `~/.claude/settings.json` for Claude

## wt Agent Launching

`wt` (the worktree/agent launcher, `wt/cmd/wt-session`) is a separate tool from the Claude Code/Codex configuration this doc otherwise covers. A session reads only its own `~/.config/agent-wt/config.toml`, never a model registry: every model it shows or launches comes from the model server (`wt-model serve`), chosen by the mode under `[model-server]` (below; [wt-session.md](wt-session.md)). Bare `wt config` edits that file in two tabs, Agents and Mode ([wt-config.md](wt-config.md)). The model registry, `~/.config/local-ai/registry.toml`, is the host's and belongs to `wt-model`: `wt-model model init` creates it and its provider rows, models are added, edited and removed with `wt-model model add|edit|rm` or on the Models tab of `wt-model model` (each syncs the LiteLLM routes itself), and `wt-model setup` sets up a local server on this machine ([wt-model-server.md](wt-model-server.md)).

### Model server setting

`[model-server]` in `~/.config/agent-wt/config.toml` (`mode`, `url`, `token_file`) names the model server a session uses. It is written by the Mode tab of `wt config` (native, remote, or local as a pointer with the default url and token file) or by `wt-model setup` (which sets up a local server on this machine), each as a plan you confirm, and stays hand-editable. The `wt config` Agents tab's save keeps it as it is. See [wt-session.md](wt-session.md) and [wt-config.md](wt-config.md).

### Exit prompt setting

`[launch] exit_prompt` in `~/.config/agent-wt/config.toml` is a legacy key, tolerated and ignored: the exit prompt it once controlled is removed (nothing is offered when an agent exits; cleanup is user-driven with `wt worktrees` and `wt remove`), no value is validated or consulted, and any value survives wt's saves. A non-empty value prints one note per process:

```
wt: note: config.toml: [launch] exit_prompt is no longer used and is ignored
```

The `wt config` editor neither shows nor writes the `[launch]` table. See `wt/docs/wt-agents/README.md#post-exit-order`.

## References

- [Claude Code Hooks](https://code.claude.com/docs/en/hooks)
- [Codex Configuration](https://developers.openai.com/codex/config-reference)
