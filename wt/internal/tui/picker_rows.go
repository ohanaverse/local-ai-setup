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
	stamp    refreshStamp // the root's fetch stamp, read in the command
	gen      int
	err      error
}

// rowPRsLoadedMsg is a picker load's report again, its PR lookups run, and
// the snapshot those lookups filled: the row actions' removal funcs are built
// from it (swapRemoveFuncs), so the engine's re-check stands on the rows the
// screen now shows.
type rowPRsLoadedMsg struct {
	Report cleanup.Report
	Snap   cleanup.Snapshot
	gen    int
}

// pickerInputs are the GatherInputs a picker load uses in cwd: this
// process's claim (so it does not hold its own root), four gh calls at a
// time once the PR fill runs, and every remote's branches (AllRemotes: the
// list shows everything local, not only what `wt worktrees --json` lists).
// Fetched reads "not run": the picker fetches nothing on its own, and the
// header and the origin rows must say so rather than name a --no-fetch flag
// there is none of (a `F` refresh overwrites it with the fetch's result).
// Registered model artifacts are not read here
// (that needs the registry, which cmd/wt owns), so no row carries a
// protected_artifact blocker in the picker; removal re-checks in the engine.
// OriginRows is set because the picker shows and acts on origin rows (its
// `o`, exactly GatherInputs.OriginRows's contract): the PR fill then makes
// the per-branch `gh pr list --base` query, so a listed origin row reads
// proven wherever `wt cleanup`'s list would — the engine removes a remote
// row only when what was listed said proven.
func pickerInputs(cwd string) cleanup.GatherInputs {
	claim, err := sessionclaim.Self(cwd, "", "wt")
	if err != nil {
		claim = sessionclaim.Claim{PID: os.Getpid(), Agent: "wt"}
	}
	return cleanup.GatherInputs{CWD: cwd, Claim: claim, Parallel: 4, AllRemotes: true, OriginRows: true,
		Fetched: cleanup.FetchResult{NotAttempted: true}}
}

// loadRowsCmd gathers root's rows for the picker. With fetch set (the
// picker's `F`, refreshRowsChosenMsg) it fetches the remotes first —
// in.FetchRemotes when the call carries one, else worktree.FetchRemotes, at
// most five seconds; ok or failure recorded in in.Fetched for the header to
// render, a failure shown there, never swallowed. Without it (every open)
// nothing fetches. Then cleanup.Gather with in (never fetching itself,
// never asking gh: SkipPRs) and Classify. A repository with no default
// branch still lists, every row Unknown.
func loadRowsCmd(root string, lookupErr error, in cleanup.GatherInputs, fetch bool) tea.Cmd {
	return func() tea.Msg {
		if lookupErr != nil {
			return rowsLoadedMsg{err: lookupErr}
		}
		if fetch {
			// The header says how the fetch went: ok, or failed with git's
			// line (never "skipped", which would hide a dead remote).
			in.Fetched = cleanup.RecordFetch(in.FetchRemotes, root)
		}
		in.Fetch, in.SkipPRs = false, true
		snap, err := cleanup.Gather(cleanup.Git{Dir: root}, in)
		if err != nil && !errors.Is(err, cleanup.ErrNoDefaultBranch) {
			return rowsLoadedMsg{err: err}
		}
		rep := cleanup.Classify(snap, cleanup.Options{Now: time.Now()})
		return rowsLoadedMsg{report: rep, snap: snap, in: in, repoRoot: root, stamp: readRefreshStamp(refreshRoot(root, rep))}
	}
}

