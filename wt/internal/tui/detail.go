package tui

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
	"github.com/ohanaverse/local-ai-setup/wt/internal/removal"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rescue"
	"github.com/ohanaverse/local-ai-setup/wt/internal/rootmove"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// The detail screen: one row, everything known about it, and only the
// actions that apply to it. `i` opens it to read; `x`, `d`, `r`, `R` and `o`
// open it too, unarmed, so the first thing every row action shows is what
// the row is (the user asked for one screen that explains, then acts).
//
// It is a phase of the same model, not a model of its own: the list keeps its
// place underneath, `esc` returns to it, and the removal actions reuse the
// list's own preview → confirm → run flow (cleanup_confirm.go) — `x`, `d` and
// `o` start it from here and the confirm returns to this screen. `r` and `R`
// keep their quit-and-hand-off flow (only the list's session loop can run
// them, once the alt screen is gone).
//
// detailBlock is the screen's reading part, shared with the single-row
// confirm: the same lines for `i` and for the confirmation of the same row
// are the point (the spec's "the detail block is identical for i and for the
// confirmation").

// detailLabelWidth is the label column's width: "worktree" plus two spaces.
const detailLabelWidth = 10

// rowDetail is the detail screen's state: the row it was opened on, what the
// engine's preview said about it (nil until it lands, and nil when there is
// no preview to make), the read-only listings (nil when it could not be read
// here, or has not landed yet), whether a load is still in flight, and how
// far down the block the user scrolled.
type rowDetail struct {
	row     cleanup.Row
	pv      *removal.ItemPreview
	lists   *cleanup.RowDetail
	loading bool
	offset  int
}

// detailLoadedMsg is a row's preview and listings landing, off the update
// goroutine. key says which row they belong to, so a load that lands after
// the screen moved (or closed) is dropped.
type detailLoadedMsg struct {
	key   rowKey
	pv    *removal.ItemPreview
	lists cleanup.RowDetail
}

// detailAction is one entry of the screen's action list: the key, the short
// label the footer and the list use, and the one plain sentence the screen
// prints for it.
type detailAction struct {
	Key, Label, Does string
}

// detailActions is what the screen offers for the row it shows: only actions
// that apply, in key order, ending with esc. It reads the same predicates the
// handlers and the list's footer read (cleanupLocked, originTarget,
// rescue.Offered, rootmove.Offered, the wired RemoveFuncs, the fill in
// flight), so an action is listed exactly when pressing its key would start
// that action rather than say why not.
func (m cleanupListModel) detailActions() []detailAction {
	if m.rowDetail == nil {
		return nil
	}
	row := m.rowDetail.row
	filling := m.filling()
	// No removal while the fill is in flight: the removal funcs are built
	// from the filled snapshot only, so the actions are absent exactly when
	// their handlers would refuse (nil-RemoveFuncs rule).
	canRemove := !filling && m.rm.Off == "" && m.rm.Preview != nil
	var out []detailAction
	if canRemove && !cleanupLocked(row) {
		out = append(out,
			detailAction{Key: "x", Label: "remove", Does: "deletes the worktree directory; keeps the branch unless it is proven merged; lists what is lost; unproven rows need the typed word delete."},
			detailAction{Key: "d", Label: "remove + branch", Does: "as x, and also deletes the unmerged branch (the box starts on, tab toggles it); on a branch-only row it equals x."},
		)
	}
	if ok, _ := rescue.Offered(row); ok && !filling && m.rm.Rescue != nil {
		out = append(out, detailAction{Key: "r", Label: "rescue", Does: "copies uncommitted and unpushed work into a new rescued/<name> branch and worktree; the original is untouched; leaves the TUI to run wt rescue, then returns to the list."})
	}
	if ok, _ := rootmove.Offered(row); ok && !filling && m.rm.RestoreRoot != nil {
		out = append(out, detailAction{Key: "R", Label: "restore root", Does: "moves its branch and its work into a new worktree and puts the root back on the default branch."})
	}
	if target, has := m.originTarget(row); canRemove && has && !cleanupLocked(target) {
		out = append(out, detailAction{Key: "o", Label: "delete origin", Does: "deletes the origin ref and nothing local; needs the typed phrase delete origin."})
	}
	return append(out, detailAction{Key: "esc", Label: "back to the list", Does: "returns to the list, with the cursor still on this row."})
}

