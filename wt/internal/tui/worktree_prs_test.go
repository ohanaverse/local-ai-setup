package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// prGroups is a picker with one row of each kind a PR can tag: the current
// worktree, a second worktree, a local branch and a remote-only branch, plus
// a branch with no PR. The worktree paths exist (buildList resolves them to
// find the (current) row); the repository root is the first.
func prGroups(t *testing.T) (groups []worktree.EntryGroup, root string) {
	t.Helper()
	root = t.TempDir()
	stop := filepath.Join(root, ".worktrees", "stop")
	if err := os.MkdirAll(stop, 0o755); err != nil {
		t.Fatal(err)
	}
	return []worktree.EntryGroup{
		{Kind: worktree.GroupWorktrees, Entries: []worktree.Entry{
			{Type: worktree.TypeCurrent, Branch: "fix/current", Path: root},
			{Type: worktree.TypeWorktree, Branch: "fix/wt-ollama-stop-outcome", Path: stop},
		}},
		{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "feat/local"},
			{Type: worktree.TypeBranch, Branch: "no-pr"},
		}},
		{Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
			{Type: worktree.TypeBranch, Branch: "origin/feat/remote"},
		}},
	}, root
}

func prMap() map[string]worktree.PR {
	return map[string]worktree.PR{
		"fix/current":                {Number: 384, State: worktree.PRDraft, Title: "Current work"},
		"fix/wt-ollama-stop-outcome": {Number: 383, State: worktree.PROpen, Title: "Stop reports its outcome"},
		"feat/local":                 {Number: 382, State: worktree.PRMerged, Title: "Local one"},
		"feat/remote":                {Number: 381, State: worktree.PRClosed, Title: "Remote one"},
		// A PR on the remote's own name must not tag "origin/feat/remote"
		// through the unstripped name.
		"origin/feat/remote": {Number: 1, State: worktree.PROpen},
	}
}

// rowByBranch finds the entry row for branch among items.
func rowByBranch(t *testing.T, items []list.Item, branch string) entryItem {
	t.Helper()
	for _, it := range items {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry && ei.entry.Branch == branch {
			return ei
		}
	}
	t.Fatalf("no row for %q", branch)
	return entryItem{}
}

// TestApplyPRsTagsEveryKindOfRow pins which rows get which tag: worktrees and
// local branches by their own name, a remote-only branch by its name without
// the remote, and nothing for a branch with no PR. It also pins the (current)
// marker surviving beside the tag. A regression leaves remote rows untagged
// or tags them with a PR on a branch literally named "origin/...".
func TestApplyPRsTagsEveryKindOfRow(t *testing.T) {
	groups, root := prGroups(t)
	l := buildList(groups, "", root, themes.Default, 80, 24)
	applyPRs(&l, prMap())
	items := l.Items()
	// Only a remote row drops its remote; a local fix/foo keeps its whole
	// name, the name its PR was opened from.
	cases := map[string]string{
		"fix/current":                "fix/current  (current)  #384 draft",
		"fix/wt-ollama-stop-outcome": "fix/wt-ollama-stop-outcome  #383 open",
		"feat/local":                 "feat/local  #382 merged",
		"origin/feat/remote":         "feat/remote  #381 closed",
		"no-pr":                      "no-pr",
	}
	for branch, want := range cases {
		if got := rowByBranch(t, items, branch).Title(); got != want {
			t.Errorf("Title(%s) = %q, want %q", branch, got, want)
		}
	}
	if _, ok := l.Items()[0].(entryItem); !ok || l.Items()[0].(entryItem).prTag() != "" {
		t.Errorf("sentinel got a tag: %+v", l.Items()[0])
	}
}

