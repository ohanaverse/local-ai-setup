# wt Flow Cleanup — PR 4: Worktree Picker Full View + Default-Branch Guarantee

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** The worktree picker always presents at least one branch row (the default branch) plus the `+ New worktree…` sentinel. Rows are ordered: sentinel → local branches and worktrees alphabetical → remote-only branches alphabetical with a separator. Picker is skipped when `-W`, `--cwd`, or non-git-repo conditions hold.

**Architecture:** `worktree.Enumerate` returns three ordered groups (worktrees, local branches, remote branches) instead of a flat mixed slice. The default branch is always emitted as a `TypeBranch` row, even when checked out in a worktree. The TUI's `buildList` renders the groups in order with a separator between locals and remotes, plus `(default)` and `(current)` markers on relevant rows.

**Tech Stack:** Go 1.26.3, Bubble Tea, `bubbles/list`.

**Spec:** `docs/superpowers/specs/2026-08-18-wt-flow-cleanup-design.md` (section "Worktree Picker 'From Any Worktree' (PR 4)" — final version with picker-skip conditions and picker-content invariants).

## Global Constraints

- Go 1.26.3.
- Test convention: top-level `//` comment on every `Test*` (lesson 18).
- Run `go test ./...` before each commit.
- PR 3b is already merged; this PR is independent of rotation/picker filter changes.

---

## File Structure (this PR)

### Created

(none — all changes are to existing files)

### Modified

- `internal/worktree/enumerate.go` — return three ordered groups; default branch always emitted as `TypeBranch`.
- `internal/worktree/enumerate_test.go` — add default-branch-always-present tests; add ordering tests.
- `internal/tui/worktree_list.go` — `entryItem.Description` adds `(default)` and `(current)` markers; `buildList` renders sentinel + locals alphabetical + remotes alphabetical with separator.
- `internal/tui/app.go` — picker-skip conditions verified at CLI level (PR 1 already did this; PR 4 just adds tests).

### Untouched

- `internal/config/*`, `internal/rotation/*`, `internal/agents/*` — none of these change.
- `cmd/wt/launch.go` — non-TUI path unchanged.
- `internal/tui/model_list.go`, `agent_picker.go` — picker layers above the worktree list are unchanged.

---

## Task 1: `Enumerate` returns three ordered groups; default branch always present

**Files:**
- Modify: `internal/worktree/enumerate.go`.
- Modify: `internal/worktree/enumerate_test.go`.

**Interfaces:**
- Changes: `Enumerate(dir, cwdRoot string) ([]EntryGroup, error)`. `EntryGroup` is a new type with a `Kind` discriminator (`GroupWorktrees` | `GroupLocalBranches` | `GroupRemoteBranches`) and `Entries []Entry`.

- [ ] **Step 1: Write the failing test**

Append to `internal/worktree/enumerate_test.go`:

