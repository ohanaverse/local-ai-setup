# wt: stats and surveys for any agent-model pair — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make wt's usage counts per agent-model pair and make usage/survey recording work for models that are not in the registry (discovered local models).

**Architecture:** Add an optional `agent` field to usage events plus additive `RecordFor`/`CountsForAgent` APIs (existing `Record`/`Counts` keep working, so the ~20 existing call sites and mocks are untouched). Thread the agent name through `rotation.RecordFor` from both launch paths. Add a `config.DiscoveredModelID` helper that defines the stat id for unregistered models, and prove both stores accept it. Retention stays 30 days (already enforced in both stores).

**Tech Stack:** Go 1.26 (module root `wt/`), stdlib `testing`, JSONL stores.

**Spec:** `docs/superpowers/specs/2026-09-19-wt-stats-any-pair-design.md`

## Global Constraints

- Retention for usage and survey stays `30 * 24 * time.Hour`, pruned on every write. Do not change it.
- Legacy usage lines (no `agent`) count toward model-level `Counts` only, never toward `CountsForAgent`.
- Discovered-model stat id format is exactly `<provider>/<on-disk artifact name>` (e.g. `omlx/Qwen3.8-27B-4bit`); a registry match keeps the registry id. No alias table.
- Survey store schema does not change.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/` (module root). Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and the attribution line `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- Execute in an isolated worktree (superpowers:using-git-worktrees), never with `main` checked out in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/usage/usage.go` | Modify: `event.Agent`, `RecordFor`, `CountsForAgent`, shared scan helper, `Store` interface gains `RecordFor` |
| `wt/internal/usage/usage_test.go` | Modify: new tests |
| `wt/internal/rotation/rotation.go` | Modify: `RecordFor(agent, modelID)`; `Record` becomes a wrapper |
| `wt/internal/rotation/rotation_test.go` | Modify: new test |
| `wt/internal/tui/model_line_test.go` | Modify: `mockStore` gains `RecordFor` (keeps satisfying `usage.Store`) |
| `wt/cmd/wt/launch.go` | Modify: line ~176 uses `RecordFor(agent, m.ID)` |
| `wt/internal/tui/app.go` | Modify: `launchAndRecord` (~line 844) uses `RecordFor(m.agent, ...)` |
| `wt/cmd/wt/launch_test.go`, `wt/internal/tui/agent_model_test.go` | Modify: assert a per-agent usage event is recorded |
| `wt/internal/config/config.go` | Modify: add `DiscoveredModelID` |
| `wt/internal/config/config_test.go` | Modify: test for it |
| `wt/internal/survey/prompt_test.go` | Modify: unregistered-model survey test |
| `wt/CLAUDE.md` | Modify: document new behavior |

---

### Task 1: Usage store — agent field, `RecordFor`, `CountsForAgent`

**Files:**
- Modify: `wt/internal/usage/usage.go`
- Test: `wt/internal/usage/usage_test.go`

**Interfaces:**
- Consumes: existing `StoreImpl`, `event`, `now` seam, `pruneOlderThan`.
- Produces:
  - `func (s *StoreImpl) RecordFor(agent, modelID string) error`
  - `func (s *StoreImpl) CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts`
  - `Store` interface becomes `{ Record(modelID string) error; RecordFor(agent, modelID string) error; Counts(modelIDs []string) map[string]UsageCounts }`
  - `Record(modelID)` remains and equals `RecordFor("", modelID)`; `Counts` unchanged in behavior.

- [ ] **Step 1: Write the failing tests** (append to `usage_test.go`)

```go
// TestRecordForScopesCountsToAgent verifies CountsForAgent counts only
// events recorded for that agent, while Counts still totals every agent
// (and legacy agent-less lines). This is what lets the selector show
// per-pair launch counts without losing model-level totals.
func TestRecordForScopesCountsToAgent(t *testing.T) {
	store := NewStoreAt(t.TempDir())
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	for _, rec := range [][2]string{{"claude", "m"}, {"claude", "m"}, {"codex", "m"}, {"", "m"}} {
		if err := store.RecordFor(rec[0], rec[1]); err != nil {
			t.Fatalf("RecordFor(%q): %v", rec[0], err)
		}
	}

	if got := store.CountsForAgent("claude", []string{"m"})["m"]; got != (UsageCounts{2, 2, 2}) {
		t.Errorf("claude pair = %+v, want {2 2 2}", got)
	}
	if got := store.CountsForAgent("codex", []string{"m"})["m"]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("codex pair = %+v, want {1 1 1}", got)
	}
	if got := store.Counts([]string{"m"})["m"]; got != (UsageCounts{4, 4, 4}) {
		t.Errorf("model-level = %+v, want {4 4 4} (all agents + agentless)", got)
	}
}