// loadRowPRsCmd runs the PR lookups the first load skipped (cleanup.FillPRs,
// gh, off the update goroutine), classifies again and drops the PR tags of
// branches whose name was reused after their PR ended (dropStalePRTags).
// snap is copied: the fill writes maps the caller's snapshot never reads;
// the message carries the filled copy, which the row actions' removal funcs
// are built from.
func loadRowPRsCmd(root string, snap cleanup.Snapshot, in cleanup.GatherInputs, gen int) tea.Cmd {
	snap.PRs, snap.BasePRs, snap.Merges = maps.Clone(snap.PRs), maps.Clone(snap.BasePRs), maps.Clone(snap.Merges)
	return func() tea.Msg {
		g := cleanup.Git{Dir: root}
		cleanup.FillPRs(g, &snap, in)
		rep := cleanup.Classify(snap, cleanup.Options{Now: time.Now()})
		dropStalePRTags(g, rep.Rows)
		return rowPRsLoadedMsg{Report: rep, Snap: snap, gen: gen}
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
// entry with no path, so selectedEntryMsg binds the worktree its launch gate
// creates at the commit point; a remote-only branch is its remote spelling
// ("origin/x") with RemoteBranch the bare name, which that creation turns
// into a local tracking branch.
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

// gotRows applies a picker load. The initial open builds the list in launch
// mode with the cursor on the innermost worktree holding the caller's
// directory — the fresh build serves the open alone: a creation happens at
// the launch gate's commit point and reloads nothing. A `F` refresh's reload is a report swap
// like the PR fill's: withReport keeps the filter, the highlighted row and
// the scroll, and the header's clock moves to now so the refresh note counts
// from this reload — the same whether the fetch succeeded or failed while
// the re-gather succeeded (the fetch's outcome is the header's line either
// way). Either way the load's outcome ends the refresh-in-flight guard and
// keeps the inputs, so the next `F` re-runs exactly this load; the PR fill
// follows when the load skipped one.
func (m model) gotRows(msg rowsLoadedMsg) (model, tea.Cmd) {
	if m.rows.done {
		// Nothing lands on a done list: in the `r`/`R` quit window the
		// hand-off the key chose must survive to finishRowSession (a reload
		// here would rebuild the list past it).
		return m, nil
	}
	refreshing := m.rowsRefreshing
	m.rowsRefreshing = false
	m.rows.fetching = false
	if refreshing && m.reloadGen != m.rowsGen {
		// A removal run began or rebuilt the rows since this reload was
		// dispatched: its report predates the run (it would bring a removed
		// row back), so the list keeps the run's own rows.
		return m, nil
	}
	if msg.err != nil {
		if m.ready {
			// A `F` refresh whose re-read failed: the list is still usable,
			// but the error must be visible: View renders it above the list.
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
	m.rowsIn = msg.in
	m.rowsGen++
	var rows cleanupListModel
	if refreshing {
		base := m.rows
		base.now = base.clock()
		base.opts.Now, base.opts.Root = base.now, msg.repoRoot
		st := msg.stamp
		base.opts.Stamp = &st
		base.fetching = false
		rows = base.withReport(msg.report)
		// The reload's snapshot builds the row actions' removal funcs (the
		// engine's re-check stands on the rows now on screen), and its PR
		// fill swaps in its own instance when it lands — the build waits
		// for its swap (swapRemoveFuncs), so a flow in progress sees
		// neither until it returns to rest.
		swapRemoveFuncs(&rows, func() RemoveFuncs { return m.removeFuncsFor(msg.snap, msg.in) })
	} else {
		rows = newRowList(msg.report, rowListOpts{Mode: modeLaunch, Theme: m.theme, Now: time.Now(), Root: msg.repoRoot,
			Stamp: &msg.stamp, RemoveFuncs: m.removeFuncsFor(msg.snap, msg.in)})
		rows.width, rows.height = m.rows.width, m.rows.height
		if here := deepestCurrent(msg.report); here != "" {
			rows.selectWhere(func(r cleanup.Row) bool { return r.Kind == cleanup.KindWorktree && r.Path == here })
		}
	}
	m.rows, m.ready = rows, true
	if !msg.snap.PRsSkipped {
		return m, nil
	}
	return m, loadRowPRsCmd(msg.repoRoot, msg.snap, msg.in, m.rowsGen)
}

// updateRows hands msg to the row list and turns what it chose into the
// launch flow's messages.
//
// A removal run's start and the rebuild after it each bump rowsGen: a PR fill
// (or `F` reload) dispatched before the run answers rows the run changed, and
// landing late it would bring a removed row back and swap in an engine built
// from the pre-removal snapshot.
func (m model) updateRows(msg tea.Msg) (model, tea.Cmd) {
	before := m.rows.phase
	next, cmd := m.rows.Update(msg)
	m.rows = next.(cleanupListModel)
	if after := m.rows.phase; (after == cleanupPhaseRunning && before != cleanupPhaseRunning) ||
		(before == cleanupPhaseRefreshing && after == cleanupPhaseList) {
		m.rowsGen++
	}
	return m, cmd
}

// removeFuncsFor is the row actions' RemoveFuncs for one load's snapshot and
// inputs — the picker's instance of the removal engine, `wt cleanup`'s
// cleanupRemoveFuncs through cmd/wt's builder — or the zero funcs, whose
// actions all say they are unavailable, for a session started without one.
// Every load builds its own instance, because the engine's re-check stands
// on that load's snapshot; the PR fill's landing swaps its instance in.
func (m model) removeFuncsFor(snap cleanup.Snapshot, in cleanup.GatherInputs) RemoveFuncs {
	if m.removeFuncs == nil {
		return RemoveFuncs{}
	}
	return m.removeFuncs(snap, in)
}

// swapRemoveFuncs offers a load's removal funcs to its rebuilt list. A
// removal flow in progress keeps the funcs its sealed previews came from (the
// engine must not change under what the user is confirming), so the build is
// deferred to the list's next return to rest — pendingRM, latest landing
// winning — rather than built here and thrown away with the refused swap.
// Nothing swaps on a done list: the `r`/`R` quit window hands off through the
// instance its key checked.
func swapRemoveFuncs(rows *cleanupListModel, build func() RemoveFuncs) {
	if rows.done {
		return
	}
	if rows.phase != cleanupPhaseList {
		rows.pendingRM = build
		return
	}
	applyRemoveFuncs(rows, build())
}

// applyRemoveFuncs puts rm on the list — both where the flow reads it and
// where a rebuild carries it from — and drops any deferred build it answers.
func applyRemoveFuncs(rows *cleanupListModel, rm RemoveFuncs) {
	rows.rm, rows.opts.RemoveFuncs = rm, rm
	rows.pendingRM = nil
}

// applyPendingRemoveFuncs applies the fill swap a flow deferred
// (swapRemoveFuncs), called at the list's every return to rest: gotPreviews'
// error path, cancelConfirm (esc, the n/alt/space cancel, the wrong-typed-word
// refusal), gotRefetch's landing, and gotRefresh's post-run rebuild. It runs
// at rest and undone only; a flow that starts before it runs keeps its own.
func (m *cleanupListModel) applyPendingRemoveFuncs() {
	if m.done || m.pendingRM == nil || m.phase != cleanupPhaseList {
		return
	}
	build := m.pendingRM
	applyRemoveFuncs(m, build())
}
