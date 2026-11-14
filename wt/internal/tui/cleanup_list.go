package tui

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The row list (layout A of the cleanup design, unified by the user-driven
// cleanup design's "The list"): the report's header, then every row grouped
// by kind (cleanup.KindGroups: WORKTREES, LOCAL BRANCHES, REMOTE BRANCHES),
// each row carrying its verdict (proven / less sure / unique work / in use),
// its cleanup.RowTags (a TagWarn tag in the warning colour) and its
// sentence, and a detail line under the list that explains the highlighted
// row. The words — headings, tags, the evidence sentence, the header lines,
// which rows are tickable — are all internal/cleanup/present.go's; this file
// only lays them out and styles them.
//
// One component, two modes (rowListOpts.Mode):
//   - manage (`wt cleanup`): a checkbox column; space ticks, `a` ticks every
//     proven row, enter removes the ticked rows.
//   - launch (the picker): a first item "+ New worktree…" (enter or `n`
//     emits newWorktreeChosenMsg), enter on any row emits rowChosenMsg —
//     the root, an in-use or held row too: launching warns, it never blocks
//     — and nothing is ticked. Rows carry a "current" and a "default" tag,
//     `/` filters the rows by name (row_list_launch.go), and the PR tags
//     arrive later (withReport).
//
// In both modes the row keys act on the highlighted row alone — the ticks
// choose rows for enter, never for these — through the same confirm flow:
// `x` removes it, `d` removes it and its unmerged branch (on a branch row `d`
// is `x`), `o` deletes only the origin ref folded into it (its own typed
// phrase, the engine's remote-only item). `r` hands the highlighted row to
// rescue and `R` hands off `wt restore-root` for the root; both quit the
// program and the hand-off runs on the real terminal once the screen is gone
// — RunCleanupList then re-enters `wt cleanup`'s list with a fresh report,
// while the picker (finishRowSession) ends its session there and wt exits.
//
// A row's text is never cut: when it does not fit beside the name it wraps
// onto continuation lines under the text column, so a row is as many lines
// as its text needs. That is why this screen is a small list of its own
// rather than a bubbles list, whose delegate gives every item the same
// height. The name column is what gives way to the width.
//
// Removal goes through a confirm screen and the removal engine
// (cleanup_confirm.go); this file runs no git and removes nothing.

// listMode is which entry point the list serves.
type listMode int

const (
	modeManage listMode = iota // `wt cleanup`: tick and remove
	modeLaunch                 // the picker: choose a row to launch in
)

// rowListOpts configures newRowList.
type rowListOpts struct {
	Mode  listMode
	Theme themes.Theme
	Now   time.Time // the header's clock
	// Root is the repository root the header's refresh note reads: a fetch
	// at this root is what "refreshed" means (worktree.FetchRemotes, and
	// RemoveFuncs.Refetch, run there). Empty falls back to the report's Repo.
	Root string
	// Stamp is the fetch stamp the header's refresh note shows, read by the
	// load command (off the update loop). Nil reads it here, once, when the
	// list is first built; the list then keeps the value in its opts so a
	// report swap (withReport) never reads git again.
	Stamp *refreshStamp
	// RemoveFuncs is the removal funcs the row keys act through: the launch
	// selector passes the instance its load built (RemoveFuncsBuilder), and
	// RunCleanupList sets its own on each manage pass right after the build.
	// The zero value makes every removal say it is unavailable.
	RemoveFuncs RemoveFuncs
}

// refreshStamp is when git fetch last ran at a root (worktree.LastFetched):
// OK false means there is no stamp.
type refreshStamp struct {
	At time.Time
	OK bool
}

// readRefreshStamp reads root's stamp: two git/stat calls, so callers run
// it in a command, never in Update.
func readRefreshStamp(root string) refreshStamp {
	if root == "" {
		return refreshStamp{}
	}
	at, ok := worktree.LastFetched(root)
	return refreshStamp{At: at, OK: ok}
}

// rowChosenMsg is launch mode's enter on a row: launch there.
type rowChosenMsg struct{ Row cleanup.Row }

// newWorktreeChosenMsg is launch mode's enter on "+ New worktree…", or `n`.
type newWorktreeChosenMsg struct{}

// newWorktreeLabel is launch mode's first item.
const newWorktreeLabel = "+ New worktree…"

// cleanupItem is one entry of the list: a group heading, a row, or launch
// mode's new-worktree item.
type cleanupItem struct {
	heading     string // set on a heading; the rest is then unset
	newWorktree bool   // launch mode's "+ New worktree…"; the rest is unset
	row         cleanup.Row
	text        string        // cleanup.RowText for the row
	tags        []cleanup.Tag // the tags drawn for the row (rowTags)
	locked      bool          // can never be ticked or removed: drawn [-], space does nothing
}

// cleanupLocked reports whether a row can never be ticked: an in-use row, a
// row with any hard blocker, a worktree whose state could not be read (the
// engine refuses it whatever is approved; a missing directory is the typical
// case, any unreadable state counts), and a
// remote row unless it is a proven origin branch (the engine removes nothing
// else on a remote: only origin, only proven). Every other row may be ticked
// by the user; only Tickable local rows are ticked for them (by default and
// by `a`): an origin row is never ticked for the user, who must space each
// one, because deleting it is
// visible to everyone with access to the repository. A ticked row that is not
// proven, or loses anything, goes through the typed confirmation
// (cleanup_confirm.go) before anything is removed; an origin row through its
// own phrase.
func cleanupLocked(r cleanup.Row) bool {
	if r.Kind == cleanup.KindRemote && (!r.Tickable() || r.Remote != "origin") {
		return true
	}
	if r.Status == cleanup.StatusInUse || r.HardStop != "" {
		return true
	}
	// A worktree whose state could not be read — any cause, a missing
	// directory being the typical one ("could not read the worktree's
	// state") — is the engine's own
	// unconditional refusal (worktreeGate's GatesUnknown gate), so the list
	// locks it like every hard stop. The gates_unavailable reason rides with
	// the flag on a classified row; either one names the same refusal.
	// losses: raw, the effective view never changes the gates-unknown flag
	if r.Losses.GatesUnknown {
		return true
	}
	for _, x := range r.Reasons {
		if x.Code == cleanup.ReasonGatesUnavailable {
			return true
		}
	}
	// Only a hard stop locks: held and default_branch are floors the user
	// may tick past (the engine then asks --accept-loss's typed confirmation).
	for _, b := range r.Blockers {
		if cleanup.IsHardStop(b.Code, r.Kind) {
			return true
		}
	}
	return false
}

// rowTags are the tags the list draws for a row: cleanup.RowTags without
// "you are here", which RowTags gives every worktree holding the caller's
// directory; the row's sentence (cleanup.RowText) already says it, on the
// innermost one only. In the manage list (drift true) the root row's drift
// badge rides behind them: the root_drift soft label's detail as a warn tag,
// the badge the user-driven cleanup design puts on the root row ("root is on
// X, not main"). The picker passes false: its header already says it once
// (headerBlock), whatever row the cursor is on.
func rowTags(r cleanup.Row, drift bool) []cleanup.Tag {
	var out []cleanup.Tag
	for _, t := range cleanup.RowTags(r) {
		if t.Text != "you are here" {
			out = append(out, t)
		}
	}
	if d := rootDriftLabel(r); drift && d != "" {
		out = append(out, cleanup.Tag{Text: d, Severity: cleanup.TagWarn})
	}
	return out
}

// rootDriftLabel is the row's root_drift soft label's detail — rootclaim.Drift's
// text as the classifier carried it live ("root is on <b>, not <d>" or the
// literal "root is detached") — and "" when the row carries no label (the root
// on its default branch, or no default known). The list never re-shapes the
// line and never invents one: classify puts the label on the root's own row
// only, and only when a default branch is known.
func rootDriftLabel(r cleanup.Row) string {
	for _, re := range r.Reasons {
		if re.Code == cleanup.ReasonRootDrift {
			return re.Detail
		}
	}
	return ""
}

// rootDriftOf is the report's root drift line — the one root_drift label any
// row carries (only the root row ever does) — and "" when none does.
func rootDriftOf(r cleanup.Report) string {
	for _, row := range r.Rows {
		if d := rootDriftLabel(row); d != "" {
			return d
		}
	}
	return ""
}

// verdictText is the row's status in words: what the status headings said
// before the list was grouped by kind.
func verdictText(s cleanup.Status) string {
	return strings.ReplaceAll(string(s), "_", " ")
}

// cleanupListModel is the row list's Bubble Tea model.
type cleanupListModel struct {
	opts   rowListOpts
	report cleanup.Report
	theme  themes.Theme
	now    time.Time
	items  []cleanupItem
	ticked map[int]bool // by item index
	cursor int          // item index of the highlighted item; -1 with none
	offset int          // the first list line on screen
	rows   int          // number of row items (headings and the new-worktree item excluded)
	// table is the shared column layout every row line renders through
	// (rowtable): built from the report's rows once, refitted to m.width on
	// every frame. Nil only before a report is loaded.
	table         *tuilayout.Columns
	width, height int
	done          bool

	// The removal flow (cleanup_confirm.go).
	rm      RemoveFuncs
	phase   cleanupPhase
	status  string // one line under the list ("nothing ticked", "cancelled...")
	pending int    // rows being previewed
	// pendingRM is a fill's removal-funcs build held while a flow is in
	// progress: swapRemoveFuncs defers it to the list's next return to rest,
	// where applyPendingRemoveFuncs takes it — every path that sets the phase
	// back to cleanupPhaseList (gotPreviews' error path, cancelConfirm,
	// gotRefetch, gotRefresh's post-run rebuild) calls that. Latest landing
	// wins; a done list holds none (nothing swaps after done).
	pendingRM func() RemoveFuncs
	// chosenByTicks says what chose the rows of the flow in progress: the
	// ticks (enter) or the cursor (the row keys) — chosenWord's wording.
	chosenByTicks bool
	// fillGen numbers the manage list's fill dispatches: Init sends one for
	// a facts report, and any report replacement (an F refetch) spends the
	// generation so a fill dispatched before it is dropped on landing.
	fillGen int
	// seedBranch pre-toggles the confirm entries' "also delete unmerged
	// branch" (`d` on a worktree row).
	seedBranch bool
	conf       *cleanupConfirm   // the confirm screen; nil outside it
	run        *cleanupRun       // the last run; set from the progress screen on
	log        []removal.Outcome // the session's outcomes (a new pass starts with the ones before it)
	exitErr    error             // returned by RunCleanupList (the post-run re-read failed)
	// quitAfterRun: a signal came during a run; the program quits when the
	// run ends (Ruling T).
	quitAfterRun bool
	// rescueRow is the row `r` chose: the list quits and the session hands it
	// to RemoveFuncs.Rescue once the alt screen is gone — RunCleanupList then
	// re-enters the list (set only in the pass that chose it), the picker
	// (finishRowSession) ends its session there.
	rescueRow *cleanup.Row
	// restoreRoot is set when `R` chose `wt restore-root`: the list quits and
	// the session hands off to RemoveFuncs.RestoreRoot once the alt screen is
	// gone (one hand-off per pass) — RunCleanupList then re-enters the list,
	// the picker (finishRowSession) ends its session there.
	restoreRoot bool
	clock       func() time.Time // the list's clock on a rebuild
	// refreshNote is the header's last line — "refreshed 3m ago" or "not
	// refreshed here" — read once when the list was built (refreshedNote):
	// it is a git read, and View must not spawn git every frame.
	stamp refreshStamp
	// fetching is true while the picker's `F` runs: the header says so.
	fetching bool

	// Launch mode's type-to-filter (row_list_launch.go): filter is the
	// query, filtering is true while it is being typed (every printable key
	// goes into it then).
	filter    string
	filtering bool
}

// newCleanupList builds the manage-mode list `wt cleanup` shows, standing on
// rm. rm goes into the opts as well as the model's own field:
// opts.RemoveFuncs is what every rebuild (withReport, gotRefresh) seeds `rm`
// from, so seeding only one of the two leaves them divergent for the rest of
// the session and makes each future rebuild path responsible for copying `rm`
// back by hand.
func newCleanupList(r cleanup.Report, th themes.Theme, now time.Time, rm RemoveFuncs) cleanupListModel {
	return newRowList(r, rowListOpts{Mode: modeManage, Theme: th, Now: now, RemoveFuncs: rm})
}

// newRowList builds the list for r in o.Mode: launch mode's new-worktree
// item first, then every kind group's heading and rows in
// cleanup.KindGroups' order. In manage mode Tickable local rows start
// ticked; in launch mode nothing is. The cursor starts on the first item.
func newRowList(r cleanup.Report, o rowListOpts) cleanupListModel {
	m := cleanupListModel{opts: o, report: r, theme: o.Theme, now: o.Now, rm: o.RemoveFuncs, ticked: map[int]bool{}, cursor: -1, clock: time.Now}
	root := refreshRoot(o.Root, r)
	if o.Stamp != nil {
		m.stamp = *o.Stamp
	} else {
		m.stamp = readRefreshStamp(root)
	}
	st := m.stamp
	m.opts.Stamp = &st
	here := deepestCurrent(r)
	if o.Mode == modeLaunch {
		m.items = append(m.items, cleanupItem{newWorktree: true})
		m.cursor = 0
	}
	for _, g := range cleanup.KindGroups(r) {
		m.items = append(m.items, cleanupItem{heading: g.Heading})
		for _, row := range g.Rows {
			it := cleanupItem{row: row, text: cleanup.RowText(r, row), tags: rowTags(row, o.Mode != modeLaunch), locked: cleanupLocked(row)}
			if o.Mode == modeLaunch {
				it.tags = append(launchTags(r, row, here), it.tags...)
			}
			if o.Mode == modeManage && row.Tickable() && !it.locked && row.Kind != cleanup.KindRemote {
				m.ticked[len(m.items)] = true
			}
			if m.cursor < 0 {
				m.cursor = len(m.items)
			}
			m.items = append(m.items, it)
			m.rows++
		}
	}
	// One table for the header and every row; the prefix is the rows' fixed
	// left margin (4 in launch mode, 8 in manage: the checkbox).
	prefix := rowPrefixWidthLaunch
	if o.Mode != modeLaunch {
		prefix = rowPrefixWidthManage
	}
	m.table = newRowTable(r.Rows, prefix)
	return m
}

// launch reports whether the list is in launch mode.
func (m *cleanupListModel) launch() bool { return m.opts.Mode == modeLaunch }

// empty reports whether the list has nothing to highlight: a manage list
// with no rows (a launch list always has its new-worktree item).
func (m *cleanupListModel) empty() bool { return m.cursor < 0 }

// HandOffNote is what a `r`/`R` hand-off returns when it ended with the
// repository exactly as the list left it or with its job done — declined at
// its prompt, refused before anything was created, or finished — and the
// session should carry on: RunCleanupList re-enters the list with Text on its
// status line. Err is what the hand-off would otherwise have returned (nil
// when it succeeded, the command's own exit error for a decline or a
// refusal): a caller that cannot re-enter (the launch picker, a nil Refetch)
// returns it unchanged, so the exit status of a hand-off that ends the
// session is as it always was. A hand-off that FAILED midway, or could not
// start, returns a plain error and ends the session so its recovery text is
// the last thing printed.
type HandOffNote struct {
	Text string
	Err  error
}

func (n *HandOffNote) Error() string {
	if n.Err != nil {
		return n.Err.Error()
	}
	return n.Text
}

func (n *HandOffNote) Unwrap() error { return n.Err }

// noteOf splits a hand-off's return: the note when it is one (and the error
// the session would end with without re-entry), else the plain error.
func noteOf(err error) (note *HandOffNote, rest error) {
	if errors.As(err, &note) {
		return note, note.Err
	}
	return nil, err
}

// RunCleanupList shows r as the tick list on the alt screen and runs the
// whole session. enter removes the ticked rows through rm after a
// confirmation; `r` and `R` run their action on the real terminal once the
// screen is gone and then re-enter the list with a fresh report
// (RemoveFuncs.Refetch), so one session can do several actions. Every outcome
// of every pass prints exactly once: the ones not yet printed are flushed
// before each hand-off (which can end the process at its prompt) and the
// remainder when the session ends, however it ends (what was removed and
// each deleted branch's commit outlive the alt screen).
func RunCleanupList(r cleanup.Report, th themes.Theme, rm RemoveFuncs) error {
	// One pass per program run, each built fresh from its own report: the
	// confirm state, the ticks and the hand-off flags never cross a pass.
	// logged is the whole session's outcome log — every pass's outcomes join
	// it — and it is the one thing that does travel between passes. printed
	// counts the outcomes already on stdout; status is the line a hand-off
	// left for the next pass.
	var logged []removal.Outcome
	var last cleanupListModel
	haveLast := false
	var err, werr error
	printed := 0
	status := ""
	// The program runner is read once per session from the funcs handed in
	// (nil: the real alt-screen program), never a package global, so a test's
	// fake cannot leak into a parallel one.
	run := rm.Program
	if run == nil {
		run = realCleanupProgram
	}
	for {
		m := newCleanupList(r, th, time.Now(), rm)
		m.log = logged
		m.status = status
		status = ""
		final, stop, perr := run(m)
		defer stop() // idempotent; the hand-offs and the re-entry stop it first
		fm, ok := final.(cleanupListModel)
		if ok {
			last, haveLast, logged = fm, true, fm.log
		}
		if perr != nil {
			err = perr
			break
		}
		if !ok {
			break
		}
		if fm.exitErr != nil {
			err = fm.exitErr
			break
		}
		// The rescue and the restore-root hand-offs ask and print on the
		// real terminal, so they run here, after the alt screen has closed.
		// The log so far prints first: a hand-off's prompt can be answered
		// with ctrl+c, which ends the process (the forwarding is stopped).
		if fm.rescueRow != nil || fm.restoreRoot {
			printed, werr = flushLog(fm.log, printed, werr)
		}
		var note *HandOffNote
		var herr error
		if herr = runRescue(fm, rm, stop); herr != nil {
			if note, herr = noteOf(herr); note == nil {
				err = herr
				break
			}
		}
		if herr == nil && note == nil {
			if herr = runRestoreRoot(fm, rm, stop); herr != nil {
				if note, herr = noteOf(herr); note == nil {
					err = herr
					break
				}
			}
		}
		// Re-entry is only for those two hand-offs: a pass with none ends
		// the session (a removal run's exit from the results screen is
		// final). A nil Refetch cannot re-enter either — with no re-read
		// there is no fresh report, and a pass is never built from a cached
		// one.
		if (fm.rescueRow == nil && !fm.restoreRoot) || rm.Refetch == nil {
			err = herr
			break
		}
		// Re-enter: the re-read's own failure ends the session (its error,
		// never a silent retry); its report builds the next pass's model.
		rep, rerr := rm.Refetch()
		if rerr != nil {
			err = rerr
			break
		}
		stop() // the forwarding stops before the next pass's program runs
		r = rep
		if note != nil {
			status = note.Text
		}
	}
	// The rest of the session's log prints at the end — however the session
	// ended — so every outcome is out exactly once.
	// Error precedence: an error from the session (the program, a hand-off or
	// the re-read) wins over a failed write of the log. Both orders drop one
	// error; the session's is the one that says what the run did, and the two
	// coincide only on a broken stdout pipe plus a failure in the same
	// session.
	if haveLast {
		if msg := last.exitMessageFrom(printed); msg != "" {
			if _, w := fmt.Fprintln(os.Stdout, msg); w != nil && werr == nil {
				werr = w
			}
		}
	}
	if err == nil {
		err = werr
	}
	return err
}

// flushLog prints the outcomes of log from index printed on, as one block,
// and returns the new count; a failed write is kept in werr (the first one)
// and the outcomes count as printed, never retried into a second copy.
func flushLog(log []removal.Outcome, printed int, werr error) (int, error) {
	if printed >= len(log) {
		return printed, werr
	}
	if _, w := fmt.Fprintln(os.Stdout, logText(log[printed:])); w != nil && werr == nil {
		werr = w
	}
	return len(log), werr
}

// realCleanupProgram runs the list on the alt screen and returns the final
// model and the function that stops the signal forwarding (idempotent).
// RemoveFuncs.Program is the seam over it: nil runs this.
func realCleanupProgram(m cleanupListModel) (tea.Model, func(), error) {
	// Bubble Tea's own signal handler quits on SIGINT/SIGTERM in any phase,
	// mid-removal too; the model handles them instead (Ruling T). The
	// forwarding stops only after the exit message is printed.
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithoutSignalHandler())
	return guardedRun(forwardSignals(p.Send), p.Run)
}