// openDetail opens the detail screen on the highlighted row and dispatches the
// reads it needs off the update goroutine: the engine's preview (read-only,
// so `i` and `x` show the same "will lose" lines) and the read-only listings.
// Nothing is previewed when the row cannot be removed or no funcs are wired;
// the screen says so instead.
func (m cleanupListModel) openDetail() (cleanupListModel, tea.Cmd) {
	row, ok := m.selectedRow()
	if !ok {
		// Launch mode's "+ New worktree…" is not a row: it has nothing to
		// show and nothing to remove, and it says so rather than swallowing
		// the key.
		if m.cursor >= 0 && m.cursor < len(m.items) && m.items[m.cursor].newWorktree {
			m.status = "this is the + New worktree… item, not a row: it creates a worktree, there is nothing to remove"
		}
		return m, nil
	}
	m.phase = cleanupPhaseDetail
	m.rowDetail = &rowDetail{row: row}
	if m.rm.Preview == nil && m.rm.Detail == nil {
		return m, nil
	}
	m.rowDetail.loading = true
	return m, m.detailCmd(row)
}

// detailCmd reads one row's preview and listings. The preview goes through
// previewCmd, so a preview that panics or answers for another row is dropped
// rather than shown; the listings are the read-only detail reads.
func (m cleanupListModel) detailCmd(row cleanup.Row) tea.Cmd {
	preview, detail := m.rm.Preview, m.rm.Detail
	key := keyOf(row)
	return func() tea.Msg {
		var pv *removal.ItemPreview
		if preview != nil {
			if msg, ok := previewCmd(preview, []cleanup.Row{row})().(cleanupPreviewMsg); ok && msg.err == nil && len(msg.previews) == 1 {
				pv = &msg.previews[0]
			}
		}
		var lists cleanup.RowDetail
		if detail != nil {
			lists = detail(row)
		}
		return detailLoadedMsg{key: key, pv: pv, lists: lists}
	}
}

// gotDetailLoaded applies a landing: a load for another row, or one that
// arrives after the screen closed (rowDetail is nil then), is dropped.
func (m cleanupListModel) gotDetailLoaded(msg detailLoadedMsg) (cleanupListModel, tea.Cmd) {
	// Any phase with the screen's state alive takes it: a load that lands
	// while a confirm opened from the screen is up must not be lost, or the
	// screen the cancel returns to would read "loading" for good.
	if m.rowDetail == nil || keyOf(m.rowDetail.row) != msg.key {
		return m, nil
	}
	m.rowDetail.pv = msg.pv
	if m.rm.Detail != nil {
		lists := msg.lists
		m.rowDetail.lists = &lists
	}
	m.rowDetail.loading = false
	return m, nil
}

// settleDetail follows a report swap onto the open detail screen: the row it
// shows is re-pointed at the report's own copy of it (same key), and a row the
// new report no longer carries closes the screen with a status line. Why: the
// screen must never offer actions against a row the list has since dropped.
func (m *cleanupListModel) settleDetail() {
	if m.phase != cleanupPhaseDetail || m.rowDetail == nil {
		return
	}
	key := keyOf(m.rowDetail.row)
	for _, row := range cleanup.ListOrder(m.report) {
		if keyOf(row) == key {
			m.rowDetail.row = row
			return
		}
	}
	gone := cleanup.DisplayName(m.rowDetail.row)
	m.phase, m.rowDetail = cleanupPhaseList, nil
	m.status = gone + " is gone from the refreshed list"
}

// detailKey is a key on the detail screen: esc back to the list, the movement
// keys scroll the block, and the action keys run the actions the screen
// lists.
func (m cleanupListModel) detailKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.rowDetail
	if d == nil {
		m.phase = cleanupPhaseList
		return m, nil
	}
	page := max(1, m.height/2)
	// A refusal or a cancel's status answers the key before this one: the next
	// key takes it down, as on the list.
	m.status = ""
	switch msg.String() {
	case "esc":
		// The detail screen is a rest: a fill that landed under it was
		// deferred (swapRemoveFuncs), and the list it returns to must not
		// keep the facts-stage funcs.
		m.applyPendingRemoveFuncs()
		m.phase, m.rowDetail = cleanupPhaseList, nil
		return m, nil
	case "ctrl+c":
		m.done = true
		return m, tea.Quit
	case "up", "k":
		d.offset = max(0, d.offset-1)
	case "down", "j":
		d.offset++
	case "pgup", "b":
		d.offset = max(0, d.offset-page)
	case "pgdown", "f":
		d.offset += page
	case "home", "g":
		d.offset = 0
	case "end", "G":
		d.offset = 1 << 16 // clamped by detailView against the block's length
	case "x", "d", "o":
		kind := map[string]rowAction{"x": rowActionRemove, "d": rowActionDeleteBranch, "o": rowActionDeleteOrigin}[msg.String()]
		// This screen is a rest: a report swap deferred under it (picker
		// fill or PR lookup) is applied before an action reads the funcs, so
		// the action stands on the rows the screen shows. startRemovalOf
		// records that the confirm it opens returns here.
		m.applyPendingRemoveFuncs()
		return m.startRowAction(kind)
	case "r":
		m.applyPendingRemoveFuncs()
		return m.startRescue()
	case "R":
		m.applyPendingRemoveFuncs()
		return m.startRestoreRoot()
	}
	return m, nil
}

