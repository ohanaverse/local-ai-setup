# wt Flow Cleanup — PR 2: Agent+Command Picker Screen

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an explicit `phaseAgent` screen between the worktree picker and the model picker. The screen lists every configured agent plus every registered command; Enter on a command launches immediately (skipping the model layer), Enter on an agent continues to the model picker.

**Architecture:** New TUI phase following the existing `phaseNewWorktree` template pattern (file-per-phase, kind-discriminated list items). New `IsCommand` lookup in `internal/agents` generalized from the PR-1 stub via an optional interface so existing drivers don't break.

**Tech Stack:** Bubble Tea (`tea.Model`/`Update`/`View`), `bubbles/list`.

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (section "Agent+Command Picker Screen (PR 2)").

## Global Constraints

- Go 1.26.3 (per `go.mod`).
- Test convention: every `Test*` function has a top-level `//` block (lesson 18).
- Each TUI phase lives in its own file (`phaseAgent` → `internal/tui/agent_picker.go`).
- Run `go test ./...` and `go vet ./...` before each commit.
- The PR-1 stub `agents.IsCommand(name)` returns `true` for `"shell"` only; this PR generalizes it via an optional interface so other drivers don't break.

---

## File Structure (this PR)

### Created

- `internal/tui/agent_picker.go` — `agentItem`, `buildAgentList`, `phaseAgentView`.
- `internal/tui/agent_picker_test.go` — tests for the picker item/populator.

### Modified

- `internal/agents/agents.go` (or wherever `Driver` is defined) — add optional `Command` interface (or `IsCommand() bool` method on `Driver` with a default-`false` helper).
- `internal/agents/registry.go` — update `IsCommand` to consult the optional interface.
- `internal/agents/shell.go` — implement the `IsCommand() bool` method returning `true`.
- `internal/tui/app.go` — add `phaseAgent` to the phase enum; update `selectedEntryMsg` handler to transition to `phaseAgent` instead of resolving the agent synchronously; add Enter/`esc` handlers for `phaseAgent`.
- `internal/tui/app_test.go` — update `selectedEntryMsg` test to expect `phaseAgent`; add new tests for `phaseAgent` transitions.

### Untouched

- `internal/config/*` — no changes (EligibleModels from PR 1 is enough).
- `cmd/wt/*` — non-TUI path from PR 1 already handles commands via `errCommandAgent`.
- `internal/rotation/*` — rotation refactor is PR 3.

---

## Task 1: Generalize `agents.IsCommand` via optional interface

**Files:**
- Modify: `internal/agents/agents.go` (Driver interface).
- Modify: `internal/agents/shell.go` (implement the method).
- Modify: `internal/agents/registry.go` (IsCommand lookup).

**Interfaces:**
- Adds: optional interface `type Commanded interface { IsCommand() bool }` to `internal/agents`.
- Updates: `IsCommand(name string) bool` to return true when the registered driver's value implements `Commanded` and returns `true`.

- [ ] **Step 1: Write the failing test**

Create `internal/agents/command_test.go`:

```go
package agents

import "testing"

// TestIsCommand verifies the agent/command distinction: shell is a
// command (no model layer); every other registered driver is an agent.
func TestIsCommand(t *testing.T) {
	if !IsCommand("shell") {
		t.Error(`IsCommand("shell") = false, want true`)
	}
	for _, n := range Names() {
		if n == "shell" {
			continue
		}
		if IsCommand(n) {
			t.Errorf("IsCommand(%q) = true, want false", n)
		}
	}
}

// TestIsCommandUnknown verifies that IsCommand is safe on unregistered names.
func TestIsCommandUnknown(t *testing.T) {
	if IsCommand("does-not-exist") {
		t.Error(`IsCommand("does-not-exist") = true, want false`)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/agents -run TestIsCommand -v`
Expected: PASS for shell (stub from PR 1); may FAIL for unknown name if the stub returns true for everything. Adjust the stub if needed so unknown names return false.

- [ ] **Step 3: Generalize via optional interface**

Add to `internal/agents/agents.go`:

```go
// Commanded is an optional interface a Driver can implement to mark
// itself as a command (no model layer) rather than an agent.
// Drivers that don't implement it default to IsCommand() == false.
type Commanded interface {
	IsCommand() bool
}
```

Update `internal/agents/registry.go`:

```go
// IsCommand reports whether name is a registered command (no model layer),
// as opposed to an agent. Drivers that implement the Commanded optional
// interface and return true from IsCommand() are commands.
func IsCommand(name string) bool {
	d := ByName(name)
	if d == nil {
		return false
	}
	if c, ok := d.(Commanded); ok {
		return c.IsCommand()
	}
	return false
}
```

Update `internal/agents/shell.go` — the shell driver already exists; add the method:

```go
// IsCommand marks shell as a command (no model layer).
func (d *shellDriver) IsCommand() bool { return true }
```

If the existing shell driver uses a different name (e.g. `ShellDriver`), adjust accordingly.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/agents -v`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agents/agents.go internal/agents/registry.go internal/agents/shell.go internal/agents/command_test.go
git commit -m "feat(agents): generalize IsCommand via optional Commanded interface"
```

---

## Task 2: Add `phaseAgent` enum value and `agentItem` list item

**Files:**
- Modify: `internal/tui/app.go` (phase enum).
- Create: `internal/tui/agent_picker.go`.

**Interfaces:**
- Adds: `phaseAgent` constant to the phase enum (between `phaseList` and `phaseModel`).
- Adds: `agentItem` struct (kind-discriminated: command vs agent) implementing `list.Item`.

- [ ] **Step 1: Write the failing test**

Create `internal/tui/agent_picker_test.go`:

```go
package tui

import (
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// TestBuildAgentList verifies the agent+command picker contains every
// configured agent and every registered command, with their kinds
// correctly labeled. This drives the `phaseAgent` view.
func TestBuildAgentList(t *testing.T) {
	cfg := &config.Config{
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "codex", SupportedProviders: []string{"openai"}},
		},
	}
	items := buildAgentList(cfg, 80, 24)
	if len(items) < 3 { // 2 agents + 1 command (shell)
		t.Fatalf("expected at least 3 items, got %d", len(items))
	}
	var sawShell, sawClaude, sawCodex bool
	for _, it := range items {
		ai, ok := it.(agentItem)
		if !ok {
			t.Fatalf("item %T is not an agentItem", it)
		}
		switch ai.name {
		case "shell":
			sawShell = true
			if !ai.command {
				t.Error("shell should be marked as a command")
			}
		case "claude":
			sawClaude = true
			if ai.command {
				t.Error("claude should be marked as an agent, not a command")
			}
		case "codex":
			sawCodex = true
		}
	}
	if !sawShell || !sawClaude || !sawCodex {
		t.Errorf("missing items: shell=%v claude=%v codex=%v", sawShell, sawClaude, sawCodex)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestBuildAgentList -v`
Expected: FAIL with `undefined: buildAgentList`.

- [ ] **Step 3: Implement `agentItem` and `buildAgentList`**

Create `internal/tui/agent_picker.go`:

```go
package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/agents"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// agentItem is one row in the phaseAgent picker. command distinguishes
// agents (require model layer) from commands (launch directly).
type agentItem struct {
	name    string
	command bool
}

func (a agentItem) FilterValue() string { return a.name }

func (a agentItem) Title() string {
	if a.command {
		return a.name + "  (command)"
	}
	return a.name + "  (agent)"
}

func (a agentItem) Description() string {
	if a.command {
		return "no model; runs passthrough commands or interactive shell"
	}
	return "agent: launches with a model"
}

// buildAgentList constructs the agent+command picker. Each configured
// agent and each registered command appears once.
func buildAgentList(cfg *config.Config, width, height int) list.Model {
	items := make([]list.Item, 0)
	seen := map[string]bool{}

	// Configured agents first (so they sort before any unknown commands).
	for _, a := range cfg.Agents {
		if seen[a.Name] {
			continue
		}
		seen[a.Name] = true
		items = append(items, agentItem{name: a.Name, command: agents.IsCommand(a.Name)})
	}

	// Any registered driver not already listed (e.g. a future command
	// driver that wasn't pre-declared in config).
	for _, n := range agents.Names() {
		if seen[n] {
			continue
		}
		seen[n] = true
		items = append(items, agentItem{name: n, command: agents.IsCommand(n)})
	}

	l := list.New(items, list.NewDefaultDelegate(), width, height)
	l.Title = "Pick an agent or command"
	l.SetShowStatusBar(false)
	return l
}

// phaseAgentView renders the agent+command picker.
func (m *model) phaseAgentView() string {
	header := fmt.Sprintf("directory: %s\n\n", m.selectedPath)
	footer := "\n[↑/↓] navigate   [enter] continue   [esc] back"
	return header + m.agentList.View() + footer
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/tui -run TestBuildAgentList -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/agent_picker.go internal/tui/agent_picker_test.go
git commit -m "feat(tui): add buildAgentList + agentItem for phaseAgent"
```