// guardedRun runs the program and returns its result with stop, which the
// caller calls once it is done with the forwarding. A panic in run releases
// the forwarding itself before it propagates: the caller never got stop.
func guardedRun(stop func(), run func() (tea.Model, error)) (tea.Model, func(), error) {
	returned := false
	defer func() {
		if !returned {
			stop()
		}
	}()
	final, err := run()
	returned = true
	return final, stop, err
}

// runRescue hands the `r` key's row to rm.Rescue, if there is one, on the
// real terminal's streams (the alt screen is gone by now). It stops the
// signal forwarding first: the program has finished, so a forwarded
// signal would go to a dead p.Send and be dropped, leaving Ctrl-C at the
// rescue's y/N prompt (or during a long copy) with no effect. With the
// forwarding stopped Go's default handling ends the process again.
func runRescue(fm cleanupListModel, rm RemoveFuncs, stop func()) error {
	if fm.rescueRow == nil || rm.Rescue == nil {
		return nil
	}
	stop()
	return rm.Rescue(*fm.rescueRow, os.Stdin, os.Stdout, os.Stderr)
}

// runRestoreRoot hands off to rm.RestoreRoot after `R` chose to restore the
// root. It stops the signal forwarding first, like runRescue: the program has
// finished, so a forwarded signal would go to a dead p.Send and be dropped,
// leaving Ctrl-C at the restore's prompt (or SIGHUP during a long move) with
// no effect. With the forwarding stopped Go's default handling ends the
// process again.
func runRestoreRoot(fm cleanupListModel, rm RemoveFuncs, stop func()) error {
	if !fm.restoreRoot || rm.RestoreRoot == nil {
		return nil
	}
	stop()
	return rm.RestoreRoot(os.Stdin, os.Stdout, os.Stderr)
}

