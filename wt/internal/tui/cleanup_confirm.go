package tui

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/sessionclaim"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
	"github.com/ohanaverse/local-ai-setup/wt/internal/worktree"
)

// The `wt cleanup` list's removal: enter on the ticked rows opens a confirm
// screen, then a progress screen while the engine works, then the results.
// Nothing here removes anything or builds a removal.Item: the engine
// previews the ticked rows (RemoveFuncs.Preview), the confirm screen turns
// each preview into an Item only through ItemPreview.Confirmed or, for an
// item whose unmerged branch the user toggled, ConfirmedDeletingBranch, with
// the approvals that item's own preview needs, and RemoveFuncs.Run removes.
//
// The approvals (Ruling B, K, M, P): a selection where every item is proven
// is confirmed with y; one where any item needs anything (not proven, a
// loss, uncommitted changes, an unmerged branch) needs the word `delete`
// typed. The typed word stands in for the CLI's --accept-loss, --force and
// -D, item by item: each item gets exactly what its own preview needs, never
// another item's. Deleting the unmerged branch a worktree item merely
// offers is a separate toggle on that item (tab), off by default.

// RemoveFuncs is how the list reaches the removal engine (cmd/wt wires
// internal/removal in). A zero RemoveFuncs, or one with Off set, removes
// nothing: enter says why.
type RemoveFuncs struct {
	// Preview previews the ticked rows, one ItemPreview per row in the same
	// order, as Engine.Preview returns them (sealed: only they can be
	// confirmed).
	Preview func(rows []cleanup.Row) []removal.ItemPreview
	// Run removes the confirmed items one at a time, calling progress after
	// each, and returns one outcome per item (Engine.Run).
	Run func(items []removal.Item, progress func(removal.Outcome)) []removal.Outcome
	// Refresh re-reads the repository after a run; the list is rebuilt from
	// its report. nil ends the program after the results instead.
	Refresh func() (cleanup.Report, error)
	// Refetch fetches the remotes and re-reads the repository on the list's
	// `F`, returning the fresh report the list is rebuilt from. Fetch at the
	// report's repository root (worktree.FetchRemotes), so the header's
	// refresh note advances; a fetch failure is not this error — it travels
	// in the report's Fetch field, which the header renders. This error is
	// the re-read's own failure: the list keeps its rows and says why, one
	// line. nil: `F` says it is unavailable.
	Refetch func() (cleanup.Report, error)
	// Off, when not "", is why removal is unavailable (the registry did not
	// load): enter says it and nothing is previewed.
	Off string
	// Rescue copies one row's local-only work into a new branch and worktree
	// (`wt rescue`). It runs after the list has quit and the alt screen is
	// gone, so it may ask on the terminal. nil: `r` says it is unavailable.
	Rescue func(row cleanup.Row) error
	// RestoreRoot moves the root checkout's branch back to the default
	// branch (`wt restore-root`). It runs after the list has quit and the alt
	// screen is gone, so it may ask on the terminal. nil: `R` says it is not
	// wired here.
	RestoreRoot func() error
}

// cleanupPhase is which screen the list program shows.
type cleanupPhase int

const (
	cleanupPhaseList       cleanupPhase = iota // the tick list
	cleanupPhasePreviewing                     // the engine is previewing the ticked rows
	cleanupPhaseConfirm                        // the confirm screen
	cleanupPhaseRunning                        // the engine is removing
	cleanupPhaseResults                        // the outcomes, until a key
	cleanupPhaseRefreshing                     // the repository is being re-read
)

// confirmEntry is one ticked item on the confirm screen: its preview and
// whether the user toggled deleting the unmerged branch its preview offers.
type confirmEntry struct {
	pv        removal.ItemPreview
	delBranch bool
}

// cleanupConfirm is the confirm screen's state.
type cleanupConfirm struct {
	entries []confirmEntry // grouped by approval kind (confirmGroups' order)
	cursor  int            // the highlighted entry
	offset  int            // the first body line on screen
	// follow: the view keeps the highlighted entry in sight (arrows);
	// false after pgup/pgdown, so they scroll within a tall entry.
	follow bool
	typed  string // what has been typed toward `delete`
}

// cleanupRun is a run's state, kept for the results screen.
type cleanupRun struct {
	entries []confirmEntry
	results []removal.Outcome // per entry
	have    []bool            // results[i] is set
	logged  []bool            // results[i] is in the session's exit log
	runAt   []int             // the entry index of each item handed to Run
	items   []removal.Item
	got     int // outcomes received so far
	err     error
	offset  int // results screen scroll
}

// Messages of the removal flow. The run's messages carry the channel the
// next one comes on.
type (
	cleanupPreviewMsg struct {
		previews []removal.ItemPreview
		err      error
	}
	cleanupProgressMsg struct {
		o  removal.Outcome
		ch <-chan tea.Msg
	}
	cleanupRunDoneMsg struct {
		outcomes []removal.Outcome
		err      error
	}
	cleanupRefreshMsg struct {
		report cleanup.Report
		err    error
	}
	// cleanupRefetchMsg is the `F` re-read's answer (the user's refresh, not
	// the post-run one: its failure keeps the program alive).
	cleanupRefetchMsg struct {
		report cleanup.Report
		stamp  refreshStamp
		err    error
	}
	// cleanupSignalMsg is a SIGINT or SIGTERM to the process (forwardSignals).
	cleanupSignalMsg struct{ sig os.Signal }
)

// forwardedSignals are the signals forwardSignals turns into messages.
var forwardedSignals = []os.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP}

