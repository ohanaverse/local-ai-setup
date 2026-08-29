# wt Flow Cleanup — PR 3a: Rotation Refactor (Slot Type + Back-Compat)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor `internal/rotation` to scope state files by `(agent, tag, family)` instead of just `tag`. Keep `wt rotate <tag>` working via back-compat. The TUI and non-TUI launch paths still consume the old `tag` API in this PR — they migrate to the new API in PR 3b.

**Architecture:** Replace `Rotation.tag` with a `Slot{Agent, Tag, Family}`. State file naming: `rotation-<agent>-<tag>-<family>.state`. When the new file is missing, fall back to the legacy `rotation-<tag>.state`. Old files are read-only (no migration writes to them).

**Tech Stack:** Go 1.26.3, `config.WriteFileAtomic`.

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (section "Filter-Aware Model Picker & Rotation Scoping (PR 3)", specifically the "Rotation scoping" and "Migration / back-compat" subsections).

## Global Constraints

- Go 1.26.3.
- Test convention: top-level `//` comment on every `Test*` (lesson 18).
- Run `go test ./...` before each commit.
- This PR must keep `wt rotate <tag>` working unchanged from the user's perspective.

---

## File Structure (this PR)

### Created

- `internal/rotation/slot_test.go` — tests for `Slot` construction, normalization, escaping.

### Modified

- `internal/rotation/rotation.go` — replace `tag string` with `Slot{Agent, Tag, Family}`; add `SlotFromFlags`; new state-file naming; back-compat read.
- `cmd/wt/commands.go` — `rotateCmd` uses the new `Slot` API while keeping its CLI signature (`wt rotate <tag>`).
- `internal/tui/rotation_helpers.go` — update `positionAfterLastLaunched` to take a `Slot` (or temporarily keep the old `tag string` API and mark it deprecated; see Task 4).

### Untouched

- `internal/config/*` — `EligibleModels` is enough.
- `cmd/wt/launch.go` — non-TUI launch path uses the rotation only via `tag` today; migration to `Slot` is PR 3b.

---

## Task 1: Add `Slot` type and `SlotFromFlags` constructor

**Files:**
- Modify: `internal/rotation/rotation.go`.
- Create: `internal/rotation/slot_test.go`.

**Interfaces:**
- Adds: `type Slot struct{ Agent, Tag, Family string }`.
- Adds: `func SlotFromFlags(agent, tag, family string) Slot` — normalizes empty values to `"-"`, escapes commas/dots in tag/family names to underscores (defensive — caller should pre-filter).

- [ ] **Step 1: Write the failing test**

Create `internal/rotation/slot_test.go`:

```go
package rotation

import "testing"

// TestSlotFromFlags covers the constructor used by the TUI and non-TUI
// launch paths to build a rotation slot from the (agent, tag, family)
// triple. The empty-value normalization is the contract that keeps
// state-file names predictable.
func TestSlotFromFlags(t *testing.T) {
	tests := []struct {
		name           string
		agent, tag, fam string
		want           Slot
	}{
		{"all set", "claude", "code", "gemma4", Slot{"claude", "code", "gemma4"}},
		{"empty tag normalized", "claude", "", "gemma4", Slot{"claude", "-", "gemma4"}},
		{"empty family normalized", "claude", "code", "", Slot{"claude", "code", "-"}},
		{"both empty", "claude", "", "", Slot{"claude", "-", "-"}},
		{"comma in tag escaped", "claude", "code,design", "", Slot{"claude", "code_design", "-"}},
		{"dot in tag escaped", "claude", "v1.0", "", Slot{"claude", "v1_0", "-"}},
		{"slash in family passed through", "claude", "code", "team/ai", Slot{"claude", "code", "team/ai"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SlotFromFlags(tc.agent, tc.tag, tc.fam)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rotation -run TestSlotFromFlags -v`
Expected: FAIL with `undefined: SlotFromFlags`.

- [ ] **Step 3: Implement `Slot` and `SlotFromFlags`**

Add to `internal/rotation/rotation.go`:

