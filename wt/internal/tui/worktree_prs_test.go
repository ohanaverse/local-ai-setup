package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// prGroups is a picker with one row of each kind a PR can tag: the current
// worktree, a second worktree, a local branch and a remote-only branch, plus
// a branch with no PR. The worktree paths exist (buildList resolves them to
// find the (current) row); the repository root is the first. The remote row's
// remote is the repository prMap's listing came from (o/base).
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
			{Type: worktree.TypeBranch, Branch: "origin/feat/remote", RemoteRepo: "o/base", RemoteBranch: "feat/remote"},
		}},
	}, root
}

func prMap() worktree.PRSet {
	return prsIn("o/base", map[string]worktree.PR{
		"fix/current":                {Number: 384, State: worktree.PRDraft, Title: "Current work"},
		"fix/wt-ollama-stop-outcome": {Number: 383, State: worktree.PROpen, Title: "Stop reports its outcome"},
		"feat/local":                 {Number: 382, State: worktree.PRMerged, Title: "Local one"},
		"feat/remote":                {Number: 381, State: worktree.PRClosed, Title: "Remote one"},
		// A PR on the remote's own name must not tag "origin/feat/remote"
		// through the unstripped name.
		"origin/feat/remote": {Number: 1, State: worktree.PROpen},
	})
}

// prsIn is a listing whose PRs were all opened from repo's branches, keyed
// by branch name; repo is also the listing's BaseRepo.
func prsIn(repo string, byBranch map[string]worktree.PR) worktree.PRSet {
	s := worktree.PRSet{BaseRepo: repo, ByHead: map[worktree.PRKey]worktree.PR{}}
	for b, pr := range byBranch {
		s.ByHead[worktree.PRKey{Repo: repo, Branch: b}] = pr
	}
	return s
}