// forwardSignals turns every SIGINT, SIGTERM and SIGHUP (a closed terminal)
// to the process into a cleanupSignalMsg until stop is called (Ruling T). RunCleanupList runs the
// program without Bubble Tea's own handler, which quits on the first signal
// in any phase and then lets a second one end the process: a removal in
// flight would be abandoned and its outcome lost. The model decides what a
// signal does (signalled).
func forwardSignals(send func(tea.Msg)) (stop func()) {
	sig := make(chan os.Signal, 4)
	signal.Notify(sig, forwardedSignals...)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case s := <-sig:
				send(cleanupSignalMsg{sig: s})
			case <-done:
				return
			}
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			signal.Stop(sig)
			close(done)
		})
	}
}

// signalled is a signal's effect: during a run it is remembered and the
// program quits once the run ends (Ruling T: a run in flight is never
// abandoned); otherwise it quits now.
func (m cleanupListModel) signalled() (cleanupListModel, tea.Cmd) {
	if m.phase == cleanupPhaseRunning {
		m.quitAfterRun = true
		return m, nil
	}
	m.done = true
	return m, tea.Quit
}

// confirmWord is what must be typed to confirm a selection that is not all
// proven; confirmOriginWord when it deletes any origin branch. The plain word
// never grants an origin deletion.
const (
	confirmWord       = "delete"
	confirmOriginWord = "delete origin"
)

// confirmGroups is the confirm screen's group order and headings.
var confirmGroups = []struct {
	approval removal.Approval
	heading  string
}{
	{removal.ApprovalProven, "PROVEN MERGED · nothing to lose"},
	{removal.ApprovalNeedsConfirm, "NOT PROVEN, OR LOSES WHAT IS LISTED"},
	{removal.ApprovalNeedsForce, "UNCOMMITTED CHANGES OR DETACHED COMMITS"},
	{removal.ApprovalNeedsDelete, "UNMERGED BRANCHES"},
	{removal.ApprovalNeedsRemote, "ORIGIN BRANCHES · visible to everyone with access to the repository"},
	{removal.ApprovalRefused, "WILL NOT BE REMOVED"},
}

// confirmCountWords name each group in the confirm screen's summary.
var confirmCountWords = map[removal.Approval]string{
	removal.ApprovalProven:       "proven",
	removal.ApprovalNeedsConfirm: "not proven or losing something",
	removal.ApprovalNeedsForce:   "with uncommitted changes or detached commits",
	removal.ApprovalNeedsDelete:  "unmerged branch",
	removal.ApprovalNeedsRemote:  "on origin",
	removal.ApprovalRefused:      "will not be removed",
}

// tickedRows are the rows enter hands the engine: ticked and never locked.
// The engine is the guard for an origin row (only proven, only origin, and
// only with the Remote approval, which only the typed phrase gives).
func (m cleanupListModel) tickedRows() []cleanup.Row {
	var out []cleanup.Row
	for _, it := range m.tickedItems() {
		if it.locked {
			continue
		}
		out = append(out, it.row)
	}
	return out
}

// startRemoval is enter on the list: the ticked rows go to the engine's
// preview, or the status says why not.
func (m cleanupListModel) startRemoval() (cleanupListModel, tea.Cmd) {
	rows := m.tickedRows()
	if len(rows) == 0 {
		m.status = "nothing ticked: space ticks the highlighted row, x removes it"
		return m, nil
	}
	return m.startRemovalOf(rows)
}

// startRemovalOf hands rows (enter's ticked rows, or `x`'s) to the engine's
// preview, or the status says why not.
func (m cleanupListModel) startRemovalOf(rows []cleanup.Row) (cleanupListModel, tea.Cmd) {
	switch {
	case m.rm.Off != "":
		m.status = cleanup.OneLine(m.rm.Off)
		return m, nil
	case m.rm.Preview == nil:
		m.status = "removal is not available here; nothing was removed"
		return m, nil
	}
	m.phase = cleanupPhasePreviewing
	m.pending = len(rows)
	return m, previewCmd(m.rm.Preview, rows)
}

// previewCmd runs the engine's preview off the update loop. A panic, or a
// preview that does not answer exactly the rows asked, is an error: nothing
// is removed.
func previewCmd(preview func([]cleanup.Row) []removal.ItemPreview, rows []cleanup.Row) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = cleanupPreviewMsg{err: fmt.Errorf("the re-check failed: %v", r)}
			}
		}()
		ps := preview(rows)
		if len(ps) != len(rows) {
			return cleanupPreviewMsg{err: fmt.Errorf("the re-check answered %d items for %d asked", len(ps), len(rows))}
		}
		for i, p := range ps {
			r := rows[i]
			if p.Item.Kind != r.Kind || p.Item.Path != r.Path || p.Item.Ref != r.Ref {
				return cleanupPreviewMsg{err: fmt.Errorf("the re-check of %s answered for another item", cleanup.DisplayName(r))}
			}
		}
		return cleanupPreviewMsg{previews: ps}
	}
}

// gotPreviews opens the confirm screen, its entries grouped by approval.
func (m cleanupListModel) gotPreviews(msg cleanupPreviewMsg) (cleanupListModel, tea.Cmd) {
	if m.phase != cleanupPhasePreviewing {
		return m, nil
	}
	if msg.err != nil {
		m.phase = cleanupPhaseList
		m.status = "nothing was removed: " + cleanup.OneLine(msg.err.Error())
		return m, nil
	}
	c := &cleanupConfirm{}
	for _, g := range confirmGroups {
		for _, p := range msg.previews {
			if p.Approval == g.approval {
				c.entries = append(c.entries, confirmEntry{pv: p})
			}
		}
	}
	for _, p := range msg.previews {
		if !knownApproval(p.Approval) {
			// An approval this screen does not know is never runnable.
			p.Approval = removal.ApprovalRefused
			c.entries = append(c.entries, confirmEntry{pv: p})
		}
	}
	c.follow = true
	m.phase, m.conf = cleanupPhaseConfirm, c
	return m, nil
}

func knownApproval(a removal.Approval) bool {
	for _, g := range confirmGroups {
		if g.approval == a {
			return true
		}
	}
	return false
}