```go
// TestEnumerateReturnsGroups verifies the new three-group return
// shape. The picker relies on this ordering to render rows without
// re-sorting.
func TestEnumerateReturnsGroups(t *testing.T) {
    dir := setupTestRepo(t) // helper that builds a temp git repo with main + feature + a remote branch
    groups, err := Enumerate(dir, dir)
    if err != nil {
        t.Fatal(err)
    }
    if len(groups) != 3 {
        t.Fatalf("got %d groups, want 3 (worktrees, local branches, remote branches)", len(groups))
    }
    if groups[0].Kind != GroupWorktrees {
        t.Errorf("group[0] = %v, want GroupWorktrees", groups[0].Kind)
    }
    if groups[1].Kind != GroupLocalBranches {
        t.Errorf("group[1] = %v, want GroupLocalBranches", groups[1].Kind)
    }
    if groups[2].Kind != GroupRemoteBranches {
        t.Errorf("group[2] = %v, want GroupRemoteBranches", groups[2].Kind)
    }
}

// TestEnumerateAlwaysIncludesDefaultBranch verifies the picker-content
// invariant: the default branch is always listed as a branch row, even
// when checked out in a worktree or when it's also the current branch.
func TestEnumerateAlwaysIncludesDefaultBranch(t *testing.T) {
    dir := setupTestRepo(t)
    groups, err := Enumerate(dir, dir)
    if err != nil {
        t.Fatal(err)
    }
    var foundAsBranch, foundAsWorktree bool
    for _, g := range groups {
        for _, e := range g.Entries {
            if e.Branch == "main" {
                switch g.Kind {
                case GroupLocalBranches:
                    foundAsBranch = true
                case GroupWorktrees:
                    foundAsWorktree = true
                }
            }
        }
    }
    if !foundAsBranch {
        t.Error("default branch 'main' missing from local branches group")
    }
    _ = foundAsWorktree // may or may not be true depending on test repo layout
}

// TestEnumerateOrdering verifies that within each group, entries are
// sorted alphabetically by branch name (with remote prefix stripped
// for the remote group).
func TestEnumerateOrdering(t *testing.T) {
    dir := setupTestRepoWithManyBranches(t) // creates main, alpha, beta, gamma + origin/beta
    groups, err := Enumerate(dir, dir)
    if err != nil {
        t.Fatal(err)
    }
    for _, g := range groups {
        for i := 1; i < len(g.Entries); i++ {
            if g.Entries[i-1].Branch > g.Entries[i].Branch {
                t.Errorf("group %v not sorted: %q > %q", g.Kind, g.Entries[i-1].Branch, g.Entries[i].Branch)
                break
            }
        }
    }
}
```

(Implement the test repo helpers inline; they likely already exist in some form in the existing test file.)

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/worktree -run TestEnumerateReturns -v`
Expected: FAIL with `undefined: EntryGroup`.

- [ ] **Step 3: Refactor `Enumerate` to return groups**

Replace the body of `Enumerate` in `internal/worktree/enumerate.go`:

```go
// GroupKind identifies a logical group of picker rows.
type GroupKind int

const (
    GroupWorktrees GroupKind = iota
    GroupLocalBranches
    GroupRemoteBranches
)

// EntryGroup is one ordered slice of entries in the picker. The picker
// renders groups in order (worktrees, locals, remotes) with a
// separator between locals and remotes.
type EntryGroup struct {
    Kind    GroupKind
    Entries []Entry
}

// Enumerate returns pickable targets grouped by kind: worktrees
// first, then local branches, then remote branches. The default
// branch is always emitted as a TypeBranch row (even when also
// checked out in a worktree) so the picker is never empty for that
// reason alone.
func Enumerate(dir, cwdRoot string) ([]EntryGroup, error) {
    worktreeEntries, err := listWorktrees(dir, cwdRoot)
    if err != nil {
        return nil, err
    }
    used := inUse(worktreeEntries)
    db, _ := DefaultBranch(dir)

    local, err := listLocalBranches(dir)
    if err != nil {
        return nil, err
    }
    localSet := make(map[string]bool, len(local))
    var localEntries []Entry
    for _, b := range local {
        localSet[b] = true
        // Always emit the default branch as a TypeBranch row.
        if b == db || !used[b] {
            localEntries = append(localEntries, Entry{Type: TypeBranch, Branch: b})
        }
    }

    remotes, err := listRemoteBranches(dir)
    if err != nil {
        return nil, err
    }
    var remoteEntries []Entry
    for _, r := range remotes {
        short := r[strings.IndexByte(r, '/')+1:]
        if localSet[short] {
            continue
        }
        if !used[r] {
            remoteEntries = append(remoteEntries, Entry{Type: TypeBranch, Branch: r})
        }
    }

    // Sort each group by branch name (with remote prefix stripped for remotes).
    sortByBranch := func(es []Entry, stripRemote bool) {
        sort.Slice(es, func(i, j int) bool {
            a, b := es[i].Branch, es[j].Branch
            if stripRemote {
                if i := strings.IndexByte(a, '/'); i >= 0 { a = a[i+1:] }
                if i := strings.IndexByte(b, '/'); i >= 0 { b = b[i+1:] }
            }
            return a < b
        })
    }
    sortByBranch(worktreeEntries, false)
    sortByBranch(localEntries, false)
    sortByBranch(remoteEntries, true)

    return []EntryGroup{
        {Kind: GroupWorktrees, Entries: worktreeEntries},
        {Kind: GroupLocalBranches, Entries: localEntries},
        {Kind: GroupRemoteBranches, Entries: remoteEntries},
    }, nil
}
```

Add `"sort"` to imports if not present.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./internal/worktree -v`
Expected: new tests PASS; existing tests that called `Enumerate(...)` and indexed into a flat slice will FAIL — fix them in Task 2.