// TestPRRowDescriptionAndFilterValue pins the second line and the filter: the
// PR title sits between the row type and the path, a long one is cut so the
// path keeps some room, and the PR number is part of the filter value so
// typing "383" finds the row. A row with no PR is unchanged.
func TestPRRowDescriptionAndFilterValue(t *testing.T) {
	pr := worktree.PR{Number: 383, State: worktree.PROpen, Title: "Stop reports its outcome"}
	ei := entryItem{kind: kindEntry, entry: worktree.Entry{Type: worktree.TypeWorktree, Branch: "fix/stop", Path: "/repo/.worktrees/stop"}, pr: &pr}
	if got, want := ei.Description(), "[worktree] Stop reports its outcome · /repo/.worktrees/stop"; got != want {
		t.Errorf("Description = %q, want %q", got, want)
	}
	if got, want := ei.FilterValue(), "fix/stop #383"; got != want {
		t.Errorf("FilterValue = %q, want %q", got, want)
	}

	long := worktree.PR{Number: 1, State: worktree.PROpen, Title: strings.Repeat("x", 100)}
	ei.pr = &long
	title, _, _ := strings.Cut(ei.Description(), " · ")
	if w := ansi.StringWidth(strings.TrimPrefix(title, "[worktree] ")); w != prTitleMax {
		t.Errorf("long PR title is %d columns in the description, want %d", w, prTitleMax)
	}

	ei.pr = nil
	if got, want := ei.Description(), "[worktree] /repo/.worktrees/stop"; got != want {
		t.Errorf("untagged Description = %q, want %q", got, want)
	}
	if got := ei.FilterValue(); got != "fix/stop" {
		t.Errorf("untagged FilterValue = %q, want fix/stop", got)
	}
}

// TestFilterByPRNumberFindsTheRow pins the user-facing half of FilterValue:
// a filter of "383" leaves the row with PR #383 visible.
func TestFilterByPRNumberFindsTheRow(t *testing.T) {
	groups, root := prGroups(t)
	l := buildList(groups, "", root, themes.Default, 80, 24)
	applyPRs(&l, prMap())
	l.SetFilterText("383")
	found := false
	for _, it := range l.VisibleItems() {
		if ei, ok := it.(entryItem); ok && ei.entry.Branch == "fix/wt-ollama-stop-outcome" {
			found = true
		}
	}
	if !found {
		t.Errorf("filter 383 did not find the #383 row; visible = %v", l.VisibleItems())
	}
}

// renderRow draws row index of l through the delegate buildList gives it,
// escape codes removed.
func renderRow(l list.Model, index int) string {
	var b strings.Builder
	d := prTagDelegate{DefaultDelegate: ThemedListDelegate(themes.Default), theme: themes.Default}
	d.Render(&b, l, index, l.VisibleItems()[index])
	return ansi.Strip(b.String())
}

// TestPRTagRendersAfterTheTitleAndFits pins what the picker draws: the tag
// after the branch at 80 columns, and on a branch too long for the line, the
// branch cut short with the tag kept whole and the line no wider than the
// list. A regression either drops the tag off the right edge or pushes the
// row past the terminal width.
func TestPRTagRendersAfterTheTitleAndFits(t *testing.T) {
	long := "feat/" + strings.Repeat("very-long-branch-name-", 6)
	groups := []worktree.EntryGroup{{Kind: worktree.GroupLocalBranches, Entries: []worktree.Entry{
		{Type: worktree.TypeBranch, Branch: "fix/wt-ollama-stop-outcome"},
		{Type: worktree.TypeBranch, Branch: long},
	}}}
	prs := map[string]worktree.PR{
		"fix/wt-ollama-stop-outcome": {Number: 383, State: worktree.PROpen, Title: "Stop"},
		long:                         {Number: 12345, State: worktree.PRMerged, Title: "Long"},
	}
	l := buildList(groups, "", "/repo", themes.Default, 80, 24)
	applyPRs(&l, prs)

	short := renderRow(l, 1)
	first, second, _ := strings.Cut(short, "\n")
	if first != "fix/wt-ollama-stop-outcome  #383 open" {
		t.Errorf("title line = %q", first)
	}
	if !strings.HasPrefix(second, "[branch]  Stop · (no worktree)") {
		t.Errorf("description line = %q", second)
	}

	cut, _, _ := strings.Cut(renderRow(l, 2), "\n")
	if !strings.HasSuffix(cut, "…  #12345 merged") {
		t.Errorf("long title line = %q, want the branch cut and the tag whole", cut)
	}
	if w := ansi.StringWidth(cut); w > 80 {
		t.Errorf("long title line is %d columns, want at most 80", w)
	}
}

// TestPRTagDroppedWhenTooNarrow pins the narrow fallback: when the tag would
// leave fewer than prTagMinTitle columns of branch, the row is drawn as
// before (the default delegate cuts the whole title), rather than showing a
// tag with no branch name in front of it.
func TestPRTagDroppedWhenTooNarrow(t *testing.T) {
	groups := singleGroup(worktree.GroupLocalBranches, worktree.Entry{Type: worktree.TypeBranch, Branch: "fix/some-branch"})
	l := buildList(groups, "", "/repo", themes.Default, 20, 24)
	applyPRs(&l, map[string]worktree.PR{"fix/some-branch": {Number: 383, State: worktree.PROpen}})
	line, _, _ := strings.Cut(renderRow(l, 1), "\n")
	if strings.TrimSpace(line) != "fix/some-branch" {
		t.Errorf("narrow title line = %q, want the branch alone", line)
	}
}