// runnable says whether e will be handed to Run once confirmed.
func (e confirmEntry) runnable() bool {
	return e.pv.Approval != removal.ApprovalRefused && knownApproval(e.pv.Approval)
}

// approved is what confirming gives e: exactly its own Needs, plus Delete
// when the user toggled its offered branch.
func (e confirmEntry) approved() removal.Needs {
	n := e.pv.Needs
	if e.delBranch {
		n.Delete = true
	}
	return n
}

// runnableCount is how many entries a confirmation would hand to Run.
func (c *cleanupConfirm) runnableCount() int {
	n := 0
	for _, e := range c.entries {
		if e.runnable() {
			n++
		}
	}
	return n
}

// originCount is how many runnable entries delete a branch on origin.
func (c *cleanupConfirm) originCount() int {
	n := 0
	for _, e := range c.entries {
		if e.runnable() && e.approved().Remote {
			n++
		}
	}
	return n
}

// word is what must be typed: the origin phrase when any runnable entry
// deletes an origin branch, else the plain word.
func (c *cleanupConfirm) word() string {
	if c.originCount() > 0 {
		return confirmOriginWord
	}
	return confirmWord
}

// needsTyped says whether the selection needs the typed word: some runnable
// item needs an approval (or has its branch toggled).
func (c *cleanupConfirm) needsTyped() bool {
	for _, e := range c.entries {
		if e.runnable() && e.approved() != (removal.Needs{}) {
			return true
		}
	}
	return false
}

// offersToggle says whether any entry offers deleting its unmerged branch.
func (c *cleanupConfirm) offersToggle() bool {
	for _, e := range c.entries {
		if e.runnable() && e.pv.CanDeleteBranch {
			return true
		}
	}
	return false
}

// cancelConfirm leaves the confirm screen with nothing removed.
func (m cleanupListModel) cancelConfirm(why string) (cleanupListModel, tea.Cmd) {
	m.phase, m.conf = cleanupPhaseList, nil
	m.status = why + ": nothing was removed"
	return m, nil
}

// confirmKey is a key on the confirm screen.
func (m cleanupListModel) confirmKey(msg tea.KeyMsg) (cleanupListModel, tea.Cmd) {
	c := m.conf
	k := msg.String()
	switch k {
	case "esc", "ctrl+c":
		return m.cancelConfirm("cancelled")
	case "up":
		c.cursor, c.follow = max(0, c.cursor-1), true
		return m, nil
	case "down":
		c.cursor, c.follow = min(len(c.entries)-1, c.cursor+1), true
		return m, nil
	case "pgup":
		// Scroll the lines, the cursor stays: within a tall entry too.
		c.offset, c.follow = max(0, c.offset-max(1, m.height/2)), false
		return m, nil
	case "pgdown":
		c.offset, c.follow = c.offset+max(1, m.height/2), false
		return m, nil
	case "tab":
		// Ruling U: turning a toggle ON applies only to an entry whose
		// decision lines are on screen. After pgup/pgdown the highlighted
		// entry may be out of sight: this tab brings it back into view and
		// toggles nothing; the next one toggles it, in view. Turning one
		// OFF never adds an approval, so it applies even to an entry too
		// tall for the screen (which could otherwise never be untoggled).
		visible := m.frame().visible(c.cursor)
		c.follow = true
		e := &c.entries[c.cursor]
		if !visible && !e.delBranch {
			return m, nil
		}
		if e.runnable() && e.pv.CanDeleteBranch {
			e.delBranch = !e.delBranch
		}
		return m, nil
	}
	if c.runnableCount() == 0 {
		return m.cancelConfirm("nothing could be removed")
	}
	if !c.needsTyped() {
		if k == "y" || k == "Y" {
			return m.confirmed()
		}
		return m.cancelConfirm("not confirmed")
	}
	word := c.word()
	spaceOK := strings.Contains(word, " ") && c.typed != "" && !strings.HasSuffix(c.typed, " ") && len([]rune(c.typed)) < len([]rune(word))
	isSpace := k == " " || msg.Type == tea.KeySpace || msg.Type == tea.KeyRunes && strings.ContainsRune(string(msg.Runes), ' ')
	switch {
	case isSpace && spaceOK && (msg.Type == tea.KeySpace || string(msg.Runes) == " " || k == " "):
		// "delete origin" holds one space; it is part of the word only
		// after the first word, once.
		c.typed += " "
	case k == "n" && !strings.HasPrefix(word, c.typed+"n"), msg.Alt, isSpace:
		// None of these can be part of the word: an alt-modified letter
		// (alt+d), a space typed toward it, or an n that is not the next letter
		// of the word ("origin" has an n of its own).
		return m.cancelConfirm("cancelled")
	case k == "backspace":
		if r := []rune(c.typed); len(r) > 0 {
			c.typed = string(r[:len(r)-1])
		}
	case k == "enter":
		if c.typed == word {
			return m.confirmed()
		}
		return m.cancelConfirm(fmt.Sprintf("typed %q, not %q", cleanup.OneLine(c.typed), word))
	case msg.Type == tea.KeyRunes:
		if len([]rune(c.typed)) < 2*len(word) {
			c.typed += string(msg.Runes)
		}
	}
	return m, nil
}