// startRescue handles `r`: on a row a rescue is offered for, quit and
// remember the row; otherwise stay and say why on the status line.
func (m cleanupListModel) startRescue() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	if offered, why := rescue.Offered(row); !offered {
		m.status = "rescue: " + why
		return m, nil
	}
	if m.rm.Rescue == nil || m.filling() {
		m.status = "rescue is not available here"
		return m, nil
	}
	m.rescueRow = &row
	m.done = true
	return m, tea.Quit
}

// startRestoreRoot handles `R`: on the row rootmove.Offered offers a restore
// for (the root off its default), quit and flag the hand-off; otherwise stay
// and put that gate's own line on the status line — byte for byte, never
// re-worded here. An offered row with no func wired says so instead.
//
// R deliberately reads neither the row's lock nor RemoveFuncs.Off: a restore
// is not a removal. The hand-off runs `wt restore-root`, a command that never
// loads the registry (so Off, the registry's failure, is none of its
// business) and carries the root's work into a new worktree before any reset
// (so a hard stop like protected_artifact, which guards deletions, bars it
// no more than it bars `r`). enter, x, d and o are removals and refuse.
func (m cleanupListModel) startRestoreRoot() (tea.Model, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		return m, nil
	}
	if offered, why := rootmove.Offered(row); !offered {
		m.status = why
		return m, nil
	}
	if m.rm.RestoreRoot == nil || m.filling() {
		m.status = "restore root is not wired here"
		return m, nil
	}
	m.restoreRoot = true
	m.done = true
	return m, tea.Quit
}

