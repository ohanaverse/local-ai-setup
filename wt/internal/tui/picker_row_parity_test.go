package tui

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// divergentRoots makes a repo at dir and returns two commit oids neither of
// which is an ancestor of the other: dropStalePRTags' ancestor probes answer
// "no" both ways for them.
func divergentRoots(t *testing.T, dir string) (string, string) {
	t.Helper()
	run := func(args ...string) string {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		var out strings.Builder
		cmd.Stdout = &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return strings.TrimSpace(out.String())
	}
	run("init", "-q", "--initial-branch=main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "T")
	tree := run("write-tree")
	return run("commit-tree", tree, "-m", "one"), run("commit-tree", tree, "-m", "two")
}

// TestPickerRefreshDropsAStalePRTag pins #578's first half: the launch
// picker's post-run rebuild applies the same stale-PR-tag rule its loads do
// (dropStalePRTags), so a reused branch's rows do not shift after a removal.
// The manage list's rebuild keeps its own behavior — its loads never dropped
// the tag.
func TestPickerRefreshDropsAStalePRTag(t *testing.T) {
	dir := t.TempDir()
	head, tip := divergentRoots(t, dir)

	rebuild := func(mode listMode) *cleanup.PRInfo {
		rep := cleanup.Report{Repo: dir, Rows: []cleanup.Row{{
			Name: "reused", Kind: cleanup.KindWorktree, Status: cleanup.StatusProven,
			Tip: tip, PRTag: &cleanup.PRInfo{State: "MERGED", HeadRefOid: head},
		}}}
		m := newRowList(rep, rowListOpts{Mode: mode, Theme: themes.Default, Now: time.Now()})
		m.phase = cleanupPhaseRefreshing
		m.run = &cleanupRun{}
		next, _ := m.Update(cleanupRefreshMsg{report: rep})
		return next.(cleanupListModel).report.Rows[0].PRTag
	}
	if got := rebuild(modeLaunch); got != nil {
		t.Errorf("the launch picker's rebuild kept a reused branch's ended-PR tag: %+v", got)
	}
	if got := rebuild(modeManage); got == nil {
		t.Error("the manage list's rebuild dropped the PR tag; that list's rows are unchanged")
	}
}

// TestPickerLoadCarriesProtectedPaths pins #578's second half: the registry's
// protected paths reach the picker's loads (handed in by cmd, which owns the
// registry), so a row holding a registered model artifact shows its
// protected_artifact tag from the load — the engine's re-check still refuses
// the removal either way.
func TestPickerLoadCarriesProtectedPaths(t *testing.T) {
	r := newRmRepo(t)
	r.merged("one")
	one := filepath.Join(r.dir, ".worktrees", "one")

	old := findRepoRoot
	findRepoRoot = func() (string, error) { return r.dir, nil }
	t.Cleanup(func() { findRepoRoot = old })

	msg, ok := loadPickerCmd([]string{one})().(rowsLoadedMsg)
	if !ok {
		t.Fatal("the open load did not answer rowsLoadedMsg")
	}
	if !slices.Equal(msg.in.ProtectedPaths, []string{one}) {
		t.Errorf("the load's inputs carry %v; want [%s]", msg.in.ProtectedPaths, one)
	}
	var row *cleanup.Row
	for i := range msg.report.Rows {
		if msg.report.Rows[i].Name == "one" {
			row = &msg.report.Rows[i]
		}
	}
	if row == nil {
		t.Fatal("no row for the merged worktree one")
	}
	for _, b := range row.Blockers {
		if b.Code == cleanup.BlockProtectedArtifact {
			return
		}
	}
	t.Errorf("row one carries no protected_artifact blocker: %+v", row.Blockers)
}
