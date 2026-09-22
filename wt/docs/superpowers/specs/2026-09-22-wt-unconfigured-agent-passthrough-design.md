# wt: pass through to an unconfigured agent

Status: Draft. From the brainstorming session 2026-09-22 for
[issue #147](https://github.com/ohanaverse/local-ai-setup/issues/147). Source
of truth for the implementation plan; do not diverge without updating this doc.

**Extends** `wt/docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md`
(which tolerated a missing registry for command agents only) to model-driven
agents. The command-agent carve-out stays as-is; this spec replaces only the
"real agents always fail closed" half.

## Problem

Running `claude-wt` (i.e. `wt --agent claude`) on a machine that has no
modelman/wt configuration fails before any UI appears:

1. `newApp()` calls `config.Load()`, which calls `loadRegistry()`.
2. `loadRegistry()` fails closed when `registry.toml` is missing, returning
   `model registry not found at … — seed it with modelman migrate`.
3. That error lands in `a.cfgErr`, and `rootCmd().RunE` bails at the
   `a.cfgErr` gate (`cmd/wt/main.go`) before the worktree picker runs.

The result is that wt's worktree/branch selector — which needs no models at
all — is unusable on a new machine until the user sets up a full model
registry. The goal is to make the selector usable with no additional
configuration: the agent just launches the installed binary directly.

## Decisions

1. **Passthrough for unconfigured agents.** A registered model-driven agent
   with no `config.toml` entry launches its installed binary with no wt model
   routing, in the selected worktree, with the args after `--`. The whole
   config being absent is just the extreme case of "no entry".
2. **"Configured" means the agent has a `config.toml` entry.** A configured
   agent with zero *eligible* models (bad `-T`/`-F`, or a registry data gap)
   is still an error — that is a config problem, not a fresh machine.
3. **Installed check is early and pinned-agent-only.** A pinned `-A` agent
   whose binary is missing exits with a clear error before worktree
   selection. The unpinned agent picker keeps its current inline issue.
4. **Provider-count does not gate the model picker.** The issue's "one
   provider → skip the picker" is satisfied by the existing single-launchable-
   model shortcut. Counting distinct providers would have silently removed
   the picker for `pi`/`opencode`, which are ollama-only and normally have
   many models.
5. **`-M` on an unconfigured agent is an error.** An explicit pin that cannot
   be honored is surfaced, never silently dropped. `-T`/`-F`/`--replace` are
   ignored (there is no model to filter or replace).
6. **Passthrough reuses the drivers' native branch via a sentinel model.**
   Every model-driven driver already builds a bare command when
   `config.Model.Native` is set; `opencode` is the one gap (its `Build`
   ignores `Native`). No new per-driver interface.
7. **Fail closed on malformed config.** Only `config.ErrRegistryMissing` is
   tolerated by the launch-path gate. A `config.toml`/`registry.toml` that
   exists but does not parse still fails with the repair hint.

## Behavior rules

- **Command agents** (`shell`): unchanged — no model layer, launch directly
  today.
- **Configured agent**: `cfg.AgentByName(agent)` succeeds. Normal model flow:
  worktree → (agent picker) → model picker, with the existing "skip when
  exactly one launchable model exists" shortcut. Zero eligible models keeps
  the existing `no models for agent …` error.
- **Unconfigured model-driven agent**: after worktree selection,
  - `-M` supplied → error
    `agent "claude" is not configured; cannot pin model "ollama/foo"`.
  - otherwise → launch the bare agent command in the selected worktree with
    the args after `--`. No model picker.
- **Installed**:
  - pinned model-driven agent, binary absent → exit 1 before any worktree
    work with `agent "claude" is not installed — install the claude binary`.
  - unpinned agent picker → uninstalled agents stay listed but unselectable
    with the existing `not installed — install the binary` issue.

## Design

### Detection API (`internal/agents`)

Add the single source of truth for the launch decision:

```go
// IsConfigured reports whether name has an agent entry in cfg, and therefore
// a model catalog to resolve a launch against. Commands (e.g. shell) are
// always "configured": they have no model layer and need no entry.
func IsConfigured(cfg *config.Config, name string) bool {
    if IsCommand(name) {
        return true
    }
    if cfg == nil {
        return false
    }
    _, err := cfg.AgentByName(name)
    return err == nil
}
```

`IssueFor` and `ListEntries` keep their current semantics — the
`wt config` editor (`internal/configeditor`) relies on the
`not configured` marker for its agent tab, and that display is correct: the
editor exists to add the missing entry. The TUI overrides the row's issue for
passthrough-able agents (below).

### Passthrough builder (`internal/agents`)

```go
// passthroughModel is the sentinel model for a bare launch. Every
// model-driven driver's native branch treats it as "no model override": no
// --model, no gateway env. ModelName "native" is the value drivers already
// use to mean "launch bare" (claude adds --model only for a specific native
// model).
func passthroughModel() config.Model {
    return config.Model{Native: true, ModelName: "native"}
}

// BuildPassthroughCmd builds the command that launches agent with no model
// routing — equivalent to running the installed binary directly, plus the
// agent's yolo flag and the passthrough args. Used when an agent has no
// config.toml entry (issue #147).
func BuildPassthroughCmd(agent, worktreePath string, yolo bool, extraArgs []string) (*exec.Cmd, error) {
    return BuildLaunchCmd(agent, passthroughModel(), worktreePath, yolo, nil, nil, extraArgs)
}
```

Passing `nil` cfg is safe: `BuildLaunchCmd` substitutes an empty `Config`,
`ResolveRoute` early-returns for `Native`, and pi's `SyncModels` becomes a
no-op with no models to sync (and does not create `models.json` when it does
not already exist and LiteLLM routing is off).

Add the missing native guard to `opencodeDriver.Build` immediately after the
yolo arg:

```go
if m.Native {
    return lc
}
```

Today it is unreachable (opencode is ollama-only, so it never receives a
`Native` model), but it is what makes the passthrough sentinel produce a bare
`opencode` command instead of an `OPENCODE_CONFIG_CONTENT` blob pointing at an
empty base URL.

**Env behavior.** Passthrough runs through the drivers' native branches, so
wt's native env clearing applies (claude clears `ANTHROPIC_*`, copilot clears
`COPILOT_*`). This is deliberate: a stale exported gateway variable must not
silently redirect what is supposed to be a plain agent launch.

