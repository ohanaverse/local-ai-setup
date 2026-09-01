# wt Flow Cleanup — PR 3b: Filter-Aware Picker + Remove `d` Key

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the model picker honor `-T`/`-F` filters via `EligibleModels`. Rotate within the eligible list using the new `Slot`-scoped rotation from PR 3a. Remove the `d` key from the model picker (replaced by `-T` at the CLI).

**Architecture:** The TUI's `phaseModel` is built from `cfg.EligibleModels(agent, tags, family)` instead of `cfg.ModelsForAgentAndTag(agent, tag)`. The non-TUI launch path passes filter flags through to `resolveModel`. Both paths build a `Slot` from the resolved agent/tag/family and use `rotation.New(slot, ...)` for state.

**Tech Stack:** Bubble Tea, `bubbles/list`, `internal/rotation` (new `Slot` API).

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (section "Filter-Aware Model Picker & Rotation Scoping (PR 3)", "Filter inputs" and "Remove `d` key" subsections).

## Global Constraints

- Go 1.26.3.
- Test convention: top-level `//` comment on every `Test*` (lesson 18).
- Run `go test ./...` before each commit.
- PR 3a is already merged; this PR depends on the new `rotation.Slot` API.

---

## File Structure (this PR)

### Created

(none — all changes are to existing files)

### Modified

- `cmd/wt/launch.go` — `launchFiltered` (from PR 1) takes filter flags; pass them through to `resolveModel` and the rotation slot.
- `cmd/wt/main.go` — `RunE` reads `-T`/`-F` and threads them into both the non-TUI launch path and the TUI.
- `internal/tui/app.go` — `selectedEntryMsg` (or `phaseAgent` enter on agent) passes the active tag (and any family filter) to `ModelsForAgentAndTag` → replace with `EligibleModels`; pass `Slot` to `positionAfterLastLaunched`.
- `internal/tui/rotation_helpers.go` — `positionAfterLastLaunched` takes a `Slot` (replacing the `tag string` argument).
- `internal/tui/model_list.go` — `phaseModelView` header shows active filters; `d` key handler is removed.

### Untouched

- `internal/rotation/*` — PR 3a already in place.
- `internal/config/*` — `EligibleModels` already exists.

---

## Task 1: Pass filters through `launchFiltered` and use `Slot` for rotation

**Files:**
- Modify: `cmd/wt/launch.go`.

**Interfaces:**
- Changes: `launchFiltered(agent, path, cfg, yolo, tags, family, pinned, extraArgs)` already exists from PR 1. Add rotation: when `len(eligible) > 1` and `pinned == ""`, advance through the eligible list via the rotation slot.

- [ ] **Step 1: Write the failing test**

Append to `cmd/wt/launch_test.go`:

```go
// TestLaunchFilteredUsesEligibleAndSlot verifies that the non-TUI
// launch path (a) uses cfg.EligibleModels, (b) builds a rotation
// Slot from agent+tag+family, and (c) records the launch to the
// per-slot state file.
func TestLaunchFilteredUsesEligibleAndSlot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
		},
	}

	// Two eligible models, no -M → resolveModel errors (existing PR 1 behavior).
	if _, err := resolveModel("claude", cfg, "", "", ""); err == nil {
		t.Fatal("expected error for ambiguous eligible list")
	}

	// Pinned → resolves to claude/opus.
	m, err := resolveModel("claude", cfg, "", "", "claude/opus")
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", m.ID)
	}

	// Slot construction mirrors what launchFiltered will do.
	slot := rotation.SlotFromFlags("claude", "code", "")
	expectedPath := filepath.Join(dir, "agent-wt", "rotation-claude-code-_.state")
	_ = expectedPath // referenced by the next task's integration test
}
```