// confirmed turns each runnable entry into the Item Run receives, through
// the preview's own constructors with that entry's own approvals, and
// starts the run. An entry the constructors refuse is a refusal in the
// results; a refused preview's outcome is its plan.
func (m cleanupListModel) confirmed() (cleanupListModel, tea.Cmd) {
	c := m.conf
	r := &cleanupRun{entries: c.entries, results: make([]removal.Outcome, len(c.entries)),
		have: make([]bool, len(c.entries)), logged: make([]bool, len(c.entries))}
	for i, e := range c.entries {
		if !e.runnable() {
			o := e.pv.Plan
			if o.Reason == "" && e.pv.Reason != "" {
				o.SetReason(e.pv.Reason)
			}
			o.Item = e.pv.Item
			r.results[i], r.have[i] = o, true
			continue
		}
		var (
			it  removal.Item
			err error
		)
		if e.delBranch {
			it, err = e.pv.ConfirmedDeletingBranch(e.approved())
		} else {
			it, err = e.pv.Confirmed(e.approved())
		}
		if err != nil {
			o := removal.NewOutcome(e.pv.Item, removal.WtRefused, removal.BrNotAttempted, err.Error())
			o.LossLines = e.pv.LossLines
			r.results[i], r.have[i] = o, true
			continue
		}
		r.runAt = append(r.runAt, i)
		r.items = append(r.items, it)
	}
	m.conf, m.run = nil, r
	if len(r.items) == 0 || m.rm.Run == nil {
		if len(r.items) > 0 {
			r.err = fmt.Errorf("removal is not available here")
		}
		return m.finishRun(nil), nil
	}
	m.phase = cleanupPhaseRunning
	return m, startRunCmd(m.rm.Run, r.items)
}

// startRunCmd starts Run on its own goroutine and returns its first
// message. Each progress call becomes one message, then a done message
// carries every outcome; a panic in Run ends the stream with an error. The
// channel holds every message the stream can send, so the goroutine never
// blocks on a program that has gone.
func startRunCmd(run func([]removal.Item, func(removal.Outcome)) []removal.Outcome, items []removal.Item) tea.Cmd {
	return func() tea.Msg {
		ch := make(chan tea.Msg, len(items)+1)
		go func() {
			defer close(ch)
			defer func() {
				if r := recover(); r != nil {
					ch <- cleanupRunDoneMsg{err: fmt.Errorf("the removal stopped: %v", r)}
				}
			}()
			sent := 0
			outs := run(items, func(o removal.Outcome) {
				if sent < len(items) {
					sent++
					ch <- cleanupProgressMsg{o: o, ch: ch}
				}
			})
			ch <- cleanupRunDoneMsg{outcomes: outs}
		}()
		return waitRun(ch)()
	}
}

// waitRun is the next message of a run's stream.
func waitRun(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return cleanupRunDoneMsg{err: fmt.Errorf("the removal ended without reporting")}
		}
		return msg
	}
}

// gotProgress records one finished item and waits for the next message.
func (m cleanupListModel) gotProgress(msg cleanupProgressMsg) (cleanupListModel, tea.Cmd) {
	r := m.run
	if m.phase != cleanupPhaseRunning || r == nil {
		return m, nil
	}
	if r.got < len(r.runAt) {
		i := r.runAt[r.got]
		r.results[i], r.have[i] = msg.o, true
		// Into the exit log now, not when the run ends: a program that
		// ends before then still prints it (Ruling T).
		m.log = append(m.log, msg.o)
		r.logged[i] = true
		r.got++
	}
	return m, waitRun(msg.ch)
}

// gotRunDone shows the results, or quits when a signal asked for it
// during the run.
func (m cleanupListModel) gotRunDone(msg cleanupRunDoneMsg) (cleanupListModel, tea.Cmd) {
	if m.phase != cleanupPhaseRunning || m.run == nil {
		return m, nil
	}
	m = m.finishRun(&msg)
	if m.quitAfterRun {
		m.done = true
		return m, tea.Quit
	}
	return m, nil
}

// unfinishedLines are what a program ended mid-run says about the items it
// has no outcome for (Ruling T): the one in flight is in an unknown state,
// the ones after it were not attempted, the refused ones were not removed.
func (r *cleanupRun) unfinishedLines() []string {
	at := map[int]int{}
	for k, i := range r.runAt {
		at[i] = k
	}
	var out []string
	for i, e := range r.entries {
		if r.logged[i] {
			continue
		}
		name := string(e.pv.Item.Kind) + " " + cleanup.OneLine(e.pv.Item.Name)
		k, ran := at[i]
		switch {
		case ran && k == r.got:
			out = append(out, name+": state unknown: wt ended while it was being removed; check the repository")
		case ran:
			out = append(out, name+": not attempted")
		case r.have[i]:
			out = append(out, outcomeLines(r.results[i])...)
		}
	}
	return out
}

// finishRun settles every entry's outcome and opens the results screen.
// Run's returned outcomes win when there is one per item; otherwise what
// progress reported stands and an item nothing was reported for says so:
// its state is unknown, never "fine".
func (m cleanupListModel) finishRun(msg *cleanupRunDoneMsg) cleanupListModel {
	r := m.run
	if msg != nil {
		switch {
		case msg.err != nil:
			r.err = msg.err
		case len(msg.outcomes) == len(r.items):
			for k, o := range msg.outcomes {
				i := r.runAt[k]
				r.results[i], r.have[i] = o, true
			}
		default:
			r.err = fmt.Errorf("the removal reported %d outcomes for %d items", len(msg.outcomes), len(r.items))
		}
	}
	for k, i := range r.runAt {
		if r.have[i] {
			continue
		}
		why := "not reported"
		if r.err != nil {
			why += ": " + r.err.Error()
		}
		o := removal.NewOutcome(r.items[k], removal.WtFailed, removal.BrRetainedUnknown,
			why+"; check the repository: this item may or may not have been removed")
		r.results[i], r.have[i] = o, true
	}
	// Outcomes progress already logged stay as they were logged; the rest
	// (refusals, items nothing was reported for) join the log now.
	for i, o := range r.results {
		if !r.logged[i] {
			m.log = append(m.log, o)
			r.logged[i] = true
		}
	}
	m.phase = cleanupPhaseResults
	return m
}

