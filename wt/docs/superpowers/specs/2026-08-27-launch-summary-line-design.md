# Launch Summary Line

## Goal

After an agent or command subprocess exits, `wt` prints a single line to stdout
summarizing the run: the agent/command name, the model used, and the subprocess
wall-clock duration.

## Non-goals

- No model lifecycle management (reference counting, shutting down local models).
- No usage/stats capture beyond the existing rotation/usage state.
- No cleanup steps.
- No change to exit-code propagation or resume behavior.

## Behavior

The summary line is printed **always** — on success and on non-zero exit — after
the subprocess has finished. It goes to **stdout**.

Format:

```
wt: <agent> · <model-id> · <duration>
```

- `<agent>` — the `-A` value (`claude`, `codex`, `copilot`, `pi`, `agy`,
  `opencode`, `shell`, …).
- `<model-id>` — the model's full `ID` (e.g. `claude/sonnet`,
  `ollama/gemma4:9b`). Omitted for command agents (`shell`), which have no
  model layer.
- `<duration>` — subprocess wall-clock time, rounded to seconds when ≥1s,
  otherwise shown in milliseconds (e.g. `850ms`).

Examples:

```
wt: claude · claude/sonnet · 3m42s
wt: shell · 1s
```

## Implementation

Two launch paths both already block on the subprocess via `cmd.Run()`; each
gets a timing wrapper and a print after the run. A single shared formatter
prevents the two paths from drifting.

### Shared formatter

Add a small helper in `internal/agents` (already imported by both `cmd/wt` and
`internal/tui`):

```go
// Summary formats the post-run summary line for an agent/command run.
func Summary(agent string, m config.Model, d time.Duration) string
```

- When `m.ID == ""` (command agent), omit the model segment.
- Duration formatting: `d.Round(time.Second)` when `d >= time.Second`, else
  `d.Round(time.Millisecond)`.

### Non-TUI path (`cmd/wt/launch.go`)

`runAgentCmd` currently takes only `*exec.Cmd`. Change it to accept the agent
name and model, measure `time.Now()` around `cmd.Run()`, and print the summary
after the run returns (before propagating the exit code).

`launchFilteredImpl` already has `agent` and `m` in scope at both call sites
(command-agent branch and model branch), so it passes them through.

### TUI path (`internal/tui/launch.go`)

`runAndWaitCmd` wraps `cmd.Run()` in a closure that releases/restores the
terminal. Capture `agent` and `model` in the closure and print the summary
**after** `RestoreTerminal()` so the line lands on a clean line, not inside the
alt-screen.

The call sites (`launchAndRecord` for models, `launchCommand` for commands)
already have `m.agent` / `m.launchModel` in scope and pass them through.

## Error handling

The summary is printed regardless of the subprocess exit code. A non-zero exit
still propagates exactly as today (non-TUI: `os.Exit(ee.ExitCode())`; TUI:
`launchDoneMsg{err}` → status + `tea.Quit`). The summary print itself is
best-effort and never changes the exit path.

## Testing

- Formatter unit tests: model agent (full ID + duration), command agent (no
  model segment), sub-second vs. ≥1s duration rounding.
- Non-TUI: assert the summary line is emitted on both success and non-zero exit.
- TUI: assert the summary is emitted after terminal restore (clean line), on
  both success and failure.