- [ ] **Step 5: Commit**

```bash
git add internal/worktree/enumerate.go internal/worktree/enumerate_test.go
git commit -m "refactor(worktree): Enumerate returns three ordered groups"
```

---

## Task 2: Update all callers of `Enumerate` for the new shape

**Files:**
- Modify: `internal/tui/app.go` (`loadEntriesCmd` and `entriesLoadedMsg` handlers).
- Modify: `internal/tui/worktree_list.go` (`buildList` consumes the groups).
- Modify: any other call sites (`grep -rn "worktree.Enumerate" .`).

- [ ] **Step 1: Find all callers**

```bash
grep -rn "worktree.Enumerate" /Users/keith/github/ohanaverse/agent-worktree
```

- [ ] **Step 2: Update each caller's iteration**

For each caller, change `entries := worktree.Enumerate(...)` to iterate `groups, _ := worktree.Enumerate(...)` and accumulate entries from each group. Most callers just want a flat slice for the picker; the picker rebuild happens in Task 3.

In `internal/tui/app.go`'s `loadEntriesCmd`:

```go
func loadEntriesCmd() tea.Cmd {
    return func() tea.Msg {
        root, err := worktree.RepoRoot()
        if err != nil {
            return entriesLoadedMsg{err: err}
        }
        groups, err := worktree.Enumerate(root, root)
        defaultBranch, _ := worktree.DefaultBranch(root)
        return entriesLoadedMsg{groups: groups, defaultBranch: defaultBranch, repoRoot: root, err: err}
    }
}
```

Update `entriesLoadedMsg`:

```go
type entriesLoadedMsg struct {
    groups        []worktree.EntryGroup
    defaultBranch string
    repoRoot      string
    err           error
}
```

In the `entriesLoadedMsg` handler, flatten `groups` into `m.entries` for the existing single-list picker (the picker rebuild happens in Task 3):

```go
case entriesLoadedMsg:
    // ...
    m.entries = flattenGroups(msg.groups)
    m.defaultBranch = msg.defaultBranch
    m.repoRoot = msg.repoRoot
    m.list = buildList(msg.groups, msg.defaultBranch, m.repoRoot, m.width-2, m.height-2)
    // ...
```

(`flattenGroups` is a small helper; see Task 3.)