### Non-TUI wiring (`cmd/wt`)

- `main.go` launch-path gate tolerates a missing registry for every agent, not
  just commands:
  ```go
  if a.cfgErr != nil && !errors.Is(a.cfgErr, config.ErrRegistryMissing) {
      return fmt.Errorf("config error: %w (run `wt config` to repair)", a.cfgErr)
  }
  ```
- After the unknown-agent check, fast-fail a pinned model-driven agent whose
  binary is missing (`agents.Installed`; the agent name is the binary name for
  every registered driver), before `-W`/`--cwd`/TUI routing.
- `runLaunchPath`, after the `launchPath == ""` (TUI) branch and before the
  `needsModelPicker` branch:
  ```go
  if !agents.IsCommand(agent) && !agents.IsConfigured(a.cfg, agent) {
      if pinned != "" {
          return fmt.Errorf("agent %q is not configured; cannot pin model %q", agent, pinned)
      }
      fmt.Fprintf(os.Stderr, "wt: %s is not configured — launching it directly without a model\n", agent)
      return launchPassthrough(agent, launchPath, yolo(cmd), args, a.cfg)
  }
  ```
- New helper:
  ```go
  func launchPassthrough(agent, worktreePath string, yolo bool, extraArgs []string, cfg *config.Config) error {
      cmd, err := agents.BuildPassthroughCmd(agent, worktreePath, yolo, extraArgs)
      if err != nil {
          return err
      }
      return runAgentCmd(cmd, agent, config.Model{}, cfg)
  }
  ```
  The zero `config.Model` makes the post-exit flow behave exactly like a
  command agent: no survey, no stop picker, no price notice, and a summary
  line without a model segment.

### TUI wiring (`internal/tui`)

- `agentItem` gains `passthrough bool`. `buildAgentList` classifies each
  model-driven row:
  - uninstalled → keep the `not installed` issue (unselectable);
  - installed but unconfigured → `passthrough = true`, `issue = ""`,
    Description explaining it launches directly with no model;
  - otherwise → unchanged.