// withFork adds PRs opened from fork's branches to a copy of s (a fork's
// PRs leave BaseRepo alone).
func withFork(s worktree.PRSet, fork string, byBranch map[string]worktree.PR) worktree.PRSet {
	out := worktree.MergePRs(s, worktree.PRSet{})
	for b, pr := range byBranch {
		out.ByHead[worktree.PRKey{Repo: fork, Branch: b}] = pr
	}
	return out
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
	prs := prsIn("o/base", map[string]worktree.PR{
		"fix/wt-ollama-stop-outcome": {Number: 383, State: worktree.PROpen, Title: "Stop"},
		long:                         {Number: 12345, State: worktree.PRMerged, Title: "Long"},
	})
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
	applyPRs(&l, prsIn("o/base", map[string]worktree.PR{"fix/some-branch": {Number: 383, State: worktree.PROpen}}))
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
	msg := loadPRsCmd("/repo", worktree.PRListAll)()
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

// TestLoadPRsCmdReturnsTheListing pins loadPRsCmd's plumbing: gh runs once,
// for the listing asked for, in the repository root it is handed, and its
// JSON reaches the message as tags. No real gh runs: the listing is stubbed.
func TestLoadPRsCmdReturnsTheListing(t *testing.T) {
	var dirs, states []string
	t.Cleanup(worktree.StubPRListForTest(func(dir, state string) ([]byte, error) {
		dirs = append(dirs, dir)
		states = append(states, state)
		return []byte(`[{"number": 383, "state": "OPEN", "isDraft": false, "headRefName": "fix/x", "isCrossRepository": false, "title": "X", "headRepository": {"nameWithOwner": "o/base"}}]`), nil
	}))
	msg, ok := loadPRsCmd("/the/root", worktree.PRListOpen)().(prsLoadedMsg)
	if !ok {
		t.Fatalf("loadPRsCmd returned %T", msg)
	}
	if got, _ := msg.prs.ForBranch("fix/x"); got.Number != 383 || got.State != worktree.PROpen {
		t.Errorf("prs[fix/x] = %+v, want #383 open", got)
	}
	if !reflect.DeepEqual(dirs, []string{"/the/root"}) || !reflect.DeepEqual(states, []string{worktree.PRListOpen}) {
		t.Errorf("gh ran in %v for %v, want once in /the/root for %q", dirs, states, worktree.PRListOpen)
	}
}

// initBatch runs Init's command (the one root lookup) and returns the
// commands it fans out to: the entry load, then one per PR listing.
func initBatch(t *testing.T) tea.BatchMsg {
	t.Helper()
	batch, ok := model{}.Init()().(tea.BatchMsg)
	if !ok {
		t.Fatal("Init's command did not answer with a batch")
	}
	if len(batch) != 1+len(worktree.PRListStates) {
		t.Fatalf("Init batched %d commands, want 1 + %d listings", len(batch), len(worktree.PRListStates))
	}
	return batch
}

// TestInitRunsEveryPRListing pins that a picker open asks for each listing
// in worktree.PRListStates, each in its own command, so the open PRs' tags
// are not held back behind the longer every-state listing, and that every
// listing runs in the repository root Init found. A regression to one
// listing loses either old open PRs or merged and closed tags.
func TestInitRunsEveryPRListing(t *testing.T) {
	var mu sync.Mutex
	var states, dirs []string
	t.Cleanup(worktree.StubPRListForTest(func(dir, state string) ([]byte, error) {
		mu.Lock()
		defer mu.Unlock()
		states = append(states, state)
		dirs = append(dirs, dir)
		return nil, errors.New("stubbed")
	}))
	repo := t.TempDir()
	t.Chdir(repo)
	if out, err := exec.Command("git", "init", "-q", ".").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	batch := initBatch(t)
	for _, cmd := range batch[1:] {
		if _, ok := cmd().(prsLoadedMsg); !ok {
			t.Error("a listing command did not answer with prsLoadedMsg")
		}
	}
	sort.Strings(states)
	if want := []string{worktree.PRListAll, worktree.PRListOpen}; !reflect.DeepEqual(states, want) {
		t.Errorf("listings run = %v, want %v", states, want)
	}
	want, _ := filepath.EvalSymlinks(repo)
	for _, d := range dirs {
		if got, _ := filepath.EvalSymlinks(d); got != want {
			t.Errorf("gh ran in %q, want the repository root %q", d, want)
		}
	}
}

// TestPickerOpenResolvesTheRepoRootOnce pins #390: one picker open runs one
// `git rev-parse --show-toplevel` (findRepoRoot), and the entry load and
// both PR listings share its answer; the reload after a create still looks
// the root up afresh (loadEntriesCmd), since the directory may have changed.
// A regression runs the lookup once per command again — three git processes
// on every picker open.
func TestPickerOpenResolvesTheRepoRootOnce(t *testing.T) {
	var mu sync.Mutex
	lookups := 0
	old := findRepoRoot
	t.Cleanup(func() { findRepoRoot = old })
	findRepoRoot = func() (string, error) {
		mu.Lock()
		defer mu.Unlock()
		lookups++
		return "", errors.New("not a git repository")
	}
	for _, cmd := range initBatch(t) {
		cmd()
	}
	if lookups != 1 {
		t.Errorf("one picker open ran %d root lookups, want 1", lookups)
	}
	lookups = 0
	loadEntriesCmd()()
	if lookups != 1 {
		t.Errorf("the reload ran %d root lookups, want its own 1", lookups)
	}
}

// TestPRListingsMergeInEitherOrder pins the progressive tags: the first
// listing to answer tags the rows at once, the second adds its heads
// without dropping the first's, a head in both keeps its open PR over a
// newer merged one whichever arrived first, and a fork's PRs for a branch
// name never reach the base remote's row of that name (#387). A regression
// either wipes the open tags when the every-state listing lands, lets the
// late listing's merged PR hide the branch's open one, or shows a fork's PR
// on the base repository's remote branch.
func TestPRListingsMergeInEitherOrder(t *testing.T) {
	open := withFork(prsIn("o/base", map[string]worktree.PR{
		"fix/wt-ollama-stop-outcome": {Number: 383, State: worktree.PROpen, Title: "Stop reports its outcome"},
		"feat/local":                 {Number: 380, State: worktree.PROpen, Title: "Older open"},
	}), "me/fork", map[string]worktree.PR{
		"feat/remote": {Number: 390, State: worktree.PROpen, Title: "Fork's remote"},
	})
	all := withFork(prsIn("o/base", map[string]worktree.PR{
		"feat/local":  {Number: 382, State: worktree.PRMerged, Title: "Local one"},
		"fix/current": {Number: 384, State: worktree.PRDraft, Title: "Current work"},
		"feat/remote": {Number: 381, State: worktree.PRClosed, Title: "Remote one"},
	}), "me/fork", map[string]worktree.PR{
		"feat/remote": {Number: 391, State: worktree.PRMerged, Title: "Fork's later one"},
	})
	for name, order := range map[string][]worktree.PRSet{
		"open first": {open, all},
		"all first":  {all, open},
	} {
		t.Run(name, func(t *testing.T) {
			m := pickerModel(t)
			got, _ := m.Update(prsLoadedMsg{prs: order[0]})
			m = got.(model)
			for k := range order[0].ByHead {
				branch := k.Branch
				if branch == "fix/current" || branch == "feat/remote" {
					continue // the (current) row is found by label below
				}
				if rowByBranch(t, m.list.Items(), branch).pr == nil {
					t.Errorf("%s untagged after the first listing", branch)
				}
			}
			got, _ = m.Update(prsLoadedMsg{prs: order[1]})
			m = got.(model)
			if tag := rowByBranch(t, m.list.Items(), "fix/wt-ollama-stop-outcome").prTag(); tag != "#383 open" {
				t.Errorf("stop-outcome tag = %q, want #383 open", tag)
			}
			if tag := rowByBranch(t, m.list.Items(), "feat/local").prTag(); tag != "#380 open" {
				t.Errorf("feat/local tag = %q, want #380 open (open beats a newer merged PR)", tag)
			}
			// origin/feat/remote is o/base's: its own closed #381, never
			// the fork's open #390 or merged #391 for the same branch name.
			if tag := rowByBranch(t, m.list.Items(), "origin/feat/remote").prTag(); tag != "#381 closed" {
				t.Errorf("origin/feat/remote tag = %q, want #381 closed", tag)
			}
		})
	}
}

// TestLoadPRsCmdOutsideARepoIsEmpty pins the no-repository case: a picker
// opened outside a repository runs no gh, and each listing's message
// carries a loaded, empty set — never an error — so the picker shows no
// tags; loadPRsCmd handed no root does the same.
func TestLoadPRsCmdOutsideARepoIsEmpty(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Cleanup(worktree.StubPRListForTest(func(string, string) ([]byte, error) {
		t.Error("gh ran outside a repository")
		return nil, nil
	}))
	for _, cmd := range initBatch(t)[1:] {
		msg := cmd().(prsLoadedMsg)
		if !msg.prs.Loaded() || len(msg.prs.ByHead) != 0 {
			t.Errorf("prs = %#v, want a loaded, empty set", msg.prs)
		}
	}
	msg := loadPRsCmd("", worktree.PRListAll)().(prsLoadedMsg)
	if !msg.prs.Loaded() || len(msg.prs.ByHead) != 0 {
		t.Errorf("loadPRsCmd(\"\") prs = %#v, want a loaded, empty set", msg.prs)
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
	ei.pr.Title = "two\twords"
	if got := ei.Description(); !strings.Contains(got, "two words") {
		t.Errorf("Description() = %q, want the tab read as a space", got)
	}
}

// TestStaleFilterRunAfterPRsKeepsTheTags pins the filter run a keystroke
// started before the tags arrived: bubbles runs it as a command over the
// items as they were, so its matches carry the untagged rows. Arriving after
// the tags, it must not put those rows back on screen. A regression drops
// every tag from the filtered rows (and a typed "#383" finds nothing) until
// the next keystroke.
func TestStaleFilterRunAfterPRsKeepsTheTags(t *testing.T) {
	m := pickerModel(t)
	m.list.SetFilterState(list.Filtering)
	m.list.FilterInput.SetValue("fix")
	// The keystroke's filter run, over the untagged items.
	before := m.list
	staleRun := before.SetItems(before.Items())

	got, _ := m.Update(prsLoadedMsg{prs: prMap()})
	m = got.(model)
	got, _ = m.Update(staleRun())
	m = got.(model)

	vis := m.list.VisibleItems()
	if len(vis) == 0 {
		t.Fatal("no visible rows after the stale filter run")
	}
	for _, it := range vis {
		if ei := it.(entryItem); ei.pr == nil {
			t.Errorf("row %q lost its tag to a stale filter run", ei.entry.Branch)
		}
	}
}

// forkGroups is a picker with the same branch name on two remotes: origin,
// the repository gh lists (o/base), and keith, a fork (keith/fork).
func forkGroups() []worktree.EntryGroup {
	return []worktree.EntryGroup{{Kind: worktree.GroupRemoteBranches, Entries: []worktree.Entry{
		{Type: worktree.TypeBranch, Branch: "origin/feat-x", RemoteRepo: "o/base", RemoteBranch: "feat-x"},
		{Type: worktree.TypeBranch, Branch: "keith/feat-x", RemoteRepo: "keith/fork", RemoteBranch: "feat-x"},
	}}}
}

// basePRs is o/base's listing: PR #4242 from its own feat-x.
func basePRs() worktree.PRSet {
	return prsIn("o/base", map[string]worktree.PR{
		"feat-x": {Number: 4242, State: worktree.PROpen, Title: "Base feat-x"},
	})
}

// TestRemoteRowsFromASecondRemoteAreNotTaggedWithTheBaseReposPR pins #388 in
// its #387 form: a remote-tracking row is tagged only with a PR whose head is
// its own remote's repository and branch. origin/feat-x (o/base) gets
// o/base's PR for feat-x; keith/feat-x, a fork's branch of the same name,
// does not, and once the listing has a PR from keith/fork's feat-x, that is
// the fork row's tag while origin's keeps its own. A regression shows the
// base repository's PR on a fork branch it has nothing to do with, or the
// other way round.
func TestRemoteRowsFromASecondRemoteAreNotTaggedWithTheBaseReposPR(t *testing.T) {
	l := buildList(forkGroups(), "", "/repo", themes.Default, 80, 24)
	applyPRs(&l, basePRs())
	if tag := rowByBranch(t, l.Items(), "origin/feat-x").prTag(); tag != "#4242 open" {
		t.Errorf("origin/feat-x tag = %q, want #4242 open", tag)
	}
	if ei := rowByBranch(t, l.Items(), "keith/feat-x"); ei.pr != nil {
		t.Errorf("keith/feat-x tagged %q with o/base's PR", ei.prTag())
	}
	applyPRs(&l, withFork(basePRs(), "keith/fork", map[string]worktree.PR{
		"feat-x": {Number: 77, State: worktree.PRMerged, Title: "Fork feat-x"},
	}))
	if tag := rowByBranch(t, l.Items(), "origin/feat-x").prTag(); tag != "#4242 open" {
		t.Errorf("origin/feat-x tag = %q with the fork's PR listed, want #4242 open", tag)
	}
	if tag := rowByBranch(t, l.Items(), "keith/feat-x").prTag(); tag != "#77 merged" {
		t.Errorf("keith/feat-x tag = %q, want its own #77 merged", tag)
	}
}

// TestRemoteRowUnknownRemoteIsUntagged pins the cases where wt cannot tell
// which repository a remote row belongs to — a remote it could not resolve
// (no RemoteBranch either), a local-path remote (RemoteRepo empty) — or
// where no PR's head is the row's repository and branch (a PR gh gave no
// head repository for, another repository's PR), as untagged, never
// guessed. A regression tags a row from an unknown repository with some
// repository's PR of the same branch name.
func TestRemoteRowUnknownRemoteIsUntagged(t *testing.T) {
	for name, tc := range map[string]struct {
		entry worktree.Entry
		prs   worktree.PRSet
	}{
		"unresolved remote": {
			entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "weird/feat-x"},
			prs:   basePRs(),
		},
		"local-path remote": {
			entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "mirror/feat-x", RemoteBranch: "feat-x"},
			prs:   basePRs(),
		},
		"PR with no head repo": {
			entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "origin/feat-x", RemoteRepo: "o/base", RemoteBranch: "feat-x"},
			prs:   prsIn("", map[string]worktree.PR{"feat-x": {Number: 4242, State: worktree.PROpen}}),
		},
		"another repo's PR": {
			entry: worktree.Entry{Type: worktree.TypeBranch, Branch: "mine/feat-x", RemoteRepo: "me/fork", RemoteBranch: "feat-x"},
			prs:   basePRs(),
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := buildList(singleGroup(worktree.GroupRemoteBranches, tc.entry), "", "/repo", themes.Default, 80, 24)
			applyPRs(&l, tc.prs)
			if ei := rowByBranch(t, l.Items(), tc.entry.Branch); ei.pr != nil {
				t.Errorf("%s tagged %q", tc.entry.Branch, ei.prTag())
			}
		})
	}
}

