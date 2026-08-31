package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
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

// Build an EntryGroup with a single entry for compact test setup.
func singleGroup(kind worktree.GroupKind, e worktree.Entry) []worktree.EntryGroup {
	return []worktree.EntryGroup{{Kind: kind, Entries: []worktree.Entry{e}}}
}

// buildList must create a list with the sentinel alone and set the title when
// given an empty group slice. The sentinel is always present, even with zero
// real entries, so users in an empty repo can still create a worktree from
// the TUI.
func TestBuildListEmpty(t *testing.T) {
	l := buildList(nil, "", "/tmp/repo", themes.Default, 80, 24)
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

// buildList must enable the status bar. This gives the user feedback on the
// number of entries and filter state.
func TestBuildListShowsStatusBar(t *testing.T) {
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"})
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
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
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"})
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
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
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"})
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
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
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{Type: worktree.TypeBranch, Branch: "feature"})
	l := buildList(groups, "", "/tmp/repo", themes.Default, 78, 22)
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
// sentinel as the first item, even when groups contain many entries.
// Without the sentinel, users would need to know the `n` shortcut to
// create worktrees from the picker.
func TestBuildListPrependsSentinel(t *testing.T) {
	entries := []worktree.Entry{
		{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		{Type: worktree.TypeBranch, Branch: "feature", Path: ""},
	}
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: entries},
	}
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
	items := l.Items()
	if first, ok := items[0].(entryItem); !ok || first.kind != kindNewWorktree {
		t.Errorf("items[0] = %+v, want sentinel (kindNewWorktree)", items[0])
	}
	// Real entries follow in order with their kind markers.
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
	l := buildList(nil, "", "/tmp/repo", themes.Default, 80, 24)
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

// TestBuildListOrdering verifies the picker renders sentinel first,
// then worktrees group (group 0), then locals group (group 1), then
// a separator row, then the remotes group (group 2). This three-group
// rendering is the core of the new picker shape: the separator is the
// visible affordance that tells users "this set is remote-tracking
// branches" without colliding with the filter UI.
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
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
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

// TestBuildListCurrentMarker asserts the entry whose Path resolves to
// repoRoot is annotated with "(current)" so users see which worktree
// they launched from. The marker lives on entryItem.label, not the
// underlying worktree.Entry, so the original entry value remains safe
// to forward into selectedEntryMsg and on to the agent launch.
func TestBuildListCurrentMarker(t *testing.T) {
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{
		Type:   worktree.TypeCurrent,
		Branch: "main",
		Path:   "/tmp/repo",
	})
	l := buildList(groups, "", "/tmp/repo", themes.Default, 80, 24)
	items := l.Items()
	// items[0] is the sentinel; the only real entry sits at items[1].
	got, ok := items[1].(entryItem)
	if !ok {
		t.Fatalf("items[1] is %T, want entryItem", items[1])
	}
	if got.label != "(current)" {
		t.Errorf("label = %q, want (current)", got.label)
	}
	if got.entry.Path != "/tmp/repo" {
		t.Errorf("entry.Path = %q, want /tmp/repo (entry must not be mutated)", got.entry.Path)
	}
}

// TestBuildListDefaultMarker asserts a non-current worktree on the default
// branch is annotated with "(default)". Bare default-branch rows are skipped
// (the default branch must never be a linked worktree), so the marker now
// only applies to a linked worktree created outside wt. Like the (current)
// marker, the original entry.Branch is never mutated.
func TestBuildListDefaultMarker(t *testing.T) {
	// Use a real repo root so EvalSymlinks resolves it; a non-existent
	// fixture path would resolve to "" and falsely match the (current) marker.
	repoRoot := t.TempDir()
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeWorktree, Branch: "main", Path: filepath.Join(repoRoot, ".worktrees", "main")},
		}},
	}
	l := buildList(groups, "main", repoRoot, themes.Default, 80, 24)
	items := l.Items()
	got, ok := items[1].(entryItem)
	if !ok {
		t.Fatalf("items[1] is %T, want entryItem", items[1])
	}
	if got.label != "(default)" {
		t.Errorf("label = %q, want (default)", got.label)
	}
	if got.entry.Branch != "main" {
		t.Errorf("entry.Branch = %q, want main (entry must not be mutated)", got.entry.Branch)
	}
}