```go
// Slot identifies the rotation state slot for an (agent, tag, family)
// combination. Tag and Family are normalized: empty values become "-"
// and special characters (commas, dots) are escaped to underscores.
// The normalization guarantees a predictable state-file name.
type Slot struct {
	Agent, Tag, Family string
}

// SlotFromFlags builds a Slot from the agent name and resolved
// tag/family. Empty tag/family values become "-"; commas and dots are
// escaped to underscores to keep state-file names safe.
func SlotFromFlags(agent, tag, family string) Slot {
	return Slot{
		Agent:  agent,
		Tag:    escapeComponent(tag),
		Family: escapeComponent(family),
	}
}

// escapeComponent normalizes a single slot component: empty -> "-",
// replace commas and dots with underscores.
func escapeComponent(s string) string {
	if s == "" {
		return "-"
	}
	s = strings.ReplaceAll(s, ",", "_")
	s = strings.ReplaceAll(s, ".", "_")
	return s
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rotation -run TestSlotFromFlags -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/rotation/rotation.go internal/rotation/slot_test.go
git commit -m "feat(rotation): add Slot type and SlotFromFlags constructor"
```

---

## Task 2: Replace `Rotation.tag` with `Rotation.slot` and add new state-file path

**Files:**
- Modify: `internal/rotation/rotation.go`.

**Interfaces:**
- Changes: `Rotation.tag string` → `Rotation.slot Slot`.
- Changes: `New(slot Slot, models []config.Model, stateDir string) *Rotation` (replaces the existing `New(tag string, ...)`).
- Adds: `func StateFileForSlot(stateDir string, slot Slot) string` — returns the per-slot file path.
- Keeps: legacy `StateFile(stateDir, tag)` for back-compat reads.

- [ ] **Step 1: Write the failing test**

Append to `internal/rotation/slot_test.go`:

```go
// TestStateFileForSlot locks down the on-disk naming convention. Any
// change here is a state-file migration; reviewers must be loud.
func TestStateFileForSlot(t *testing.T) {
	tests := []struct {
		slot Slot
		want string
	}{
		{Slot{"claude", "code", "-"}, "/tmp/agent-wt/rotation-claude-code-_.state"},
		{Slot{"claude", "code", "gemma4"}, "/tmp/agent-wt/rotation-claude-code-gemma4.state"},
		{Slot{"pi", "design", "-"}, "/tmp/agent-wt/rotation-pi-design-_.state"},
	}
	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			got := StateFileForSlot("/tmp/agent-wt", tc.slot)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rotation -run TestStateFileForSlot -v`
Expected: FAIL with `undefined: StateFileForSlot`.

- [ ] **Step 3: Implement the new constructor and state-file path**

In `internal/rotation/rotation.go`, replace the existing `Rotation` struct and `New`:

```go
// Rotation remembers which model was last launched in a rotation slot.
// The model set is fixed at construction time and must match the
// picker's filtered snapshot. Rotation is driven by the launch
// action: RecordLaunch persists the pick, and LastLaunched +
// FirstAfter compute the cursor position for the next picker entry.
type Rotation struct {
	mu       sync.Mutex
	slot     Slot
	models   []config.Model
	stateDir string
}

// New builds a Rotation for the given slot from the given models.
// stateDir is where the per-slot state file lives; pass "" to use the
// default (~/.config/agent-wt).
func New(slot Slot, models []config.Model, stateDir string) *Rotation {
	return &Rotation{slot: slot, models: models, stateDir: stateDir}
}

// Slot returns the slot this Rotation serves.
func (r *Rotation) Slot() Slot { return r.slot }

// StateDir returns the resolved state directory for this Rotation.
func (r *Rotation) StateDir() string {
	if r.stateDir != "" {
		return r.stateDir
	}
	return defaultStateDir()
}

// StateFileForSlot returns the per-slot state path.
// Format: rotation-<agent>-<tag>-<family>.state
func StateFileForSlot(stateDir string, slot Slot) string {
	return filepath.Join(stateDir, "rotation-"+slot.Agent+"-"+slot.Tag+"-"+slot.Family+".state")
}
```

