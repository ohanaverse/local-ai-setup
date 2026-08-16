package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/agent-worktree/internal/worktree"
)

// entryItem must implement list.Item so buildList can pass it to list.New.
// A compile-time failure here means the list widget cannot be constructed.
func TestEntryItemIsListItem(t *testing.T) {
	var _ list.Item = entryItem{}
}

// Title must display the plain branch name for a local branch.
func TestEntryItemTitleLocalBranch(t *testing.T) {
	item := entryItem{entry: worktree.Entry{Branch: "feature"}}
	if got := item.Title(); got != "feature" {
		t.Errorf("Title = %q, want feature", got)
	}
}

// Title must strip a remote prefix so users see just the short branch name.
func TestEntryItemTitleRemoteBranch(t *testing.T) {
	item := entryItem{entry: worktree.Entry{Branch: "origin/feature"}}
	if got := item.Title(); got != "feature" {
		t.Errorf("Title = %q, want feature", got)
	}
}

// FilterValue must be the full branch name, including any remote prefix, so
// users can filter on "origin/feature" as well as "feature".
func TestEntryItemFilterValueUsesFullBranch(t *testing.T) {
	item := entryItem{entry: worktree.Entry{Branch: "origin/feature"}}
	if got := item.FilterValue(); got != "origin/feature" {
		t.Errorf("FilterValue = %q, want origin/feature", got)
	}
}

// Description for a branch without a worktree must say so, and include the
// entry type. This tells the user the branch is available but not yet
// checked out.
func TestEntryItemDescriptionNoWorktree(t *testing.T) {
	item := entryItem{entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"}}
	desc := item.Description()
	if !strings.Contains(desc, "[branch]") {
		t.Errorf("Description missing type: %q", desc)
	}
	if !strings.Contains(desc, "(no worktree)") {
		t.Errorf("Description missing no-worktree marker: %q", desc)
	}
}

// Description for a checked-out worktree must show its path.
func TestEntryItemDescriptionWithWorktree(t *testing.T) {
	item := entryItem{entry: worktree.Entry{Type: worktree.TypeWorktree, Branch: "feature", Path: "/tmp/repo/.worktrees/feature"}}
	desc := item.Description()
	if !strings.Contains(desc, "[worktree]") {
		t.Errorf("Description missing type: %q", desc)
	}
	if !strings.Contains(desc, "/tmp/repo/.worktrees/feature") {
		t.Errorf("Description missing path: %q", desc)
	}
}

// pad must not truncate strings that already meet or exceed the target
// length. Truncating would lose data in the list row.
func TestPadDoesNotTruncate(t *testing.T) {
	got := pad("verylong", 4)
	if got != "verylong" {
		t.Errorf("pad = %q, want verylong", got)
	}
}

// buildList must create a list with one item per entry and set the title.
// An empty entry list is also valid; it should not panic.
func TestBuildListEmpty(t *testing.T) {
	l := buildList(nil, 80, 24)
	if l.Title != "Pick a worktree or branch" {
		t.Errorf("title = %q, want Pick a worktree or branch", l.Title)
	}
	if len(l.Items()) != 0 {
		t.Errorf("items = %d, want 0", len(l.Items()))
	}
}

// buildList must preserve all entries as list items so every branch is
// selectable.
func TestBuildListPopulatesItems(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
		{Type: worktree.TypeWorktree, Branch: "bugfix", Path: "/tmp/repo/.worktrees/bugfix"},
	}
	l := buildList(entries, 80, 24)
	if len(l.Items()) != 3 {
		t.Fatalf("items = %d, want 3", len(l.Items()))
	}
	for i, want := range entries {
		item, ok := l.Items()[i].(entryItem)
		if !ok {
			t.Fatalf("item %d is %T, want entryItem", i, l.Items()[i])
		}
		if item.entry != want {
			t.Errorf("item %d entry = %+v, want %+v", i, item.entry, want)
		}
	}
}

// buildList must enable the status bar. This gives the user feedback on the
// number of entries and filter state.
func TestBuildListShowsStatusBar(t *testing.T) {
	l := buildList([]worktree.Entry{{Type: worktree.TypeBranch, Branch: "feature"}}, 80, 24)
	if !l.ShowStatusBar() {
		t.Error("ShowStatusBar = false, want true")
	}
}

// buildList should respect the provided dimensions so the list fits the
// terminal without overflowing.
func TestBuildListDimensions(t *testing.T) {
	l := buildList([]worktree.Entry{{Type: worktree.TypeBranch, Branch: "feature"}}, 78, 22)
	if l.Width() != 78 || l.Height() != 22 {
		t.Errorf("dimensions = (%d, %d), want (78, 22)", l.Width(), l.Height())
	}
}