// Init dispatches the fill when the list was given a facts-only report and
// the command wired one in: stage 1 (the facts) is already painted by the
// constructor; this is stage 2 arriving off the update goroutine. A filled
// report, or no Fill, inits nothing — behaviour unchanged.
func (m cleanupListModel) Init() tea.Cmd {
	if m.rm.Fill == nil || !factsPending(m.report) {
		return nil
	}
	return m.fillCmd(m.fillGen)
}

// fillCmd runs rm.Fill off the update loop, tagging the message with gen.
func (m cleanupListModel) fillCmd(gen int) tea.Cmd {
	fill := m.rm.Fill
	return func() tea.Msg {
		report, rm, err := fill()
		return cleanupFillMsg{report: report, rm: rm, err: err, gen: gen}
	}
}

// gotFill applies a fill landing: a stale one (its generation spent by an F
// refetch) is dropped; a failure is one status line on the still-showing
// facts list (F can still re-read); a success swaps the filled report in
// (withReport: cursor, filter, ticks-per-the-pending-rule) and applies the
// funcs built from its snapshot.
func (m cleanupListModel) gotFill(msg cleanupFillMsg) (tea.Model, tea.Cmd) {
	if m.done || msg.gen != m.fillGen {
		return m, nil
	}
	m.fillGen++
	if msg.err != nil {
		m.status = "fill failed: " + cleanup.OneLine(msg.err.Error())
		return m, nil
	}
	next := m.withReport(msg.report)
	applyRemoveFuncs(&next, msg.rm)
	return next, nil
}