// TestCountsForAgentIgnoresLegacyLinesAndEmptyAgent verifies a usage line
// written before the agent field existed (and an empty agent argument)
// never count toward any pair. Guards against inflating a pair's numbers
// with history that cannot be attributed to it.
func TestCountsForAgentIgnoresLegacyLinesAndEmptyAgent(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	fixed := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return fixed }
	defer func() { now = time.Now }()

	legacy := `{"model_id":"m","timestamp":"2026-09-19T11:00:00Z"}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "usage.jsonl"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := store.CountsForAgent("claude", []string{"m"})["m"]; got != (UsageCounts{}) {
		t.Errorf("claude = %+v, want zero (legacy line has no agent)", got)
	}
	if got := store.CountsForAgent("", []string{"m"})["m"]; got != (UsageCounts{}) {
		t.Errorf("empty agent = %+v, want zero", got)
	}
	if got := store.Counts([]string{"m"})["m"]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("model-level = %+v, want {1 1 1} (legacy still counts)", got)
	}
}

// TestRecordForUnregisteredModelAndPrune verifies a model id that exists in
// no registry (a discovered on-disk model) records, reads back, and is
// pruned at the 30-day window like any other id — the store must never
// depend on registry membership.
func TestRecordForUnregisteredModelAndPrune(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreAt(dir)
	const id = "omlx/Qwen3.8-27B-4bit"

	old := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	now = func() time.Time { return old }
	if err := store.RecordFor("claude", id); err != nil {
		t.Fatal(err)
	}
	fresh := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC) // 49 days later
	now = func() time.Time { return fresh }
	defer func() { now = time.Now }()
	if err := store.RecordFor("claude", id); err != nil {
		t.Fatal(err)
	}

	if got := store.CountsForAgent("claude", []string{id})[id]; got != (UsageCounts{1, 1, 1}) {
		t.Errorf("counts = %+v, want {1 1 1} (49-day-old event pruned)", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run (from `wt/`): `go test ./internal/usage -run 'RecordFor|CountsForAgent' -v`
Expected: FAIL to compile — `RecordFor`/`CountsForAgent` undefined.

- [ ] **Step 3: Implement**

In `usage.go`:

1. Add the field: `Agent string \`json:"agent,omitempty"\`` to `event` (after `ModelID`).
2. Extend the interface:
```go
type Store interface {
	Record(modelID string) error
	RecordFor(agent, modelID string) error
	Counts(modelIDs []string) map[string]UsageCounts
}
```
3. Rename the existing `Record` body to `RecordFor(agent, modelID string)`, building `e := event{ModelID: modelID, Agent: agent, Timestamp: now().UTC()}`. Keep the doc comment and locking unchanged. Add:
```go
// Record appends a launch event with no agent attribution. Prefer RecordFor;
// Record remains for callers that only know the model.
func (s *StoreImpl) Record(modelID string) error { return s.RecordFor("", modelID) }
```
4. Refactor `Counts` into a shared scanner and add `CountsForAgent`:
```go
func (s *StoreImpl) Counts(modelIDs []string) map[string]UsageCounts {
	return s.countsWhere(modelIDs, func(event) bool { return true })
}

// CountsForAgent is Counts scoped to one agent. Events without an agent
// (legacy lines, or Record) never match, and an empty agent matches nothing.
func (s *StoreImpl) CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts {
	return s.countsWhere(modelIDs, func(ev event) bool { return agent != "" && ev.Agent == agent })
}

func (s *StoreImpl) countsWhere(modelIDs []string, match func(event) bool) map[string]UsageCounts {
	// body of the old Counts, with `if !want[ev.ModelID] || !match(ev) { continue }`
}
```
Keep the old `Counts` doc comment (best-effort semantics) on `countsWhere`.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/usage -v` — Expected: all PASS (existing tests included).
Then `go vet ./... 2>&1 | head` — expect failures only for `usage.Store` implementers (`mockStore`, fixed in Task 2). Confirm none in `internal/usage`.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal/usage
git add wt/internal/usage
git commit -m "feat(usage): per-agent events, RecordFor and CountsForAgent - completes plan item #1"
```

