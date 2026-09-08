# `wt stats` sort order implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Simplify `wt stats` so its output is sorted by agent name with the per-model `(all)` aggregate rows first, then by model id.

**Architecture:** A single code change in the row comparator plus a regression test. `(all)` sorts first naturally in lexicographic order because `(` precedes letters.

**Tech Stack:** Go 1.26.7, `testing` package, existing `wt/cmd/wt/stats_test.go` patterns.

---

## File structure

- **Modify:** `wt/cmd/wt/stats.go` — simplify `sort.SliceStable` comparator in `buildStatsRows`.
- **Modify:** `wt/cmd/wt/stats_test.go` — add regression test for sort order.
- **Modify:** `wt/docs/wt-stats.md` — update sort description and example table.
- **No changes:** `wt/internal/survey/stats.go` (aggregation unchanged).

---

### Task 1: Add regression test for sort order

**Files:**
- Modify: `wt/cmd/wt/stats_test.go`

- [ ] **Step 1: Write the failing test**

Add the following test to `wt/cmd/wt/stats_test.go` after the existing tests:

```go
// TestStatsCmdSortsByModelThenAgentWithAllFirst verifies that wt stats
// prints rows sorted by agent name with "(all)" first, then by model id.
func TestStatsCmdSortsByModelThenAgentWithAllFirst(t *testing.T) {
	a, tmp := newTestApp(t)
	now := time.Now()
	seedSurveyEvents(t, tmp, []survey.Event{
		{Agent: "codex", ModelID: "model-b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "codex", ModelID: "model-a", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
		{Agent: "claude", ModelID: "model-b", Timestamp: now.Add(-1 * time.Hour), Worked: boolPtr(true)},
	})

	cmd := statsCmd(a)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	// Extract ordered (model, agent) pairs from the rendered table.
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	var got []string
	for _, line := range lines {
		if !strings.HasPrefix(line, "│") {
			continue
		}
		fields := strings.FieldsFunc(line, func(r rune) bool { return r == '│' || r == ' ' })
		if len(fields) < 2 {
			continue
		}
		// Skip header row and divider-only rows.
		if fields[0] == "MODEL" || strings.Contains(fields[0], "─") {
			continue
		}
		got = append(got, fields[0]+"|"+fields[1])
	}

	want := []string{
		"model-a|(all)",
		"model-b|(all)",
		"model-a|claude",
		"model-b|claude",
		"model-a|codex",
		"model-b|codex",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("row order = %v, want %v\noutput:\n%s", got, want, out.String())
	}
}
```

Add the required import at the top of the test file if not already present:

```go
"reflect"
```

- [ ] **Step 2: Run test to verify it fails**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
go test ./cmd/wt -run TestStatsCmdSortsByModelThenAgentWithAllFirst -v
```

Expected: FAIL before the comparator change. The test documents the desired ordering.

- [ ] **Step 3: Commit the test**

```bash
git add wt/cmd/wt/stats_test.go
git commit -m "test(wt stats): add regression test for agent/model sort order"
```

---

### Task 2: Simplify the row comparator

**Files:**
- Modify: `wt/cmd/wt/stats.go`

- [ ] **Step 1: Replace the comparator**

In `wt/cmd/wt/stats.go`, locate the `sort.SliceStable` call in `buildStatsRows` and replace it with the agent-first lexicographic comparator:

```go
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].Agent != rows[j].Agent {
			return rows[i].Agent < rows[j].Agent
		}
		return rows[i].ModelID < rows[j].ModelID
	})
```

- [ ] **Step 2: Remove now-unused sentinel handling in comparator**

The `statsAllAgents` constant is still used for row labels, so keep it. Only the two `if rows[i].Agent == statsAllAgents` and `if rows[j].Agent == statsAllAgents` branches inside the comparator are removed by the replacement above.

- [ ] **Step 3: Run the stats tests**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
go test ./cmd/wt -run TestStatsCmd -v
```

Expected: all `TestStatsCmd*` tests pass, including the new sort-order test.

- [ ] **Step 4: Run the full Go test suite**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
go test ./...
```

Expected: PASS across all packages.

- [ ] **Step 5: Build the binary**

Run:

```bash
cd /Users/keith/github/ohanaverse/local-ai-setup/wt
go build ./...
```

Expected: no compilation errors.

- [ ] **Step 6: Update docs**

Update `wt/docs/wt-stats.md`:
- Change the Output section sort description to: "sorted by agent name with `(all)` first, then by model id."
- Update the example table to show at least two models under `(all)` and a real agent so the new ordering is visually obvious.

- [ ] **Step 7: Commit the comparator and doc changes**

```bash
git add wt/cmd/wt/stats.go wt/docs/wt-stats.md
git commit -m "refactor(wt stats): sort by agent then model with (all) first"
```

---

### Task 3: Optional manual verification

**Files:**
- None

- [ ] **Step 1: Run against real data if available**

If the machine has survey data, run the installed binary:

```bash
wt stats --window 30d | head -30
```

Confirm visually that:
- `(all)` rows appear first, sorted by model id.
- Per-agent sections follow, sorted by agent name.
- Within each agent section, models are in ascending alphabetical order.

- [ ] **Step 2: Record outcome**

If the manual run is performed, note in the commit message or PR description that the sort order was verified against live data. If no survey data exists, the unit test coverage is sufficient.

---

## Self-review

1. **Spec coverage:** The spec calls for (a) simplifying the sort comparator — Task 2; (b) adding a regression test — Task 1; (c) doc update — Task 2 Step 6; (d) test verification — Task 2 Step 4 and Task 3.
2. **Placeholder scan:** No TBDs or vague steps. All code blocks contain concrete content.
3. **Type consistency:** Uses existing `statsRow`, `survey.Event`, and `renderTable` types. No new types introduced.