// TestRemoteRowsStayUntaggedUntilThePRsArriveThenTag pins the arrival order
// in the picker: before any listing (the zero PRSet) and after one that
// failed (loaded, empty, no repo) no remote row is tagged; the listing that
// names o/base tags origin's row and still not the fork's. A regression
// either never tags a remote row once the repository is known, or tags the
// fork's on the late listing.
func TestRemoteRowsStayUntaggedUntilThePRsArriveThenTag(t *testing.T) {
	m := model{phase: phaseList, width: 80, height: 24, theme: themes.Default}
	got, _ := m.Update(entriesLoadedMsg{groups: forkGroups(), repoRoot: t.TempDir()})
	m = got.(model)
	untagged := func(when string) {
		t.Helper()
		for _, b := range []string{"origin/feat-x", "keith/feat-x"} {
			if ei := rowByBranch(t, m.list.Items(), b); ei.pr != nil {
				t.Errorf("%s: %s tagged %q", when, b, ei.prTag())
			}
		}
	}
	applyPRs(&m.list, worktree.PRSet{})
	untagged("before any listing")

	got, _ = m.Update(prsLoadedMsg{prs: worktree.PRSet{ByHead: map[worktree.PRKey]worktree.PR{}}})
	m = got.(model)
	untagged("after a failed listing")

	got, _ = m.Update(prsLoadedMsg{prs: basePRs()})
	m = got.(model)
	if tag := rowByBranch(t, m.list.Items(), "origin/feat-x").prTag(); tag != "#4242 open" {
		t.Errorf("origin/feat-x tag = %q, want #4242 open", tag)
	}
	if ei := rowByBranch(t, m.list.Items(), "keith/feat-x"); ei.pr != nil {
		t.Errorf("keith/feat-x tagged %q", ei.prTag())
	}
}