// shortSHA is a commit id as the screen shows it: its first eight characters,
// the same abbreviation the listings use.
func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// detailLine is one labelled line of the block, its value wrapped and its
// continuation lines indented under the value.
func detailLine(name, value string, width int) []string {
	if value == "" {
		return nil
	}
	head := tuilayout.PadRunes(name, detailLabelWidth)
	w := 0
	if width > 0 {
		w = max(1, width-detailLabelWidth)
	}
	lines := strings.Split(tuilayout.WrapText(value, w), "\n")
	out := []string{head + lines[0]}
	indent := strings.Repeat(" ", detailLabelWidth)
	for _, l := range lines[1:] {
		out = append(out, indent+l)
	}
	return out
}

// detailBlock is the reading part of the detail screen, and the same lines
// the single-row confirmation prints above its prompt: the row's name, branch
// and full path, its facts, its Verdict line (the verdict word and, for a row
// nothing can be done about, why), the local-only commits, the uncommitted
// and ignored files, and what the removal preview says would be lost. Never
// an action: those are detailActions', below the block.
//
// width is the terminal's width; a width of zero means no wrapping.
func detailBlock(rep cleanup.Report, row cleanup.Row, d *rowDetail, th themes.Theme, width int) []string {
	dim := lipgloss.NewStyle().Foreground(th.Token(themes.TokenDim))
	var out []string
	add := func(name, value string) { out = append(out, detailLine(name, value, width)...) }
	// say is a line with no label — a loss, a warning, a note — at the block's
	// own column, the way a section heading sits: the label column is for the
	// row's facts, and an indent there reads as a missing label.
	say := func(text string) { out = append(out, wrapIndented(text, "", width)...) }
	section := func(text string) {
		out = append(out, dim.Render(clip(text, width)))
	}

	name := cleanup.DisplayName(row)
	switch row.Kind {
	case cleanup.KindWorktree:
		// The path names the worktree when its directory is the name (the
		// usual case): printing both says it twice. A root always names
		// itself, since being the primary checkout is a fact about it and not
		// a path.
		switch base := filepath.Base(row.Path); {
		case row.Primary:
			add("worktree", cleanup.OneLine(name)+" (primary checkout)")
		case name != "" && base != name:
			add("worktree", cleanup.OneLine(name))
		}
		if row.Branch != "" {
			add("branch", cleanup.OneLine(row.Branch))
		} else if !row.Bare {
			add("branch", "(detached)")
		}
		add("path", cleanup.OneLine(row.Path))
	case cleanup.KindBranch:
		add("branch", cleanup.OneLine(row.Branch))
		if row.Tip != "" {
			add("tip", shortSHA(row.Tip))
		}
	default:
		// A remote row has no path: its ref, the commit it points at and the
		// remote it lives on are what name it.
		add("remote", cleanup.OneLine(row.Remote))
		add("branch", cleanup.OneLine(row.Branch))
		if row.Tip != "" {
			add("tip", shortSHA(row.Tip))
		}
	}
	switch {
	case row.PRTag != nil:
		add("PR", fmt.Sprintf("#%d %s", row.PRTag.Number, strings.ToLower(cleanup.OneLine(row.PRTag.State))))
	case row.PRUnknown:
		add("PR", "unknown (the lookup has not answered)")
	}
	if row.AgeKnown {
		add("age", strconv.Itoa(row.AgeDays)+" days")
	}
	if row.Sessions > 0 {
		add("sessions", strconv.Itoa(row.Sessions))
	}
	for _, tag := range cleanup.RowTags(row) {
		if strings.HasPrefix(tag.Text, "held by session") {
			add("holder", cleanup.OneLine(tag.Text))
		}
	}
	verdict := verdictWord(row)
	if why := detailWhy(rep, row); why != "" {
		verdict += " — " + why
	}
	add("Verdict", verdict)
	if row.Kind == cleanup.KindRemote {
		say("note: deleting an origin branch is visible to everyone with access to the repository; GitHub can restore it from its merged PR.")
	}

	// The listings: what the row holds that nothing else has.
	switch {
	case row.Kind == cleanup.KindRemote:
		// A remote row has no local-only work to list: nothing is missing.
	case d == nil || d.lists == nil:
		switch {
		case d != nil && d.loading:
			say("loading the commits and files…")
		default:
			say("the commit and file lists are not available here")
		}
	default:
		l := d.lists
		if l.Err != "" {
			say(cleanup.OneLine(l.Err))
		}
		if l.Note != "" {
			say(cleanup.OneLine(l.Note))
		}
		// Each entry wraps under its own two-space indent: a long subject or
		// a deep path is readable at 40 columns too, never a line past the
		// terminal's edge.
		item := func(text string) { out = append(out, wrapIndented(text, "  ", width)...) }
		if n := len(l.Commits) + l.CommitsMore; n > 0 {
			section(fmt.Sprintf("Local-only commits (%d)", n))
			for _, c := range l.Commits {
				item(cleanup.OneLine(c.Hash) + " " + cleanup.OneLine(c.Subject))
			}
			if l.CommitsMore > 0 {
				item(fmt.Sprintf("… and %d more", l.CommitsMore))
			}
		}
		if n := len(l.Uncommitted) + l.UncommittedMore; n > 0 {
			section(fmt.Sprintf("Uncommitted files (%d)", n))
			for _, f := range l.Uncommitted {
				item(cleanup.OneLine(f.Status) + "  " + cleanup.OneLine(f.Path))
			}
			if l.UncommittedMore > 0 {
				item(fmt.Sprintf("… and %d more", l.UncommittedMore))
			}
		}
		if n := len(l.Ignored) + l.IgnoredMore; n > 0 {
			section(fmt.Sprintf("Ignored files (%d)", n))
			for _, p := range l.Ignored {
				item(cleanup.OneLine(p))
			}
			if l.IgnoredMore > 0 {
				item(fmt.Sprintf("… and %d more", l.IgnoredMore))
			}
		}
	}

	// What the engine's preview says removal would lose.
	if d != nil && d.pv != nil {
		for _, l := range d.pv.LossLines {
			say("will lose: " + cleanup.OneLine(l))
		}
		for _, p := range d.pv.Procs {
			say(fmt.Sprintf("running in it: pid %d %s", p.PID, cleanup.OneLine(p.Command)))
		}
		for _, w := range d.pv.Warnings {
			say("warning: " + cleanup.OneLine(w))
		}
		for _, n := range d.pv.Notes {
			say("note: " + cleanup.OneLine(n))
		}
	}
	return out
}