// resultsKey is a key on the results screen: arrows scroll, ctrl+c and q
// leave the program, anything else goes back to a list rebuilt from the
// repository as it is now.
func (m cleanupListModel) resultsKey(msg tea.KeyMsg) (cleanupListModel, tea.Cmd) {
	switch msg.String() {
	case "up":
		m.run.offset = max(0, m.run.offset-1)
		return m, nil
	case "down":
		m.run.offset++
		return m, nil
	case "pgup":
		m.run.offset = max(0, m.run.offset-max(1, m.height/2))
		return m, nil
	case "pgdown":
		m.run.offset += max(1, m.height/2)
		return m, nil
	case "ctrl+c", "q":
		m.done = true
		return m, tea.Quit
	}
	if m.rm.Refresh == nil {
		m.done = true
		return m, tea.Quit
	}
	m.phase = cleanupPhaseRefreshing
	return m, refreshCmd(m.rm.Refresh)
}

// refreshCmd re-reads the repository off the update loop; a panic is an
// error.
func refreshCmd(refresh func() (cleanup.Report, error)) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = cleanupRefreshMsg{err: fmt.Errorf("%v", r)}
			}
		}()
		rep, err := refresh()
		return cleanupRefreshMsg{report: rep, err: err}
	}
}

// gotRefresh rebuilds the list from the fresh report, or ends the program
// when the repository could not be re-read (the results print either way).
func (m cleanupListModel) gotRefresh(msg cleanupRefreshMsg) (tea.Model, tea.Cmd) {
	if m.phase != cleanupPhaseRefreshing {
		return m, nil
	}
	if msg.err != nil {
		m.done = true
		m.exitErr = fmt.Errorf("could not re-read the repository after removing: %w", msg.err)
		return m, tea.Quit
	}
	o := m.opts
	o.Theme, o.Now = m.theme, m.clock()
	n := newRowList(msg.report, o)
	n.rm, n.log, n.clock = m.rm, m.log, m.clock
	n.width, n.height = m.width, m.height
	n.status = removedSummary(m.run.results) + "; the results print again when you quit"
	if !n.empty() {
		n.offset = n.layout().offset
	}
	return n, nil
}

// startRefetch handles `F` in the manage list: drop the stale session claims
// (pruneStaleFirst), fetch the remotes and re-read the repository through
// RemoveFuncs.Refetch, then rebuild from the fresh report. A nil Refetch says
// so on the status line (like `r` with no rescue) and prunes nothing — no
// refresh ran. While the read is in flight the list is in
// cleanupPhaseRefreshing, which keeps every key but ctrl+c.
func (m cleanupListModel) startRefetch() (cleanupListModel, tea.Cmd) {
	if m.rm.Refetch == nil {
		m.status = "refresh is not available here"
		return m, nil
	}
	m.phase = cleanupPhaseRefreshing
	return m, pruneStaleFirst(refreshRoot(m.opts.Root, m.report), refetchCmd(m.rm.Refetch, m.opts.Root))
}

// refetchCmd runs the `F` re-read off the update loop; a panic is an error.
func refetchCmd(refetch func() (cleanup.Report, error), root string) tea.Cmd {
	return func() (msg tea.Msg) {
		defer func() {
			if r := recover(); r != nil {
				msg = cleanupRefetchMsg{err: fmt.Errorf("%v", r)}
			}
		}()
		rep, err := refetch()
		return cleanupRefetchMsg{report: rep, stamp: readRefreshStamp(refreshRoot(root, rep)), err: err}
	}
}

// pruneStaleFirst wraps a `F` refresh's command (never nil) so the user's
// refresh first drops the stale session claims in root's wt-sessions —
// sessionclaim.Prune: the file of a dead or reused pid, or a malformed one —
// then runs next (the fetch and the re-read) unchanged. This is the list
// half of wt's prune contract and this package's single sessionclaim.Prune
// call site: the list's refresh (this one key path, the picker's rows reload
// and the manage list's re-read alike) and cmd/wt's fetch-first `--json` run
// are the only two paths that prune — opening the list, the post-run re-read
// and `--no-fetch` never drop a claim. All of it is best-effort: no readable
// common directory, or a prune that fails, leaves the claims as they were
// and the refresh runs anyway (the readers ignore a stale claim in display,
// so nothing hangs on the drop).
func pruneStaleFirst(root string, next tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		if root != "" {
			if common, err := worktree.CommonDirAt(root); err == nil && common != "" {
				_, _ = sessionclaim.Prune(common)
			}
		}
		return next()
	}
}

// gotRefetch applies the `F` re-read: a fresh report rebuilds the list with
// the cursor and filter kept (withReport) and the header's clock moved to
// now, so its "refreshed …" note counts from this refresh. A failure is one
// line on the status and the old list stays — nothing was removed here,
// unlike the post-run refresh, whose failure ends the program (gotRefresh).
func (m cleanupListModel) gotRefetch(msg cleanupRefetchMsg) (tea.Model, tea.Cmd) {
	if m.phase != cleanupPhaseRefreshing {
		return m, nil
	}
	m.phase = cleanupPhaseList
	if msg.err != nil {
		m.status = "refresh failed: " + cleanup.OneLine(msg.err.Error())
		return m, nil
	}
	m.now = m.clock()
	m.opts.Now = m.now
	st := msg.stamp
	m.opts.Stamp = &st
	return m.withReport(msg.report), nil
}

// removedSummary counts what a run removed.
func removedSummary(outs []removal.Outcome) string {
	n := 0
	for _, o := range outs {
		if o.Worktree == removal.WtRemoved || o.Branch == removal.BrDeleted {
			n++
		}
	}
	return fmt.Sprintf("removed %d of %d", n, len(outs))
}

// outcomeLines are one outcome as text: a line naming the item and what
// happened to its worktree and branch, then its reason, losses, warnings
// and, for a branch this run deleted (or may have), the commit it pointed
// at (Ruling J). Every piece of engine text is one sanitised line.
func outcomeLines(o removal.Outcome) []string {
	out := []string{removal.Headline(o)}
	if o.Reason != "" {
		out = append(out, "    "+cleanup.OneLine(o.Reason))
	}
	for _, l := range removal.DetailLines(o) {
		out = append(out, "    "+l)
	}
	return out
}