// TestRemoteRepoMatchIgnoresCase pins that the remote's owner/repo and a
// PR's head repository compare without case: GitHub names are
// case-insensitive, so a remote URL written O/Base is the same repository gh
// reports as o/BASE. A regression drops the tag whenever the two are spelled
// differently.
func TestRemoteRepoMatchIgnoresCase(t *testing.T) {
	groups := singleGroup(worktree.GroupRemoteBranches,
		worktree.Entry{Type: worktree.TypeBranch, Branch: "origin/feat-x", RemoteRepo: "O/Base", RemoteBranch: "feat-x"})
	l := buildList(groups, "", "/repo", themes.Default, 80, 24)
	stubPRList(t, `[{"number": 4242, "state": "OPEN", "headRefName": "feat-x", "isCrossRepository": false, "headRepository": {"nameWithOwner": "o/BASE"}}]`)
	applyPRs(&l, worktree.PRs("/repo", worktree.PRListAll))
	if tag := rowByBranch(t, l.Items(), "origin/feat-x").prTag(); tag != "#4242 open" {
		t.Errorf("origin/feat-x tag = %q, want #4242 open", tag)
	}
}

// TestForkRowKeepsItsDisplayTitle pins that the fix changes the tag, not the
// name: keith/feat-x is still drawn as feat-x (its remote stripped), with the
// PRs in or not. A regression shows the remote's name on fork rows or drops
// the branch name.
func TestForkRowKeepsItsDisplayTitle(t *testing.T) {
	l := buildList(forkGroups(), "", "/repo", themes.Default, 80, 24)
	if got := rowByBranch(t, l.Items(), "keith/feat-x").Title(); got != "feat-x" {
		t.Errorf("Title before the PRs = %q, want feat-x", got)
	}
	applyPRs(&l, basePRs())
	if got := rowByBranch(t, l.Items(), "keith/feat-x").Title(); got != "feat-x" {
		t.Errorf("Title after the PRs = %q, want feat-x", got)
	}
}