func (m cleanupListModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case cleanupPreviewMsg:
		return m.gotPreviews(msg)
	case cleanupProgressMsg:
		return m.gotProgress(msg)
	case cleanupRunDoneMsg:
		return m.gotRunDone(msg)
	case cleanupRefreshMsg:
		return m.gotRefresh(msg)
	case cleanupFillMsg:
		return m.gotFill(msg)
	case cleanupRefetchMsg:
		return m.gotRefetch(msg)
	case cleanupSignalMsg:
		return m.signalled()
	case tea.KeyMsg:
		if m.done {
			return m, nil
		}
		switch m.phase {
		case cleanupPhaseConfirm:
			return m.confirmKey(msg)
		case cleanupPhaseResults:
			return m.resultsKey(msg)
		case cleanupPhaseRunning:
			// A removal in flight is never abandoned: keys wait.
			return m, nil
		case cleanupPhasePreviewing, cleanupPhaseRefreshing:
			// Nothing is being removed: ctrl+c may leave.
			if msg.String() == "ctrl+c" {
				m.done = true
				return m, tea.Quit
			}
			return m, nil
		}
		m.status = ""
		if m.launch() {
			if next, cmd, ok := m.filterKey(msg); ok {
				return next, cmd
			}
		}
		if m.empty() {
			// "nothing to clean up": any key leaves.
			m.done = true
			return m, tea.Quit
		}
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			m.done = true
			return m, tea.Quit
		case "enter":
			if m.launch() {
				return m.choose()
			}
			return m.startRemoval()
		case "x":
			return m.startRowAction(rowActionRemove)
		case "d":
			return m.startRowAction(rowActionDeleteBranch)
		case "o":
			return m.startRowAction(rowActionDeleteOrigin)
		case "up", "k":
			m.move(-1, 1)
		case "down", "j":
			m.move(1, 1)
		case "pgup", "b":
			m.move(-1, max(1, m.layout().listH/2))
		case "pgdown", "f":
			m.move(1, max(1, m.layout().listH/2))
		case "home", "g":
			m.move(-1, len(m.items))
		case "end", "G":
			m.move(1, len(m.items))
		case "F":
			// The user's refresh (`f` stays pgdown): the picker re-runs its
			// rows load with a fetch first — the app owns its inputs and the
			// in-flight guard — and the manage list re-reads through
			// RemoveFuncs.Refetch. Either way the refresh drops the stale
			// session claims first (pruneStaleFirst, the key path's one
			// prune): refresh is one of the two paths that prune, and
			// opening the list never is. An F pressed while a refresh is
			// already in flight still prunes — idempotent, and the reload
			// the app drops does not undo it — which is intended.
			if m.launch() {
				return m, pruneStaleFirst(refreshRoot(m.opts.Root, m.report), emit(refreshRowsChosenMsg{}))
			}
			return m.startRefetch()
		default:
			// `r` and `R` are the highlighted row's hand-off keys in both
			// modes (they act like the row keys above: the highlighted row
			// alone). The filter (filterKey) has already taken every
			// printable rune it is owed before any of this runs.
			switch msg.String() {
			case "r":
				return m.startRescue()
			case "R":
				return m.startRestoreRoot()
			}
			if m.launch() {
				if msg.String() == "n" {
					return m, emit(newWorktreeChosenMsg{})
				}
				break
			}
			switch msg.String() {
			case " ":
				if it := m.items[m.cursor]; !it.locked {
					m.ticked[m.cursor] = !m.ticked[m.cursor]
				}
			case "a":
				for i, it := range m.items {
					if it.heading == "" && !it.locked && it.row.Tickable() && it.row.Kind != cleanup.KindRemote {
						m.ticked[i] = true
					}
				}
			}
		}
	}
	if !m.empty() {
		m.offset = m.layout().offset
	}
	return m, nil
}