- `phaseAgent` Enter: command → `launchCommand`; non-empty issue → status;
  `passthrough` → `launchPassthrough(name)`; else the existing model flow.
- `proceedFromSelectedPath` (pinned `-A`): command → `launchCommand`; not
  installed → status; unconfigured → `-M` errors, otherwise
  `launchPassthrough`; else the existing `IssueFor` + model flow.
- New `buildPassthrough` seam var wrapping `agents.BuildPassthroughCmd`
  (mirroring the existing `launchAgent` seam) and:
  ```go
  func (m model) launchPassthrough(name string) (model, tea.Cmd) {
      cmd, err := buildPassthrough(name, m.selectedPath, m.yolo, m.extraArgs)
      if err != nil {
          m.status = "launch failed: " + err.Error()
          return m, nil
      }
      return m, runAndWaitCmd(cmd, name, config.Model{})
  }
  ```
  `runAndWaitCmd` with a zero model produces the command-agent post-exit
  behavior described above. No rotation/refcount entry is recorded (there is
  no model id), so nothing needs releasing.

## Edge cases

- **`config.toml` present, `registry.toml` absent** → `ErrRegistryMissing`,
  empty in-memory config, every model-driven agent reads as unconfigured →
  passthrough. A partially migrated machine stays usable; `modelman migrate`
  is the opt-in to the model layer.
- **Malformed `config.toml`/`registry.toml`** → still fails closed with the
  repair hint (not `ErrRegistryMissing`).
- **Configured agent, zero eligible models** → existing error; never
  passthrough.
- **Configured agent, exactly one launchable model** → existing single-row
  skip (satisfies the issue's "skip the model screen" bullet for the
  fresh-config shape).
- **Unconfigured + `-M`** → error on both paths.
- **Unconfigured + `-T`/`-F`/`--replace`** → ignored.
- **`wt config`, `--init`, `--version`, guard flags** → handled before the
  gate/agent checks; unaffected.
- **Resume** → passthrough does not offer resume, because native/zero-model
  launches never resume. Known limitation, consistent with native models.
- **No LiteLLM route hook** → passthrough starts/stops no model, so it never
  touches `config.yaml` or restarts the proxy.

## Testing

- `internal/agents`:
  - `opencode.Build` with a `Native` model returns a bare command (no
    `OPENCODE_CONFIG_CONTENT`).
  - Every model-driven driver, given the sentinel, produces no `--model`
    argument and no model env vars — a locked convention test so a future
    driver cannot silently break passthrough.
  - `BuildPassthroughCmd` command shape per driver (via `requireBinary`).
- `cmd/wt`:
  - Missing-registry fixture (`XDG_CONFIG_HOME` + nonexistent
    `MODELMAN_REGISTRY`): `-A claude -W x` passes through (assert the built
    command through a seam, not a real agent launch).
  - `-M` with an unconfigured agent errors.
  - Pinned uninstalled agent errors before any worktree work.
  - Malformed registry still fails.
  - Configured agent keeps today's behavior.
- `internal/tui`:
  - `buildAgentList` marks installed-but-unconfigured agents as passthrough
    and launchable; uninstalled agents stay blocked.
  - `phaseAgent` Enter on a passthrough row calls the `buildPassthrough` seam.
  - Pinned-unconfigured path, with and without `-M`.
- `make test-all` and `make lint` clean.

## Docs

- `wt/CLAUDE.md`: update the config-gate note (currently "command agents
  tolerated") and add the passthrough rule to the Agents/launch-path section.
- `wt/docs/configuration.md`: note that a missing registry means model-driven
  agents launch directly, with `modelman migrate` as the opt-in to routing.
- `wt/docs/superpowers/specs/2026-09-04-shell-wt-no-registry-design.md`: add a
  pointer noting this spec extends it to model-driven agents.
- No `modelman` changes and no cross-language contract fixture changes.

## Out of scope

- Changing `IssueFor`/`ListEntries`, or the `wt config` editor's
  `not configured` marker.
- Provider-count-based model-picker skipping (explicitly rejected: it would
  remove the picker for ollama-only agents such as `pi`/`opencode`).
- A `wt models`-style fallback, model discovery, or auto-seeding of
  `registry.toml` on a fresh machine.
- Resume support for passthrough launches.
- Any `modelman` behavior change.

## Open questions

None.