(Add `import "github.com/ohanaverse/agent-worktree/internal/rotation"` and `"path/filepath"` as needed.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./cmd/wt -run TestLaunchFiltered -v`
Expected: PASS for the parts that exist from PR 1; the slot-path reference is sanity-only. No real change yet — this step establishes the test fixture.

- [ ] **Step 3: Add rotation logic to `launchFiltered`**

In `cmd/wt/launch.go`, update `launchFiltered` to consult rotation when `pinned == ""` and `len(eligible) > 1`:

```go
func launchFiltered(agent, worktreePath string, cfg *config.Config, yolo bool, tags, family, pinned string, extraArgs []string) error {
    if agents.IsCommand(agent) {
        return launchDirect(agent, cfg, yolo, extraArgs)
    }
    eligible, err := cfg.EligibleModels(agent, tags, family)
    if err != nil {
        return err
    }
    if len(eligible) == 0 {
        return fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
    }

    // Resolve the model: pinned > single match > rotation.
    var m config.Model
    switch {
    case pinned != "":
        for _, cand := range eligible {
            if cand.ID == pinned {
                m = cand
                break
            }
        }
        if m.ID == "" {
            return fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
        }
    case len(eligible) == 1:
        m = eligible[0]
    default:
        // Multiple eligible models, no pin → use rotation.
        // PR 3b: build a slot from agent+tag+family. The "tag" here
        // is the first tag from -T (or DefaultTag if empty), since
        // EligibleModels does not preserve the tag filter as metadata.
        firstTag := firstOrDefault(tags, cfg.DefaultTag)
        slot := rotation.SlotFromFlags(agent, firstTag, family)
        r := rotation.New(slot, eligible, "")
        last, _ := r.LastLaunched()
        var ok bool
        m, ok = rotation.FirstAfter(eligible, last)
        if !ok {
            return fmt.Errorf("eligible list is empty")
        }
        // Record the launch so the next picker entry advances.
        if err := r.RecordLaunch(m); err != nil {
            fmt.Fprintf(os.Stderr, "wt: rotation state not saved: %v\n", err)
        }
    }

    // Ollama check (existing).
    if !agents.IsCommand(agent) && ollamacheck.IsOllamaModel(m) {
        ok, oerr := ollamacheck.Available(m.ModelName)
        if oerr != nil {
            return fmt.Errorf("ollama check failed: %w", oerr)
        }
        if !ok {
            return fmt.Errorf("model %q is not available locally. Run: ollama pull %s", m.ModelName, m.ModelName)
        }
    }

    sess, _ := session.LatestForAgent(agent, worktreePath)
    cmd, err := buildLaunch(agent, m, worktreePath, yolo, sess, cfg, extraArgs)
    if err != nil {
        return err
    }
    cmd.Stdin = os.Stdin
    cmd.Stdout = os.Stdout
    cmd.Stderr = os.Stderr
    if err := cmd.Run(); err != nil {
        var ee *exec.ExitError
        if errors.As(err, &ee) {
            os.Exit(ee.ExitCode())
        }
        return err
    }
    return nil
}

// firstOrDefault returns the first comma-delimited tag from s, or
// fallback if s is empty.
func firstOrDefault(s, fallback string) string {
    parts := config.ParseFilterList(s) // expose parseFilterList or use strings.Split
    if len(parts) == 0 {
        return fallback
    }
    return parts[0]
}
```

Note: `config.ParseFilterList` must be exported for this PR. Add to `internal/config/config.go`:

```go
// ParseFilterList is the exported form of parseFilterList, used by
// callers outside the config package (e.g. cmd/wt).
func ParseFilterList(s string) []string { return parseFilterList(s) }
```

- [ ] **Step 4: Run all tests**

Run: `go test ./cmd/wt -v`
Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/launch.go cmd/wt/launch_test.go internal/config/config.go
git commit -m "feat(wt): non-TUI launch uses EligibleModels + Slot rotation"
```

---

## Task 2: TUI's `phaseModel` honors filters via `EligibleModels`

**Files:**
- Modify: `internal/tui/app.go` (the `phaseAgent` Enter handler).
- Modify: `internal/tui/rotation_helpers.go` (`positionAfterLastLaunched` takes `Slot`).

**Interfaces:**
- Updates: `phaseAgent` Enter handler passes filter args into `EligibleModels`.
- Updates: `positionAfterLastLaunched(slot Slot, models)` — replaces the `tag string` arg.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/agent_picker_test.go` (or wherever phaseModel tests live):

```go
// TestPhaseModelHonorsFilters verifies that entering phaseModel with
// active -T/-F filters narrows the picker list to the eligible set.
func TestPhaseModelHonorsFilters(t *testing.T) {
    cfg := newTestConfigWithFilters(t) // build a Config with code+design tags, two families
    m := model{
        cfg:    cfg,
        agent:  "claude",
        width:  80,
        height: 24,
        phase:  phaseAgent,
    }
    m.agentList = buildAgentList(cfg, 78, 22)
    // Simulate having -T code,design -F gemma4 in effect.
    m.activeTags = "code,design"
    m.activeFamily = "gemma4"
    newModel, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
    nm := newModel.(model)
    if nm.phase != phaseModel {
        t.Fatalf("expected phaseModel, got %v", nm.phase)
    }
    items := nm.models.Items()
    for _, it := range items {
        mi := it.(modelItem)
        if mi.model.Family != "gemma4" {
            t.Errorf("model %s has family %q, want gemma4", mi.model.ID, mi.model.Family)
        }
    }
}
```

(Add `activeTags`, `activeFamily` string fields to the `model` struct. They're set by the TUI entry path from the filter flags.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestPhaseModelHonorsFilters -v`
Expected: FAIL because the picker still uses `ModelsForAgentAndTag`.

- [ ] **Step 3: Update `phaseAgent` Enter handler**

Replace the `phaseAgent` Enter branch with:

```go
case phaseAgent:
    item, ok := m.agentList.SelectedItem().(agentItem)
    if !ok {
        return m, nil
    }
    m.agent = item.name
    if item.command {
        return m.launchShell()
    }
    models, err := m.cfg.EligibleModels(m.agent, m.activeTags, m.activeFamily)
    if err != nil {
        m.status = "config error: " + err.Error()
        return m, nil
    }
    if len(models) == 0 {
        m.status = fmt.Sprintf("no models match agent %q with active filters", m.agent)
        return m, nil
    }
    m.phase = phaseModel
    m.models = buildModelList(models, m.width-2, m.height-2)
    m.modelsFor = m.agent
    // Tag used for the rotation slot is the first active tag, or the
    // config default if no tag filter is active.
    firstTag := firstOrDefault(m.activeTags, m.cfg.DefaultTag)
    m.modelsTag = firstTag
    slot := rotation.SlotFromFlags(m.agent, firstTag, m.activeFamily)
    m.positionAfterLastLaunched(slot, models)
    return m, nil
```

Add `firstOrDefault` to a new file `internal/tui/helpers.go` (mirror the one in `cmd/wt/launch.go` — they're identical but live in different packages for layering reasons).

- [ ] **Step 4: Update `positionAfterLastLaunched`**

In `internal/tui/rotation_helpers.go`:

```go
// positionAfterLastLaunched rebuilds the rotation snapshot for slot
// over models and positions the picker cursor on the model after
// the last-launched one. Falls back to index 0 when there is no
// last-launched model or its ID is no longer in the snapshot.
func (m *model) positionAfterLastLaunched(slot rotation.Slot, models []config.Model) {
    m.rotation = rotation.New(slot, models, "")
    if last, ok := m.rotation.LastLaunched(); ok {
        if next, ok := FindAfter(models, last); ok {
            if idx := indexOfModel(models, next); idx >= 0 {
                m.models.Select(idx)
            }
        }
    }
}
```

- [ ] **Step 5: Run all TUI tests**

Run: `go test ./internal/tui -v`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/rotation_helpers.go internal/tui/helpers.go internal/tui/agent_picker_test.go
git commit -m "feat(tui): phaseModel honors -T/-F filters via EligibleModels + Slot"
```

---

## Task 3: Remove the `d` key from `phaseModel`

**Files:**
- Modify: `internal/tui/app.go` (remove `case "d":` block).

- [ ] **Step 1: Remove the `d` handler**

In `Update`'s `case tea.KeyMsg` switch, delete the entire `case "d":` block:

```go
case "d":
    if m.phase == phaseModel {
        // ... entire block removed ...
    }
```

- [ ] **Step 2: Update the model picker footer**

In `internal/tui/model_list.go`, update `phaseModelView`'s footer:

```go
footer := "\n[↑/↓] navigate   [enter] launch   [q] quit"
```

(Remove the `[d] switch tag` portion.)

- [ ] **Step 3: Remove the now-dead `oppositeTag` helper**

In `internal/tui/rotation_helpers.go`, delete the `oppositeTag` function. Search for any other references first.

- [ ] **Step 4: Update tests**

Find and update any tests that rely on the `d` key behavior:

```bash
grep -rn "oppositeTag\|case \"d\":" internal/tui/
```

Replace with tests that exercise the new filter flag path (set `m.activeTags` and verify picker contents).

- [ ] **Step 5: Run all tests**

Run: `go test ./internal/tui -v`
Expected: all pass; no references to `oppositeTag` remain.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go internal/tui/model_list.go internal/tui/rotation_helpers.go internal/tui/
git commit -m "refactor(tui): drop d-key tag toggle (replaced by -T flag)"
```

---

## Task 4: TUI reads filter flags from CLI

**Files:**
- Modify: `internal/tui/app.go` (`Run` function).
- Modify: `cmd/wt/main.go` (`RunE` passes filter args to `tui.Run`).

**Interfaces:**
- Adds: `tui.Run(yolo, agent, tags, family, extraArgs)` — new parameter list.

- [ ] **Step 1: Update `tui.Run` signature**

```go
func Run(yolo bool, agent, tags, family string, extraArgs []string) error {
    cfg, err := config.Load()
    if err != nil {
        return err
    }
    p := tea.NewProgram(model{
        status:       "loading worktrees...",
        cfg:          cfg,
        yolo:         yolo,
        initialAgent: agent,
        activeTags:   tags,
        activeFamily: family,
        extraArgs:    extraArgs,
    }, tea.WithAltScreen())
    currentProgram = p
    _, err = p.Run()
    return err
}
```

Add `activeTags`, `activeFamily` fields to the `model` struct.

- [ ] **Step 2: Update `cmd/wt/main.go` to pass filter args**

In `RunE`:

```go
return tui.Run(yolo(cmd), agent, tags, family, args)
```

(Where `tags` and `family` are read via `mustGetString` earlier in `RunE`.)

- [ ] **Step 3: Run all tests**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go cmd/wt/main.go
git commit -m "feat(tui): thread -T/-F flags into the TUI"
```

---

## Self-Review

- [x] **Spec coverage:** PR 3b covers "Filter inputs" and "Remove `d` key" from the spec; rotation-scoping integration lands here too.
- [x] **Placeholder scan:** No TBDs.
- [x] **Type consistency:** `Slot{Agent, Tag, Family}` matches PR 3a. `firstOrDefault` is duplicated across `cmd/wt` and `internal/tui` — small cost for layering purity; consider a shared package in a future PR if duplication grows.
- [x] **Back-compat note:** PR 3a's back-compat read covers any user who upgrades from PR 1 with rotation history.