// emit is a command that delivers msg.
func emit(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// choose is launch mode's enter: the new-worktree item or the highlighted
// row, whatever its verdict (launching warns, never blocks). The one
// refusal is a bare repository's own row: it has no directory to run an
// agent in, so enter only says so.
func (m cleanupListModel) choose() (cleanupListModel, tea.Cmd) {
	if m.hidden(m.cursor) || m.items[m.cursor].heading != "" {
		return m, nil
	}
	if m.items[m.cursor].newWorktree {
		return m, emit(newWorktreeChosenMsg{})
	}
	if m.items[m.cursor].row.Bare {
		m.status = "a bare repository has no working tree to launch in"
		return m, nil
	}
	return m, emit(rowChosenMsg{Row: m.items[m.cursor].row})
}

// rowAction is which per-row removal key was pressed.
type rowAction int

const (
	rowActionRemove       rowAction = iota // `x`: remove the highlighted row
	rowActionDeleteBranch                  // `d`: also delete its unmerged branch; `x` on a branch row
	rowActionDeleteOrigin                  // `o`: delete only its origin ref
)

// originActionSay is `o`'s refusal when the highlighted row carries no origin
// ref: the first clause names the key's action, the second what about the row
// stopped it. Only `o` reaches it — `x` and `d` refuse with the row's own
// reason instead — so it is one string, not a per-action table.
const originActionSay = "o deletes the highlighted row's origin ref"

// startRowAction is one of the per-row removal keys (`x`, `d`, `o`): the
// highlighted row alone goes to the removal flow — the ticks choose rows for
// enter, never for these — and a locked row or the new-worktree item says why
// not. kind picks the branch's fate: rowActionDeleteBranch pre-toggles "also
// delete unmerged branch" on the confirm screen (on a branch row it is `x`:
// the branch is the removal), and rowActionDeleteOrigin hands the engine the
// row's origin ref alone — never its worktree or local branch — as the
// remote-only item `wt remove origin/<name> --remote` builds, under the typed
// "delete origin" — and for it the lock that counts is the origin target's,
// not the local row's.
func (m cleanupListModel) startRowAction(kind rowAction) (cleanupListModel, tea.Cmd) {
	if m.hidden(m.cursor) {
		return m, nil
	}
	it := m.items[m.cursor]
	switch {
	case it.newWorktree:
		m.status = "this is the + New worktree… item, not a row: it creates a worktree, there is nothing to remove"
		return m, nil
	case it.locked && kind != rowActionDeleteOrigin:
		m.status = "cannot be removed: " + cleanup.OneLine(it.text)
		return m, nil
	}
	rows := []cleanup.Row{it.row}
	if kind == rowActionDeleteOrigin {
		// `o` deletes the origin ref alone, which the engine allows whatever
		// the local row is (`wt remove origin/x --remote` does not care that
		// you stand in the worktree): the lock that counts is the target's.
		target, ok := m.originTarget(it.row)
		switch {
		case ok && cleanupLocked(target):
			// Name the row that is locked: the origin ref, not the local row
			// the cursor is on (which `o` does not touch).
			m.status = "its origin ref cannot be removed: " + cleanup.OneLine(cleanup.RowText(m.report, target))
			return m, nil
		case !ok && it.locked:
			m.status = "cannot be removed: " + cleanup.OneLine(it.text)
			return m, nil
		case !ok:
			m.status = originActionSay + "; this row has none"
			return m, nil
		}
		rows = []cleanup.Row{target}
	}
	m.chosenByTicks = false
	m.seedBranch = kind == rowActionDeleteBranch && it.row.Kind == cleanup.KindWorktree
	return m.startRemovalOf(rows)
}

// originTarget is the remote row whose removal deletes row's origin ref and
// nothing else: the row itself when it is an origin ref, else the remote row
// folded into it. Remote rows are looked up through RemoteRows — Rows alone
// omits the folded ones — the seam `wt remove origin/<name> --remote`
// resolves its argument through.
func (m cleanupListModel) originTarget(row cleanup.Row) (cleanup.Row, bool) {
	if row.Kind == cleanup.KindRemote && row.Remote == "origin" && strings.HasPrefix(row.Ref, "refs/remotes/origin/") {
		return row, true
	}
	if row.Upstream == nil || row.Upstream.Gone || row.Upstream.Remote != "origin" {
		return cleanup.Row{}, false
	}
	for _, r := range m.report.RemoteRows() {
		if r.Ref == row.Upstream.Ref {
			return r, true
		}
	}
	return cleanup.Row{}, false
}

// move moves the cursor n items in direction dir, skipping headings and
// hidden rows. A single step (n == 1) wraps past either end — down from the
// last item lands on the first, up from the first on the last (spec §6) —
// while a multi-item move (paging: pgup/pgdown, home/end) still stops at
// the ends. A wrap that would land back on the cursor (one item to sit on)
// leaves it put.
func (m *cleanupListModel) move(dir, n int) {
	wrap := n == 1
	sittable := func(j int) bool { return m.items[j].heading == "" && !m.hidden(j) }
	for ; n > 0; n-- {
		next := -1
		for j := m.cursor + dir; j >= 0 && j < len(m.items); j += dir {
			if sittable(j) {
				next = j
				break
			}
		}
		if next < 0 {
			if !wrap {
				return
			}
			// Past the end: scan from the far side for the first item the
			// cursor may sit on (headings and hidden rows skipped there too).
			from, step := 0, 1
			if dir < 0 {
				from, step = len(m.items)-1, -1
			}
			for j := from; j >= 0 && j < len(m.items); j += step {
				if sittable(j) {
					next = j
					break
				}
			}
			if next < 0 || next == m.cursor {
				return
			}
		}
		m.cursor = next
	}
}

// itemLines renders item i: a heading is one line; a row is exactly one
// table line — its prefix (session digit, cursor, checkbox) plus the fitted
// columns, with its badges as trailing marks — and the new-worktree item its
// own label line. The row's sentence and age live in the detail block.
func (m *cleanupListModel) itemLines(i int) []string {
	if m.hidden(i) {
		return nil
	}
	it := m.items[i]
	if it.heading != "" {
		style := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader)).Bold(true)
		return []string{style.Render(clip(it.heading, m.width))}
	}
	style := m.rowStyle(it, i == m.cursor)
	if it.newWorktree {
		return []string{style.Render(clip(rowPrefix(0, i == m.cursor, "")+newWorktreeLabel, m.width))}
	}
	box := ""
	if !m.launch() {
		box = m.box(i)
	}
	line := rowPrefix(it.row.Sessions, i == m.cursor, box) + rowLine(it.row, m.table)
	if texts := tagTexts(it.tags); len(texts) > 0 {
		line += "  · " + strings.Join(texts, " · ")
	}
	return []string{style.Render(clip(line, m.width))}
}

// tagTexts is the badges' text in order: the trailing marks on a row's
// table line (their severity styles live in the detail sentence; the row
// itself is one styled line).
func tagTexts(tags []cleanup.Tag) []string {
	var out []string
	for _, t := range tags {
		out = append(out, t.Text)
	}
	return out
}

// filling reports whether the shown report is still the facts stage: some
// row has not been filled in yet. While it is, nothing may be removed (the
// removal funcs are not built; rowKeys hides the keys) — the facts carry no
// gates, records or proofs to stand on.
func (m cleanupListModel) filling() bool {
	for _, it := range m.items {
		if it.heading == "" && !it.newWorktree && it.row.Pending {
			return true
		}
	}
	return false
}

func (m *cleanupListModel) box(i int) string {
	switch {
	case m.items[i].locked:
		return "[-]"
	case m.ticked[i]:
		return "[x]"
	}
	return "[ ]"
}

func (m *cleanupListModel) rowStyle(it cleanupItem, selected bool) lipgloss.Style {
	switch {
	case selected:
		return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	case it.locked:
		return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	}
	return lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenUnselected))
}

// cleanupLayout is one frame of the screen: what goes above and below the
// list, how many list lines fit between, and the list's lines and offset.
type cleanupLayout struct {
	top, bottom string
	listH       int // list lines shown, the scroll hint not counted
	lines       []string
	offset      int
	hint        bool // the list is taller than listH: a scroll hint line follows it
}

