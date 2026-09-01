# Consolidate Agent-Specific Knowledge into the Driver — Design

> **Status:** draft (fleshed out from placeholder).
> Follow-up simplification PR from the 2026-08-28 `/simplify` review
> (Altitude #5, #8).

## Goal

Move two pieces of agent-specific knowledge out of shared packages and into
the `Driver` so that adding a new agent requires changes only in that agent's
own driver package.

The two knowledge areas are:

1. **Instruction-file pointer mapping** — which file an agent uses as a
   project-level pointer to `AGENTS.md`, and what its default content is.
2. **Ollama gateway URL** — the full URL each agent uses to route non-native
   models through the local Ollama-compatible gateway.

## Background

### Instruction-file mapping

`internal/initseed/initseed.go` currently contains a private `pointerFor`
function with an agent-name `switch`:

```go
func pointerFor(agent string) (pointer, bool) {
    switch agent {
    case "claude":
        return pointer{"CLAUDE.md", "@AGENTS.md\n"}, true
    case "copilot":
        return pointer{".github/copilot-instructions.md", "Read AGENTS.md and follow all instructions in it.\n"}, true
    default:
        return pointer{}, false
    }
}
```

Adding a new agent that needs a pointer file requires editing shared code in
`initseed`.

### Ollama URL resolution

`internal/config/config.go` exports a single constant:

```go
const OllamaBaseURL = "http://localhost:11434"
```

Four drivers import it and compose their own endpoint shapes:

| Agent | Shape | Usage |
|---|---|---|
| claude | `http://localhost:11434` | `ANTHROPIC_BASE_URL` env |
| copilot | `http://localhost:11434/v1` | `COPILOT_PROVIDER_BASE_URL` env |
| opencode | `http://localhost:11434/v1` | inline JSON `baseURL` |
| codex | `http://localhost:11434/v1/` | inline `-c` override |

This spreads per-agent routing knowledge across the drivers *and* the shared
`config` package.

## Proposed Design

### 1. Add two optional `Driver` capabilities

In `internal/agents/agents.go`, add `Seeder` and `OllamaURLer` next to the
existing optional interfaces (`Syncer`, `ArgSetter`, `Commanded`, `Resumer`):

```go
// Seeder is an optional Driver capability for agents that need a
// project-level instruction pointer file created by `wt --init`.
type Seeder interface {
    // InstructionPointers returns the pointer files to create. Each pointer
    // is written only if it does not already exist.
    InstructionPointers() []InstructionPointer
}

// InstructionPointer describes a single file created by `wt --init`.
type InstructionPointer struct {
    Path    string // relative to the repo root, e.g. "CLAUDE.md"
    Content string // file body, e.g. "@AGENTS.md\n"
}

// OllamaURLer is an optional Driver capability for agents that route
// non-native models through a local Ollama-compatible gateway.
type OllamaURLer interface {
    // OllamaURL returns the full gateway URL the agent expects. Drivers are
    // free to include path suffixes such as "/v1" or "/v1/" because each
    // agent's wire protocol is different.
    OllamaURL() string
}
```

### 2. Implement `Seeder` in resumable agents

`internal/agents/claude.go`:

```go
func (claudeDriver) InstructionPointers() []InstructionPointer {
    return []InstructionPointer{
        {Path: "CLAUDE.md", Content: "@AGENTS.md\n"},
    }
}
```

`internal/agents/copilot.go`:

```go
func (copilotDriver) InstructionPointers() []InstructionPointer {
    return []InstructionPointer{
        {Path: ".github/copilot-instructions.md", Content: "Read AGENTS.md and follow all instructions in it.\n"},
    }
}
```

No other agent currently needs a pointer file.

### 3. Implement `OllamaURLer` in gateway-routed agents

`internal/agents/claude.go`:

```go
func (claudeDriver) OllamaURL() string { return "http://localhost:11434" }
```

`internal/agents/copilot.go`:

```go
func (copilotDriver) OllamaURL() string { return "http://localhost:11434/v1" }
```

`internal/agents/opencode.go`:

```go
func (opencodeDriver) OllamaURL() string { return "http://localhost:11434/v1" }
```

`internal/agents/codex.go`:

```go
func (codexDriver) OllamaURL() string { return "http://localhost:11434/v1/" }
```

`piDriver`, `agyDriver`, and `shellDriver` do not implement `OllamaURLer`.

### 4. Update driver `Build` methods to use `OllamaURLer`

`claude.go`:

```go
lc.Env = append(lc.Env,
    "ANTHROPIC_AUTH_TOKEN=ollama",
    "ANTHROPIC_API_KEY=",
    "ANTHROPIC_BASE_URL="+claudeDriver{}.OllamaURL(),
)
```

`copilot.go`:

```go
lc.Env = append(lc.Env,
    "COPILOT_PROVIDER_BASE_URL="+copilotDriver{}.OllamaURL(),
    ...
)
```

`opencode.go`:

```go
"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
    `{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s","apiKey":""}}}}`,
    m.ModelName, opencodeDriver{}.OllamaURL(),
)
```

`codex.go`:

```go
const ollamaProviderURL = codexDriver{}.OllamaURL()
```

(or inline directly in the `-c` override).

### 5. Update `internal/initseed/initseed.go`

Remove `pointerFor` and the agent-name `switch`. Replace the pointer-creation
logic with a capability check:

```go
if d := agents.ByName(agent); d != nil {
    if s, ok := d.(agents.Seeder); ok {
        for _, ptr := range s.InstructionPointers() {
            ptrPath := filepath.Join(repoRoot, ptr.Path)
            created, err := writeIfMissing(ptrPath, ptr.Content)
            if err != nil {
                return nil, err
            }
            track(res, created, ptr.Path)
        }
    }
}
```

`initseed` will need to import `internal/agents`.

### 6. Preserve `config.OllamaBaseURL` for migration only

`internal/config/migrate.go` still seeds the legacy ollama provider with the
default base URL. `config.OllamaBaseURL` remains in `config.go`, but the
comment should clarify it is now migration-only and not consumed by drivers.

No driver should import `config` for the Ollama URL after this change.

## Interface Boundaries

| Unit | Responsibility | Depends on |
|---|---|---|
| `internal/agents` | Defines `Seeder`, `OllamaURLer`, and `InstructionPointer`. | `config` (for `Model`) |
| `internal/agents/claude` | Knows claude's pointer file and gateway URL. | `agents` types only |
| `internal/agents/copilot` | Knows copilot's pointer file and gateway URL. | `agents` types only |
| `internal/agents/codex` | Knows codex's gateway URL. | `agents` types only |
| `internal/agents/opencode` | Knows opencode's gateway URL. | `agents` types only |
| `internal/initseed` | Writes `AGENTS.md` and delegates pointer files to the `Seeder` capability. | `agents` |
| `internal/config` | Keeps `OllamaBaseURL` for migration. | standard library only |

## Error Handling

- **Unknown agent in `initseed.Seed`:** `AGENTS.md` is still created. Pointer
  files are skipped. No error.
- **Driver lacks `Seeder`:** Only `AGENTS.md` is created.
- **Driver lacks `OllamaURLer`:** No Ollama URL is used in its launch path.
- **`Seeder` returns duplicate paths:** `writeIfMissing` skips duplicates, so
  this is harmless.
- **`OllamaURL()` returns empty string:** Shared code does not validate.
  This is a driver bug and will show up in the driver's own tests.

## Testing Plan

1. **Capability contract tests** in `internal/agents/agents_test.go`:
   - `TestSeederCapability`: claude and copilot implement `Seeder`; others do not.
   - `TestOllamaURLerCapability`: claude, copilot, codex, opencode implement
     `OllamaURLer`; pi, agy, shell do not.

2. **Driver-specific tests** in existing/new driver test files:
   - `internal/agents/claude_test.go`: `TestClaudeOllamaURL`, `TestClaudeSeeder`.
   - `internal/agents/copilot_test.go`: `TestCopilotOllamaURL`, `TestCopilotSeeder`.
   - `internal/agents/codex_test.go`: `TestCodexOllamaURL`.
   - `internal/agents/opencode_test.go`: `TestOpenCodeOllamaURL`.

3. **Initseed tests** (update `internal/initseed/initseed_test.go`):
   - `TestSeedClaude`: creates `AGENTS.md` and `CLAUDE.md` via the `Seeder` capability.
   - `TestSeedCopilot`: creates `AGENTS.md` and `.github/copilot-instructions.md`.
   - `TestSeedUnknownAgent` (codex): creates only `AGENTS.md` because codex lacks `Seeder`.
   - `TestSeedShell`: creates only `AGENTS.md`.
   - `TestSeedNoOverwrite`: existing files are skipped.

4. **Existing build tests** in `internal/agents/agents_test.go` and
   `cmd/wt/launch_test.go` keep the same assertions but the source of the URL
   is now the driver. No test value changes; only implementation path changes.

## Scope

**In:**
- Adding `Seeder` and `OllamaURLer` optional capabilities.
- Moving instruction-file pointers into claude and copilot drivers.
- Moving full Ollama gateway URLs into claude, copilot, codex, and opencode
  drivers.
- Removing `pointerFor` from `internal/initseed`.
- Updating tests to exercise the new capabilities.
- Marking `config.OllamaBaseURL` as migration-only via comment.

**Out:**
- Changing the actual URL values or port.
- Making the Ollama URL configurable.
- Changing the `AGENTS.md` template.
- Changing how `initseed.Seed` is called from `cmd/wt/main.go`.
- Any registry/config data model changes.

## Migration / Rollout

This is a pure internal refactor with no user-facing CLI changes. The same
files are created by `wt --init` and the same env/args are passed to agents.

## Risks

| Risk | Mitigation |
|---|---|
| Driver authors forget to implement `OllamaURLer` for a new gateway agent. | Capability contract test in `agents_test.go` documents the current set. |
| A future Ollama base URL change touches four drivers. | Acceptable — the whole point is per-agent ownership of the full URL. |
| `initseed` now imports `internal/agents`, creating a cycle. | `agents` does not import `initseed`, so this is safe. |

## Approval

Design approved: implement Option A (full driver ownership of instruction
pointers and Ollama URLs via optional capabilities).