// logText is every outcome of the session, printed after the program ends
// so that what was removed, and every restore id, outlives the alt screen.
func logText(log []removal.Outcome) string {
	if len(log) == 0 {
		return ""
	}
	lines := []string{"wt worktrees: " + removedSummary(log) + " items"}
	for _, o := range log {
		lines = append(lines, outcomeLines(o)...)
	}
	return strings.Join(lines, "\n")
}

// wrapIndented wraps text to width with indent in front of every line.
func wrapIndented(text, indent string, width int) []string {
	w := 0
	if width > 0 {
		w = max(1, width-lipgloss.Width(indent))
	}
	var out []string
	for _, l := range strings.Split(tuilayout.WrapText(text, w), "\n") {
		out = append(out, indent+l)
	}
	return out
}

// wrapOutcome is outcomeLines wrapped, continuation lines indented. The
// deleted branch's commit id is never cut: when "branch tip was <id>" does
// not fit, the id gets a line of its own, indented only as far as the width
// leaves room for all of it.
func wrapOutcome(o removal.Outcome, width int) []string {
	var out []string
	const tipLead = "branch tip was "
	for k, l := range outcomeLines(o) {
		indent := "  "
		if k > 0 {
			indent = "      "
			l = strings.TrimLeft(l, " ")
		}
		if tip, ok := strings.CutPrefix(l, tipLead); ok && k > 0 && width > 0 && len(indent)+len(l) > width {
			out = append(out, indent+strings.TrimSpace(tipLead))
			if width >= len(tip) {
				out = append(out, strings.Repeat(" ", max(0, min(len(indent), width-len(tip))))+tip)
				continue
			}
			// Narrower than the id: split it over lines, never cut it.
			for len(tip) > 0 {
				n := min(width, len(tip))
				out = append(out, tip[:n])
				tip = tip[n:]
			}
			continue
		}
		out = append(out, wrapIndented(l, indent, width)...)
	}
	return out
}

// approvalText names the approvals the typed word gives an item, as the
// CLI's flags.
func approvalText(n removal.Needs) string {
	var f []string
	if n.Confirm {
		f = append(f, "--accept-loss")
	}
	if n.Force {
		f = append(f, "--force")
	}
	if n.Delete {
		f = append(f, "-D")
	}
	if n.Remote {
		f = append(f, "--remote (delete origin)")
	}
	if len(f) == 0 {
		return "approvals: none needed (proven)"
	}
	return "approvals: " + strings.Join(f, " ")
}

// shortPath is a worktree path relative to the repository when it is inside
// it (".worktrees/topic"), else the path as it is.
func (m *cleanupListModel) shortPath(p string) string {
	if m.report.Repo == "" {
		return p
	}
	rel, err := filepath.Rel(m.report.Repo, p)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return rel
}

// branchOf is a full refname's short name.
func branchOf(ref string) string { return strings.TrimPrefix(ref, "refs/heads/") }

// entryLines are one confirm entry: its name line, then, first, what the
// user decides about it — the branch toggle (or the branch it deletes) and
// the approvals it gets, or why it will not be removed — and only then what
// it loses, the processes running in it, warnings and notes. keyN is how
// many lines the decision part takes from the top: an entry taller than
// the screen (dozens of processes) never hides it.
func (m *cleanupListModel) entryLines(e confirmEntry, selected bool) (out []string, keyN int) {
	pv := e.pv
	cursor := "  "
	if selected {
		cursor = "> "
	}
	head := cursor + string(pv.Item.Kind) + " " + cleanup.OneLine(pv.Item.Name)
	if pv.Item.Kind == cleanup.KindRemote {
		head = cursor + "remote origin/" + cleanup.OneLine(pv.Item.Name)
	}
	if pv.Item.Kind == cleanup.KindWorktree && pv.Item.Path != "" {
		head += "  " + cleanup.OneLine(m.shortPath(pv.Item.Path))
	}
	out = wrapIndented(head, "", m.width)
	add := func(s string) { out = append(out, wrapIndented(s, "      ", m.width)...) }
	if !e.runnable() {
		why := pv.Reason
		if why == "" {
			why = pv.Plan.Reason
		}
		add("not removed: " + cleanup.OneLine(why))
	} else {
		switch {
		case pv.CanDeleteBranch && e.delBranch:
			add("[x] also delete unmerged branch " + cleanup.OneLine(branchOf(pv.Fresh.Ref)) + " (tab)")
		case pv.CanDeleteBranch:
			add("[ ] also delete unmerged branch " + cleanup.OneLine(branchOf(pv.Fresh.Ref)) + " (tab)")
		case pv.Item.Kind == cleanup.KindBranch && pv.Needs.Delete:
			add("deletes unmerged branch " + cleanup.OneLine(pv.Item.Name))
		}
		add(approvalText(e.approved()))
		if pv.Item.Kind == cleanup.KindRemote && pv.Fresh.Tip != "" {
			if pv.Fresh.PR != nil {
				add(fmt.Sprintf("merged PR #%d; no open PR uses this branch as of the re-check", pv.Fresh.PR.Number))
			}
			add("restore: git push origin " + cleanup.OneLine(pv.Fresh.Tip) + ":refs/heads/" + cleanup.OneLine(pv.Item.Name))
		}
	}
	keyN = len(out)
	for _, l := range pv.LossLines {
		add("will lose: " + cleanup.OneLine(l))
	}
	for _, p := range pv.Procs {
		add(fmt.Sprintf("running in it: pid %d %s", p.PID, cleanup.OneLine(p.Command)))
	}
	for _, w := range pv.Warnings {
		add("warning: " + cleanup.OneLine(w))
	}
	// A toggled-on branch deletion replaces the kept-branch note, which
	// would then be false.
	if e.runnable() && !(pv.CanDeleteBranch && e.delBranch) {
		for _, n := range pv.Notes {
			add("note: " + cleanup.OneLine(n))
		}
	}
	return out, keyN
}

