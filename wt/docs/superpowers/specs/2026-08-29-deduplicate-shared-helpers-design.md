# Deduplicate Shared Helpers — Design

> **Status:** draft (fleshed out from placeholder).
> Follow-up simplification PR from the 2026-08-28 `/simplify` review
> (Simplification #3, Reuse #1–2, Altitude #6).

## Goal

Collapse three areas of duplicated helper logic across the `wt` codebase:

1. **Near-identical launch branches in `cmd/wt/main.go`.** `-W`, `--cwd`, and
   outside-repo each repeat the same guard install, model-picker routing,
   TTY check, and `launchFiltered`/`tuiRun` dispatch.
2. **Agent-list building in `internal/tui` and `internal/configeditor`.**
   Both packages independently merge configured agents with registered drivers,
   classify commands vs agents, and produce sorted list items.
3. **Git-root resolution in three places.** `cmd/wt/helpers.go:inGitRepoAt`,
   `internal/worktree/enumerate.go:RepoRoot`, and `internal/tui/app.go:repoRootFor`
   all run `git rev-parse` in slightly different forms.

## Background

### Launch-branch duplication

`cmd/wt/main.go` currently has three launch branches after flag parsing:

- `-W <name>`: resolve repo root, install guard, create worktree, then
  either launch or route to the TUI.
- `--cwd`: resolve repo root, install guard, then either launch or route to
  the TUI.
- Outside a git repo: no guard, then either launch or route to the TUI.

Each branch contains nearly identical copies of:

- `worktree.RepoRoot()` error handling,
- `maybeInstallGuard()`,
- `needsModelPicker(agent, pinned)`,
- `resolveModelForLaunch(...)` single-model short-circuit,
- `pickerNeedsTTYError(agent)` / `stdinTTY()` check,
- `tuiRun(...)` fallback,
- `launchFiltered(...)` call.

Only the resolved path and the guard-install timing differ.

### Agent-list duplication

`internal/tui/agent_picker.go:buildAgentList` and
`internal/configeditor/agents_tab.go:buildAgentsList` both:

- Iterate `cfg.Agents`.
- Iterate `agents.Names()`.
- Use a `seen` map to deduplicate.
- Call `agents.IsCommand(name)`.
- Produce a sorted list.

They differ only in rendering metadata and sort order:

- TUI sorts alphabetically and interleaves agents/commands.
- Config editor sorts commands first, then alphabetically, and displays
  provider/install/configured state.

### Git-root resolution triplication

- `cmd/wt/helpers.go:inGitRepoAt(dir)` runs `git -C <dir> rev-parse --git-dir`.
- `internal/worktree/enumerate.go:RepoRoot()` runs `git rev-parse --show-toplevel`.
- `internal/tui/app.go:repoRootFor(path)` runs `git -C <path> rev-parse --show-toplevel`.

The `worktree` package is the natural owner for both "is this a repo?" and
"what is the repo root?" operations.

## Proposed Design

### 1. Git-root resolution: one helper in `internal/worktree`

Add two functions and refactor the existing `RepoRoot`:

```go
// IsRepo reports whether dir is inside a git repository. It returns false
// for any git error, matching the previous best-effort behavior.
func IsRepo(dir string) bool {
    _, err := RepoRootAt(dir)
    return err == nil
}

// RepoRootAt returns the absolute path of the git repository root that owns
// dir. It uses the same rev-parse invocation as the existing RepoRoot so
// tests and callers see consistent behavior.
func RepoRootAt(dir string) (string, error) {
    out, err := runGit(dir, "rev-parse", "--show-toplevel")
    if err != nil {
        return "", err
    }
    return strings.TrimSpace(string(out)), nil
}

// RepoRoot is shorthand for RepoRootAt(".").
func RepoRoot() (string, error) {
    return RepoRootAt(".")
}
```

`RepoRoot` keeps its current signature for existing callers.
`runGit` already accepts a working directory, so `RepoRootAt` reuses it.

### 2. Agent-list builder: neutral helper in `internal/agents`

Add a public function and a neutral struct:

```go
// AgentListEntry is one row returned by ListEntries. Callers convert it to
// their own list.Item / bubbletea item type.
type AgentListEntry struct {
    Name       string
    Command    bool // true for commands like shell (no model layer)
    Configured bool // present in config.toml
    Installed  bool // binary found on PATH (always true for commands)
    Issue      string // human-readable launch blocker ("" if ready)
}

// ListEntries returns every configured agent and every registered driver
// exactly once, deduplicated and sorted alphabetically by name. Commands are
// classified by agents.IsCommand; non-commands are marked configured/installed
// and receive an issue string if they cannot launch.
func ListEntries(cfg *config.Config) []AgentListEntry {
    seen := map[string]bool{}
    var entries []AgentListEntry

    add := func(name string) {
        if seen[name] {
            return
        }
        seen[name] = true

        ag, err := cfg.AgentByName(name)
        configured := err == nil
        if !configured {
            ag = &config.Agent{Name: name}
        }

        command := IsCommand(name)
        entry := AgentListEntry{
            Name:       name,
            Command:    command,
            Configured: configured,
            Installed:  command || Installed(name),
        }

        if !command && !configured {
            entry.Issue = "not configured — add it to config.toml"
        } else if !command && !entry.Installed {
            entry.Issue = "not installed — install the binary"
        }

        entries = append(entries, entry)
    }

    for _, a := range cfg.Agents {
        add(a.Name)
    }
    for _, n := range Names() {
        add(n)
    }

    sort.Slice(entries, func(i, j int) bool {
        return entries[i].Name < entries[j].Name
    })

    return entries
}
```

### 3. Launch dispatcher: single helper in `cmd/wt/main.go`

Introduce a `runLaunchPath` function and replace the three branches with a
single call site each.

```go
// runLaunchPath resolves the target directory, installs the guard when inside
// a git repo, and either auto-launches or routes to the picker/TUI. prePath is
// the resolved worktree path for -W, the repo root for --cwd, "." for
// outside-repo, and "" when the worktree picker should be shown.
func runLaunchPath(
    cmd *cobra.Command,
    a *app,
    agent, pinned, tags, family string,
    args []string,
    prePath string,
) error {
    // Resolve repo root if we have a path; otherwise stay empty so the TUI
    // shows the worktree picker.
    var root string
    if prePath != "" {
        rr, err := worktree.RepoRootAt(prePath)
        if err != nil {
            // Only error when we were given a concrete path inside a repo.
            // Outside-repo passthrough uses prePath == "." and should keep
            // going even if not in a repo.
            if prePath != "." {
                return fmt.Errorf("not in a git repo: %w", err)
            }
        } else {
            root = rr
        }
    }

    // Install the guard once when inside any git repo.
    if root != "" {
        maybeInstallGuard()
    }

    // Determine the effective launch directory.
    launchPath := prePath
    if launchPath == "" {
        launchPath = root // may still be "" if TUI starts outside a repo
    }

    if needsModelPicker(agent, pinned) {
        if resolved, _, err := resolveModelForLaunch(agent, a.cfg, tags, family, pinned); err == nil && resolved {
            return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, cmd.Flags().Changed("model"), args)
        }
        if !stdinTTY() {
            return pickerNeedsTTYError(agent)
        }
        return tuiRun(yolo(cmd), agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
    }

    return launchFiltered(agent, launchPath, a.cfg, yolo(cmd), tags, family, pinned, cmd.Flags().Changed("model"), args)
}
```

The four call sites in `rootCmd.RunE` become:

```go
// -W
path, err := worktree.EnsureForName(root, name)
if err != nil {
    return err
}
return runLaunchPath(cmd, a, agent, pinned, tags, family, args, path)

// --cwd
return runLaunchPath(cmd, a, agent, pinned, tags, family, args, root)

// outside-repo
return runLaunchPath(cmd, a, agent, pinned, tags, family, args, ".")

// interactive TUI
return runLaunchPath(cmd, a, agent, pinned, tags, family, args, "")
```

Flag parsing still happens in `RunE`; `runLaunchPath` receives resolved
string values.

### 4. Adapter the TUI and configeditor lists to `agents.ListEntries`

`internal/tui/agent_picker.go`:

```go
func buildAgentList(cfg *config.Config) []list.Item {
    entries := agents.ListEntries(cfg)
    items := make([]list.Item, 0, len(entries))
    for _, e := range entries {
        it := agentItem{name: e.Name, command: e.Command}
        if !e.Command {
            it.issue = e.Issue
        }
        items = append(items, it)
    }
    return items
}
```

`internal/configeditor/agents_tab.go`:

```go
func buildAgentsList(theme themes.Theme, width, height int, cfg *config.Config) list.Model {
    entries := agents.ListEntries(cfg)
    items := make([]list.Item, 0, len(entries))
    for _, e := range entries {
        ag, err := cfg.AgentByName(e.Name)
        if err != nil {
            ag = &config.Agent{Name: e.Name}
        }
        it := agentItem{
            agent:      *ag,
            command:    e.Command,
            configured: e.Configured,
            installed:  e.Installed,
            issue:      e.Issue,
        }
        items = append(items, it)
    }

    sort.SliceStable(items, func(i, j int) bool {
        ai := items[i].(agentItem)
        aj := items[j].(agentItem)
        if ai.command != aj.command {
            return ai.command
        }
        return ai.agent.Name < aj.agent.Name
    })

    l := list.New(items, tui.ThemedListDelegate(theme), width, height)
    l.Title = "Agents"
    l.SetShowStatusBar(false)
    return l
}
```

## Guard Install Normalization

The current code has inconsistent guard timing:

- `-W`: `maybeInstallGuard()` before `worktree.EnsureForName`.
- `--cwd`: `maybeInstallGuard()` after resolving root.
- TUI: `maybeInstallGuard()` right before `tuiRun`.
- Outside-repo: none.

After this refactor, the rule is simple:

> If `runLaunchPath` can resolve a git repo root, it installs the guard
> once. Outside a git repo, no guard is installed.

This moves the guard install into the shared helper and removes the
per-branch copies.

## Interface Boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/worktree` | Git repo detection and root resolution (`IsRepo`, `RepoRootAt`, `RepoRoot`). | standard library |
| `internal/agents` | Neutral agent-list builder (`ListEntries`, `AgentListEntry`). | `internal/config` |
| `cmd/wt/main.go` | Flag parsing and one-off handlers; `runLaunchPath` dispatches. | many internal packages |
| `internal/tui` | Converts `agents.ListEntries` to its own `agentItem` and renders. | `internal/agents`, `internal/config` |
| `internal/configeditor` | Converts `agents.ListEntries` to its own `agentItem` and renders. | `internal/agents`, `internal/config` |

## Error Handling

| Scenario | Behavior |
|---|---|
| `IsRepo(dir)` git failure | Returns `false`. Non-repo is the safe assumption. |
| `RepoRootAt(dir)` git failure | Returns the error. Callers translate to user-facing "not in a git repo" messages. |
| `prePath` is `"."` and not in a repo | `runLaunchPath` keeps `launchPath == "."`, skips guard, and proceeds. |
| `prePath` is a concrete worktree path and not in a repo | Returns "not in a git repo" error. |
| `runLaunchPath` called with `prePath == ""` | No root resolution; TUI handles its own repo enumeration. Guard is still installed because TUI will later operate inside the chosen repo — **wait**: this changes behavior. | See open question below. |

## Open Question: Guard Install Timing for the Interactive TUI

Currently the TUI installs the guard *before* the user picks a worktree,
which means the guard installs in whatever repo the user happened to invoke
`wt` from. After moving guard install into `runLaunchPath`, a `prePath == ""`
TUI entry cannot resolve a repo root up front.

Two options:

1. **Install guard at invocation time** (preserve current behavior). Move
   `maybeInstallGuard()` back to the TUI branch only, before `tuiRun`.
2. **Install guard after worktree selection** (deferred). Add a guard-install
   step inside the TUI launch flow once a repo root is known.

Option 1 preserves existing behavior and is safer. The normalized rule can be
phrased as:

> `runLaunchPath` installs the guard whenever a repo root is already known.
> The TUI branch keeps its pre-TUI guard install to preserve the current
> best-effort behavior at the invocation directory.

I recommend **Option 1**. This keeps the guard install in the shared helper
for all concrete-path branches and leaves the TUI branch's install as a
single explicit call before `tuiRun`.

## Testing Plan

### `internal/worktree`

- Extend `TestRepoRoot` to also call `RepoRootAt(".")` and assert equal
  results.
- Add `TestRepoRootAt` for a subdirectory inside a temp repo.
- Add `TestIsRepo` for repo root, subdirectory, and non-repo directory.

### `internal/agents`

- Add `TestListEntries`:
  - Merges configured agents and registered drivers.
  - Deduplicates when an agent is both configured and registered.
  - Classifies commands (`shell`).
  - Marks non-commands as "not configured" when missing from config.
  - Marks non-commands as "not installed" when configured but binary missing.
  - Sorts alphabetically.

### `internal/tui`

- `TestBuildAgentList` (existing) continues to pass; `buildAgentList` now
  wraps `agents.ListEntries`.
- Add `TestBuildAgentListAdapter` to assert the wrapper delegates to
  `agents.ListEntries` and preserves command/issue state.

### `internal/configeditor`

- `TestBuildAgentsList` (existing, implicit) continues to pass.
- Add `TestBuildAgentsListAdapter` to assert the wrapper delegates to
  `agents.ListEntries` and applies commands-first sorting.

### `cmd/wt`

- Add `TestRunLaunchPathCWD` using a stubbed `tuiRun` and `launchFiltered`
  to verify `--cwd` resolves root, installs guard, and launches.
- Add `TestRunLaunchPathWorktree` for `-W`.
- Add `TestRunLaunchPathOutsideRepo` for outside-repo passthrough.
- Add `TestRunLaunchPathPickerNeedsTTY` for no-TTY picker fallback.
- Keep existing `TestInitUsesAgentFlag` and other `launch_test.go` tests.

## Scope

**In:**
- `internal/worktree.IsRepo`, `RepoRootAt`.
- `internal/agents.AgentListEntry`, `ListEntries`.
- `cmd/wt/main.go:runLaunchPath` replacing duplicated branches.
- Adapter refactors in `internal/tui/agent_picker.go` and
  `internal/configeditor/agents_tab.go`.
- Test updates and additions.
- Removal of `cmd/wt/helpers.go:inGitRepo` and `inGitRepoAt`.
- Removal of `internal/tui/app.go:repoRootFor`.

**Out:**
- Changing actual launch behavior or flag semantics.
- Altering the TUI or configeditor visual presentation.
- Making guard installation behavior stricter or deferred.
- Registry/config data model changes.
- Performance optimizations.

## Migration / Rollout

Pure internal refactor. No CLI changes. Existing tests should continue to
pass with the same observable behavior.

## Risks

| Risk | Mitigation |
|---|---|
| `runLaunchPath` becomes a "god function" | Keep it focused: it only resolves path, installs guard, and dispatches. Flag parsing and one-off handlers stay in `RunE`. |
| TUI guard timing changes | Document Option 1 explicitly and keep the pre-TUI `maybeInstallGuard()` call. |
| `RepoRootAt` behaves differently from `inGitRepoAt` | `inGitRepoAt` used `--git-dir`; `RepoRootAt` uses `--show-toplevel`. Both report failure outside a repo, so `IsRepo` is equivalent. Add tests. |
| Configeditor loses its provider display | The adapter still looks up the configured agent to populate `agentItem`. |

## Approval

Design approved: implement Approach A with Option 1 guard timing.
