# wt model ref counts

## Summary

Track how many live `wt` sessions are currently using each model, and show
that count as a single-digit column (blank when unused) in the first column
of the model picker. A session is identified by the `wt` process's own pid;
a sweep on each launch drops pids that are no longer alive, which is what
keeps the counts accurate after crashed or killed sessions.

## Background

Local MLX/GGUF models share Apple Silicon GPU/RAM and distort each other's
benchmarks, so the rule is "only one local model loaded at a time." The
model picker currently shows no signal about which models are already in
use by other concurrent `wt` sessions. This feature adds an advisory,
single-digit "in use" count so a user can see — at a glance — that a model
is already being driven by another terminal before launching a second
instance.

The counts are **display-only today** (no launch gating or warning), but the
design exposes a clean query API (`Counts`) so future behavior — warnings,
gating, or otherwise — can consume the same live-session state without
re-parsing anything.

## Design

### 1. New package: `internal/refcount`

A dedicated package mirroring `internal/usage`'s store/lock/atomic-write
shape. Separation is deliberate: `usage` is 30-day-retained *history*;
`refcount` is *live-session state* with opposite retention semantics.

- **State file** `~/.config/agent-wt/refcount.jsonl` — one entry per line:
  `{"pid":12345,"model_id":"ollama/qwen3.8:27b-mlx"}`. No retention window:
  an entry lives until its pid is found dead by a sweep.
- **`Store` interface:**
  - `Record(pid int, modelID string) error` — append one entry.
  - `Sweep() error` — read all entries, drop dead pids, rewrite.
  - `Counts(modelIDs []string) map[string]int` — live count per model,
    zero-filling every requested ID (missing → 0 → blank column).
- **Pid liveness seam** `var pidAlive = func(pid int) bool` wrapping
  `syscall.Kill(pid, 0)`: `nil` or `EPERM` → alive; `ESRCH` → dead. The seam
  lets tests inject live/dead without spawning real processes (same shape as
  `newUsageStore`/`flushTTY`).
- **Concurrency:** sidecar `refcount.jsonl.lock` + POSIX `flock`, copied from
  `usage.Record`, so `Sweep`'s read-modify-write and `Record`'s append from
  concurrent `wt` processes serialize and neither loses an entry.

### 2. Sweep on launch (fixes counts)

Runs once at the top of `runLaunchPath` in `cmd/wt/main.go` — the single
funnel every launch goes through (TUI, non-TUI, `-W`, `--cwd`, outside-repo,
**and `shell-wt`**). Best-effort: errors are ignored (a corrupt state file
means stale counts, not a blocked launch).

Placement matters: the sweep must run for `shell-wt` (the issue requires it
to fix counts) even though `shell` never builds a picker. A lazy sweep inside
`Counts()` would never fire for `shell-wt`, so the sweep is an explicit
startup call rather than hidden in the read path.

### 3. Record on launch (increments)

Recorded at the existing single commit point in each launch path, right next
to `rotation.Record`, using `os.Getpid()` + the launched model's `ID`:

- **TUI:** `launchAndRecord` (`internal/tui/app.go`).
- **Non-TUI:** `launchFilteredImpl` (`cmd/wt/launch.go`).

**Command agents (`shell`) never record** — and need no explicit guard.
They take the early-return path (`buildFilteredCmd` → `runAgentCmd` with
`config.Model{}`) in the non-TUI path, and `launchCommand` → `runAndWaitCmd`
directly (bypassing `launchAndRecord`) in the TUI. The structure already
excludes them from the record point.

Best-effort, mirroring `rotation.Record`'s "print a note but launch anyway"
behavior.

### 4. Display in the picker

`buildModelItems` (`internal/tui/model_list.go`) gains a `refcount.Store`
param (mirroring the existing `usage.Store` param), calls
`.Counts(catalogIDs)` in one pass, and sets a new `ref int` field on each
`modelItem`. `Title()` prepends a 2-char ref column **before** the existing
rotation marker; both stay out of `.line` so `FilterValue` fuzzy-matching is
unaffected:

| state | rendered prefix |
|-------|-----------------|
| in use + marked | `3 > family …` |
| in use + unmarked | `3   family …` |
| unused + unmarked | `    family …` |

The count clamps at `9` (any count ≥ 9 renders `9`); a single digit keeps the
column width fixed.

## Scope

- New `internal/refcount` package + state file.
- Sweep call in `cmd/wt/main.go`; record calls in `internal/tui/app.go` and
  `cmd/wt/launch.go`; display in `internal/tui/model_list.go`.
- **No** launch gating, warnings, or behavior changes — display-only.
- **No** changes to `usage`, `rotation`, `survey`, config, or the registry.

## Known limitation

Pid liveness is advisory: if the OS recycles a dead pid for an unrelated
process before the next sweep, that entry is counted as alive until a later
sweep. Extremely unlikely within a session window, and the column is advisory
— accepted rather than adding a start-time/ppid check (YAGNI).

## Testing

- `internal/refcount` unit tests: `Record`/`Sweep`/`Counts` via a
  `NewStoreAt(dir)` seam and injected `pidAlive` (live and dead pids).
- `buildModelItems` tests: ref column renders blank for 0, a digit for 1–9,
  and clamps at `9` for larger counts.
- Record-point tests: a command agent does **not** record; a model-driven
  launch does.
- Sweep-on-launch test: `runLaunchPath` triggers a sweep even for `shell-wt`.
- Existing suite (`go test ./...`, `go vet ./...`) continues to pass.