---

### Task 2: Rotation `RecordFor` and mock update

**Files:**
- Modify: `wt/internal/rotation/rotation.go` (Record, ~line 51-63)
- Modify: `wt/internal/tui/model_line_test.go` (`mockStore`)
- Test: `wt/internal/rotation/rotation_test.go`

**Interfaces:**
- Consumes: `usage.Store.RecordFor(agent, modelID string) error` (Task 1).
- Produces: `func (r *Rotation) RecordFor(agent, modelID string) error`; `Record(modelID)` remains as `RecordFor("", modelID)`.

- [ ] **Step 1: Write the failing test** (append to `rotation_test.go`; add imports `usage` if missing)

```go
// TestRecordForAttributesUsageToAgent verifies RecordFor writes the same
// rotation state as Record and also records a usage event tagged with the
// agent, so launch history is attributable to an agent-model pair.
func TestRecordForAttributesUsageToAgent(t *testing.T) {
	dir := t.TempDir()
	r := NewAt(dir)
	if err := r.RecordFor("claude", "omlx/Qwen3.8-27B-4bit"); err != nil {
		t.Fatalf("RecordFor: %v", err)
	}
	if last, _ := r.Last(); last != "omlx/Qwen3.8-27B-4bit" {
		t.Errorf("Last = %q", last)
	}
	got := usage.NewStoreAt(dir).CountsForAgent("claude", []string{"omlx/Qwen3.8-27B-4bit"})
	if got["omlx/Qwen3.8-27B-4bit"].ThirtyDay != 1 {
		t.Errorf("claude pair 30d = %d, want 1", got["omlx/Qwen3.8-27B-4bit"].ThirtyDay)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/rotation -run RecordFor -v` — Expected: FAIL, `RecordFor` undefined.

- [ ] **Step 3: Implement**

In `rotation.go`, rename the current `Record` to `RecordFor(agent, modelID string)`, replacing `_ = r.store.Record(modelID)` with `_ = r.store.RecordFor(agent, modelID)`, and add:
```go
// Record is RecordFor with no agent attribution.
func (r *Rotation) Record(modelID string) error { return r.RecordFor("", modelID) }
```
In `wt/internal/tui/model_line_test.go`, add to `mockStore`:
```go
func (s *mockStore) RecordFor(agent, modelID string) error { return nil }
```

- [ ] **Step 4: Run to verify pass**