Keep `StateFile(stateDir, tag)` as-is for back-compat reads (Task 3).

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rotation -run TestStateFileForSlot -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/rotation/rotation.go internal/rotation/slot_test.go
git commit -m "feat(rotation): scope state files to (agent, tag, family) Slot"
```

---

## Task 3: Back-compat read of legacy `rotation-<tag>.state` files

**Files:**
- Modify: `internal/rotation/rotation.go`.

**Interfaces:**
- Updates: `LastLaunched()` to read the per-slot file first; if missing, read the legacy `rotation-<slot.Tag>.state`; if still missing, return `(zero, false)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/rotation/slot_test.go` (or create a separate `back_compat_test.go`):

```go
// TestLastLaunchedBackCompat verifies that a Rotation reads the legacy
// rotation-<tag>.state file when the new per-slot file is missing.
// This protects existing users from losing their rotation state when
// they upgrade.
func TestLastLaunchedBackCompat(t *testing.T) {
	dir := t.TempDir()
	// Write the legacy tag-only file.
	legacyPath := filepath.Join(dir, "rotation-code.state")
	if err := os.WriteFile(legacyPath, []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	models := []config.Model{
		{ID: "claude/opus"},
		{ID: "claude/sonnet"},
	}
	r := New(Slot{"claude", "code", "-"}, models, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("expected ok=true from legacy file")
	}
	if got.ID != "claude/opus" {
		t.Errorf("got %q, want claude/opus", got.ID)
	}
}

// TestLastLaunchedPrefersNewFile verifies that the per-slot file wins
// when both exist.
func TestLastLaunchedPrefersNewFile(t *testing.T) {
	dir := t.TempDir()
	// Legacy file with old model.
	if err := os.WriteFile(filepath.Join(dir, "rotation-code.state"), []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// New per-slot file with a different model.
	newPath := StateFileForSlot(dir, Slot{"claude", "code", "-"})
	if err := os.WriteFile(newPath, []byte("claude/sonnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	models := []config.Model{
		{ID: "claude/opus"},
		{ID: "claude/sonnet"},
	}
	r := New(Slot{"claude", "code", "-"}, models, dir)
	got, ok := r.LastLaunched()
	if !ok {
		t.Fatal("expected ok=true")
	}
	if got.ID != "claude/sonnet" {
		t.Errorf("got %q, want claude/sonnet (from new file)", got.ID)
	}
}
```

(Add `import "os"` and `import "github.com/ohanaverse/agent-worktree/internal/config"` as needed.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rotation -run TestLastLaunched -v`
Expected: FAIL because `LastLaunched` still reads only the legacy file.

- [ ] **Step 3: Update `LastLaunched`**

Replace `lastLaunchedLocked` in `internal/rotation/rotation.go`:

```go
func (r *Rotation) lastLaunchedLocked() (config.Model, bool) {
	// Try the per-slot file first.
	data, err := os.ReadFile(StateFileForSlot(r.StateDir(), r.slot))
	if err == nil {
		return matchLastLaunched(string(data), r.models), true
	}
	// Back-compat: fall back to the legacy rotation-<tag>.state.
	data, err = os.ReadFile(StateFile(r.StateDir(), r.slot.Tag))
	if err == nil {
		return matchLastLaunched(string(data), r.models), true
	}
	return config.Model{}, false
}

// matchLastLaunched parses a state-file body and returns the first
// model ID that matches one in the snapshot. The legacy 2-line format
// is supported by reading the last non-empty line.
func matchLastLaunched(body string, models []config.Model) (config.Model, bool) {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		id := strings.TrimSpace(lines[i])
		if id == "" {
			continue
		}
		for _, m := range models {
			if m.ID == id {
				return m, true
			}
		}
	}
	return config.Model{}, false
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rotation -v`
Expected: PASS for both new tests AND the existing rotation tests.

- [ ] **Step 5: Commit**

```bash
git add internal/rotation/rotation.go internal/rotation/slot_test.go
git commit -m "feat(rotation): back-compat read of legacy rotation-<tag>.state"
```

---

## Task 4: `RecordLaunch` writes per-slot file (legacy untouched)

**Files:**
- Modify: `internal/rotation/rotation.go`.

- [ ] **Step 1: Write the failing test**

Append to back-compat tests:

```go
// TestRecordLaunchWritesNewFile verifies that RecordLaunch writes to
// the new per-slot file and leaves any legacy file untouched.
func TestRecordLaunchWritesNewFile(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "rotation-code.state")
	if err := os.WriteFile(legacyPath, []byte("claude/opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	r := New(Slot{"claude", "code", "-"}, []config.Model{{ID: "claude/sonnet"}}, dir)
	if err := r.RecordLaunch(config.Model{ID: "claude/sonnet"}); err != nil {
		t.Fatal(err)
	}

	// New per-slot file has the new model.
	newPath := StateFileForSlot(dir, Slot{"claude", "code", "-"})
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "claude/sonnet" {
		t.Errorf("new file = %q, want claude/sonnet", strings.TrimSpace(string(data)))
	}

	// Legacy file is unchanged.
	data, err = os.ReadFile(legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(data)) != "claude/opus" {
		t.Errorf("legacy file = %q, want claude/opus (untouched)", strings.TrimSpace(string(data)))
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/rotation -run TestRecordLaunch -v`
Expected: FAIL because `RecordLaunch` still writes to the legacy file.

- [ ] **Step 3: Update `RecordLaunch`**

```go
func (r *Rotation) RecordLaunch(m config.Model) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return config.WriteFileAtomic(
		StateFileForSlot(r.StateDir(), r.slot),
		[]byte(m.ID+"\n"),
		0o600,
	)
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/rotation -v`
Expected: all tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/rotation/rotation.go internal/rotation/slot_test.go
git commit -m "feat(rotation): RecordLaunch writes per-slot file"
```

---

## Task 5: Keep `wt rotate <tag>` working via the new API

**Files:**
- Modify: `cmd/wt/commands.go`.

**Interfaces:**
- `rotateCmd` keeps its CLI signature (`wt rotate <tag>`); internally it builds a `Slot` with empty agent/family (rotation scoped by tag only, as today).

- [ ] **Step 1: Update `rotateCmd`**

```go
RunE: func(cmd *cobra.Command, args []string) error {
    tag := args[0]
    models := a.cfg.ModelsWithTag(tag)
    if len(models) == 0 {
        return fmt.Errorf("no models tagged %q", tag)
    }
    // Legacy CLI shape: rotate is scoped by tag only. We use an empty
    // agent/family so the state-file name matches legacy behavior
    // (rotation-<tag>.state via the back-compat read path).
    slot := rotation.Slot{Agent: "-", Tag: tag, Family: "-"}
    r := rotation.New(slot, models, "")
    last, ok := r.LastLaunched()
    if !ok {
        fmt.Println(models[0].ID)
        return nil
    }
    next, _ := rotation.FirstAfter(models, last)
    fmt.Println(next.ID)
    return nil
},
```

- [ ] **Step 2: Run the test**

Run: `go test ./cmd/wt -v`
Expected: existing tests pass.

- [ ] **Step 3: Commit**

```bash
git add cmd/wt/commands.go
git commit -m "refactor(wt): rotateCmd uses new Slot API"
```

---

## Self-Review

- [x] **Spec coverage:** PR 3a covers "Rotation scoping" and "Migration / back-compat" from the spec. TUI/launch path migration is PR 3b.
- [x] **Placeholder scan:** No TBDs.
- [x] **Type consistency:** `Slot{Agent, Tag, Family string}` is consistent across tasks; `SlotFromFlags` always returns a fully populated Slot; `New(slot Slot, models, stateDir)` matches.
- [x] **Back-compat note:** `wt rotate <tag>` keeps its CLI; legacy tag-only files remain readable; new writes go to per-slot files only.
- [x] **Migration path:** PR 3b consumes the new API in TUI + non-TUI launch paths. PR 3a does not break existing behavior.