---

## Task 3: Add `phaseAgent` to the phase enum and wire transitions

**Files:**
- Modify: `internal/tui/app.go` (phase enum, model struct, Update, View, handlers).
- Modify: `internal/tui/launch.go` (extract shared launch path for command vs agent).

**Interfaces:**
- Adds: `phaseAgent` enum value.
- Adds: `agentList list.Model` field to `model` struct.
- Updates: `selectedEntryMsg` handler transitions to `phaseAgent` instead of resolving agent synchronously.
- Updates: `Update` Enter handler for `phaseAgent`: command → launch directly; agent → transition to `phaseModel`.
- Updates: `Update` `esc` handler: `phaseAgent` → return to `phaseList`.

- [ ] **Step 1: Update the phase enum**

In `internal/tui/app.go`, add to the phase enum:

```go
const (
	phaseList        phase = iota
	phaseAgent                  // new: agent+command picker
	phaseModel
	phaseResume
	phaseGuardWarn
	phaseOllamaWarn
	phaseNewWorktree
)
```

Note: keep `iota` ordering so existing phase values are unchanged. Insert `phaseAgent` between `phaseList` and `phaseModel` only if you're sure no code depends on numeric phase values (search confirms only `phase == phaseX` comparisons are used).

- [ ] **Step 2: Add `agentList` field to model struct**

In `internal/tui/app.go`'s `model` struct, add:

```go
agentList list.Model // bubble/list of agent+command items (PR 2)
```

- [ ] **Step 3: Update `selectedEntryMsg` handler**

Replace the agent-resolution block in `selectedEntryMsg`:

```go
case selectedEntryMsg:
    m.selectedPath = msg.entry.Path
    // Build the agent+command picker. The agent is resolved when the
    // user picks a row in phaseAgent (or skipped if --agent was given).
    m.agentList = buildAgentList(m.cfg, m.width-2, m.height-2)
    m.phase = phaseAgent
    return m, nil
```

Note: this removes the synchronous `m.agent = ...` resolution that previously lived here. The agent is now resolved inside `phaseAgent` (or skipped if `m.initialAgent != ""`).

- [ ] **Step 4: Update `Run()` to pass `initialAgent` through**

Already done in PR 1 wiring; verify `Run()` in `internal/tui/app.go` still threads `initialAgent` into the model:

```go
func Run(yolo bool, agent string, extraArgs []string) error {
    // existing — no change
}
```

- [ ] **Step 5: Update the Enter handler for `phaseAgent`**

In the `enter` switch in `Update`, add a `case phaseAgent:` block before `case phaseList:`:

```go
case phaseAgent:
    item, ok := m.agentList.SelectedItem().(agentItem)
    if !ok {
        return m, nil
    }
    m.agent = item.name
    if item.command {
        // Command: launch directly without the model layer.
        return m.launchShell()
    }
    // Agent: continue to model picker.
    m.tag = m.cfg.DefaultTag
    models, err := m.cfg.ModelsForAgentAndTag(m.agent, m.tag)
    if err != nil {
        m.status = "config error: " + err.Error()
        return m, nil
    }
    if len(models) == 0 {
        m.status = fmt.Sprintf("no models for agent %q in tag %q — edit your config", m.agent, m.tag)
        return m, nil
    }
    m.phase = phaseModel
    m.models = buildModelList(models, m.width-2, m.height-2)
    m.modelsFor = m.agent
    m.modelsTag = m.tag
    m.positionAfterLastLaunched(m.tag, models)
    return m, nil
```

- [ ] **Step 6: Update the `esc` handler**

Add an `esc` branch before the default `m, tea.Quit`:

```go
if m.phase == phaseAgent {
    m.phase = phaseList
    return m, nil
}
```

- [ ] **Step 7: Update the View function**

Add a `phaseAgent` branch in `View`:

```go
if m.phase == phaseAgent {
    if m.width <= 0 || m.height <= 0 {
        return "agent picker (waiting for window size)"
    }
    return m.phaseAgentView()
}
```

- [ ] **Step 8: Update the `WindowSizeMsg` handler**