Run: `go vet ./... && go test ./internal/rotation ./internal/usage ./internal/tui` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal cmd
git add wt/internal/rotation wt/internal/tui/model_line_test.go
git commit -m "feat(rotation): RecordFor threads agent into usage events - completes plan item #2"
```

---

### Task 3: Launch paths record the agent

**Files:**
- Modify: `wt/cmd/wt/launch.go` (~line 176)
- Modify: `wt/internal/tui/app.go` (`launchAndRecord`, ~line 844)
- Test: `wt/cmd/wt/launch_test.go`, `wt/internal/tui/agent_model_test.go`

**Interfaces:**
- Consumes: `(*Rotation).RecordFor(agent, modelID string) error` (Task 2); `usage.NewStoreAt(dir).CountsForAgent`.
- Produces: every real launch writes a usage event with the launching agent.

- [ ] **Step 1: Write the failing tests**

In `launch_test.go`, extend the setup of `TestLaunchFilteredRecordsRefcount` by adding a sibling test (copy its fixture; add import `.../internal/usage`):

```go
// TestLaunchFilteredRecordsUsageForAgent verifies the non-TUI launch path
// records a usage event tagged with the launching agent, so per-pair 1d/7d/30d
// counts include launches made through -W/--cwd.
func TestLaunchFilteredRecordsUsageForAgent(t *testing.T) {
	// ... identical fixture to TestLaunchFilteredRecordsRefcount ...
	store := usage.NewStoreAt(filepath.Join(dir, "agent-wt"))
	if got := store.CountsForAgent("claude", []string{"claude/opus"})["claude/opus"]; got.ThirtyDay != 1 {
		t.Fatalf("claude pair 30d = %d, want 1", got.ThirtyDay)
	}
	if got := store.CountsForAgent("codex", []string{"claude/opus"})["claude/opus"]; got.ThirtyDay != 0 {
		t.Fatalf("codex pair 30d = %d, want 0", got.ThirtyDay)
	}
}
```
(Write the fixture out in full — do not leave the comment.)

In `agent_model_test.go`, inside `TestLaunchAndRecordWritesLast` after the refcount assertion, set `m.agent = "claude"` before calling `launchAndRecord` (phaseModelWithList already sets it for `"claude"`; confirm) and add:
```go
	if u := usage.NewStoreAt(dir).CountsForAgent("claude", []string{first.model.ID})[first.model.ID]; u.ThirtyDay != 1 {
		t.Fatalf("usage claude pair 30d = %d, want 1", u.ThirtyDay)
	}
```
Update the test's `//` comment to mention the usage assertion; add the `usage` import.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/wt -run UsageForAgent -v; go test ./internal/tui -run LaunchAndRecordWritesLast -v` — Expected: FAIL (pair count 0).

- [ ] **Step 3: Implement**

`launch.go`: `rotation.New().Record(m.ID)` -> `rotation.New().RecordFor(agent, m.ID)`.
`app.go`: `rotation.New().Record(m.launchModel.ID)` -> `rotation.New().RecordFor(m.agent, m.launchModel.ID)`. If `m.agent` is empty on any path that reaches `launchAndRecord` (e.g. `-A` pinned), find where the pinned agent is stored and use that value instead; add a test line for that path.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./... && go vet ./...` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l cmd internal
git add wt/cmd wt/internal
git commit -m "feat(launch): record usage per agent in TUI and non-TUI paths - completes plan item #3"
```

---

### Task 4: Discovered-model stat id + unregistered-model survey

**Files:**
- Modify: `wt/internal/config/config.go` (add function near `Model` helpers, ~line 505)
- Test: `wt/internal/config/config_test.go`, `wt/internal/survey/prompt_test.go`

**Interfaces:**
- Consumes: `survey.PromptRun(r, w, store, agent, m config.Model)`.
- Produces: `func DiscoveredModelID(providerID, artifactName string) string` — returns `providerID + "/" + artifactName`. Sub-projects 2 and 4 use it to build the `config.Model` for an on-disk model with no registry entry.

- [ ] **Step 1: Write the failing tests**