// TestStaleFilterRunWithRemoteRowsKeepsTheTags is
// TestStaleFilterRunAfterPRsKeepsTheTags over a base remote row and a fork's:
// after the stale filter run, origin's row keeps its tag and the fork's
// stays untagged. prsCurrent asks prFor, so the staleness check and the tags
// agree; a regression where they disagree either strips origin's tag or
// re-runs applyPRs on every filter keystroke forever.
func TestStaleFilterRunWithRemoteRowsKeepsTheTags(t *testing.T) {
	m := model{phase: phaseList, width: 80, height: 24, theme: themes.Default}
	got, _ := m.Update(entriesLoadedMsg{groups: forkGroups(), repoRoot: t.TempDir()})
	m = got.(model)
	m.list.SetFilterState(list.Filtering)
	m.list.FilterInput.SetValue("feat")
	before := m.list
	staleRun := before.SetItems(before.Items())

	got, _ = m.Update(prsLoadedMsg{prs: basePRs()})
	m = got.(model)
	got, _ = m.Update(staleRun())
	m = got.(model)

	vis := m.list.VisibleItems()
	if len(vis) != 2 {
		t.Fatalf("visible = %v, want both remote rows", vis)
	}
	if tag := rowByBranch(t, vis, "origin/feat-x").prTag(); tag != "#4242 open" {
		t.Errorf("origin/feat-x tag = %q after the stale run, want #4242 open", tag)
	}
	if ei := rowByBranch(t, vis, "keith/feat-x"); ei.pr != nil {
		t.Errorf("keith/feat-x tagged %q after the stale run", ei.prTag())
	}
	if !prsCurrent(vis, m.prs) {
		t.Error("prsCurrent = false on the rows applyPRs just tagged")
	}
}

