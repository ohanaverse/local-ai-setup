# wt Flow Cleanup — Design Spec

**Status:** Draft (pending user review)
**Date:** 2026-08-18
**Approach:** A — Incremental (5 PRs)

## Summary

Reshape the `wt` CLI around an explicit three-input mental model:

1. **Where to run?** Directory (worktree/branch picker, `--cwd`, or `-W <name>`).
2. **What runs?** Agent or command (picker, `-A <name>`).
3. **Which LLM?** Model (picker, `-M <provider>/<name>`, filtered by `-T`/`-F`).

Today `wt` mixes these concerns: agent selection is implicit (config default), model selection rides on the agent+model screen, and there is no way to filter models by tag or family at the CLI. This spec makes each input explicit and independently controllable, splits the agent+command decision into its own picker, and makes model rotation operate on the **eligible list** (post-filter) rather than the tag group.

The work is delivered in five small, independently mergeable PRs. Each PR keeps existing behavior for users who don't adopt the new flags.

## Background

The current `wt` (Go, `cmd/wt`) launched an agent via:

```bash
wt                          # interactive TUI: pick worktree → pick model
wt -w my-feature            # skip TUI; create/reuse worktree
wt --cwd                    # launch in current repo root
wt --agent codex            # override default agent
```

The model picker showed every model in the agent's `default_tag` group and rotated through that group on each launch. There was no way to:

- Pin an agent/command at the CLI (today `-A` doesn't exist; `--agent` does).
- Pin a model (`-M` doesn't exist).
- Filter models by tag (`-T` doesn't exist).
- Filter models by family (`-F` doesn't exist).
- Distinguish commands (no model) from agents in the picker.

## Goals

- Each of the three inputs has its own CLI flag(s) and its own picker screen.
- The flag surface is small, orthogonal, and discoverable.
- Model rotation respects filter choices: rotating `-T code` cycles through code-tagged models only.
- Migration from the current flag surface is clear and one-way (hard error on the removed `-w`).

## Non-goals

- Live model discovery stays in the `wt models` subcommand. The picker sources its model list from `config.toml` only.
- No new agent drivers; `shell` remains the only command.
- No changes to session resume, ollama availability check, or the guard warning.
- No new in-picker filter UI (keybinding to change `-T`/`-F` interactively); flags only for now.

## User-facing changes

### Flag surface

| Flag | Type | Effect |
|---|---|---|
| `-W`, `--worktree` | string | Use/create worktree for the named branch; skip the directory picker |
| `--cwd` | bool | Use the current repo root; skip the directory picker |
| `-A`, `--agent` | string | Pin the agent or command |
| `-M`, `--model` | string | Pin the model as `<provider>/<name>`; verified against the eligible list |
| `-T`, `--tags` | string | Comma-delimited tags to include (OR within flag) |
| `-F`, `--family` | string | Comma-delimited model families to include (OR within flag) |
| `--yolo` | bool | Prepend skip-permissions flag (existing) |
| `--version` | bool | Print version (existing) |
| `--init` | bool | Seed agent instructions (existing) |
| `--check-guard`, `--no-guard` | bool | Guard management (existing) |
| `--debug-worktrees`, `--debug-session` | string | Test helpers (existing, hidden) |

**Removed:** `-w` short flag for `--worktree`. Invoking it is a hard error: `wt: error: -w is removed; use -W or --worktree`.

**Unchanged:** subcommands `wt models`, `wt agents`, `wt rotate <tag>`.

### Resolution order

When multiple flags cover the same input:

- Directory: `-W` > `--cwd` > picker.
- Agent: `-A` > picker.
- Model: `-M` (if in eligible list) > picker (with eligible-list rotation).

If a flag is given and the corresponding screen would show 1+ options, the screen is skipped.

### Picker-skip conditions for the worktree picker

The worktree picker is shown only when none of the following hold:

1. `-W <name>` was given.
2. `--cwd` was given.
3. The current directory is not inside a git repo.

In every other case — including when launched from inside a worktree — the picker is shown.

### Picker content invariants

- The worktree picker always presents **at least one pickable row** (the repo's default branch as a worktree entry if checked out, or as a bare branch row otherwise), plus the `+ New worktree…` sentinel. The picker is never empty.
- The worktree picker marks the entry whose path matches the launch directory with `(current)` in its description; the default branch row gets `(default)`.
- The worktree picker renders rows in this order:
  1. **`+ New worktree…`** sentinel (always first).
  2. **Local branches and worktrees**, alphabetical by branch name (with slash-prefixed remote names stripped for sort, so `feature/foo` sorts under `feature/`). Branches already checked out in a worktree are omitted from the local-branches group so the picker never shows duplicates.
  3. **Remote-only branches**, alphabetical by short name (the part after the remote prefix), after a separator so users can tell where locals end and remotes begin. Remote branches shadowed by a local branch are omitted (existing behavior).
- The agent+command picker always lists every configured agent plus every registered command.
- The model picker shows every model in the eligible list (post-filter); if exactly one model is eligible, the picker is skipped.

### Model filtering semantics

- Provider filter is hard: a model is eligible only if its `provider_id` is in the agent's `supported_providers`.
- `-T code,design`: model must have **at least one** listed tag (OR within flag).
- `-F gemma4,claude`: model's `family` must equal **one of** the listed (OR within flag).
- When both `-T` and `-F` are non-empty: tag set and family set are AND-combined.

### Rotation semantics

- Rotation state is scoped to a `(agent, tag, family)` slot.
- The state file is `rotation-<agent>-<tag>-<family>.state`, with empty values normalized to `-`.
- Rotation advances through the **eligible list** (the picker snapshot, i.e. models that pass the agent + tag + family filters), starting from the last-launched model, wrapping to the first eligible model after it.
- If the last-launched model is no longer in the eligible list, the cursor lands on the first eligible model.
- Backward compatibility: when the new per-slot file doesn't exist, the rotation reader falls back to the legacy `rotation-<tag>.state` file (read-only). New writes go to the per-slot file; legacy files are left in place.

## Architecture

### Package layout

- `internal/config` gains:
  - `(c *Config) EligibleModels(agentName, tags, family string) ([]Model, error)` — the single source of truth for "which models can this user launch right now".
  - `parseFilterList(s string) []string` — comma-split helper.

- `internal/agents` gains:
  - `IsCommand(name string) bool` — distinguishes commands (no model) from agents.
  - An optional `Command` interface (or `IsCommand() bool` method on `Driver`) with a default `false` implementation for existing drivers. The `shell` driver returns `true`; future command drivers do the same. This avoids breaking every existing driver implementation.

- `internal/rotation` is refactored:
  - `Slot{Agent, Tag, Family string}` replaces the bare `tag string` constructor argument.
  - `New(slot Slot, models []config.Model, stateDir string) *Rotation`.
  - State-file naming: `StateFile(stateDir, slot)`.
  - Backward-compat read: if the per-slot file is missing, read `rotation-<tag>.state` (legacy); if that's also missing, return `(zero, false)`.

- `internal/tui` gains:
  - `phaseAgent` — new picker phase between `phaseList` and `phaseModel`.
  - `phaseAgent` enter on a command item → launch directly (skip `phaseModel`, ollama check, session resume, rotation).
  - The `d` key handler in `phaseModel` is removed (replaced by `-T` at the CLI).

- `internal/worktree` changes:
  - `Enumerate` returns three ordered groups: worktrees, local branches, remote branches. The picker layer renders them in this order with a separator between locals and remotes. Branches already checked out in a worktree are omitted from the local-branches group (no duplicates). Remote branches shadowed by locals are omitted.
  - Default-branch detection (`DefaultBranch`) is unchanged.

- `cmd/wt/main.go`:
  - `-w` short flag is removed; `--worktree` keeps the lowercase long form.
  - `-A` is added as the short form for `--agent`.
  - `-M`, `-T`, `-F` are added.
  - The `RunE` handler short-circuits to the non-TUI launch path when `-W`, `--cwd`, or non-git-repo conditions hold.

- `cmd/wt/launch.go`:
  - `launch` and `launchDirect` accept the new filter flags and resolve the eligible model list before constructing the agent command.
  - `-M` is verified against the eligible list; error if missing.
  - `-M` is ignored with a stderr note when `-A` resolves to a command.

### Three-input flow (TUI)

```
phaseList
  ↓ pick worktree/branch
selectedEntryMsg
  ↓ resolve cwd + path
phaseAgent
  ↓ pick agent or command
  ├─ command → launch (skip model layer)
  └─ agent
      ↓
phaseModel
  ↓ pick eligible model
proceedToLaunch
  ↓
phaseResume (if prior session)
  ↓
launch + record rotation
```

### Three-input flow (non-TUI)

```
RunE:
  1. Handle --version, --init, --check-guard, --no-guard, --debug-* (early returns).
  2. Resolve directory:
     -W <name>   → worktree.EnsureForName(root, name)
     --cwd       → root
     !inGitRepo() → .
     else        → TUI
  3. Resolve agent: -A > cfg.DefaultAgent().
  4. If command: launch (skip model layer); ignore -M with stderr note.
  5. Compute eligible := cfg.EligibleModels(agent, -T, -F).
  6. If -M given: verify in eligible; error if missing.
  7. If len(eligible) == 0: error "no models match ...".
  8. If len(eligible) == 1: use it; no rotation.
  9. Else: rotate through eligible, starting from slot's last-launched.
  10. Ollama check (existing).
  11. Session resume (existing).
  12. Launch.
```

## PR-by-PR breakdown

### PR 1 — Flag surface + eligible-list function + non-TUI path

**Files touched:**
- `cmd/wt/main.go` — register `-W`, `-A`, `-M`, `-T`, `-F`; remove `-w`; error on `-w`.
- `cmd/wt/launch.go` — wire filter flags into the launch path.
- `cmd/wt/helpers.go` — accept filter flags.
- `internal/config/config.go` — add `EligibleModels` and `parseFilterList`.

**New tests:**
- `parseFilterList`: empty, single, multi, whitespace, trailing/leading comma.
- `EligibleModels`: agent with multiple providers; tag filter (single, multi, none); family filter; combined filters (AND); agent not found; agent with no supported models.
- Flag parsing: `-W` resolves worktree; `--cwd` resolves cwd; `-A` overrides default; `-M` verified; `-M` not in eligible errors; `-T`/`-F` parsed as comma lists.
- `wt -w foo` errors out with a clear migration message.

**Behavior preserved:** TUI is unchanged. `wt rotate <tag>` still uses the tag-only state file. Existing users who don't adopt the new flags see no change.

### PR 2 — Agent+command picker screen

**Files touched:**
- `internal/agents/<name>.go` — add `IsCommand() bool` method (default `false`) to `Driver` interface; `shell` driver returns `true`.
- `internal/agents/registry.go` — `IsCommand(name)` lookup.
- `internal/tui/app.go` — new `phaseAgent` in the `phase` enum; `selectedEntryMsg` handler transitions to `phaseAgent` instead of resolving the agent synchronously.
- `internal/tui/agent_picker.go` — new file: `agentItem`, `buildAgentList`, `phaseAgentView`.
- `internal/tui/launch.go` — `launchShell` already exists; extend the picker to handle commands generically.

**New tests:**
- `IsCommand`: returns true for `shell`; false for `claude`, `codex`, etc.
- `phaseAgent` enter with command item → launches directly (no model screen transition, no rotation recorded).
- `phaseAgent` enter with agent item → transitions to `phaseModel`.
- `phaseAgent` `esc` → returns to `phaseList`.

### PR 3 — Filter-aware model picker + rotation scoping

**Files touched:**
- `internal/config/config.go` — `EligibleModels` already exists from PR 1; this PR adds `ModelsForAgentAndFilters` aliases if needed.
- `internal/rotation/rotation.go` — replace `tag string` with `Slot`; new state-file naming; backward-compat read.
- `internal/tui/app.go` — `phaseModel` honors `-T`/`-F` filters; removes the `d` key handler.
- `internal/tui/model_list.go` — `phaseModelView` header shows active filters.
- `cmd/wt/launch.go` — pass filters through to the rotation slot.

**Note:** PR 3 touches three subsystems (`rotation`, TUI, non-TUI launch). If the rotation refactor grows during implementation (e.g. the state-file migration needs its own test harness), it can be split into PR 3a (rotation refactor only, TUI/launch still use the old API) and PR 3b (TUI/launch consume the new API). Decision deferred to implementation.

**New tests:**
- `SlotFromFlags`: empty values normalized; special chars escaped.
- `Rotation.New(slot, ...)`: state file path correct; back-compat reads old `rotation-<tag>.state` when new file is missing.
- `LastLaunched`: returns `false` when only old file exists but the model isn't in the snapshot.
- `RecordLaunch`: writes to new per-slot file; old file untouched.
- Picker: cursor positioned on the model after the last-launched in the eligible list.
- Picker: when eligible list is a single model, no picker is shown.

### PR 4 — Worktree picker always-full view + default-branch guarantee

**Files touched:**
- `internal/worktree/enumerate.go` — `Enumerate` returns three ordered groups (worktrees, local branches, remote branches) so the picker layer can render them in order without re-sorting. Branches already checked out in a worktree are omitted from the local-branches group.
- `internal/worktree/enumerate_test.go` — update existing tests; add default-branch-present cases; add ordering tests.
- `internal/tui/worktree_list.go` — `entryItem.Description` includes `(default)` for default-branch rows; `(current)` for the entry matching the launch directory; `buildList` renders `+ New worktree…` first, then the local group (alphabetical by branch), then the remote group (alphabetical by short name) with a separator between them.
- `internal/tui/app.go` — keep the default-branch warning as a conditional title change (only when `len(entries) == 1 && isCurrentOnDefaultBranch(...)`); no other picker-title logic changes.
- `cmd/wt/main.go` — `wt -W <name>` and `wt --cwd` short-circuit to non-TUI launch path.

**New tests:**
- `Enumerate`: default branch appears as a `TypeBranch` row when not checked out anywhere.
- `Enumerate`: default branch does NOT appear as a `TypeBranch` row when already checked out in a worktree (no duplicates).
- `Enumerate`: from inside a worktree, the picker list contains all worktrees plus any bare branches that are not checked out elsewhere.
- `Enumerate`: results are returned as three groups (worktrees, local branches, remote branches) so the picker layer can preserve the ordering.
- Picker ordering test: `+ New worktree…` is row 0; local group rows are alphabetical; remote group follows a separator and is alphabetical by short name; remote branches shadowed by locals are omitted.
- `wt -W <branch>` where `<branch>` is checked out in the main checkout → launches in main path; no new worktree created.
- Picker row description for current worktree includes `(current)`; default branch rows include `(default)`.
- Picker-skip conditions: `-W` skips picker; `--cwd` skips picker; outside git repo skips picker.

### PR 5 — Docs + deprecation polish

**Files touched:**
- `docs/configuration.md` — new flag surface, filter examples.
- `docs/wt-cli.md` (new) — three-input mental model walkthrough with examples.
- `docs/wt-agents/<agent>.md` — note any agent-specific flag interactions.
- `README.md` — flag table refresh; migration callout.
- `CHANGELOG.md` — entry under "Unreleased".

**No new tests.** Docs build/lint, broken-link check.

## Error handling

| Condition | Behavior |
|---|---|
| `-w` invoked | `wt: error: -w is removed; use -W or --worktree`, exit 1 |
| `-M claude/opus` not in eligible list for chosen agent | `wt: error: model "claude/opus" is not in the eligible list for agent "X"`, exit 1 |
| `-M claude/default` not in config | `wt: error: model "claude/default" not found in config`, exit 1 |
| `-T foo,bar` matches no models | `wt: error: no models match agent "X" with tags [foo, bar]`, exit 1 (non-TUI); TUI surfaces in `status` |
| `-F gemma4` matches no models | same as above with families |
| `-A shell` with `-M any/model` | stderr note: `wt: -M ignored for command "shell"` |
| `wt` with no configured agents and no commands | `status: "no agents or commands registered — edit config.toml"` |
| `phaseModel` with empty eligible list | `status: "no models for agent %q ..."` (existing message) |

## Test plan summary

| PR | New tests | Updated tests |
|---|---|---|
| 1 | `parseFilterList`, `EligibleModels` matrix, `-w` error, flag parsing | existing flag-parsing tests for `-w` expect error path |
| 2 | `IsCommand`, `phaseAgent` enter transitions, `esc` pops back | `selectedEntryMsg` handler test |
| 3 | `SlotFromFlags`, per-slot state file, back-compat read, picker cursor after last-launched in eligible list, `d` key removal | existing rotation tests parameterized over `Slot` |
| 4 | default-branch always present, picker-skip conditions, `(current)`/`(default)` markers, picker ordering (sentinel → locals alphabetical → remotes alphabetical), "stay on main" behavior | existing `Enumerate` tests updated |
| 5 | (no new tests — docs only) | docs build/lint, broken-link check |

Each new test file gets a top-level `//` comment explaining what it tests and why (per the project's what/why comment convention, lesson 18).

## Risk register

| Risk | Mitigation |
|---|---|
| Rotation state-file migration breaks users mid-upgrade | Backward-compat read of legacy `rotation-<tag>.state`; new file written alongside |
| `-w` removal breaks existing scripts | Hard error with clear message + docs + CHANGELOG note |
| Filter-aware rotation produces surprising order | Tests cover eligible-list narrowing; `positionAfterLastLaunched` falls back to index 0 when prior model isn't in snapshot |
| TUI complexity grows beyond testability | Each phase in its own file (existing pattern); `phaseAgent` follows `phaseNewWorktree` template |
| Filter parsing mistakes (whitespace, empty values) | `parseFilterList` covered with explicit cases |

## Open questions

None at spec time. Resolved during brainstorming:
- `-W` replaces `-w` (hard error on old).
- `-M` verified against eligible list (error if missing).
- Rotation operates over the eligible list (filter-aware).
- "Main on branch X" → launch in main checkout with branch hint.
- Agent+command picker is one unified screen.
- `wt` from a worktree shows full picker view.
- `-T`/`-F` use OR-within-flag semantics.
- Picker always presents at least one pickable row (default branch, or `+ New worktree…` sentinel).
- Picker is skipped on `-W`, `--cwd`, or non-git-repo.

## Out of scope (future work)

- In-picker filter UI (keybinding to change `-T`/`-F` interactively).
- Multi-pick rotation slots (today one rotation state per slot).
- Live model discovery in the picker (today `config.toml` only).
- New command drivers beyond `shell`.
