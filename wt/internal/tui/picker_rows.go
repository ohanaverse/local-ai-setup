package tui

import (
	"errors"
	"maps"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The worktree picker is a view over internal/cleanup's rows (the
// user-driven cleanup design's "The list"): it opens with cleanup.Gather
// (no fetch of its own, no gh), paints at once in the row list's launch
// mode, then runs the PR lookups off the update goroutine (loadRowPRsCmd)
// and swaps the filled report in. A chosen row becomes the worktree.Entry
// the launch flow has always taken (entryOfRow, selectedEntryMsg).

// rowsLoadedMsg carries one picker load: the report painted first (no PR
// lookup yet), the snapshot and inputs the PR fill continues from, the
// repository root the picker creates worktrees in, and gen, the load it
// answers (a fill for an older load is dropped).
type rowsLoadedMsg struct {
	report   cleanup.Report
	snap     cleanup.Snapshot
	in       cleanup.GatherInputs
	repoRoot string
	gen      int
	err      error
}

// rowPRsLoadedMsg is a picker load's report again, its PR lookups run.
type rowPRsLoadedMsg struct {
	Report cleanup.Report
	gen    int
}

// pickerInputs are the GatherInputs a picker load uses in cwd: this
// process's claim (so it does not hold its own root), four gh calls at a
// time once the PR fill runs, and every remote's branches (AllRemotes: the
// list shows everything local, not only what `wt cleanup --json` lists).
// Registered model artifacts are not read here
// (that needs the registry, which cmd/wt owns), so no row carries a
// protected_artifact blocker in the picker; removal re-checks in the engine.
func pickerInputs(cwd string) cleanup.GatherInputs {
	claim, err := sessionclaim.Self(cwd, "", "wt")
	if err != nil {
		claim = sessionclaim.Claim{PID: os.Getpid(), Agent: "wt"}
	}
	return cleanup.GatherInputs{CWD: cwd, Claim: claim, Parallel: 4, AllRemotes: true}
}

// loadRowsCmd gathers root's rows for the picker: the fetch the picker has
// always done on open (worktree.FetchRemotes, best effort, at most five
// seconds, a failure silent) when fetch is set, then cleanup.Gather with in
// (never fetching itself, never asking gh: SkipPRs) and Classify. A
// repository with no default branch still lists, every row Unknown.
func loadRowsCmd(root string, lookupErr error, in cleanup.GatherInputs, fetch bool) tea.Cmd {
	return func() tea.Msg {
		if lookupErr != nil {
			return rowsLoadedMsg{err: lookupErr}
		}
		if fetch {
			// The header says how the fetch went: ok, or failed with git's
			// line (never "skipped", which would hide a dead remote).
			if err := worktree.FetchRemotes(root); err != nil {
				in.Fetched = cleanup.FetchResult{At: time.Now(), Err: err.Error()}
			} else {
				in.Fetched = cleanup.FetchResult{OK: true, At: time.Now()}
			}
		}
		in.Fetch, in.SkipPRs = false, true
		snap, err := cleanup.Gather(cleanup.Git{Dir: root}, in)
		if err != nil && !errors.Is(err, cleanup.ErrNoDefaultBranch) {
			return rowsLoadedMsg{err: err}
		}
		rep := cleanup.Classify(snap, cleanup.Options{Now: time.Now()})
		return rowsLoadedMsg{report: rep, snap: snap, in: in, repoRoot: root}
	}
}

// loadRowPRsCmd runs the PR lookups the first load skipped (cleanup.FillPRs,
// gh, off the update goroutine), classifies again and drops the PR tags of
// branches whose name was reused after their PR ended (dropStalePRTags).
// snap is copied: the fill writes maps the painted report never reads, but
// the model still holds the snapshot.
func loadRowPRsCmd(root string, snap cleanup.Snapshot, in cleanup.GatherInputs, gen int) tea.Cmd {
	snap.PRs, snap.BasePRs, snap.Merges = maps.Clone(snap.PRs), maps.Clone(snap.BasePRs), maps.Clone(snap.Merges)
	return func() tea.Msg {
		g := cleanup.Git{Dir: root}
		cleanup.FillPRs(g, &snap, in)
		rep := cleanup.Classify(snap, cleanup.Options{Now: time.Now()})
		dropStalePRTags(g, rep.Rows)
		return rowPRsLoadedMsg{Report: rep, gen: gen}
	}
}

// dropStalePRTags clears the PR tag of every worktree or local branch row
// whose ended (merged or closed) PR's head neither is in the branch nor has
// the branch's tip in it: the name was reused after the PR (#389, #451).
// cleanup looks PRs up by branch name alone, so without this the new branch
// shows the old PR. A remote row, an open PR and anything git cannot answer
// keep the tag. The verdict is cleanup's and is left as it is.
func dropStalePRTags(g cleanup.Git, rows []cleanup.Row) {
	for i := range rows {
		r := &rows[i]
		if r.Kind == cleanup.KindRemote || r.PRTag == nil || r.PRTag.State == "OPEN" {
			continue
		}
		head, tip := r.PRTag.HeadRefOid, r.Tip
		if head == "" || tip == "" || head == tip {
			continue
		}
		if ancestor(g, head, tip) == cleanup.TriNo && ancestor(g, tip, head) == cleanup.TriNo {
			r.PRTag = nil
		}
	}
}

// ancestor is git's answer to "is a an ancestor of b": yes, no, or unknown
// (a commit this repository lacks, any other failure).
func ancestor(g cleanup.Git, a, b string) cleanup.Tri {
	o, err := g.Run("merge-base", "--is-ancestor", a, b)
	switch {
	case err != nil:
		return cleanup.TriUnknown
	case o.Code == 0:
		return cleanup.TriYes
	case o.Code == 1:
		return cleanup.TriNo
	}
	return cleanup.TriUnknown
}

// entryOfRow is the launch target a picker row stands for, in the shape the
// old picker's entries had: the root and every worktree are their path and
// branch (the root marked Root); a local branch with no worktree is a branch
// entry with no path, so selectedEntryMsg creates its worktree; a remote-only
// branch is its remote spelling ("origin/x") with RemoteBranch the bare
// name, which EnsureForBranch turns into a local tracking branch.
func entryOfRow(r cleanup.Row) worktree.Entry {
	switch r.Kind {
	case cleanup.KindBranch:
		return worktree.Entry{Type: worktree.TypeBranch, Branch: r.Branch}
	case cleanup.KindRemote:
		return worktree.Entry{Type: worktree.TypeBranch, Branch: r.Remote + "/" + r.Name, RemoteBranch: r.Name}
	}
	branch := r.Branch
	if branch == "" {
		branch = "(detached)"
	}
	t := worktree.TypeWorktree
	if r.Current {
		t = worktree.TypeCurrent
	}
	return worktree.Entry{Type: t, Branch: branch, Path: r.Path, Root: r.Primary}
}

// gotRows applies a picker load: the list in launch mode, the cursor on the
// row a create just made (pendingHighlight) or the innermost worktree holding
// the caller's directory, and the PR fill when the load skipped one.
func (m model) gotRows(msg rowsLoadedMsg) (model, tea.Cmd) {
	if msg.err != nil {
		if m.ready {
			// Reload after a create: the list is still usable, but the error
			// must be visible: View renders it above the list.
			m.listError = msg.err.Error()
		} else {
			m.status = "error: " + msg.err.Error()
		}
		return m, nil
	}
	m.listError = ""
	// The load is over: take the placeholder down. Only the placeholder: a
	// status another path set while the load was in flight (a pinned agent's
	// config error, say) is still worth showing.
	if m.status == loadingStatus {
		m.status = ""
	}
	m.repoRoot = msg.repoRoot
	m.rowsGen++
	rows := newRowList(msg.report, rowListOpts{Mode: modeLaunch, Theme: m.theme, Now: time.Now()})
	rows.width, rows.height = m.rows.width, m.rows.height
	switch here := deepestCurrent(msg.report); {
	case m.pendingHighlight != "":
		rows.selectWhere(func(r cleanup.Row) bool { return r.Kind == cleanup.KindWorktree && r.Branch == m.pendingHighlight })
		m.pendingHighlight = ""
	case here != "":
		rows.selectWhere(func(r cleanup.Row) bool { return r.Kind == cleanup.KindWorktree && r.Path == here })
	}
	m.rows, m.ready = rows, true
	if !msg.snap.PRsSkipped {
		return m, nil
	}
	return m, loadRowPRsCmd(msg.repoRoot, msg.snap, msg.in, m.rowsGen)
}

// updateRows hands msg to the row list and turns what it chose into the
// launch flow's messages.
func (m model) updateRows(msg tea.Msg) (model, tea.Cmd) {
	next, cmd := m.rows.Update(msg)
	m.rows = next.(cleanupListModel)
	return m, cmd
}