// TestFilterByPRNumberDoesNotMatchAnUntaggedRemoteRow pins the filter half
// of #388: typing the base PR's number finds origin's row and not the fork's,
// since an untagged row's filter value carries no number. A regression lists
// the fork's branch under a PR it is not part of.
func TestFilterByPRNumberDoesNotMatchAnUntaggedRemoteRow(t *testing.T) {
	l := buildList(forkGroups(), "", "/repo", themes.Default, 80, 24)
	applyPRs(&l, basePRs())
	l.SetFilterText("4242")
	var visible []string
	for _, it := range l.VisibleItems() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry {
			visible = append(visible, ei.entry.Branch)
		}
	}
	if !reflect.DeepEqual(visible, []string{"origin/feat-x"}) {
		t.Errorf("filter 4242 shows %v, want only origin/feat-x", visible)
	}
}

// TestForkWorkflowTagsEachRemoteRowFromItsOwnRepository pins #387 end to
// end, from git's remotes through Enumerate, the parsed gh listing and the
// picker's lookup: origin is your fork (me/fork) with PR #7 from its feat-x
// into upstream, and upstream (org/up) has its own PR #9 from feat-y.
// origin/feat-x shows #7 and upstream/feat-y #9; upstream/feat-x, the same
// branch name in the other repository, shows nothing, and so do a remote
// that is a local path and a remote-tracking ref whose remote is gone. A
// regression shows no tags at all when origin is a fork (every PR is
// cross-repository), or puts a fork's PR on upstream's branch of the same
// name.
func TestForkWorkflowTagsEachRemoteRowFromItsOwnRepository(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("commit", "-q", "--allow-empty", "-m", "init")
	// Enumerate fetches every remote first: ssh fails at once under
	// gitenv's GIT_SSH_COMMAND=false (and .invalid resolves nowhere), and
	// the mirror path does not exist, so no fetch reaches anything.
	git("remote", "add", "origin", "git@github.invalid:Me/Fork.git")
	git("remote", "add", "upstream", "git@github.invalid:org/up.git")
	git("remote", "add", "mirror", filepath.Join(repo, "no-such-mirror.git"))
	for _, ref := range []string{"origin/feat-x", "upstream/feat-y", "upstream/feat-x", "mirror/feat-x", "gone/feat-x"} {
		git("update-ref", "refs/remotes/"+ref, "HEAD")
	}
	stubPRList(t, `[
		{"number": 7, "state": "OPEN", "headRefName": "feat-x", "isCrossRepository": true, "title": "Fork work", "headRepository": {"nameWithOwner": "me/fork"}},
		{"number": 9, "state": "OPEN", "headRefName": "feat-y", "isCrossRepository": false, "title": "Upstream work", "headRepository": {"nameWithOwner": "org/up"}}
	]`)
	groups, err := worktree.Enumerate(repo, repo)
	if err != nil {
		t.Fatal(err)
	}
	l := buildList(groups, "main", repo, themes.Default, 80, 24)
	applyPRs(&l, worktree.PRs(repo, worktree.PRListAll))
	for branch, want := range map[string]string{
		"origin/feat-x":   "#7 open",
		"upstream/feat-y": "#9 open",
		"upstream/feat-x": "",
		"mirror/feat-x":   "",
		"gone/feat-x":     "",
	} {
		if got := rowByBranch(t, l.Items(), branch).prTag(); got != want {
			t.Errorf("%s tag = %q, want %q", branch, got, want)
		}
	}
}