- [ ] **Step 3: Run all tests**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add internal/tui/app.go internal/tui/worktree_list.go
git commit -m "refactor(tui): worktree picker consumes grouped Enumerate output"
```

---

## Task 3: Render picker with sentinel + locals alphabetical + remotes alphabetical

**Files:**
- Modify: `internal/tui/worktree_list.go`.
- Modify: `internal/tui/worktree_list_test.go`.

**Interfaces:**
- Updates: `buildList(groups []worktree.EntryGroup, defaultBranch, repoRoot string, width, height int) list.Model`.

- [ ] **Step 1: Write the failing test**

Append to `internal/tui/worktree_list_test.go`:

```go
// TestBuildListOrdering verifies the picker renders sentinel first,
// then locals alphabetical, then a separator row, then remotes
// alphabetical.
func TestBuildListOrdering(t *testing.T) {
    groups := []worktree.EntryGroup{
        {Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
            {Type: worktree.TypeWorktree, Branch: "feature/x", Path: "/tmp/repo/.worktrees/x"},
            {Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
        }},
        {Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
            {Type: worktree.TypeBranch, Branch: "alpha"},
            {Type: worktree.TypeBranch, Branch: "main"},
        }},
        {Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
            {Type: worktree.TypeBranch, Branch: "origin/beta"},
            {Type: worktree.TypeBranch, Branch: "origin/gamma"},
        }},
    }
    l := buildList(groups, "main", "/tmp/repo", 80, 24)
    items := l.Items()
    if len(items) < 6 {
        t.Fatalf("expected at least 6 items (sentinel + 2 worktrees + 2 locals + 1 remote + 1 separator?), got %d", len(items))
    }
    // Row 0 must be the sentinel.
    if first, ok := items[0].(entryItem); !ok || first.kind != kindNewWorktree {
        t.Errorf("row 0 is not the sentinel")
    }
    // Find the separator row (kindSeparator) between locals and remotes.
    var sawSeparator bool
    for _, it := range items {
        if ei, ok := it.(entryItem); ok && ei.kind == kindSeparator {
            sawSeparator = true
            break
        }
    }
    if !sawSeparator {
        t.Error("no separator row found between locals and remotes")
    }
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/tui -run TestBuildListOrdering -v`
Expected: FAIL because `kindSeparator` doesn't exist yet.

- [ ] **Step 3: Implement the new `buildList`**

In `internal/tui/worktree_list.go`:

```go
// entryKind discriminates picker rows.
type entryKind int

const (
    kindEntry entryKind = iota
    kindNewWorktree
    kindSeparator
)

// entryItem adapts a worktree.Entry to a list.Item, or represents a
// sentinel/separator row.
type entryItem struct {
    kind  entryKind
    entry worktree.Entry
    label string // for separator rows
}

func (e entryItem) FilterValue() string {
    if e.kind != kindEntry {
        return ""
    }
    return e.entry.Branch
}

func (e entryItem) Title() string {
    switch e.kind {
    case kindNewWorktree:
        return "+ New worktree…"
    case kindSeparator:
        return e.label
    }
    b := e.entry.Branch
    if i := strings.IndexByte(b, '/'); i >= 0 {
        b = b[i+1:] // strip remote prefix for display
    }
    return b
}

func (e entryItem) Description() string {
    switch e.kind {
    case kindNewWorktree:
        return "create a new branch and worktree"
    case kindSeparator:
        return ""
    }
    parts := []string{pad("["+string(e.entry.Type)+"]", 9), e.entry.Path}
    return strings.TrimSpace(strings.Join(parts, " "))
}