`config_test.go`:
```go
// TestDiscoveredModelID pins the stat-id format for on-disk models with no
// registry entry: usage/survey history is keyed on this string, so changing
// it silently orphans every recorded discovered-model launch.
func TestDiscoveredModelID(t *testing.T) {
	if got := DiscoveredModelID("omlx", "Qwen3.8-27B-4bit"); got != "omlx/Qwen3.8-27B-4bit" {
		t.Errorf("got %q", got)
	}
	if got := DiscoveredModelID("mtplx", "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"); got != "mtplx/Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality" {
		t.Errorf("got %q", got)
	}
}
```
`prompt_test.go`:
```go
// TestPromptRunUnregisteredModel verifies the survey records a verdict for a
// model that exists in no registry (a discovered on-disk model): the store
// keys on the id string only, so surveys work for every agent-model pair.
func TestPromptRunUnregisteredModel(t *testing.T) {
	withTTY(t, true)
	store := NewStoreAt(t.TempDir())
	id := config.DiscoveredModelID("omlx", "Qwen3.8-27B-4bit")
	m := config.Model{ID: id, ProviderID: "omlx", ModelName: "Qwen3.8-27B-4bit"}
	var out bytes.Buffer
	PromptRun(strings.NewReader("y\n4\n5\nsmoke\n"), &out, store, "claude", m)

	events := store.Events()
	if len(events) != 1 || events[0].ModelID != id || events[0].Agent != "claude" {
		t.Fatalf("Events() = %+v, want one claude event for %q", events, id)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config -run DiscoveredModelID -v` — Expected: FAIL, undefined.

- [ ] **Step 3: Implement** (in `config.go`)

```go
// DiscoveredModelID is the usage/survey id for a local model found on disk
// with no registry entry: "<provider>/<artifact name as the provider lists
// it>". Registry-matched models keep their registry id instead; callers do
// that lookup before falling back to this.
func DiscoveredModelID(providerID, artifactName string) string {
	return providerID + "/" + artifactName
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config ./internal/survey -v` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/config wt/internal/survey
git commit -m "feat(config): DiscoveredModelID stat key; survey works for unregistered models - completes plan item #4"
```

---

### Task 5: Documentation

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Edit `wt/CLAUDE.md`**
  - In the `internal/usage/` table row: append "; events carry an optional `agent` (`RecordFor`); `CountsForAgent` gives per-agent-model-pair counts, legacy agent-less lines count toward `Counts` only".
  - In the Rotation section's usage-history bullet: note launch paths call `rotation.RecordFor(agent, id)`, which also records the agent-tagged usage event.
  - In "Session survey (Go)": add a bullet: "Surveys and usage key on a free-form model id and never require a registry entry. Unregistered on-disk models use `config.DiscoveredModelID(provider, artifact)`; a registry match keeps its registry id."

- [ ] **Step 2: Verify**

Run (from repo root): `make check-links && git grep -n "RecordFor" wt/CLAUDE.md` — Expected: links OK, matches present.

- [ ] **Step 3: Final verification**

Run (from `wt/`): `go build ./... && go vet ./... && go test ./... && gofmt -l .` — Expected: all pass, `gofmt -l` prints nothing.

- [ ] **Step 4: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document per-pair usage and discovered-model stat ids - completes plan item #5"
```

---

## Self-Review

- **Spec coverage:** identity scheme -> Task 4 (`DiscoveredModelID`; registry-match branch is sub-project 2's matcher, noted in the helper doc); usage `agent` field, `RecordFor`, `CountsForAgent`, legacy handling, 30d prune -> Task 1; survey no schema change + unregistered model -> Task 4; launch paths (TUI + non-TUI) record -> Task 3; CLAUDE.md update -> Task 5.
- **Deliberate deviation from spec:** the spec said `Record(modelID)` "becomes `Record(agent, modelID)`". The plan keeps `Record` and adds `RecordFor` instead: same behavior, but avoids editing ~20 existing call sites and test mocks. Flag this at review.
- **Placeholders:** Task 3's launch_test fixture must be written out in full by copying `TestLaunchFilteredRecordsRefcount` (stated explicitly).
- **Type consistency:** `RecordFor(agent, modelID string) error` and `CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts` are used identically in Tasks 1-4.