// layout fits the screen to the terminal. The frames, fullest first: title,
// header, detail and footer; title, detail and footer; detail and footer;
// status and footer; nothing. The first that leaves room for the whole
// highlighted row is used, so a short terminal loses the report header
// first, then the title, then the detail line, and the footer only when even
// the row alone barely fits. The offset keeps the whole
// highlighted row on screen (its top line when it is taller than the room),
// with its group heading when it is the group's first row and there is room.
// A list taller than its room gives one of its lines to a scroll hint.
func (m *cleanupListModel) layout() cleanupLayout {
	// Fit the table to this frame's width before any line renders: which
	// columns are shown decides the header, every row line and detail's
	// diffLine. Fit recomputes from the whole table each time, so a resize
	// widens columns back as well as dropping them.
	if m.table != nil && m.width > 0 {
		m.table.Fit(m.width)
	}
	var lines []string
	start := make([]int, len(m.items)+1)
	for i := range m.items {
		start[i] = len(lines)
		lines = append(lines, m.itemLines(i)...)
	}
	start[len(m.items)] = len(lines)
	cs, ce := start[m.cursor], start[m.cursor+1]

	title := clip(m.titleLine(), m.width) + "\n"
	repHdr := m.headerBlock() + "\n"
	stat := m.statusBlock()
	if stat != "" && !strings.HasSuffix(stat, "\n") {
		stat += "\n"
	}
	// The table's own header line: a dim line over the rows, with the blank
	// line the model screen's title bar keeps under its header.
	tblLine := ""
	if m.table != nil {
		dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		tblLine = dim.Render(clip(m.table.Header(), m.width)) + "\n"
	}
	// topOf closes a top block with the one blank line before the list.
	topOf := func(content string) string {
		if content == "" {
			return ""
		}
		return content + "\n"
	}
	detail := "\n" + m.detail()
	foot := m.footer()
	if foot != "" {
		foot = "\n" + foot
	}
	// The footer is the key legend for the highlighted row: the last thing
	// given up, after the report header, the title, the detail (and the
	// table header with it) and the status. The filter and status ride the
	// top block, above the table header (spec §6).
	frames := [][2]string{
		{topOf(title + repHdr + stat + tblLine), detail + foot},
		{topOf(title + stat + tblLine), detail + foot},
		{topOf(stat + tblLine), detail + foot},
		{topOf(stat), foot},
		{"", ""},
	}
	l := cleanupLayout{lines: lines}
	for k, f := range frames {
		l.top, l.bottom = f[0], f[1]
		if m.height <= 0 {
			l.listH = len(lines)
			break
		}
		avail := m.height - (lipgloss.Height(f[0]+"x"+f[1]) - 1)
		l.hint = len(lines) > avail && avail > 1
		l.listH = avail
		if l.hint {
			l.listH--
		}
		if l.listH >= ce-cs || k == len(frames)-1 {
			l.listH = max(1, l.listH)
			break
		}
	}

	off := min(m.offset, max(0, len(lines)-l.listH))
	if ce > off+l.listH {
		off = ce - l.listH
	}
	if cs < off {
		off = cs
	}
	if m.cursor > 0 && m.items[m.cursor-1].heading != "" {
		if hs := start[m.cursor-1]; hs < off && ce-hs <= l.listH {
			off = hs
		}
	}
	// Never open the list on the tail of a wrapped row: start at the next
	// item instead, while that still shows the highlighted row.
	for k := 0; k < len(m.items); k++ {
		if start[k] < off && off < start[k+1] {
			if start[k+1] <= cs {
				off = start[k+1]
			}
			break
		}
	}
	l.offset = max(0, off)
	return l
}

// titleLine is the screen's name. The key legend is the footer (footer,
// rowKeys), which names the keys that work on the highlighted row.
func (m *cleanupListModel) titleLine() string {
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	if m.launch() {
		return accent.Render("wt")
	}
	return accent.Render("wt worktrees")
}

// statusBlock is the status, wrapped, with its line break; "" without one.
func (m *cleanupListModel) statusBlock() string {
	st := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent))
	out := m.filterLine()
	if m.status == "" {
		return out
	}
	return out + st.Render(clip(tuilayout.WrapText(m.status, m.width), m.width)) + "\n"
}

// headerBlock is cleanup.HeaderLines plus the refresh note, wrapped: the
// fetch failure ends in what it means for the list, and the note says when
// this list was last refreshed here. The picker (launch mode) leads with the
// root drift note, shown whatever row the cursor is on ("the `wt` launch
// header", the user-driven cleanup design) and not repeated as a root-row
// badge — while the manage list surfaces the drift as its root row's badge
// alone.
func (m *cleanupListModel) headerBlock() string {
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	lines := cleanup.HeaderLines(m.report, m.now)
	if m.launch() {
		if d := rootDriftOf(m.report); d != "" {
			lines = append([]string{d}, lines...)
		}
	}
	if m.fetching {
		lines = append(lines, "fetching every remote… (up to 5s)")
	}
	if note := refreshedNote(m.stamp, m.now, m.report.Fetch); note != "" {
		lines = append(lines, note)
	}
	text := tuilayout.WrapText(strings.Join(lines, "\n"), m.width)
	return clip(dim.Render(text), m.width)
}

// refreshedNote is the header's one line about the list's fetch stamp:
// "refreshed 3m ago" from the stamp (worktree.LastFetched reads the
// FETCH_HEAD every fetch rewrites, an up-to-date one included) and "not
// refreshed here" when root has none — a fresh clone has none until a fetch
// runs there. A fetch this list recorded as failed gets no note: git
// truncates FETCH_HEAD when a fetch starts, so a failed or timed-out fetch
// advances the stamp too, and "refreshed 0s ago" under "fetch: FAILED"
// would contradict it.
func refreshedNote(s refreshStamp, now time.Time, f cleanup.FetchResult) string {
	if !f.OK && f.Err != "" {
		return ""
	}
	if !s.OK {
		return "not refreshed here"
	}
	return "refreshed " + sinceUnit(now.Sub(s.At)) + " ago"
}