// pickerModel is a ready worktree picker on prGroups at 80x24.
func pickerModel(t *testing.T) model {
	t.Helper()
	groups, root := prGroups(t)
	m := model{phase: phaseList, width: 80, height: 24, theme: themes.Default}
	got, _ := m.Update(entriesLoadedMsg{groups: groups, repoRoot: root})
	return got.(model)
}

// TestPRsArrivingKeepTheCursor pins the async refresh: the picker is drawn
// before gh answers, and when the tags arrive the highlighted row stays the
// highlighted row. A regression jumps the cursor back to the (current) row
// while the user is moving through the list.
func TestPRsArrivingKeepTheCursor(t *testing.T) {
	m := pickerModel(t)
	if rowByBranch(t, m.list.Items(), "fix/wt-ollama-stop-outcome").pr != nil {
		t.Fatal("rows tagged before any PR listing arrived")
	}
	selectFirstEntry(&m.list, func(ei entryItem) bool { return ei.entry.Branch == "feat/local" })
	if m.list.Index() < 2 {
		t.Fatalf("cursor at %d, want it moved off the (current) row", m.list.Index())
	}
	before := m.list.SelectedItem().(entryItem).entry

	got, _ := m.Update(prsLoadedMsg{prs: prMap()})
	m = got.(model)
	sel, ok := m.list.SelectedItem().(entryItem)
	if !ok || sel.entry != before {
		t.Fatalf("selected = %+v, want %+v", m.list.SelectedItem(), before)
	}
	if sel.prTag() != "#382 merged" {
		t.Errorf("selected row tag = %q, want #382 merged", sel.prTag())
	}
	if !strings.Contains(m.View(), "#383 open") {
		t.Errorf("view does not show the #383 tag:\n%s", m.View())
	}
}

// TestPRsArrivingKeepTheFilter pins the refresh under a filter, applied or
// being typed: the filter text and state stay, the visible rows are the
// filter's matches against the new values (so a number typed before the tags
// arrived finds its row once they do), and the highlighted row stays.
func TestPRsArrivingKeepTheFilter(t *testing.T) {
	t.Run("applied", func(t *testing.T) {
		m := pickerModel(t)
		m.list.SetFilterText("feat")
		m.list.Select(1)
		before := m.list.SelectedItem().(entryItem).entry
		got, _ := m.Update(prsLoadedMsg{prs: prMap()})
		m = got.(model)
		if m.list.FilterState() != list.FilterApplied || m.list.FilterValue() != "feat" {
			t.Errorf("filter = %v %q, want applied \"feat\"", m.list.FilterState(), m.list.FilterValue())
		}
		if sel := m.list.SelectedItem().(entryItem); sel.entry != before || sel.pr == nil {
			t.Errorf("selected = %+v, want %+v with its tag", sel, before)
		}
	})
	t.Run("typing", func(t *testing.T) {
		m := pickerModel(t)
		// "383" typed into the filter before the tags arrived: no row
		// matches yet.
		m.list.SetFilterState(list.Filtering)
		m.list.FilterInput.SetValue("383")
		if m.list.FilterState() != list.Filtering || len(m.list.VisibleItems()) != 0 {
			t.Fatalf("before the tags: state %v, %d visible, want Filtering with no match", m.list.FilterState(), len(m.list.VisibleItems()))
		}
		got, _ := m.Update(prsLoadedMsg{prs: prMap()})
		m = got.(model)
		if m.list.FilterState() != list.Filtering || m.list.FilterValue() != "383" {
			t.Errorf("filter = %v %q, want Filtering \"383\"", m.list.FilterState(), m.list.FilterValue())
		}
		vis := m.list.VisibleItems()
		if len(vis) != 1 || vis[0].(entryItem).entry.Branch != "fix/wt-ollama-stop-outcome" {
			t.Errorf("visible = %v, want only the #383 row", vis)
		}
	})
}