// buildList constructs a list.Model from worktree groups. Order:
// sentinel + worktrees + locals + separator + remotes.
func buildList(groups []worktree.EntryGroup, defaultBranch, repoRoot string, width, height int) list.Model {
    items := make([]list.Item, 0)
    items = append(items, entryItem{kind: kindNewWorktree})

    resolvedRepo, _ := filepath.EvalSymlinks(repoRoot)

    for _, g := range groups {
        for _, e := range g.Entries {
            ei := entryItem{kind: kindEntry, entry: e}
            // Mark the entry matching the launch directory as (current).
            if e.Path != "" {
                resolved, _ := filepath.EvalSymlinks(e.Path)
                if resolved == resolvedRepo {
                    ei.entry.Path = e.Path + "  (current)"
                }
            }
            // Mark default-branch entries with (default).
            if e.Branch == defaultBranch {
                ei.entry.Branch = e.Branch + "  (default)"
            }
            items = append(items, ei)
        }
        // Add a separator between locals and remotes.
        if g.Kind == worktree.GroupLocalBranches {
            items = append(items, entryItem{kind: kindSeparator, label: "── remote branches ──"})
        }
    }

    l := list.New(items, list.NewDefaultDelegate(), width, height)
    l.Title = "Pick a worktree or branch"
    l.SetShowStatusBar(true)
    newBinding := key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new worktree"))
    l.AdditionalShortHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
    l.AdditionalFullHelpKeys = func() []key.Binding { return []key.Binding{newBinding} }
    return l
}
```

Add `"path/filepath"` to imports.

- [ ] **Step 4: Run all TUI tests**

Run: `go test ./internal/tui -v`
Expected: all pass.

- [ ] **Step 5: Commit**

```bash
git add internal/tui/worktree_list.go internal/tui/worktree_list_test.go
git commit -m "feat(tui): worktree picker with sentinel/locals/remotes ordering"
```

---

## Task 4: Picker-skip conditions at the CLI level

**Files:**
- Modify: `cmd/wt/main.go` (verification — PR 1 already wired the short-circuit).
- Modify: `cmd/wt/main_test.go` (tests).

- [ ] **Step 1: Write the failing test**

Append to `cmd/wt/main_test.go`:

```go
// TestPickerSkippedOnWorktreeFlag verifies that `wt -W foo` doesn't
// open the TUI. We can't easily test the TUI itself from a unit test;
// instead we verify the short-circuit path runs by checking that
// `wt -W foo -A claude` errors with "not in a git repo" or similar
// before any TUI is launched.
func TestPickerSkippedOnWorktreeFlag(t *testing.T) {
    // Run from a non-git directory.
    t.Setenv("HOME", t.TempDir())
    var buf bytes.Buffer
    root := rootCmd()
    root.SetOut(&buf)
    root.SetErr(&buf)
    root.SetArgs([]string{"-W", "my-branch"})
    err := root.Execute()
    // Should error with "not in a git repo", not launch a TUI.
    if err == nil {
        t.Fatal("expected error for -W outside git repo")
    }
    if !strings.Contains(err.Error(), "git") {
        t.Errorf("error %q doesn't mention git", err.Error())
    }
}
```

- [ ] **Step 2: Run the test to verify it passes**

Run: `go test ./cmd/wt -run TestPickerSkipped -v`
Expected: PASS (since PR 1 already wired the short-circuit). If FAIL, fix `cmd/wt/main.go` to short-circuit before `tui.Run`.

- [ ] **Step 3: Commit**

```bash
git add cmd/wt/main_test.go
git commit -m "test(wt): picker-skip conditions for -W/--cwd"
```

---

## Task 5: CHANGELOG entry for PR 4

**Files:**
- Modify: `CHANGELOG.md`.

- [ ] **Step 1: Add the entry**

Append to the Unreleased section:

```markdown
### Changed

- Worktree picker now always shows at least the default branch plus the
  `+ New worktree…` sentinel, even from inside a worktree. Rows are
  ordered: sentinel → local branches and worktrees alphabetical →
  remote-only branches alphabetical with a separator.
- Picker is skipped when `-W`/`--worktree`, `--cwd`, or non-git-repo
  conditions hold.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.md
git commit -m "docs: CHANGELOG entry for wt flow cleanup PR 4"
```

---

## Self-Review

- [x] **Spec coverage:** PR 4 covers the entire "Worktree Picker 'From Any Worktree' (PR 4)" section of the spec, including the picker-content invariants (default branch always present, sentinel, ordering) and picker-skip conditions.
- [x] **Placeholder scan:** No TBDs.
- [x] **Type consistency:** `EntryGroup{Kind, Entries}` is consistent; `entryItem{kind, entry, label}` matches across `buildList` and tests.
- [x] **Back-compat note:** `Enumerate`'s return shape changed (`[]EntryGroup` vs `[]Entry`); all callers updated in Task 2.
