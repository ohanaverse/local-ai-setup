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
            "command": "/Users/keith/.claude/hooks/welcome.sh"
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

## References

- [Claude Code Hooks](https://code.claude.com/docs/en/hooks)
- [Codex Configuration](https://developers.openai.com/codex/config-reference)