// detailWhy is the sentence after the verdict word: why this row cannot be
// removed (the root first), or which facts lowered its verdict. "" when
// nothing lowers it (a safe row with nothing to say).
func detailWhy(rep cleanup.Report, row cleanup.Row) string {
	if row.Primary {
		return "the root cannot be removed"
	}
	seen := map[string]bool{}
	var parts []string
	// say once each, in the order the row gives them.
	say := func(text string) {
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		parts = append(parts, text)
	}
	add := func(code, detail string) { say(cleanup.ReasonText(code, detail)) }
	for _, b := range row.Blockers {
		switch b.Code {
		case cleanup.BlockNested:
			continue // the paths are on the list's detail line, not one per blocker
		case cleanup.BlockCurrent:
			// RowText's own words: the innermost worktree is where the caller
			// stands, an outer one only contains it.
			if row.Path == cleanup.CurrentPath(rep) {
				say("you are here")
			} else {
				say("contains the directory you are in")
			}
			continue
		}
		add(b.Code, b.Detail)
	}
	for _, r := range row.Reasons {
		add(r.Code, r.Detail)
	}
	return strings.Join(parts, "; ")
}

// detailView draws the detail screen: the title, the block, the actions
// (reached by scrolling, never cut) and the key footer made from the same
// action list.
func (m cleanupListModel) detailView() string {
	if m.rowDetail == nil {
		return ""
	}
	d := m.rowDetail
	accent := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenAccent)).Bold(true)
	top := []string{accent.Render(clip(m.screenName()+" · row detail", m.width))}
	// The status line rides the top block, where the list keeps it: a refused
	// key answers here, on the screen that refused it.
	if stat := strings.TrimSuffix(m.statusBlock(), "\n"); stat != "" {
		top = append(top, strings.Split(stat, "\n")...)
	}
	body := append(detailBlock(m.report, d.row, d, m.theme, m.width), m.detailActionLines()...)
	actions := m.detailActions()
	var foot []string
	if len(actions) > 0 {
		hints := make([]keyHint, len(actions))
		for i, a := range actions {
			hints[i] = keyHint{a.Key, a.Key + " " + a.Label, a.Key}
		}
		dim := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
		for _, line := range strings.Split(fitHints(hints, m.width), "\n") {
			foot = append(foot, dim.Render(clip(line, m.width)))
		}
	}
	room := m.bodyRoom(len(top)+len(foot), len(body))
	d.offset = max(0, min(d.offset, max(0, len(body)-room)))
	return m.framedRoom(top, body, d.offset, room, foot)
}

// detailActionLines renders the actions as the block's last section: one
// heading, then each key with its sentence, wrapped.
func (m cleanupListModel) detailActionLines() []string {
	var out []string
	if m.width > 0 {
		out = append(out, "")
	}
	out = append(out, "Actions")
	for _, a := range m.detailActions() {
		out = append(out, wrapIndented(a.Key+"  "+a.Label+" — "+a.Does, "   ", m.width)...)
	}
	return out
}