// TestBuildListSkipsBareDefaultBranch asserts bare default-branch rows
// (local and remote form) are omitted from the picker. The default branch
// must never be a linked worktree, so the picker must not offer it as a
// create-target; the primary checkout on the default branch still appears
// as (current), and non-default bare branches are unaffected. The remote
// form is matched across ALL remotes (origin/main, upstream/main), not just
// origin, so a fork workflow cannot offer the default branch via a second
// remote. A local branch whose name ends in "/main" (feature/main) is a
// feature branch, not the default, and must stay pickable.
func TestBuildListSkipsBareDefaultBranch(t *testing.T) {
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "feature", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "main"},
			{Type: worktree.TypeBranch, Branch: "other"},
			{Type: worktree.TypeBranch, Branch: "feature/main"},
		}},
		{Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "origin/main"},
			{Type: worktree.TypeBranch, Branch: "upstream/main"},
			{Type: worktree.TypeBranch, Branch: "origin/dev"},
		}},
	}
	l := buildList(groups, "main", "/tmp/repo", themes.Default, 80, 24)

	seen := map[string]bool{}
	for _, it := range l.Items() {
		ei, ok := it.(entryItem)
		if !ok {
			continue
		}
		seen[ei.entry.Branch] = true
	}
	// Bare default rows (local and every remote form) must be gone.
	for _, b := range []string{"main", "origin/main", "upstream/main"} {
		if seen[b] {
			t.Errorf("bare default-branch row %q was not skipped", b)
		}
	}
	// Non-default bare rows must survive — including a local branch whose
	// name ends in "/main", which the name-only suffix match would wrongly
	// skip without the group-kind gating.
	for _, b := range []string{"other", "feature/main", "origin/dev"} {
		if !seen[b] {
			t.Errorf("non-default bare row %q was wrongly skipped", b)
		}
	}
}

// TestBuildListCurrentWinsOverDefault asserts that when the current
// worktree is also the default branch, the (current) marker is shown
// rather than (default). (current) wins so the current worktree stays
// visually distinct from any other worktree that happens to be on the
// default branch.
func TestBuildListCurrentWinsOverDefault(t *testing.T) {
	groups := singleGroup(worktree.GroupWorktrees, worktree.Entry{
		Type:   worktree.TypeCurrent,
		Branch: "main",
		Path:   "/tmp/repo",
	})
	l := buildList(groups, "main", "/tmp/repo", themes.Default, 80, 24)
	items := l.Items()
	got := items[1].(entryItem)
	if got.label != "(current)" {
		t.Errorf("label = %q, want (current) (current must win over default)", got.label)
	}
}

// TestBuildListNoSeparatorWhenRemotesEmpty asserts the locals→remotes
// divider is only rendered when the remote-branches group is non-empty.
// Without this, a repo with no remote-tracking branches shows a dangling
// "── remote branches ──" row with nothing below it.
func TestBuildListNoSeparatorWhenRemotesEmpty(t *testing.T) {
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "feature"},
		}},
		{Kind: worktree.GroupRemoteBranches, Entries: nil},
	}
	l := buildList(groups, "main", "/tmp/repo", themes.Default, 80, 24)
	for _, it := range l.Items() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindSeparator {
			t.Errorf("separator rendered despite empty remote group: %+v", ei)
		}
	}
}

// TestBuildListNoSeparatorWhenRemotesOnlyDefaultBranch asserts the
// locals→remotes divider is omitted when every remote-tracking branch would
// be filtered out as a default-branch form. The raw remote group is non-empty,
// but only because it contains origin/main; after default-branch filtering no
// remote entries survive, so a dangling separator must not appear.
func TestBuildListNoSeparatorWhenRemotesOnlyDefaultBranch(t *testing.T) {
	groups := []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "main", Path: "/tmp/repo"},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: nil},
		{Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "origin/main"},
		}},
	}
	l := buildList(groups, "main", "/tmp/repo", themes.Default, 80, 24)
	for _, it := range l.Items() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindSeparator {
			t.Errorf("separator rendered when all remotes filtered: %+v", ei)
		}
	}
}

// TestSeparatorItemRendersLabel asserts the separator row's Title is
// its label string and its FilterValue is empty (so filtering hides
// the divider along with the sentinel). A separator that survived the
// filter would interrupt search results.
func TestSeparatorItemRendersLabel(t *testing.T) {
	item := entryItem{kind: kindSeparator, label: "── remote branches ──"}
	if got := item.Title(); got != "── remote branches ──" {
		t.Errorf("Title = %q, want the separator label", got)
	}
	if got := item.FilterValue(); got != "" {
		t.Errorf("FilterValue = %q, want empty (separator must be hidden while filtering)", got)
	}
	if got := item.Description(); got != "" {
		t.Errorf("Description = %q, want empty", got)
	}
}