// confirmBody is the confirm screen's scrolling part: each group's heading
// and entries. starts[i] is entry i's first line, keyEnds[i] the line after
// its decision part (entryLines' keyN).
func (m *cleanupListModel) confirmBody() (lines []string, starts, keyEnds []int) {
	c := m.conf
	head := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader)).Bold(true)
	sel := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	starts, keyEnds = make([]int, len(c.entries)), make([]int, len(c.entries))
	last := removal.Approval("-")
	for i, e := range c.entries {
		if e.pv.Approval != last {
			last = e.pv.Approval
			for _, g := range confirmGroups {
				if g.approval == last {
					lines = append(lines, head.Render(clip(g.heading, m.width)))
				}
			}
		}
		starts[i] = len(lines)
		el, keyN := m.entryLines(e, i == c.cursor)
		keyEnds[i] = starts[i] + keyN
		for _, l := range el {
			l = clip(l, m.width)
			if i == c.cursor {
				l = sel.Render(l)
			}
			lines = append(lines, l)
		}
	}
	return lines, starts, keyEnds
}

// confirmTop is the confirm screen's title and its summary lines.
func (m *cleanupListModel) confirmTop() (title, summary []string) {
	c := m.conf
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	n, all := c.runnableCount(), len(c.entries)
	// The count per group stays on screen when the entries scroll, so an
	// item below the fold that loses something is never unannounced.
	var parts []string
	for _, g := range confirmGroups {
		k := 0
		for _, e := range c.entries {
			if e.pv.Approval == g.approval {
				k++
			}
		}
		if k > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", k, confirmCountWords[g.approval]))
		}
	}
	sum := fmt.Sprintf("remove %d of %d %s (%s); nothing has been removed yet", n, all, m.chosenWord(), strings.Join(parts, ", "))
	if k := c.originCount(); k > 0 {
		noun := "branches"
		if k == 1 {
			noun = "branch"
		}
		sum += fmt.Sprintf(" · %d %s on origin, visible to everyone with access to the repository; no open PR uses them as of the re-check, and GitHub can restore a deleted branch from its merged PR", k, noun)
	}
	return []string{accent.Render(clip(m.screenName()+" · confirm", m.width))}, wrapIndented(sum, "", m.width)
}

// confirmHints are the confirm screen's key hints.
func (m *cleanupListModel) confirmHints() []string {
	if !m.conf.offersToggle() {
		return nil
	}
	return wrapIndented("↑↓ move · pgup/pgdown scroll · tab: also delete the highlighted item's unmerged branch", "", m.width)
}

// confirmPrompt is the confirm screen's prompt (never given up). It always
// says what the answer approves (Ruling U): how many items, how many of
// them with the --force and -D equivalents, and how many are not on screen
// (hidden), so nothing `delete` is typed for is out of sight in count and
// kind. Below 60 columns it is the short form, which fits in 40.
func (m *cleanupListModel) confirmPrompt(hidden int) []string {
	c := m.conf
	n, force, del := 0, 0, 0
	word := c.word()
	for _, e := range c.entries {
		if !e.runnable() {
			continue
		}
		n++
		a := e.approved()
		if a.Force {
			force++
		}
		if a.Delete {
			del++
		}
	}
	noun := "items"
	if n == 1 {
		noun = "item"
	}
	kinds := fmt.Sprintf("(%d force, %d -D)", force, del)
	if o := c.originCount(); o > 0 {
		kinds = fmt.Sprintf("(%d force, %d -D, %d on origin)", force, del, o)
	}
	more := ""
	if hidden > 0 {
		more = fmt.Sprintf(" · %d not shown", hidden)
	}
	narrow := m.width > 0 && m.width < 60
	var ask string
	switch {
	case n == 0:
		return wrapIndented("nothing here can be removed · any key goes back", "", m.width)
	case !c.needsTyped() && narrow:
		ask = fmt.Sprintf("y: removes %d %s%s · other keys cancel", n, kinds, more)
	case !c.needsTyped():
		ask = fmt.Sprintf("y removes %d %s %s%s · any other key cancels", n, noun, kinds, more)
	case narrow:
		ask = fmt.Sprintf("%s+enter: removes %d %s%s · esc", word, n, kinds, more)
	default:
		ask = fmt.Sprintf("type %s and press enter to remove %d %s %s with the approvals listed%s · esc cancels", word, n, noun, kinds, more)
	}
	out := wrapIndented(ask, "", m.width)
	if c.needsTyped() {
		out = append(out, clip("> "+cleanup.OneLine(c.typed)+"_", m.width))
	}
	return out
}

// confirmFrame is one frame of the confirm screen: what is above and below
// the entries, the entries' lines, where each entry and its decision part
// start and end, and the window on them.
type confirmFrame struct {
	top, foot      []string
	body           []string
	starts, keyEnd []int
	offset, room   int
}

// visible says whether entry i's decision part (name, toggle, approvals)
// is wholly on screen in f.
func (f confirmFrame) visible(i int) bool {
	return f.starts[i] >= f.offset && f.keyEnd[i] <= f.offset+f.room
}