Add:

```go
if m.phase == phaseAgent {
    m.agentList.SetSize(msg.Width-2, msg.Height-2)
}
```

- [ ] **Step 9: Run all TUI tests**

Run: `go test ./internal/tui -v`
Expected: existing tests may need updates (the `selectedEntryMsg` test from PR 1 era). Fix any that broke.

- [ ] **Step 10: Commit**

```bash
git add internal/tui/app.go internal/tui/launch.go
git commit -m "feat(tui): wire phaseAgent into the picker flow"
```

---

## Task 4: Tests for `phaseAgent` transitions

**Files:**
- Modify: `internal/tui/app_test.go`.

**Interfaces:**
- Tests: enter on command item → `launchShell` path (no `phaseModel` transition); enter on agent item → `phaseModel` transition; `esc` from `phaseAgent` returns to `phaseList`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/app_test.go`:

```go
// TestPhaseAgentCommandEnter verifies that picking a command in
// phaseAgent launches directly without transitioning to phaseModel.
func TestPhaseAgentCommandEnter(t *testing.T) {
    cfg := newTestConfig(t) // helper that builds a Config with shell as a command
    m := model{
        cfg:          cfg,
        width:        80,
        height:       24,
        phase:        phaseAgent,
        selectedPath: "/tmp/repo",
    }
    m.agentList = buildAgentList(cfg, 78, 22)
    // Select the shell row.
    for i, it := range m.agentList.Items() {
        if ai, ok := it.(agentItem); ok && ai.command {
            m.agentList.Select(i)
            break
        }
    }
    newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    nm := newModel.(model)
    // Should not have transitioned to phaseModel.
    if nm.phase == phaseModel {
        t.Errorf("command enter should skip phaseModel; got phase = %v", nm.phase)
    }
    if nm.agent != "shell" {
        t.Errorf("agent = %q, want shell", nm.agent)
    }
}

// TestPhaseAgentAgentEnter verifies that picking an agent in phaseAgent
// transitions to phaseModel and resolves the agent name.
func TestPhaseAgentAgentEnter(t *testing.T) {
    cfg := newTestConfig(t)
    m := model{
        cfg:          cfg,
        width:        80,
        height:       24,
        phase:        phaseAgent,
        selectedPath: "/tmp/repo",
    }
    m.agentList = buildAgentList(cfg, 78, 22)
    for i, it := range m.agentList.Items() {
        if ai, ok := it.(agentItem); ok && !ai.command && ai.name == "claude" {
            m.agentList.Select(i)
            break
        }
    }
    newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    nm := newModel.(model)
    if nm.phase != phaseModel {
        t.Errorf("expected phaseModel, got %v", nm.phase)
    }
    if nm.agent != "claude" {
        t.Errorf("agent = %q, want claude", nm.agent)
    }
}

// TestPhaseAgentEsc verifies that esc pops back to phaseList.
func TestPhaseAgentEsc(t *testing.T) {
    cfg := newTestConfig(t)
    m := model{
        cfg:          cfg,
        width:        80,
        height:       24,
        phase:        phaseAgent,
        selectedPath: "/tmp/repo",
    }
    newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
    nm := newModel.(model)
    if nm.phase != phaseList {
        t.Errorf("expected phaseList, got %v", nm.phase)
    }
}
```

(Adjust `newTestConfig` to whatever helper the existing tests use; if none exists, define one inline that builds a minimal Config with a `claude` agent and registers the shell driver.)

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/tui -run TestPhaseAgent -v`
Expected: PASS (since Tasks 1-3 already wired the phase). If FAIL, fix the wiring.

- [ ] **Step 3: Commit**

```bash
git add internal/tui/app_test.go
git commit -m "test(tui): phaseAgent transition coverage"
```

---

## Self-Review

- [x] **Spec coverage:** PR 2 covers the spec section "Agent+Command Picker Screen (PR 2)" including `IsCommand`, `phaseAgent`, command-vs-agent transition, `esc` back to phaseList.
- [x] **Placeholder scan:** No TBDs. Code blocks are concrete.
- [x] **Type consistency:** `agentItem{name, command bool}` matches across `buildAgentList`, `phaseAgentView`, and the test.
- [x] **Back-compat note:** `agents.IsCommand` is generalized via optional interface; PR-1 stub is replaced. Existing drivers default to `IsCommand() == false` (still agents).
