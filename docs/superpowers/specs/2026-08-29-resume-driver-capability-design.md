# Resume as a Driver Capability — Design

> **Status:** draft (fleshed out from placeholder).
> Follow-up simplification PR from the 2026-08-28 `/simplify` review
> (Altitude #1–2).

## Goal

Remove the agent-name `switch` blocks from session resume handling by making
resume an optional `Driver` capability. Adding a resumable agent should
require changes only in that agent's driver package.

## Background

Currently two shared sites switch on the agent name to decide resume
behavior:

1. `internal/agents/agents.go` appends `--resume <id>` for claude or
   `--session <id>` for opencode in `BuildLaunchCmd`.
2. `internal/session/session.go` dispatches `LatestForAgent` to
   `LatestClaude` or `LatestOpenCode`.

This means adding a resumable agent (e.g. a future agent that also stores
session files) requires editing shared code in two packages.

## Proposed Design

### 1. Add a `Resumer` optional capability

In `internal/agents/agents.go`, add a new optional interface next to the
existing `Syncer`, `ArgSetter`, and `Commanded` capabilities:

```go
// Resumer is an optional Driver capability for agents that support
// resuming a previous session. Drivers that do not implement Resumer are
// assumed to have no session-resume support.
type Resumer interface {
    // ResumeFlag is the CLI flag the agent expects before the session ID.
    // Examples: "--resume" for claude, "--session" for opencode.
    ResumeFlag() string

    // LatestSession returns the most recently modified resumable session
    // for the worktree at path, or nil if none exists. Errors indicate
    // a lookup failure (e.g. unreadable session directory); callers decide
    // whether to surface or swallow them.
    LatestSession(path string) (*session.Session, error)
}
```

### 2. Implement `Resumer` in resumable drivers

`internal/agents/claude.go`:

```go
func (claudeDriver) ResumeFlag() string { return "--resume" }

func (claudeDriver) LatestSession(path string) (*session.Session, error) {
    dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", session.Slug(path))
    return session.LatestByExt(dir, ".jsonl", func(f os.FileInfo) string {
        return strings.TrimSuffix(f.Name(), ".jsonl")
    })
}
```

`internal/agents/opencode.go`:

```go
func (opencodeDriver) ResumeFlag() string { return "--session" }

func (opencodeDriver) LatestSession(path string) (*session.Session, error) {
    projectID, err := session.OpenCodeProjectID(path)
    if err != nil {
        return nil, err
    }
    dir := filepath.Join(os.Getenv("HOME"), ".local", "share", "opencode",
        "storage", "session", projectID)
    return session.LatestByExt(dir, ".json", func(f os.FileInfo) string {
        return f.Name()
    })
}
```

### 3. Update `BuildLaunchCmd`

Replace the agent-name `switch` in `internal/agents/agents.go`:

```go
if sess != nil && !m.Native {
    if r, ok := d.(Resumer); ok {
        cmd.Args = append(cmd.Args, r.ResumeFlag(), sess.ID)
    }
}
```

The native-model guard is preserved: resuming a native model would restore the
session's stored model and silently override the user's "native" choice.

### 4. Update launch paths to look up sessions via the driver

#### Non-TUI path (`cmd/wt/launch.go`)

In `buildCommandForModel`:

```go
var sess *session.Session
if !m.Native {
    if r, ok := agents.ByName(agent).(agents.Resumer); ok {
        sess, _ = r.LatestSession(worktreePath)
    }
}
```

The existing error-swallowing semantics are preserved.

#### TUI path (`internal/tui/app.go`)

In `proceedToLaunch`:

```go
var sess *session.Session
if !highlighted.model.Native {
    if r, ok := agents.ByName(m.agent).(agents.Resumer); ok {
        var err error
        sess, err = r.LatestSession(m.selectedPath)
        if err != nil {
            m.status = "session check failed: " + err.Error()
            return m, nil
        }
    }
}
```

If the driver does not implement `Resumer`, `sess` stays nil and the resume
prompt is skipped entirely.

#### Debug helper (`cmd/wt/main.go`)

`--debug-session <agent>` should resolve the driver and, if it implements
`Resumer`, call `LatestSession`. If it does not, print that the agent has no
resume support. This replaces the current `session.LatestForAgent(agent, root)`
call.

### 5. Refactor `internal/session`

The `session` package keeps the shared value object and helpers but loses
per-agent dispatch:

- **Keep:** `Session`, `Slug`, `RelativeTime`.
- **Keep (exported):** `LatestByExt` (renamed from `latestByExt`) so
  drivers can reuse it.
- **Keep (exported):** `OpenCodeProjectID` because opencode's driver needs it.
- **Remove:** `LatestForAgent`, `LatestClaude`, `LatestOpenCode`.

After the change, no code in `internal/session` knows about specific agent
names.

## Interface Boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/agents` | Defines `Resumer`; `BuildLaunchCmd` appends flag when session + non-native + capability present. | `config`, `session`, driver registry |
| `internal/agents/claude` | Knows claude's `--resume` flag and session-file layout. | `session` helpers |
| `internal/agents/opencode` | Knows opencode's `--session` flag and project-id/session layout. | `session` helpers |
| `internal/session` | Provides `Session` value type, slug helper, relative-time rendering, and generic latest-by-extension lookup. | standard library only |
| `internal/tui` | Checks `Resumer` capability before showing resume prompt. | `agents`, `session` |
| `cmd/wt` | Checks `Resumer` capability for non-TUI launch and `--debug-session`. | `agents`, `session` |

## Error Handling

- **Lookup error in TUI:** surface as status message and block launch (same
  as today).
- **Lookup error in non-TUI launch:** swallow and proceed without a resume
  flag (same as today's `sess, _ = session.LatestForAgent(...)`).
- **Lookup error in `--debug-session`:** return the error to the user.
- **Driver lacks `Resumer`:** no lookup, no flag, no prompt.
- **Native model:** resume skipped regardless of capability.

## Testing Plan

1. **Capability contract test** in `internal/agents/agents_test.go`: assert
   that claude and opencode drivers implement `Resumer`, and that codex,
   copilot, pi, agy, and shell do not.
2. **Behavioral tests** in `internal/tui/agent_model_test.go` and
   `cmd/wt/launch_test.go`: keep the same assertions (e.g. claude command
   includes `--resume <id>`, opencode includes `--session <id>`, others do
   not), but the setup writes session files to the real locations so the
   driver's `LatestSession` finds them.
3. **Session-helper tests** in `internal/session/session_test.go`: remove
   `LatestForAgent`/`LatestClaude`/`LatestOpenCode` tests; keep tests for
   `Slug`, `RelativeTime`, and `LatestByExt`.
4. **Driver-specific session tests** in `internal/agents/claude_test.go` and
   `internal/agents/opencode_test.go` (or new files): verify each driver's
   `LatestSession` picks the newest file and returns nil when no sessions
   exist.

## Scope

**In:**
- Adding `Resumer` optional capability.
- Moving resume flag + session lookup into claude and opencode drivers.
- Removing agent-name `switch` from `BuildLaunchCmd` and launch paths.
- Refactoring `internal/session` to remove per-agent dispatch.
- Updating tests to match the new boundaries.

**Out:**
- Changing session storage format.
- Adding resume support to new agents.
- Changing the native-model resume-skip logic.
- Refactoring other shared helpers or driver knowledge (other placeholder
  specs).

## Migration / Rollout

This is a pure internal refactor with no user-facing CLI changes. The only
observable change is that `--debug-session <agent>` for a non-resumable agent
will now report "no resume support" instead of returning nil silently. This is
acceptable.

## Risks

| Risk | Mitigation |
|---|---|
| Tests that currently rely on `session.LatestForAgent` break. | Update tests as part of the PR; no production callers outside tests. |
| A future agent needs shared session logic not captured by `LatestByExt`. | `LatestByExt` remains internal; drivers may implement `LatestSession` however they need. |
| Native-model skip accidentally lost. | Keep the `!m.Native` guard in both the lookup layer and `BuildLaunchCmd`. |

## Approval

Design approved: implement Option 1 (full `Resumer` capability on driver,
lookup moved into driver package).