// sinceUnit is d as one truncated unit — "0s", "3m", "2h", "5d" — the shape
// the refresh note shows at a glance ("refreshed 3m ago"). A negative gap
// (a stamp in the future, clock skew) reads as 0s.
func sinceUnit(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// detail explains the highlighted row: a rule with its name, then its whole
// sentence and age, wrapped, never cut. A row that can never be ticked says
// so first.
func (m *cleanupListModel) detail() string {
	if m.hidden(m.cursor) {
		return "no row matches the filter"
	}
	it := m.items[m.cursor]
	name := cleanup.DisplayName(it.row)
	if it.newWorktree {
		name = newWorktreeLabel
	}
	rule := "── " + name + " "
	if m.width > 0 {
		rule = cutLines(rule, m.width)
		rule += strings.Repeat("─", max(0, m.width-lipgloss.Width(rule)))
	}
	text := it.text
	if age := cleanup.AgeText(it.row); age != "" {
		text += " [" + age + "]"
	}
	// When the table gave the DIFF column up, its numbers live here — shown
	// whole or not at all (the mode-line rule).
	if m.table != nil && !m.table.Shown(rtDiff) && it.row.Kind == cleanup.KindWorktree && !it.row.Bare && !it.row.Pending {
		text += " · " + diffLine(it.row)
	}
	switch {
	case it.newWorktree:
		text = "create a new branch and worktree, then launch in it"
	case m.launch() && it.row.Bare:
		text = "cannot be launched in or removed (a bare repository has no working tree): " + text
	case m.launch() && it.locked:
		text = "cannot be removed (launching is fine): " + text
	case m.launch():
	case it.locked && it.row.Kind == cleanup.KindRemote && it.row.Remote != "origin":
		text = "cannot be ticked (wt removes only origin branches): " + text
	case it.locked && it.row.Kind == cleanup.KindRemote:
		text = "cannot be ticked (only a proven origin branch can be removed): " + text
	case it.row.Kind == cleanup.KindRemote:
		text = "origin branch: deleting it is visible to everyone with access to the repository and needs the phrase delete origin; space ticks it: " + text
	case it.locked:
		text = "cannot be ticked: " + text
	}
	dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	out := dim.Render(rule) + "\n" + clip(tuilayout.WrapText(text, m.width), m.width)
	// The sentence counts the worktrees inside this one; their paths are
	// named here only when they all fit on one line (the JSON has them).
	if paths := cleanup.NestedPaths(it.row); len(paths) > 0 {
		if line := "worktrees inside: " + strings.Join(paths, ", "); m.width <= 0 || lipgloss.Width(line) <= m.width {
			out += "\n" + line
		}
	}
	return out
}

func (m cleanupListModel) View() string {
	if m.done {
		return ""
	}
	switch m.phase {
	case cleanupPhasePreviewing:
		return m.waitView(fmt.Sprintf("%s · re-checking %d %s items; nothing is removed yet (ctrl+c leaves)", m.screenName(), m.pending, m.chosenWord()))
	case cleanupPhaseConfirm:
		return m.confirmView()
	case cleanupPhaseRunning:
		return m.runningView()
	case cleanupPhaseResults:
		return m.resultsView()
	case cleanupPhaseRefreshing:
		return m.waitView(m.screenName() + " · re-reading the repository")
	}
	if m.empty() {
		body := m.headerBlock() + "\n\n" + m.statusBlock() + "nothing to clean up: no other worktree or branch\n\npress any key to quit"
		return capLines(clip(body, m.width), m.height)
	}
	l := m.layout()
	end := min(len(l.lines), l.offset+l.listH)
	view := l.top + strings.Join(l.lines[l.offset:end], "\n")
	if l.hint {
		var more []string
		if l.offset > 0 {
			more = append(more, "↑ more above")
		}
		if end < len(l.lines) {
			more = append(more, "↓ more below")
		}
		dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		view += "\n" + dim.Render(clip("  "+strings.Join(more, " · "), m.width))
	}
	// Anchor the bottom block to the last terminal line: when the list shows
	// fewer lines than its room (few rows), blank filler fills the gap
	// between the last row and the detail block. The scroll hint counts as a
	// list line, so it cancels out of the gap.
	if m.height > 0 {
		if filler := l.listH - (end - l.offset); filler > 0 {
			view += strings.Repeat("\n", filler)
		}
	}
	view += l.bottom
	return capLines(view, m.height)
}

// capLines keeps the first height lines of s: the last resort for a
// terminal too short for even the sparest frame. A height that is not
// positive keeps everything.
func capLines(s string, height int) string {
	if height <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	return strings.Join(lines[:height], "\n")
}

// selectedRow is the highlighted row; false on an empty list, on launch
// mode's new-worktree item, on a heading and on a row the filter hides.
func (m cleanupListModel) selectedRow() (cleanup.Row, bool) {
	if m.cursor < 0 || m.hidden(m.cursor) {
		return cleanup.Row{}, false
	}
	if it := m.items[m.cursor]; it.heading == "" && !it.newWorktree {
		return it.row, true
	}
	return cleanup.Row{}, false
}

// tickedItems are the ticked rows in list order.
func (m cleanupListModel) tickedItems() []cleanupItem {
	var out []cleanupItem
	for i, it := range m.items {
		if it.heading == "" && m.ticked[i] {
			out = append(out, it)
		}
	}
	return out
}

// tickedNames are the ticked rows' display names, in list order.
func (m cleanupListModel) tickedNames() []string {
	var out []string
	for _, it := range m.tickedItems() {
		out = append(out, cleanup.DisplayName(it.row))
	}
	return out
}

// exitMessage is what RunCleanupList prints when the session ends: every
// outcome of the session ("" when nothing ran).
func (m cleanupListModel) exitMessage() string { return m.exitMessageFrom(0) }

// exitMessageFrom is exitMessage without the first printed outcomes, which
// are already on stdout (a flush before a hand-off): the rest of the log and,
// for a program that ended mid-run, what has no outcome.
func (m cleanupListModel) exitMessageFrom(printed int) string {
	log := m.log
	if printed < len(log) {
		log = log[printed:]
	} else {
		log = nil
	}
	text := logText(log)
	if m.phase != cleanupPhaseRunning || m.run == nil {
		return text
	}
	// Ended mid-run anyway: say what has no outcome (Ruling T).
	if text == "" {
		text = "wt worktrees: removed 0 items"
	}
	lines := append([]string{text, "the run did not finish:"}, m.run.unfinishedLines()...)
	return strings.Join(lines, "\n")
}

// refreshRoot is the root the refresh stamp is read at: root, else the
// report's Repo.
func refreshRoot(root string, r cleanup.Report) string {
	if root == "" {
		return r.Repo
	}
	return root
}