// TestPRsBeforeEntriesTagTheFirstBuild pins the other arrival order: gh can
// answer before the enumeration (which fetches first), and the list built
// afterwards carries the tags with the cursor still on the (current) row.
func TestPRsBeforeEntriesTagTheFirstBuild(t *testing.T) {
	groups, root := prGroups(t)
	m := model{phase: phaseList, width: 80, height: 24, theme: themes.Default}
	got, _ := m.Update(prsLoadedMsg{prs: prMap()})
	m = got.(model)
	got, _ = m.Update(entriesLoadedMsg{groups: groups, repoRoot: root})
	m = got.(model)
	sel := m.list.SelectedItem().(entryItem)
	if sel.label != "(current)" || sel.prTag() != "#384 draft" {
		t.Errorf("selected = %q tag %q, want the (current) row tagged #384 draft", sel.baseTitle(), sel.prTag())
	}
}

// TestFailedPRListingShowsNoTags pins the silent failure end to end: gh
// failing (the TestMain stub) leaves the picker exactly as it was, with no
// status or list error.
func TestFailedPRListingShowsNoTags(t *testing.T) {
	m := pickerModel(t)
	msg := loadPRsCmd()()
	got, _ := m.Update(msg)
	m = got.(model)
	for _, it := range m.list.Items() {
		if ei := it.(entryItem); ei.pr != nil {
			t.Errorf("row %q tagged after a failed listing", ei.entry.Branch)
		}
	}
	if m.status != "" || m.listError != "" {
		t.Errorf("status %q / listError %q, want both empty", m.status, m.listError)
	}
}

// TestLoadPRsCmdReturnsTheListing pins loadPRsCmd's plumbing: gh runs in the
// repository root wt was started under (here a fresh repository), and its
// JSON reaches the message as tags. No real gh runs: the listing is stubbed.
func TestLoadPRsCmdReturnsTheListing(t *testing.T) {
	repo := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", repo).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	t.Chdir(repo)
	var dirs []string
	t.Cleanup(worktree.StubPRListForTest(func(dir, _ string) ([]byte, error) {
		dirs = append(dirs, dir)
		return []byte(`[{"number": 383, "state": "OPEN", "isDraft": false, "headRefName": "fix/x", "isCrossRepository": false, "title": "X"}]`), nil
	}))
	msg, ok := loadPRsCmd()().(prsLoadedMsg)
	if !ok {
		t.Fatalf("loadPRsCmd returned %T", msg)
	}
	if got := msg.prs["fix/x"]; got.Number != 383 || got.State != worktree.PROpen {
		t.Errorf("prs[fix/x] = %+v, want #383 open", got)
	}
	want, _ := filepath.EvalSymlinks(repo)
	if len(dirs) != 2 {
		t.Fatalf("gh ran %d times, want twice (open, then every state)", len(dirs))
	}
	for _, dir := range dirs {
		if got, _ := filepath.EvalSymlinks(dir); got != want {
			t.Errorf("gh ran in %q, want the repository root %q", dir, want)
		}
	}
}

// TestLoadPRsCmdOutsideARepoIsEmpty pins the no-repository case: no gh runs
// and the message carries an empty map, so the picker shows no tags.
func TestLoadPRsCmdOutsideARepoIsEmpty(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Cleanup(worktree.StubPRListForTest(func(string, string) ([]byte, error) {
		t.Error("gh ran outside a repository")
		return nil, nil
	}))
	msg := loadPRsCmd()().(prsLoadedMsg)
	if msg.prs == nil || len(msg.prs) != 0 {
		t.Errorf("prs = %#v, want an empty map", msg.prs)
	}
}

// TestPRTitleControlCharactersAreDropped pins that a PR title, which comes
// from GitHub and not from wt, reaches the row's description without its
// control characters or its bidi controls. A regression lets a title
// carrying an escape sequence write to the alt screen as terminal control,
// not text, or one carrying an open right-to-left override reverse the path
// drawn after it.
func TestPRTitleControlCharactersAreDropped(t *testing.T) {
	ei := entryItem{kind: kindEntry, entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "x"},
		pr: &worktree.PR{Number: 7, State: worktree.PROpen, Title: "evil\x1b]0;pwned\x07 ‮title\r"}}
	got := ei.Description()
	if strings.ContainsAny(got, "\x1b\x07\r‮") {
		t.Errorf("Description() = %q, still holds control characters", got)
	}
	if !strings.Contains(got, "evil]0;pwned title") {
		t.Errorf("Description() = %q, lost the printable text", got)
	}
}