// frame lays the confirm screen out. On a short terminal the hints, then
// the summary, then the title give way so that the highlighted entry's
// decision part shows beside the prompt. The arrows (and tab) keep the
// highlighted entry in view; pgup/pgdown scroll freely, within a tall entry
// too. The prompt is laid out for the largest "not shown" count first, so
// counting what the window hides cannot change the window.
func (m *cleanupListModel) frame() confirmFrame {
	c := m.conf
	title, summary := m.confirmTop()
	hints := m.confirmHints()
	body, starts, keyEnds := m.confirmBody()
	reserve := m.confirmPrompt(len(c.entries))
	need := keyEnds[c.cursor] - starts[c.cursor]
	if m.height > 0 {
		fits := func() bool { return len(title)+len(summary)+len(hints)+len(reserve)+need+1 <= m.height }
		if !fits() {
			hints = nil
		}
		if !fits() {
			summary = nil
		}
		if !fits() {
			title = nil
		}
	}
	top := append(title, summary...)
	room := m.bodyRoom(len(top)+len(hints)+len(reserve), len(body))
	if c.follow {
		end := len(body)
		if c.cursor+1 < len(starts) {
			end = starts[c.cursor+1]
		}
		c.offset = keepInView(c.offset, starts[c.cursor], end, len(body), room)
	}
	c.offset = max(0, min(c.offset, max(0, len(body)-room)))
	f := confirmFrame{top: top, body: body, starts: starts, keyEnd: keyEnds, offset: c.offset, room: room}
	hidden := 0
	for i := range c.entries {
		if !f.visible(i) {
			hidden++
		}
	}
	f.foot = append(hints, m.confirmPrompt(hidden)...)
	return f
}

// confirmView draws the confirm screen.
func (m *cleanupListModel) confirmView() string {
	f := m.frame()
	return m.framedRoom(f.top, f.body, f.offset, f.room, f.foot)
}

// bodyRoom is how many body lines fit beside fixed lines, one going to the
// scroll hint when the body does not fit whole.
func (m *cleanupListModel) bodyRoom(fixed, bodyLen int) int {
	if m.height <= 0 {
		return bodyLen
	}
	room := max(1, m.height-fixed)
	if bodyLen > room && room > 1 {
		room--
	}
	return room
}

// keepInView is offset moved so the lines [start, end) show in room lines
// (the start when they are taller than room), within [0, total-room].
func keepInView(offset, start, end, total, room int) int {
	if end > offset+room {
		offset = end - room
	}
	if start < offset {
		offset = start
	}
	return max(0, min(offset, max(0, total-room)))
}

// framed is top, the body from offset (with a scroll hint when it does not
// fit), and foot, no taller than the terminal.
func (m *cleanupListModel) framed(top, body []string, offset int, foot []string) string {
	return m.framedRoom(top, body, offset, m.bodyRoom(len(top)+len(foot), len(body)), foot)
}

// framedRoom is framed with the body's room given.
func (m *cleanupListModel) framedRoom(top, body []string, offset, room int, foot []string) string {
	end := min(len(body), offset+room)
	lines := append([]string{}, top...)
	lines = append(lines, body[offset:end]...)
	if end-offset < len(body) {
		var more []string
		if offset > 0 {
			more = append(more, "↑ more above")
		}
		if end < len(body) {
			more = append(more, "↓ more below")
		}
		dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		lines = append(lines, dim.Render(clip("  "+strings.Join(more, " · "), m.width)))
	}
	lines = append(lines, foot...)
	// A terminal too short for the title and the prompt together loses the
	// title first: the prompt, last, is what the user answers.
	if m.height > 0 && len(lines) > m.height {
		lines = lines[len(lines)-m.height:]
	}
	return strings.Join(lines, "\n")
}

// runningView is the progress screen: what finished, the item in flight,
// how many wait.
func (m *cleanupListModel) runningView() string {
	r := m.run
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	top := []string{accent.Render(clip(fmt.Sprintf("%s · removing %d items", m.screenName(), len(r.items)), m.width))}
	var body []string
	for k, i := range r.runAt {
		e := r.entries[i]
		name := string(e.pv.Item.Kind) + " " + cleanup.OneLine(e.pv.Item.Name)
		switch {
		case k < r.got:
			o := r.results[i]
			body = append(body, wrapIndented(fmt.Sprintf("done  %s: worktree %s, branch %s", name, o.Worktree, o.Branch), "", m.width)...)
		case k == r.got:
			body = append(body, wrapIndented("now   "+name, "", m.width)...)
		}
	}
	if w := len(r.items) - r.got - 1; w > 0 {
		body = append(body, fmt.Sprintf("%d waiting", w))
	}
	// Ruling T: nothing interrupts a run, and the screen says so.
	foot := wrapIndented("removing, please wait: keys, ctrl+c and signals wait until the run ends", "", m.width)
	if m.quitAfterRun {
		foot = append(foot, wrapIndented("a quit was asked: wt quits when this run ends", "", m.width)...)
	}
	room := m.bodyRoom(len(top)+len(foot), len(body))
	offset := max(0, len(body)-room) // the newest lines
	return m.framed(top, body, offset, foot)
}

// resultsView is the results screen: every ticked item's outcome.
func (m *cleanupListModel) resultsView() string {
	r := m.run
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	top := []string{accent.Render(clip(m.screenName()+" · results: "+removedSummary(r.results), m.width))}
	if r.err != nil {
		top = append(top, wrapIndented("the run did not finish cleanly: "+cleanup.OneLine(r.err.Error()), "", m.width)...)
	}
	var body []string
	for _, o := range r.results {
		body = append(body, wrapOutcome(o, m.width)...)
	}
	next := "any other key: back to the list"
	if m.rm.Refresh == nil {
		next = "any other key quits"
	}
	foot := wrapIndented("↑↓ scroll · q quits · "+next+" · these results print again when you quit", "", m.width)
	room := m.bodyRoom(len(top)+len(foot), len(body))
	r.offset = max(0, min(r.offset, len(body)-room))
	return m.framed(top, body, r.offset, foot)
}

// waitView is a one-line screen while the engine works.
func (m *cleanupListModel) waitView(text string) string {
	return capLines(clip(tuilayout.WrapText(text, m.width), m.width), m.height)
}