// TestLocalRowPicksOnePRAcrossRepositories pins the local half of #387: a
// local branch or worktree has no repository, so its name is looked up under
// every head repository and one PR shown — a live PR over a closed or merged
// one, then the higher number. A regression shows a merged PR from one
// repository over another's open PR for the same branch name, or leaves a
// branch you push only to your fork untagged.
func TestLocalRowPicksOnePRAcrossRepositories(t *testing.T) {
	groups, root := prGroups(t)
	prs := withFork(prsIn("o/base", map[string]worktree.PR{
		"feat/local":                 {Number: 90, State: worktree.PRMerged, Title: "Base, merged"},
		"fix/wt-ollama-stop-outcome": {Number: 20, State: worktree.PROpen, Title: "Base, open"},
	}), "me/fork", map[string]worktree.PR{
		"feat/local":                 {Number: 12, State: worktree.PROpen, Title: "Fork, open"},
		"fix/wt-ollama-stop-outcome": {Number: 21, State: worktree.PRDraft, Title: "Fork, draft"},
		"no-pr":                      {Number: 5, State: worktree.PRClosed, Title: "Fork only"},
	})
	l := buildList(groups, "", root, themes.Default, 80, 24)
	applyPRs(&l, prs)
	for branch, want := range map[string]string{
		"feat/local":                 "#12 open",  // live beats merged
		"fix/wt-ollama-stop-outcome": "#21 draft", // two live: the higher number
		"no-pr":                      "#5 closed", // only the fork has one
		"fix/current":                "",
	} {
		if got := rowByBranch(t, l.Items(), branch).prTag(); got != want {
			t.Errorf("%s tag = %q, want %q", branch, got, want)
		}
	}
	if !prsCurrent(l.Items(), prs) {
		t.Error("prsCurrent = false on the rows applyPRs just tagged")
	}
}

// TestFilterByPRNumberFindsAForkRow pins the filter for a row tagged from its
// fork's own PR (#387): typing the fork PR's number finds the fork's row and
// not the base remote's row of the same branch name. A regression leaves the
// fork's PR unsearchable or lists the base row under it.
func TestFilterByPRNumberFindsAForkRow(t *testing.T) {
	l := buildList(forkGroups(), "", "/repo", themes.Default, 80, 24)
	applyPRs(&l, withFork(basePRs(), "keith/fork", map[string]worktree.PR{
		"feat-x": {Number: 77, State: worktree.PROpen, Title: "Fork feat-x"},
	}))
	l.SetFilterText("#77")
	var visible []string
	for _, it := range l.VisibleItems() {
		if ei, ok := it.(entryItem); ok && ei.kind == kindEntry {
			visible = append(visible, ei.entry.Branch)
		}
	}
	if !reflect.DeepEqual(visible, []string{"keith/feat-x"}) {
		t.Errorf("filter #77 shows %v, want only keith/feat-x", visible)
	}
}
