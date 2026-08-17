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
// An empty entry list is also valid; it should not panic. The sentinel
// is always present, even with zero real entries, so users in an empty
// repo can still create a worktree from the TUI.
func TestBuildListEmpty(t *testing.T) {
	l := buildList(nil, 80, 24)
	if l.Title != "Pick a worktree or branch" {
		t.Errorf("title = %q, want Pick a worktree or branch", l.Title)
	}
	if len(l.Items()) != 1 {
		t.Errorf("items = %d, want 1 (the sentinel)", len(l.Items()))
	}
	if first, ok := l.Items()[0].(entryItem); !ok || first.kind != kindNewWorktree {
		t.Errorf("items[0] = %+v, want sentinel (kindNewWorktree)", l.Items()[0])
	}
}

// buildList must preserve all entries as list items so every branch is
// selectable. The sentinel is prepended at index 0, so real entries
// start at index 1.
func TestBuildListPopulatesItems(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
		{Type: worktree.TypeWorktree, Branch: "bugfix", Path: "/tmp/repo/.worktrees/bugfix"},
	}
	l := buildList(entries, 80, 24)
	if len(l.Items()) != len(entries)+1 {
		t.Fatalf("items = %d, want %d (sentinel + %d entries)", len(l.Items()), len(entries)+1, len(entries))
	}
	if first, ok := l.Items()[0].(entryItem); !ok || first.kind != kindNewWorktree {
		t.Errorf("items[0] = %+v, want sentinel (kindNewWorktree)", l.Items()[0])
	}
	for i, want := range entries {
		item, ok := l.Items()[i+1].(entryItem)
		if !ok {
			t.Fatalf("item %d is %T, want entryItem", i+1, l.Items()[i+1])
		}
		if item.kind != kindEntry {
			t.Errorf("item %d kind = %d, want kindEntry (%d)", i+1, item.kind, kindEntry)
		}
		if item.entry != want {
			t.Errorf("item %d entry = %+v, want %+v", i+1, item.entry, want)
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

// buildList must advertise the 'n' shortcut in the footer help so users know
// they can open the new-worktree prompt directly. Without this, the shortcut
// works but is invisible, so users would have to discover the sentinel row.
// bubbles/list's ShortHelp() includes AdditionalShortHelpKeys; we assert the
// binding appears with the right key and help text.
func TestBuildListShortHelpAdvertisesNewWorktree(t *testing.T) {
	l := buildList([]worktree.Entry{{Type: worktree.TypeBranch, Branch: "feature"}}, 80, 24)
	bindings := l.ShortHelp()
	found := false
	for _, b := range bindings {
		if b.Help().Key == "n" {
			found = true
			if got := b.Help().Desc; got != "new worktree" {
				t.Errorf("n help desc = %q, want \"new worktree\"", got)
			}
		}
	}
	if !found {
		t.Errorf("ShortHelp did not contain an 'n' binding; got %v", bindings)
	}
}

// buildList must also advertise 'n' in the full help view (the expanded help
// screen toggled with '?'), so the shortcut is discoverable there too.
func TestBuildListFullHelpAdvertisesNewWorktree(t *testing.T) {
	l := buildList([]worktree.Entry{{Type: worktree.TypeBranch, Branch: "feature"}}, 80, 24)
	found := false
	for _, section := range l.FullHelp() {
		for _, b := range section {
			if b.Help().Key == "n" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("FullHelp did not contain an 'n' binding")
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

// TestSentinelItemRendersAsNew asserts the sentinel's Title visibly
// signals "new" so users understand it isn't a regular branch. A
// regression to a generic label would make the row look like a normal
// entry and confuse users.
func TestSentinelItemRendersAsNew(t *testing.T) {
	item := entryItem{kind: kindNewWorktree}
	got := strings.ToLower(item.Title())
	if !strings.Contains(got, "new") {
		t.Errorf("sentinel Title = %q, want it to contain 'new'", item.Title())
	}
}

// TestSentinelItemHasEmptyFilterValue asserts the sentinel returns ""
// for FilterValue. bubbles/list hides items with empty FilterValue
// while the user is filtering; without this, the sentinel would
// clutter filtered results with a single-character match.
func TestSentinelItemHasEmptyFilterValue(t *testing.T) {
	item := entryItem{kind: kindNewWorktree}
	if got := item.FilterValue(); got != "" {
		t.Errorf("sentinel FilterValue = %q, want empty string", got)
	}
}

// TestBuildListPrependsSentinel asserts buildList always puts the
// sentinel as the first item. Without the sentinel, users would
// need to know the `n` shortcut to create worktrees from the picker.
func TestBuildListPrependsSentinel(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	l := buildList(entries, 80, 24)
	items := l.Items()
	if len(items) != len(entries)+1 {
		t.Fatalf("items = %d, want %d", len(items), len(entries)+1)
	}
	first, ok := items[0].(entryItem)
	if !ok {
		t.Fatalf("item 0 is %T, want entryItem", items[0])
	}
	if first.kind != kindNewWorktree {
		t.Errorf("item 0 kind = %d, want kindNewWorktree (%d)", first.kind, kindNewWorktree)
	}
	// Real entries follow in order.
	for i, want := range entries {
		got, ok := items[i+1].(entryItem)
		if !ok {
			t.Fatalf("item %d is %T, want entryItem", i+1, items[i+1])
		}
		if got.kind != kindEntry {
			t.Errorf("item %d kind = %d, want kindEntry (%d)", i+1, got.kind, kindEntry)
		}
		if got.entry != want {
			t.Errorf("item %d entry = %+v, want %+v", i+1, got.entry, want)
		}
	}
}

// TestBuildListSentinelFirstWhenEmpty asserts the picker still shows
// the sentinel even with zero real entries. A fresh repo with no
// worktrees must still let users create one from the TUI.
func TestBuildListSentinelFirstWhenEmpty(t *testing.T) {
	l := buildList(nil, 80, 24)
	items := l.Items()
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1 (just the sentinel)", len(items))
	}
	first, ok := items[0].(entryItem)
	if !ok {
		t.Fatalf("item 0 is %T, want entryItem", items[0])
	}
	if first.kind != kindNewWorktree {
		t.Errorf("sentinel kind = %d, want kindNewWorktree", first.kind)
	}
}
